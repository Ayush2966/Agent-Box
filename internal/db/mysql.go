package db

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/go-sql-driver/mysql"

	"github.com/Ayush2966/Agent-Box/internal/errs"
)

// MySQL clones the source database per box with mysqldump | mysql. There is
// no template: MySQL cannot copy a database instantly, so a dump is needed
// per clone anyway, and Refresh is therefore a no-op.
type MySQL struct {
	src   *url.URL
	srcDB string
}

var mysqlName = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

const mysqlToolsHint = "install the MySQL client tools (mysql and mysqldump) and put them on PATH"

func NewMySQL(source string) (*MySQL, error) {
	u, err := url.Parse(source)
	if err != nil || (u.Scheme != "mysql" && u.Scheme != "mariadb") || u.Host == "" {
		return nil, errs.New(errs.Invalid, "db.source must be a mysql:// URL with a host (got %q)", source)
	}
	name := strings.Trim(u.Path, "/")
	if name == "" {
		return nil, errs.New(errs.Invalid, "db.source %q names no database", source)
	}
	if !mysqlName.MatchString(name) {
		return nil, errs.New(errs.Invalid, "database name %q must be letters, digits and underscores", name)
	}
	return &MySQL{src: u, srcDB: name}, nil
}

func (m *MySQL) hostPort() (host, port string) {
	port = m.src.Port()
	if port == "" {
		port = "3306"
	}
	return m.src.Hostname(), port
}

// dsn is the Go driver's form for dbName ("" = no database selected).
func (m *MySQL) dsn(dbName string) string {
	host, port := m.hostPort()
	var b strings.Builder
	if m.src.User != nil {
		b.WriteString(m.src.User.Username())
		if pw, ok := m.src.User.Password(); ok {
			b.WriteString(":" + pw)
		}
		b.WriteString("@")
	}
	fmt.Fprintf(&b, "tcp(%s)/%s", net.JoinHostPort(host, port), dbName)
	if m.src.RawQuery != "" {
		b.WriteString("?" + m.src.RawQuery)
	}
	return b.String()
}

// urlFor is the source URL with the database replaced; it is what apps get.
func (m *MySQL) urlFor(dbName string) string {
	u := *m.src
	u.Path = "/" + dbName
	return u.String()
}

func (m *MySQL) clonePrefix() string { return m.srcDB + "_agentbox_" }

func (m *MySQL) cloneName(box string) (string, error) {
	name := m.clonePrefix() + strings.ReplaceAll(box, "-", "_")
	if !mysqlName.MatchString(name) {
		return "", errs.New(errs.Invalid, "database name %s must be letters, digits and underscores", name)
	}
	if len(name) > 64 {
		return "", errs.New(errs.Invalid, "database name %s is longer than MySQL's 64-character limit", name).
			WithHint("use a shorter box name")
	}
	return name, nil
}

func mysqlIdent(s string) string { return "`" + strings.ReplaceAll(s, "`", "``") + "`" }

func (m *MySQL) admin(ctx context.Context) (*sql.DB, error) {
	conn, err := sql.Open("mysql", m.dsn(""))
	if err != nil {
		return nil, err
	}
	if err := conn.PingContext(ctx); err != nil {
		conn.Close()
		return nil, errs.New(errs.Precondition, "connect to %s: %v", m.src.Redacted(), err).
			WithHint("is the server running and the user allowed to connect?")
	}
	return conn, nil
}

func (m *MySQL) Check(ctx context.Context) error {
	for _, tool := range []string{"mysql", "mysqldump"} {
		if _, err := exec.LookPath(tool); err != nil {
			return errs.New(errs.Precondition, "%s not found on PATH", tool).WithHint(mysqlToolsHint)
		}
	}
	conn, err := m.admin(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	return nil
}

func (m *MySQL) Prepare(ctx context.Context) error { return m.Check(ctx) }

func (m *MySQL) Refresh(context.Context) error { return nil }

// exists reports whether a database of that name is on the server. It is a
// plain SELECT: SHOW DATABASES LIKE cannot be server-side prepared.
func (m *MySQL) exists(ctx context.Context, conn *sql.DB, name string) (bool, error) {
	var n int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = ?", name).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

func (m *MySQL) createStatement(name string) string { return "CREATE DATABASE " + mysqlIdent(name) }

// gtidPurgedFlag reports whether mysqldump needs --set-gtid-purged=OFF: the
// server runs with GTIDs. Any error (MariaDB has no gtid_mode variable; or
// the user may not read it) means false; the tool's own error will say so if
// a GTID dump really is impossible.
func (m *MySQL) gtidPurgedFlag(ctx context.Context, conn *sql.DB) bool {
	var mode string
	if err := conn.QueryRowContext(ctx, "SELECT @@global.gtid_mode").Scan(&mode); err != nil {
		return false
	}
	return strings.ToUpper(mode) != "OFF"
}

// Clone creates the box database and fills it from the source. An existing
// database of that name is refused, never dropped: a leftover is the
// daemon's job (Recover, then rm --force) or doctor's (orphan_db), because
// two checkouts of one repo can share a source and a box name.
func (m *MySQL) Clone(ctx context.Context, box, _ string) (Clone, error) {
	name, err := m.cloneName(box)
	if err != nil {
		return Clone{}, err
	}
	conn, err := m.admin(ctx)
	if err != nil {
		return Clone{}, err
	}
	defer conn.Close()
	ok, err := m.exists(ctx, conn, name)
	if err != nil {
		return Clone{}, err
	}
	if ok {
		return Clone{}, errs.New(errs.Exists, "database %s already exists", name).
			WithHint("run `agentbox doctor --fix` to drop orphaned box databases")
	}
	q := m.createStatement(name)
	if _, err := conn.ExecContext(ctx, q); err != nil {
		if isDBExists(err) {
			return Clone{}, errs.New(errs.Exists, "database %s already exists", name).
				WithHint("run `agentbox doctor --fix` to drop orphaned box databases")
		}
		return Clone{}, errs.New(errs.Precondition, "%s: %v", q, err).
			WithHint("the user in db.source needs the CREATE privilege (GRANT CREATE, DROP ON *.* TO ...)")
	}
	gtid := m.gtidPurgedFlag(ctx, conn)
	if err := m.dumpRestore(ctx, name, gtid); err != nil {
		_, _ = conn.ExecContext(context.Background(), "DROP DATABASE IF EXISTS "+mysqlIdent(name))
		return Clone{}, err
	}
	return Clone{Name: name, URL: m.urlFor(name)}, nil
}

// isClone reports whether name is one of this source's clones; the prefix
// comparison is case-insensitive for the same reason List's is.
func (m *MySQL) isClone(name string) bool {
	p := m.clonePrefix()
	return matchesPrefix(name, p) && len(name) > len(p) && mysqlName.MatchString(name)
}

// isDBExists reports MySQL error 1007 (ER_DB_CREATE_EXISTS): another clone of
// the same name was created between the existence check and CREATE.
func isDBExists(err error) bool {
	var me *mysql.MySQLError
	return errors.As(err, &me) && me.Number == 1007
}

func (m *MySQL) Drop(ctx context.Context, name, _ string) error {
	if !m.isClone(name) {
		return errs.New(errs.Invalid, "refusing to drop %q: not an agentbox clone of %s", name, m.srcDB)
	}
	conn, err := m.admin(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = conn.ExecContext(ctx, "DROP DATABASE IF EXISTS "+mysqlIdent(name))
	return err
}

func (m *MySQL) List(ctx context.Context) ([]string, error) {
	conn, err := m.admin(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	// Plain SHOW DATABASES: the LIKE form cannot be server-side prepared.
	rows, err := conn.QueryContext(ctx, "SHOW DATABASES")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		if matchesPrefix(n, m.clonePrefix()) {
			names = append(names, n)
		}
	}
	return names, rows.Err()
}

// matchesPrefix compares case-insensitively: servers with
// lower_case_table_names=1 store database names in lower case.
func matchesPrefix(name, prefix string) bool {
	return len(name) >= len(prefix) && strings.EqualFold(name[:len(prefix)], prefix)
}

// optionValue quotes a value for a MySQL option file: "#", spaces and
// backslashes are literal inside double quotes once \ and " are escaped.
func optionValue(s string) (string, error) {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return "", errs.New(errs.Invalid, "db.source contains a control character in the user, password or host")
		}
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`, nil
}

// defaultsFile writes the connection details for the mysql tools to a 0600
// temp file, so the password never appears on a command line. It is passed
// as --defaults-file, which makes the tools ignore ~/.my.cnf, /etc/my.cnf and
// login paths, so only db.source decides which server and user they talk to.
func (m *MySQL) defaultsFile() (path string, cleanup func(), err error) {
	host, port := m.hostPort()
	qhost, err := optionValue(host)
	if err != nil {
		return "", nil, err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[client]\nprotocol=tcp\nhost=%s\nport=%s\n", qhost, port)
	if m.src.User != nil {
		qu, err := optionValue(m.src.User.Username())
		if err != nil {
			return "", nil, err
		}
		fmt.Fprintf(&b, "user=%s\n", qu)
		if pw, ok := m.src.User.Password(); ok {
			qp, err := optionValue(pw)
			if err != nil {
				return "", nil, err
			}
			fmt.Fprintf(&b, "password=%s\n", qp)
		}
	}
	f, err := os.CreateTemp("", "agentbox-my-*.cnf")
	if err != nil {
		return "", nil, err
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", nil, err
	}
	if _, err := f.WriteString(b.String()); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", nil, err
	}
	f.Close()
	return f.Name(), func() { os.Remove(f.Name()) }, nil
}

func (m *MySQL) dumpArgs(defaults string, gtid bool) []string {
	args := []string{"--defaults-file=" + defaults, "--single-transaction", "--routines", "--triggers", "--events", "--no-tablespaces"}
	if gtid {
		args = append(args, "--set-gtid-purged=OFF")
	}
	return append(args, m.srcDB)
}

func (m *MySQL) restoreArgs(defaults, target string) []string {
	return []string{"--defaults-file=" + defaults, target}
}

func (m *MySQL) dumpRestore(ctx context.Context, target string, gtid bool) error {
	defaults, cleanup, err := m.defaultsFile()
	if err != nil {
		return err
	}
	defer cleanup()
	r, w, err := os.Pipe()
	if err != nil {
		return err
	}
	dump := exec.CommandContext(ctx, "mysqldump", m.dumpArgs(defaults, gtid)...)
	restore := exec.CommandContext(ctx, "mysql", m.restoreArgs(defaults, target)...)
	var dumpErr, restoreErr bytes.Buffer
	dump.Stdout, dump.Stderr = w, &dumpErr
	restore.Stdin, restore.Stderr = r, &restoreErr
	if err := dump.Start(); err != nil {
		r.Close()
		w.Close()
		return errs.New(errs.Precondition, "run mysqldump: %v", err).WithHint(mysqlToolsHint)
	}
	if err := restore.Start(); err != nil {
		r.Close()
		w.Close()
		_ = dump.Process.Kill()
		_ = dump.Wait()
		return errs.New(errs.Precondition, "run mysql: %v", err).WithHint(mysqlToolsHint)
	}
	r.Close()
	w.Close()
	rErr := restore.Wait()
	dErr := dump.Wait()
	return dumpRestoreError("mysqldump", dErr, dumpErr.String(), "mysql", rErr, restoreErr.String())
}
