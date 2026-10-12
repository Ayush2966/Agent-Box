//go:build e2e

// Package e2e builds the agentbox binary and drives real boxes end to end.
package e2e

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
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
	home string
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
	e := &env{t: t, home: home, vars: append(os.Environ(), "AGENTBOX_HOME="+home)}
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

// runErr is run without the fatal: it returns combined output and the error.
func (e *env) runErr(dir string, args ...string) (string, error) {
	cmd := exec.Command(bin, args...)
	cmd.Dir, cmd.Env = dir, e.vars
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestProxyAndDashboard(t *testing.T) {
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
	e.run(repo, "new", "a")
	t.Cleanup(func() { e.run(repo, "rm", "--force", "a") })

	raw, err := os.ReadFile(filepath.Join(e.home, "api_addr"))
	if err != nil {
		t.Fatal(err)
	}
	addr := strings.TrimSpace(string(raw))
	viaProxy := func(host string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest("GET", "http://"+addr+"/", nil)
		req.Host = host
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		code, body := viaProxy("a.localhost")
		if code == 200 && strings.Contains(body, `"box":"a"`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("proxy never served box a: %d %s", code, body)
		}
		time.Sleep(200 * time.Millisecond)
	}
	e.run(repo, "stop", "a")
	if code, body := viaProxy("a.localhost"); code != 503 || !strings.Contains(body, "stopped") || !strings.Contains(body, "agentbox start a") {
		t.Fatalf("stopped box via proxy: %d %s", code, body)
	}
	if code, _ := viaProxy("nope.localhost"); code != 404 {
		t.Fatalf("unknown box via proxy: %d", code)
	}

	out, err := e.runErr(repo, "ui", "--no-open")
	if err != nil && strings.Contains(out, "built without the dashboard") {
		t.Skip("dashboard not built into this binary (run `make ui` first)")
	}
	if err != nil || !strings.HasPrefix(out, "dashboard: ") {
		t.Fatalf("ui --no-open: %v\n%s", err, out)
	}
	link := strings.TrimSpace(strings.TrimPrefix(out, "dashboard: "))
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := noRedirect.Get(link)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	var session *http.Cookie
	for _, ck := range resp.Cookies() {
		if ck.Name == "agentbox_session" {
			session = ck
		}
	}
	if resp.StatusCode != 303 || session == nil {
		t.Fatalf("login link: %d, cookie %v", resp.StatusCode, session)
	}
	base := "http://" + addr
	req, _ := http.NewRequest("GET", base+"/v1/boxes", nil)
	req.AddCookie(session)
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var boxes []api.BoxView
	json.NewDecoder(resp2.Body).Decode(&boxes)
	resp2.Body.Close()
	_, port, _ := net.SplitHostPort(addr)
	if resp2.StatusCode != 200 || len(boxes) != 1 || boxes[0].URL != "http://a.localhost:"+port+"/" {
		t.Fatalf("boxes with cookie: %d %+v", resp2.StatusCode, boxes)
	}
	start, _ := http.NewRequest("POST", base+"/v1/repos/"+boxes[0].Repo+"/boxes/a/start", nil)
	start.AddCookie(session)
	start.Header.Set("Origin", base)
	resp3, err := http.DefaultClient.Do(start)
	if err != nil {
		t.Fatal(err)
	}
	resp3.Body.Close()
	if resp3.StatusCode != 204 {
		t.Fatalf("start with cookie+Origin: %d", resp3.StatusCode)
	}
	if code, body := viaProxy("a.localhost"); code != 200 && code != 502 {
		t.Fatalf("after start: %d %s", code, body)
	}
}

// mysqlDSN converts a mysql:// URL to the Go driver's DSN (no database when dbName is "").
func mysqlDSN(u *url.URL, dbName string) string {
	host := u.Host
	if !strings.Contains(host, ":") {
		host += ":3306"
	}
	dsn := ""
	if u.User != nil {
		dsn = u.User.Username()
		if pw, ok := u.User.Password(); ok {
			dsn += ":" + pw
		}
		dsn += "@"
	}
	dsn += "tcp(" + host + ")/" + dbName
	if u.RawQuery != "" {
		dsn += "?" + u.RawQuery
	}
	return dsn
}

func TestThreeBoxesMySQL(t *testing.T) {
	admin := os.Getenv("AGENTBOX_TEST_MYSQL_URL")
	if admin == "" {
		t.Skip("set AGENTBOX_TEST_MYSQL_URL to run the MySQL e2e test")
	}
	for _, tool := range []string{"mysql", "mysqldump"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not on PATH", tool)
		}
	}
	e := newEnv(t)
	repo := copyExample(t, "node-mysql")
	u, err := url.Parse(admin)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := sql.Open("mysql", mysqlDSN(u, ""))
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("abe2e_%d", time.Now().UnixNano()%1_000_000_000)
	for _, q := range []string{"CREATE DATABASE `" + name + "`", "CREATE TABLE `" + name + "`.items(name VARCHAR(20))", "INSERT INTO `" + name + "`.items VALUES('one')"} {
		if _, err := conn.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		rows, err := conn.Query("SHOW DATABASES")
		if err != nil {
			t.Logf("cleanup: SHOW DATABASES: %v", err)
		} else {
			var dbs []string
			for rows.Next() {
				var d string
				rows.Scan(&d)
				if strings.HasPrefix(d, name) {
					dbs = append(dbs, d)
				}
			}
			rows.Close()
			for _, d := range dbs {
				conn.Exec("DROP DATABASE IF EXISTS `" + d + "`")
			}
		}
		conn.Close()
	})
	src := *u
	src.Path = "/" + name
	cfgPath := filepath.Join(repo, "agentbox.yaml")
	cfg, _ := os.ReadFile(cfgPath)
	os.WriteFile(cfgPath, []byte(strings.Replace(string(cfg), "mysql://root:root@127.0.0.1:3306/myapp", src.String(), 1)), 0o644)

	threeBoxes(t, e, repo, func(b api.BoxView, resp map[string]any) {
		if want := name + "_agentbox_" + b.Name; resp["db"] != want {
			t.Errorf("box %s got database %v, want %s", b.Name, resp["db"], want)
		}
	})
}

func composeProjects(t *testing.T, prefix string) []string {
	t.Helper()
	out, err := exec.Command("docker", "compose", "ls", "--all", "--format", "json").Output()
	if err != nil {
		t.Fatalf("docker compose ls: %v", err)
	}
	var rows []struct{ Name string }
	json.Unmarshal(out, &rows)
	var names []string
	for _, r := range rows {
		if strings.HasPrefix(r.Name, prefix) {
			names = append(names, r.Name)
		}
	}
	return names
}

func TestComposeRedis(t *testing.T) {
	if os.Getenv("AGENTBOX_TEST_DOCKER") != "1" {
		t.Skip("set AGENTBOX_TEST_DOCKER=1 (with a running Docker daemon) to run the Compose e2e test")
	}
	e := newEnv(t)
	repo := copyExample(t, "compose-redis")
	t.Cleanup(func() {
		_, _ = e.runErr(repo, "rm", "--force", "a")
		_, _ = e.runErr(repo, "rm", "--force", "b")
	})
	e.run(repo, "new", "a")
	e.run(repo, "new", "b")
	var boxes []api.BoxView
	if err := json.Unmarshal([]byte(e.run(repo, "ls", "--json")), &boxes); err != nil || len(boxes) != 2 {
		t.Fatalf("ls --json: %v (%d boxes)", err, len(boxes))
	}
	byName := map[string]api.BoxView{}
	for _, b := range boxes {
		byName[b.Name] = b
	}
	prefix := "agentbox-" + boxes[0].Repo + "-"
	if got := composeProjects(t, prefix); len(got) != 2 {
		t.Fatalf("compose projects: %v", got)
	}
	a := fmt.Sprintf("http://127.0.0.1:%d/", byName["a"].Ports["APP_PORT"])
	b := fmt.Sprintf("http://127.0.0.1:%d/", byName["b"].Ports["APP_PORT"])
	getJSON(t, a)
	if r := getJSON(t, a); r["hits"] != float64(2) || r["box"] != "a" {
		t.Fatalf("box a second hit: %v", r)
	}
	if r := getJSON(t, b); r["hits"] != float64(1) || r["box"] != "b" {
		t.Fatalf("box b has its own redis: %v", r)
	}
	e.run(repo, "stop", "a")
	label := "label=com.docker.compose.project=" + prefix + "a"
	if out, err := exec.Command("docker", "ps", "-q", "--filter", label).Output(); err != nil || strings.TrimSpace(string(out)) != "" {
		t.Fatalf("containers still running after stop: %q %v", out, err)
	}
	e.run(repo, "start", "a")
	if r := getJSON(t, a); r["hits"] != float64(3) || r["box"] != "a" {
		t.Fatalf("box a after stop/start must keep its redis state: %v", r)
	}
	e.run(repo, "rm", "--force", "a")
	e.run(repo, "rm", "--force", "b")
	if got := composeProjects(t, prefix); len(got) != 0 {
		t.Fatalf("projects left after rm: %v", got)
	}
	if out := e.run(repo, "doctor"); !strings.Contains(out, "no problems found") {
		t.Errorf("doctor after cleanup:\n%s", out)
	}
}
