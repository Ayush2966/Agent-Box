package db

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func pgAdminURL(t *testing.T) string {
	u := os.Getenv("AGENTBOX_TEST_PG_URL")
	if u == "" {
		t.Skip("set AGENTBOX_TEST_PG_URL (postgres:// URL of a role with CREATEDB) to run Postgres tests")
	}
	return u
}

func withDB(t *testing.T, raw, name string) string {
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	return u.String()
}

// newSource creates a throwaway source database with one row in items.
func newSource(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	admin := pgAdminURL(t)
	name := fmt.Sprintf("abtest_%d", time.Now().UnixNano()%1_000_000_000)
	c, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		rows, _ := c.Query(ctx, "SELECT datname FROM pg_database WHERE starts_with(datname, $1)", name)
		names, _ := pgx.CollectRows(rows, pgx.RowTo[string])
		for _, n := range names {
			_, _ = c.Exec(ctx, "DROP DATABASE IF EXISTS "+ident(n)+" WITH (FORCE)")
		}
		c.Close(ctx)
	})
	src := withDB(t, admin, name)
	sc, err := pgx.Connect(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	defer sc.Close(ctx)
	if _, err := sc.Exec(ctx, "CREATE TABLE items(name text); INSERT INTO items VALUES ('one')"); err != nil {
		t.Fatal(err)
	}
	return src
}

func pgCount(t *testing.T, dbURL string) int {
	t.Helper()
	ctx := context.Background()
	c, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)
	var n int
	if err := c.QueryRow(ctx, "SELECT count(*) FROM items").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func pgExec(t *testing.T, dbURL, q string) {
	t.Helper()
	ctx := context.Background()
	c, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)
	if _, err := c.Exec(ctx, q); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresCloneWhileSourceInUse(t *testing.T) {
	ctx := context.Background()
	src := newSource(t)
	hold, err := pgx.Connect(ctx, src) // stands in for the main checkout's dev server
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Close(ctx)

	p, err := NewPostgres(src, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	c, err := p.Clone(ctx, "my-box", "")
	if err != nil {
		t.Fatal(err)
	}
	if want := p.srcDB + "_agentbox_my_box"; c.Name != want {
		t.Fatalf("clone name %q, want %q", c.Name, want)
	}
	if n := pgCount(t, c.URL); n != 1 {
		t.Fatalf("clone has %d rows", n)
	}
	pgExec(t, c.URL, "INSERT INTO items VALUES ('box only')")
	if n := pgCount(t, src); n != 1 {
		t.Fatalf("write to clone leaked into source (%d rows)", n)
	}
	names, _ := p.List(ctx)
	if !slices.Contains(names, c.Name) || slices.Contains(names, p.tplName()) {
		t.Fatalf("List = %v", names)
	}
	if err := p.Drop(ctx, c.Name, ""); err != nil {
		t.Fatal(err)
	}
	if names, _ := p.List(ctx); slices.Contains(names, c.Name) {
		t.Fatal("clone still listed after Drop")
	}
}

func TestPostgresTemplateBusy(t *testing.T) {
	ctx := context.Background()
	src := newSource(t)
	p, _ := NewPostgres(src, Options{})
	if err := p.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	hold, err := pgx.Connect(ctx, p.urlFor(p.tplName()))
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Close(ctx)

	_, err = p.Clone(ctx, "a", "")
	var busy *BusyError
	if !errors.As(err, &busy) || len(busy.PIDs) != 1 {
		t.Fatalf("err = %v, want BusyError", err)
	}
	pt, _ := NewPostgres(src, Options{Terminate: true})
	if _, err := pt.Clone(ctx, "a", ""); err != nil {
		t.Fatalf("with Terminate: %v", err)
	}
}

func TestPostgresRefreshPicksUpNewData(t *testing.T) {
	ctx := context.Background()
	src := newSource(t)
	p, _ := NewPostgres(src, Options{})
	if err := p.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	pgExec(t, src, "INSERT INTO items VALUES ('two')")
	if err := p.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	c, err := p.Clone(ctx, "a", "")
	if err != nil {
		t.Fatal(err)
	}
	if n := pgCount(t, c.URL); n != 2 {
		t.Fatalf("clone has %d rows after refresh, want 2", n)
	}
}

func TestPostgresDropRefusesForeignDatabases(t *testing.T) {
	p, err := NewPostgres("postgres://localhost:5432/app", Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"app", "app_agentbox_tpl", "other_agentbox_a"} {
		if err := p.Drop(context.Background(), name, ""); err == nil || !strings.Contains(err.Error(), "refusing") {
			t.Errorf("Drop(%q) err = %v", name, err)
		}
	}
}

func TestNewPostgresValidatesURL(t *testing.T) {
	for _, bad := range []string{"mysql://x/y", "postgres://localhost", "postgres://localhost/a/b"} {
		if _, err := NewPostgres(bad, Options{}); err == nil {
			t.Errorf("NewPostgres(%q) accepted", bad)
		}
	}
}

func TestPostgresNameTooLong(t *testing.T) {
	p, _ := NewPostgres("postgres://localhost/"+strings.Repeat("x", 40), Options{})
	_, err := p.Clone(context.Background(), strings.Repeat("b", 20), "")
	if err == nil || !strings.Contains(err.Error(), "63-character") {
		t.Fatalf("err = %v", err)
	}
}

// Prepare used to treat "a database named <src>_agentbox_tpl exists" as "the
// template is ready". A Refresh killed between CREATE DATABASE and the
// pg_dump|pg_restore stream leaves an EMPTY template, and every later box was
// then silently cloned from it with no schema and no warning.
func TestPostgresPrepareRebuildsHalfBuiltTemplate(t *testing.T) {
	ctx := context.Background()
	src := newSource(t)
	p, err := NewPostgres(src, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Prepare(ctx); err != nil {
		t.Fatal(err)
	}

	// Simulate the interrupted Refresh: drop the finished template and leave
	// behind an empty database under the same name, exactly as CREATE
	// DATABASE … TEMPLATE template0 would before the restore streams in.
	admin, err := pgx.Connect(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"DROP DATABASE IF EXISTS " + ident(p.tplName()) + " WITH (FORCE)",
		"CREATE DATABASE " + ident(p.tplName()) + " TEMPLATE template0",
	} {
		if _, err := admin.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	admin.Close(ctx)

	if err := p.Prepare(ctx); err != nil {
		t.Fatalf("Prepare over a half-built template: %v", err)
	}
	c, err := p.Clone(ctx, "halfbuilt", "")
	if err != nil {
		t.Fatal(err)
	}
	if n := pgCount(t, c.URL); n != 1 {
		t.Fatalf("box cloned from a half-built template has %d rows, want 1", n)
	}
}

func TestDumpRestoreErrorReportsTheRealCause(t *testing.T) {
	dErr, rErr := errors.New("signal: broken pipe"), errors.New("exit status 1")
	err := dumpRestoreError(dErr, "", rErr, "pg_restore: error: could not execute query: permission denied")
	if err == nil {
		t.Fatal("no error")
	}
	s := err.Error()
	if !strings.HasPrefix(s, "pg_restore failed") || !strings.Contains(s, "permission denied") || !strings.Contains(s, "broken pipe") {
		t.Fatalf("both tools failed: got %q, want pg_restore first with both causes", s)
	}
	if err := dumpRestoreError(dErr, "pg_dump: error: connection failed", nil, ""); err == nil || !strings.HasPrefix(err.Error(), "pg_dump failed") || !strings.Contains(err.Error(), "connection failed") {
		t.Fatalf("dump only: got %v", err)
	}
	if err := dumpRestoreError(nil, "", rErr, "bad archive"); err == nil || !strings.HasPrefix(err.Error(), "pg_restore failed") {
		t.Fatalf("restore only: got %v", err)
	}
	if err := dumpRestoreError(nil, "", nil, ""); err != nil {
		t.Fatalf("no failure: got %v", err)
	}
}

func TestCommandURLKeepsPasswordOutOfArgv(t *testing.T) {
	u, err := url.Parse("postgres://app:hunter2@db.internal:5432/myapp?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	arg, env := commandURL(u)
	if strings.Contains(arg, "hunter2") {
		t.Fatalf("password leaked into argv: %s", arg)
	}
	if !slices.Contains(env, "PGPASSWORD=hunter2") {
		t.Fatalf("PGPASSWORD missing from env: %v", env)
	}
	if !strings.Contains(arg, "app@db.internal:5432/myapp") || !strings.Contains(arg, "sslmode=disable") {
		t.Fatalf("argv URL lost user/host/db/query: %s", arg)
	}
	if u.User.String() != "app:hunter2" {
		t.Fatal("commandURL mutated the source URL")
	}
	plain, _ := url.Parse("postgres://app@localhost/x")
	if arg, env := commandURL(plain); arg != plain.String() || len(env) != 0 {
		t.Fatalf("no password: got %q %v", arg, env)
	}
}
