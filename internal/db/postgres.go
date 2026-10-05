package db

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/agentbox-dev/agentbox/internal/errs"
)

// Postgres clones from a template snapshot (<src>_agentbox_tpl) built with
// pg_dump | pg_restore, so the live source database may stay in use.
type Postgres struct {
	src       *url.URL
	srcDB     string
	terminate bool
}

// tplReady is stamped on the template database only once its contents have
// been fully restored. It is a shared-catalog comment, so it is readable from
// any database and — unlike the template's contents — it is NOT copied into
// databases cloned from it, so box databases stay unmarked.
const tplReady = "agentbox: template ready"

const toolsHint = "install the PostgreSQL client tools, e.g. `brew install postgresql@16` or `apt install postgresql-client`"

func NewPostgres(source string, opts Options) (*Postgres, error) {
	u, err := url.Parse(source)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		return nil, errs.New(errs.Invalid, "db.source must be a postgres:// URL (got %q)", source)
	}
	name := strings.TrimPrefix(u.Path, "/")
	if name == "" || strings.Contains(name, "/") {
		return nil, errs.New(errs.Invalid, "db.source must name a database, e.g. postgres://localhost:5432/myapp")
	}
	return &Postgres{src: u, srcDB: name, terminate: opts.Terminate}, nil
}

func (p *Postgres) tplName() string     { return p.srcDB + "_agentbox_tpl" }
func (p *Postgres) clonePrefix() string { return p.srcDB + "_agentbox_" }

func (p *Postgres) boxDBName(box string) string {
	return p.clonePrefix() + strings.ReplaceAll(box, "-", "_")
}

func (p *Postgres) urlFor(dbName string) string {
	u := *p.src
	u.Path = "/" + dbName
	return u.String()
}

func ident(s string) string { return pgx.Identifier{s}.Sanitize() }

func (p *Postgres) admin(ctx context.Context) (*pgx.Conn, error) {
	c, err := pgx.Connect(ctx, p.src.String())
	if err != nil {
		return nil, errs.New(errs.Precondition, "connect to %s: %v", p.src.Redacted(), err).
			WithHint("is Postgres running? check db.source in agentbox.yaml")
	}
	return c, nil
}

func (p *Postgres) exists(ctx context.Context, c *pgx.Conn, name string) (bool, error) {
	var ok bool
	err := c.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname = $1)`, name).Scan(&ok)
	return ok, err
}

// templateReady reports whether the template exists AND finished building. A
// Refresh interrupted between CREATE DATABASE and the end of the restore
// leaves an empty, unmarked database behind; without this check every later
// box would be cloned from it silently.
func (p *Postgres) templateReady(ctx context.Context, c *pgx.Conn) (bool, error) {
	var marker *string
	err := c.QueryRow(ctx,
		`SELECT shobj_description(oid, 'pg_database') FROM pg_database WHERE datname = $1`,
		p.tplName()).Scan(&marker)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return marker != nil && *marker == tplReady, nil
}

func (p *Postgres) connections(ctx context.Context, c *pgx.Conn, name string) ([]int32, error) {
	rows, err := c.Query(ctx, `SELECT pid FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()`, name)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[int32])
}

// ensureIdle makes sure nobody is connected to name, which CREATE DATABASE … TEMPLATE requires.
func (p *Postgres) ensureIdle(ctx context.Context, c *pgx.Conn, name string) error {
	pids, err := p.connections(ctx, c, name)
	if err != nil || len(pids) == 0 {
		return err
	}
	if !p.terminate {
		return &BusyError{DB: name, PIDs: pids}
	}
	if _, err := c.Exec(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()`, name); err != nil {
		return err
	}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if pids, err = p.connections(ctx, c, name); err != nil || len(pids) == 0 {
			return err
		}
	}
	return &BusyError{DB: name, PIDs: pids}
}

func (p *Postgres) Check(ctx context.Context) error {
	for _, tool := range []string{"pg_dump", "pg_restore"} {
		if _, err := exec.LookPath(tool); err != nil {
			return errs.New(errs.Precondition, "%s not found on PATH", tool).WithHint(toolsHint)
		}
	}
	c, err := p.admin(ctx)
	if err != nil {
		return err
	}
	defer c.Close(ctx)
	var user string
	var can bool
	if err := c.QueryRow(ctx, `SELECT current_user, rolcreatedb OR rolsuper FROM pg_roles WHERE rolname = current_user`).Scan(&user, &can); err != nil {
		return err
	}
	if !can {
		return errs.New(errs.Precondition, "role %q cannot create databases", user).
			WithHint(fmt.Sprintf("ALTER ROLE %s CREATEDB;", ident(user)))
	}
	return nil
}

func (p *Postgres) Prepare(ctx context.Context) error {
	c, err := p.admin(ctx)
	if err != nil {
		return err
	}
	ready, err := p.templateReady(ctx, c)
	c.Close(ctx)
	if err != nil || ready {
		return err
	}
	if err := p.Check(ctx); err != nil {
		return err
	}
	return p.Refresh(ctx)
}

func (p *Postgres) Refresh(ctx context.Context) error {
	c, err := p.admin(ctx)
	if err != nil {
		return err
	}
	defer c.Close(ctx)
	tpl := p.tplName()
	exists, err := p.exists(ctx, c, tpl)
	if err != nil {
		return err
	}
	if exists {
		if err := p.ensureIdle(ctx, c, tpl); err != nil {
			return err
		}
		if _, err := c.Exec(ctx, "DROP DATABASE "+ident(tpl)); err != nil {
			return err
		}
	}
	if _, err := c.Exec(ctx, "CREATE DATABASE "+ident(tpl)+" TEMPLATE template0"); err != nil {
		return fmt.Errorf("create template database: %w", err)
	}
	if err := p.dumpRestore(ctx, tpl); err != nil {
		_, _ = c.Exec(context.Background(), "DROP DATABASE IF EXISTS "+ident(tpl)+" WITH (FORCE)")
		return err
	}
	// Only now is the template usable. COMMENT takes no bind parameters, but
	// tplReady is a quote-free compile-time constant.
	if _, err := c.Exec(ctx, "COMMENT ON DATABASE "+ident(tpl)+" IS '"+tplReady+"'"); err != nil {
		_, _ = c.Exec(context.Background(), "DROP DATABASE IF EXISTS "+ident(tpl)+" WITH (FORCE)")
		return fmt.Errorf("mark template ready: %w", err)
	}
	return nil
}

func (p *Postgres) dumpRestore(ctx context.Context, target string) error {
	r, w, err := os.Pipe()
	if err != nil {
		return err
	}
	dump := exec.CommandContext(ctx, "pg_dump", "--format=custom", "--no-owner", "--no-acl", "--dbname", p.src.String())
	restore := exec.CommandContext(ctx, "pg_restore", "--no-owner", "--no-acl", "--dbname", p.urlFor(target))
	var dumpErr, restoreErr bytes.Buffer
	dump.Stdout, dump.Stderr = w, &dumpErr
	restore.Stdin, restore.Stderr = r, &restoreErr

	if err := dump.Start(); err != nil {
		r.Close()
		w.Close()
		return errs.New(errs.Precondition, "run pg_dump: %v", err).WithHint(toolsHint)
	}
	if err := restore.Start(); err != nil {
		r.Close()
		w.Close()
		_ = dump.Process.Kill()
		_ = dump.Wait()
		return errs.New(errs.Precondition, "run pg_restore: %v", err).WithHint(toolsHint)
	}
	// The children hold their own copies; closing ours lets EOF/SIGPIPE propagate.
	r.Close()
	w.Close()
	rErr := restore.Wait()
	dErr := dump.Wait()
	if dErr != nil {
		return fmt.Errorf("pg_dump failed: %v: %s", dErr, strings.TrimSpace(dumpErr.String()))
	}
	if rErr != nil {
		return fmt.Errorf("pg_restore failed: %v: %s", rErr, strings.TrimSpace(restoreErr.String()))
	}
	return nil
}

func (p *Postgres) Clone(ctx context.Context, box, _ string) (Clone, error) {
	name := p.boxDBName(box)
	if len(name) > 63 {
		return Clone{}, errs.New(errs.Invalid, "database name %s is longer than Postgres's 63-character limit", name).
			WithHint("use a shorter box name")
	}
	c, err := p.admin(ctx)
	if err != nil {
		return Clone{}, err
	}
	defer c.Close(ctx)
	tpl := p.tplName()
	if err := p.ensureIdle(ctx, c, tpl); err != nil {
		return Clone{}, err
	}
	ok, err := p.exists(ctx, c, name)
	if err != nil {
		return Clone{}, err
	}
	if ok {
		return Clone{}, errs.New(errs.Exists, "database %s already exists", name).
			WithHint("run `agentbox doctor --fix` to drop orphaned box databases")
	}
	if _, err := c.Exec(ctx, "CREATE DATABASE "+ident(name)+" TEMPLATE "+ident(tpl)); err != nil {
		return Clone{}, fmt.Errorf("clone database: %w", err)
	}
	return Clone{Name: name, URL: p.urlFor(name)}, nil
}

func (p *Postgres) Drop(ctx context.Context, name, _ string) error {
	if !strings.HasPrefix(name, p.clonePrefix()) || name == p.tplName() {
		return fmt.Errorf("refusing to drop %s: not a box database of %s", name, p.srcDB)
	}
	c, err := p.admin(ctx)
	if err != nil {
		return err
	}
	defer c.Close(ctx)
	_, err = c.Exec(ctx, "DROP DATABASE IF EXISTS "+ident(name)+" WITH (FORCE)")
	return err
}

func (p *Postgres) List(ctx context.Context) ([]string, error) {
	c, err := p.admin(ctx)
	if err != nil {
		return nil, err
	}
	defer c.Close(ctx)
	rows, err := c.Query(ctx, `SELECT datname FROM pg_database`)
	if err != nil {
		return nil, err
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	var out []string
	for _, n := range names {
		if strings.HasPrefix(n, p.clonePrefix()) && n != p.tplName() {
			out = append(out, n)
		}
	}
	return out, nil
}
