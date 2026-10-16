package db

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/Ayush2966/Agent-Box/internal/errs"
)

// Mongo clones the source database per box with mongodump | mongorestore,
// renaming the namespace on the way in. Like MySQL there is no template: a
// dump is taken per clone, and Refresh is a no-op.
type Mongo struct {
	src   *url.URL
	srcDB string
}

var mongoName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

const mongoToolsHint = "install the MongoDB Database Tools (mongodump and mongorestore) and put them on PATH"

func NewMongo(source string) (*Mongo, error) {
	u, err := url.Parse(source)
	if err != nil || (u.Scheme != "mongodb" && u.Scheme != "mongodb+srv") || u.Host == "" {
		return nil, errs.New(errs.Invalid, "db.source must be a mongodb:// or mongodb+srv:// URL with a host (got %q)", source)
	}
	name := strings.Trim(u.Path, "/")
	if name == "" {
		return nil, errs.New(errs.Invalid, "db.source %q names no database (put it in the path: mongodb://host/mydb)", source)
	}
	if !mongoName.MatchString(name) {
		return nil, errs.New(errs.Invalid, "database name %q must be letters, digits, underscores and dashes", name)
	}
	return &Mongo{src: u, srcDB: name}, nil
}

func (m *Mongo) clonePrefix() string { return m.srcDB + "_agentbox_" }

func (m *Mongo) cloneName(box string) (string, error) {
	name := m.clonePrefix() + strings.ReplaceAll(box, "-", "_")
	if !mongoName.MatchString(name) {
		return "", errs.New(errs.Invalid, "database name %s must be letters, digits, underscores and dashes", name)
	}
	if len(name) > 63 {
		return "", errs.New(errs.Invalid, "database name %s is longer than MongoDB's 63-byte limit", name).
			WithHint("use a shorter box name")
	}
	return name, nil
}

// urlFor is the source URL with the database replaced; it is what apps get.
func (m *Mongo) urlFor(dbName string) string {
	u := *m.src
	u.Path = "/" + dbName
	return u.String()
}

// adminURI is the source URL without a database, for the client and the
// tools (which would otherwise read the path as the target). Dropping the
// path would change where the user authenticates, so the source database
// becomes authSource unless the URL already says.
func (m *Mongo) adminURI() string {
	u := *m.src
	u.Path = "/"
	if u.User != nil {
		q := u.Query()
		if q.Get("authSource") == "" {
			q.Set("authSource", m.srcDB)
			u.RawQuery = q.Encode()
		}
	}
	return u.String()
}

func (m *Mongo) connect(ctx context.Context) (*mongo.Client, error) {
	c, err := mongo.Connect(options.Client().ApplyURI(m.adminURI()).SetServerSelectionTimeout(5 * time.Second))
	if err != nil {
		return nil, errs.New(errs.Invalid, "db.source: %v", err)
	}
	if err := c.Ping(ctx, nil); err != nil {
		_ = c.Disconnect(context.Background())
		return nil, errs.New(errs.Precondition, "connect to %s: %v", m.src.Redacted(), err).
			WithHint("is the server running and the user allowed to connect?")
	}
	return c, nil
}

func (m *Mongo) Check(ctx context.Context) error {
	for _, tool := range []string{"mongodump", "mongorestore"} {
		if _, err := exec.LookPath(tool); err != nil {
			return errs.New(errs.Precondition, "%s not found on PATH", tool).WithHint(mongoToolsHint)
		}
	}
	c, err := m.connect(ctx)
	if err != nil {
		return err
	}
	return c.Disconnect(ctx)
}

func (m *Mongo) Prepare(ctx context.Context) error { return m.Check(ctx) }

func (m *Mongo) Refresh(context.Context) error { return nil }

func (m *Mongo) exists(ctx context.Context, c *mongo.Client, name string) (bool, error) {
	names, err := c.ListDatabaseNames(ctx, bson.D{{Key: "name", Value: name}})
	if err != nil {
		return false, err
	}
	return len(names) > 0, nil
}

// Clone fills <src>_agentbox_<box> from the source. An existing database of
// that name is refused, never dropped (two checkouts of one repo can share a
// source and a box name); doctor --fix drops orphans.
func (m *Mongo) Clone(ctx context.Context, box, _ string) (Clone, error) {
	name, err := m.cloneName(box)
	if err != nil {
		return Clone{}, err
	}
	c, err := m.connect(ctx)
	if err != nil {
		return Clone{}, err
	}
	defer c.Disconnect(context.Background())
	ok, err := m.exists(ctx, c, name)
	if err != nil {
		return Clone{}, err
	}
	if ok {
		return Clone{}, errs.New(errs.Exists, "database %s already exists", name).
			WithHint("run `agentbox doctor --fix` to drop orphaned box databases")
	}
	if err := m.dumpRestore(ctx, name); err != nil {
		_ = c.Database(name).Drop(context.Background())
		return Clone{}, err
	}
	// mongorestore of an empty source creates nothing; apps still expect the
	// database to be addressable, which MongoDB does lazily, so that is fine.
	return Clone{Name: name, URL: m.urlFor(name)}, nil
}

func (m *Mongo) isClone(name string) bool {
	p := m.clonePrefix()
	return strings.HasPrefix(name, p) && len(name) > len(p) && mongoName.MatchString(name)
}

func (m *Mongo) Drop(ctx context.Context, name, _ string) error {
	if !m.isClone(name) {
		return errs.New(errs.Invalid, "refusing to drop %q: not an agentbox clone of %s", name, m.srcDB)
	}
	c, err := m.connect(ctx)
	if err != nil {
		return err
	}
	defer c.Disconnect(context.Background())
	return c.Database(name).Drop(ctx)
}

func (m *Mongo) List(ctx context.Context) ([]string, error) {
	c, err := m.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer c.Disconnect(context.Background())
	names, err := c.ListDatabaseNames(ctx, bson.D{})
	if err != nil {
		return nil, err
	}
	var out []string
	for _, n := range names {
		if m.isClone(n) {
			out = append(out, n)
		}
	}
	return out, nil
}

// configFile writes the connection URI for the tools to a 0600 temp file: the
// tools read `uri:` from --config, so the password never appears on argv.
func (m *Mongo) configFile() (path string, cleanup func(), err error) {
	uri := m.adminURI()
	for _, r := range uri {
		if r < 0x20 || r == 0x7f {
			return "", nil, errs.New(errs.Invalid, "db.source contains a control character")
		}
	}
	f, err := os.CreateTemp("", "agentbox-mongo-*.yaml")
	if err != nil {
		return "", nil, err
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", nil, err
	}
	if _, err := fmt.Fprintf(f, "uri: %q\n", uri); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", nil, err
	}
	f.Close()
	return f.Name(), func() { os.Remove(f.Name()) }, nil
}

func (m *Mongo) dumpArgs(cfg string) []string {
	return []string{"--config=" + cfg, "--db=" + m.srcDB, "--archive", "--quiet"}
}

func (m *Mongo) restoreArgs(cfg, target string) []string {
	return []string{"--config=" + cfg, "--archive", "--nsFrom=" + m.srcDB + ".*", "--nsTo=" + target + ".*", "--quiet"}
}

func (m *Mongo) dumpRestore(ctx context.Context, target string) error {
	cfg, cleanup, err := m.configFile()
	if err != nil {
		return err
	}
	defer cleanup()
	r, w, err := os.Pipe()
	if err != nil {
		return err
	}
	dump := exec.CommandContext(ctx, "mongodump", m.dumpArgs(cfg)...)
	restore := exec.CommandContext(ctx, "mongorestore", m.restoreArgs(cfg, target)...)
	var dumpErr, restoreErr bytes.Buffer
	dump.Stdout, dump.Stderr = w, &dumpErr
	restore.Stdin, restore.Stderr = r, &restoreErr
	if err := dump.Start(); err != nil {
		r.Close()
		w.Close()
		return errs.New(errs.Precondition, "run mongodump: %v", err).WithHint(mongoToolsHint)
	}
	if err := restore.Start(); err != nil {
		r.Close()
		w.Close()
		_ = dump.Process.Kill()
		_ = dump.Wait()
		return errs.New(errs.Precondition, "run mongorestore: %v", err).WithHint(mongoToolsHint)
	}
	r.Close()
	w.Close()
	rErr := restore.Wait()
	dErr := dump.Wait()
	return dumpRestoreError("mongodump", dErr, dumpErr.String(), "mongorestore", rErr, restoreErr.String())
}
