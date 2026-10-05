//go:build e2e

// Package e2e builds the agentbox binary and drives real boxes end to end.
package e2e

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	_ "modernc.org/sqlite"

	"github.com/agentbox-dev/agentbox/internal/api"
	"github.com/agentbox-dev/agentbox/internal/testutil"
)

var bin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "agentbox-bin")
	if err != nil {
		panic(err)
	}
	bin = filepath.Join(dir, "agentbox")
	build := exec.Command("go", "build", "-o", bin, "./cmd/agentbox")
	build.Dir = ".."
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		panic(err)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

type env struct {
	t    *testing.T
	vars []string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is required for the e2e examples")
	}
	home, err := os.MkdirTemp("/tmp", "abe2e") // short path: unix socket length limit
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(home, "config.yaml"), []byte("api_addr: 127.0.0.1:0\n"), 0o600)
	e := &env{t: t, vars: append(os.Environ(), "AGENTBOX_HOME="+home)}
	t.Cleanup(func() {
		stop := exec.Command(bin, "daemon", "stop")
		stop.Env = e.vars
		_ = stop.Run()
		os.RemoveAll(home)
	})
	return e
}

func (e *env) run(dir string, args ...string) string {
	e.t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir, cmd.Env = dir, e.vars
	out, err := cmd.CombinedOutput()
	if err != nil {
		e.t.Fatalf("agentbox %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func copyExample(t *testing.T, name string) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), name)
	if err := os.CopyFS(dst, os.DirFS(filepath.Join("..", "examples", name))); err != nil {
		t.Fatal(err)
	}
	return testutil.InitRepo(t, dst)
}

func getJSON(t *testing.T, u string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		resp, err := http.Get(u)
		if err == nil {
			var v map[string]any
			err = json.NewDecoder(resp.Body).Decode(&v)
			resp.Body.Close()
			if err == nil {
				return v
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("GET %s never succeeded: %v", u, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func threeBoxes(t *testing.T, e *env, repo string, check func(b api.BoxView, resp map[string]any)) {
	names := []string{"a", "b", "c"}
	for _, n := range names {
		e.run(repo, "new", n)
	}
	var boxes []api.BoxView
	if err := json.Unmarshal([]byte(e.run(repo, "ls", "--json")), &boxes); err != nil || len(boxes) != 3 {
		t.Fatalf("ls --json: %v (%d boxes)", err, len(boxes))
	}
	seen := map[int]bool{}
	for _, b := range boxes {
		port := b.Ports["APP_PORT"]
		if port == 0 || seen[port] {
			t.Fatalf("box %s: bad or duplicate APP_PORT %d", b.Name, port)
		}
		seen[port] = true
		resp := getJSON(t, fmt.Sprintf("http://127.0.0.1:%d/", port))
		if resp["box"] != b.Name {
			t.Errorf("port %d answered as box %v, want %s", port, resp["box"], b.Name)
		}
		check(b, resp)
	}
	for _, n := range names {
		e.run(repo, "rm", "--force", n)
	}
	if out := e.run(repo, "doctor"); !strings.Contains(out, "no problems found") {
		t.Errorf("doctor after cleanup:\n%s", out)
	}
}

func TestThreeBoxesSQLite(t *testing.T) {
	e := newEnv(t)
	repo := copyExample(t, "sqlite")
	c, err := sql.Open("sqlite", filepath.Join(repo, "dev.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Exec(`CREATE TABLE items(name TEXT)`); err != nil {
		t.Fatal(err)
	}
	c.Close()
	threeBoxes(t, e, repo, func(b api.BoxView, resp map[string]any) {
		if resp["exists"] != true {
			t.Errorf("box %s: its sqlite copy is missing: %v", b.Name, resp)
		}
	})
}

func TestThreeBoxesPostgres(t *testing.T) {
	admin := os.Getenv("AGENTBOX_TEST_PG_URL")
	if admin == "" {
		t.Skip("set AGENTBOX_TEST_PG_URL to run the Postgres e2e test")
	}
	e := newEnv(t)
	repo := copyExample(t, "node-postgres")

	ctx := context.Background()
	name := fmt.Sprintf("abe2e_%d", time.Now().UnixNano()%1_000_000_000)
	ac, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ac.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		rows, _ := ac.Query(ctx, "SELECT datname FROM pg_database WHERE starts_with(datname, $1)", name)
		dbs, _ := pgx.CollectRows(rows, pgx.RowTo[string])
		for _, d := range dbs {
			ac.Exec(ctx, "DROP DATABASE IF EXISTS "+pgx.Identifier{d}.Sanitize()+" WITH (FORCE)")
		}
		ac.Close(ctx)
	})
	u, _ := url.Parse(admin)
	u.Path = "/" + name
	cfgPath := filepath.Join(repo, "agentbox.yaml")
	cfg, _ := os.ReadFile(cfgPath)
	os.WriteFile(cfgPath, []byte(strings.Replace(string(cfg), "postgres://postgres:postgres@localhost:5432/myapp", u.String(), 1)), 0o644)

	threeBoxes(t, e, repo, func(b api.BoxView, resp map[string]any) {
		if want := name + "_agentbox_" + b.Name; resp["db"] != want {
			t.Errorf("box %s got database %v, want %s", b.Name, resp["db"], want)
		}
	})
}
