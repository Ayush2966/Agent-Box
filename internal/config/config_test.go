package config

import (
	"strings"
	"testing"
	"time"
)

func TestParseValid(t *testing.T) {
	c, err := Parse([]byte(`
version: 1
setup: npm install
dev: npm run dev
ports: [APP_PORT, API_PORT]
db:
  driver: postgres
  url_env: DATABASE_URL
  source: postgres://localhost:5432/myapp
env:
  template: .env.example
  set:
    NODE_ENV: development
copy: [.env.local]
`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Env.File != ".env" {
		t.Errorf("Env.File = %q, want default .env", c.Env.File)
	}
	if len(c.Ports) != 2 || c.DB.URLEnv != "DATABASE_URL" || c.Env.Set["NODE_ENV"] != "development" || c.Copy[0] != ".env.local" {
		t.Errorf("unexpected config: %+v", c)
	}
}

func TestParseErrors(t *testing.T) {
	cases := []struct{ name, yaml, want string }{
		{"bad version", "version: 2", "version must be 1"},
		{"missing version", "dev: x", "version must be 1"},
		{"unknown key", "version: 1\nport: [A]", `unknown key "port" (did you mean "ports"?)`},
		{"unknown nested key", "version: 1\ndb:\n  drivr: sqlite", `unknown key "db.drivr" (did you mean "driver"?)`},
		{"bad port name", "version: 1\nports: [app-port]", `"app-port" is not a valid env var name`},
		{"duplicate port", "version: 1\nports: [A, A]", `"A" listed twice`},
		{"bad driver", "version: 1\ndb: {driver: redis, url_env: X, source: y}", "db.driver must be"},
		{"bad mongo source", "version: 1\ndb: {driver: mongo, url_env: X, source: y}", "db.source for mongo must look like"},
		{"url_env in ports", "version: 1\nports: [X]\ndb: {driver: sqlite, url_env: X, source: a.db}", "also listed in ports"},
		{"missing source", "version: 1\ndb: {driver: sqlite, url_env: X}", "db.source is required"},
		{"escaping copy path", "version: 1\ncopy: [../secret]", "must be a relative path inside the repo"},
		{"absolute template", "version: 1\nenv: {template: /etc/passwd}", "must be a relative path inside the repo"},
		{"escaping sqlite source", "version: 1\ndb: {driver: sqlite, url_env: X, source: 'file:../x.db'}", "must be a relative path inside the repo"},
		{"bad set key", "version: 1\nenv: {set: {'A-B': x}}", `env.set: "A-B" is not a valid env var name`},
		{"empty file", "", "is empty"},
		{"bad link name", "version: 1\nlinks: {'a-b': api}", `links: "a-b" is not a valid env var name`},
		{"link clashes with port", "version: 1\nports: [X]\nlinks: {X: api}", `links: "X" is already a port`},
		{"link clashes with set", "version: 1\nenv: {set: {X: y}}\nlinks: {X: api}", `links: "X" is also in env.set`},
		{"bad link kind", "version: 1\nlinks: {X: 'api:host'}", "must end in :url, :port or :addr"},
		{"empty link repo", "version: 1\nlinks: {X: ':port'}", "the repo name is empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.yaml))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "run `agentbox init`") {
		t.Fatalf("err = %v", err)
	}
}

func TestSQLitePathStripsFilePrefix(t *testing.T) {
	c, err := Parse([]byte("version: 1\ndb: {driver: sqlite, url_env: DATABASE_URL, source: 'file:./data/dev.db'}"))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.DB.Path(); got != "data/dev.db" {
		t.Fatalf("Path() = %q", got)
	}
}

func TestProxyKey(t *testing.T) {
	c, err := Parse([]byte("version: 1\nports: [APP_PORT, API_PORT]\nproxy: API_PORT\n"))
	if err != nil || c.Proxy != "API_PORT" || c.ProxyPort() != "API_PORT" {
		t.Fatalf("explicit proxy: %+v %v", c, err)
	}
	c, err = Parse([]byte("version: 1\nports: [APP_PORT, API_PORT]\n"))
	if err != nil || c.ProxyPort() != "APP_PORT" {
		t.Fatalf("default proxy should be the first port: %+v %v", c, err)
	}
	c, err = Parse([]byte("version: 1\n"))
	if err != nil || c.ProxyPort() != "" {
		t.Fatalf("no ports: %q %v", c.ProxyPort(), err)
	}
	_, err = Parse([]byte("version: 1\nports: [APP_PORT]\nproxy: WEB_PORT\n"))
	if err == nil || !strings.Contains(err.Error(), "proxy") || !strings.Contains(err.Error(), "WEB_PORT") {
		t.Fatalf("proxy not in ports: %v", err)
	}
	_, err = Parse([]byte("version: 1\nports: [APP_PORT]\nproxi: APP_PORT\n"))
	if err == nil || !strings.Contains(err.Error(), `did you mean "proxy"`) {
		t.Fatalf("typo suggestion: %v", err)
	}
}

func TestComposeKeys(t *testing.T) {
	c, err := Parse([]byte("version: 1\nports: [APP_PORT, REDIS_PORT]\ncompose:\n  file: deploy/compose.yaml\n  services: [redis, db]\n  wait: 90s\n"))
	if err != nil || c.Compose == nil || c.Compose.File != "deploy/compose.yaml" || len(c.Compose.Services) != 2 || c.Compose.WaitTimeout() != 90*time.Second {
		t.Fatalf("%+v %v", c.Compose, err)
	}
	c, err = Parse([]byte("version: 1\ncompose: {}\n"))
	if err != nil || c.Compose == nil || c.Compose.File != "" || c.Compose.WaitTimeout() != 60*time.Second {
		t.Fatalf("defaults: %+v %v", c.Compose, err)
	}
	for _, bad := range []string{
		"version: 1\ncompose:\n  file: ../x/compose.yaml\n",
		"version: 1\ncompose:\n  services: ['bad name']\n",
		"version: 1\ncompose:\n  wait: 500ms\n",
		"version: 1\ncompose:\n  wait: soon\n",
		"version: 1\ncompose:\n  servcies: [redis]\n",
	} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if _, err := Parse([]byte("version: 1\ncompose:\n  servcies: [redis]\n")); err == nil || !strings.Contains(err.Error(), `did you mean "services"`) {
		t.Fatalf("typo suggestion: %v", err)
	}
}

func TestMySQLDriverConfig(t *testing.T) {
	c, err := Parse([]byte("version: 1\ndb:\n  driver: mysql\n  url_env: DATABASE_URL\n  source: mysql://app:secret@127.0.0.1:3306/myapp\n"))
	if err != nil || c.DB.Driver != "mysql" {
		t.Fatalf("%+v %v", c.DB, err)
	}
	if _, err := Parse([]byte("version: 1\ndb:\n  driver: mysql\n  url_env: DATABASE_URL\n  source: mariadb://app@db/myapp\n")); err != nil {
		t.Fatalf("mariadb scheme: %v", err)
	}
	for _, bad := range []string{
		"mysql://app@127.0.0.1:3306/",         // no database
		"mysql:///myapp",                      // no host
		"postgres://app@127.0.0.1:5432/myapp", // wrong scheme
		"127.0.0.1:3306/myapp",                // not a URL
	} {
		if _, err := Parse([]byte("version: 1\ndb:\n  driver: mysql\n  url_env: DATABASE_URL\n  source: " + bad + "\n")); err == nil || !strings.Contains(err.Error(), "db.source") {
			t.Errorf("accepted %q (%v)", bad, err)
		}
	}
	if _, err := Parse([]byte("version: 1\ndb:\n  driver: oracle\n  url_env: X\n  source: y\n")); err == nil || !strings.Contains(err.Error(), `"mysql"`) {
		t.Fatalf("driver list in the error: %v", err)
	}
}

func TestLinks(t *testing.T) {
	c, err := Parse([]byte("version: 1\nports: [WEB_PORT]\nlinks:\n  API_URL: pl-api-admin-service\n  API_PORT: pl-api-admin-service:port\n  API_ADDR: 'pl-api-admin-service-1a2b3c:addr'\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Links) != 3 {
		t.Fatalf("links: %+v", c.Links)
	}
	for spec, want := range map[string]Link{
		"api":            {Repo: "api", Kind: "url"},
		"api:url":        {Repo: "api", Kind: "url"},
		"api:port":       {Repo: "api", Kind: "port"},
		" api-1a2b:addr": {Repo: "api-1a2b", Kind: "addr"},
	} {
		got, err := ParseLink(spec)
		if err != nil || got != want {
			t.Errorf("ParseLink(%q) = %+v, %v; want %+v", spec, got, err, want)
		}
	}
}
