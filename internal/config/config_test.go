package config

import (
	"strings"
	"testing"
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
		{"bad driver", "version: 1\ndb: {driver: mongo, url_env: X, source: y}", "db.driver must be"},
		{"url_env in ports", "version: 1\nports: [X]\ndb: {driver: sqlite, url_env: X, source: a.db}", "also listed in ports"},
		{"missing source", "version: 1\ndb: {driver: sqlite, url_env: X}", "db.source is required"},
		{"escaping copy path", "version: 1\ncopy: [../secret]", "must be a relative path inside the repo"},
		{"absolute template", "version: 1\nenv: {template: /etc/passwd}", "must be a relative path inside the repo"},
		{"escaping sqlite source", "version: 1\ndb: {driver: sqlite, url_env: X, source: 'file:../x.db'}", "must be a relative path inside the repo"},
		{"bad set key", "version: 1\nenv: {set: {'A-B': x}}", `env.set: "A-B" is not a valid env var name`},
		{"empty file", "", "is empty"},
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
