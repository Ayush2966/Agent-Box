package initcfg

import (
	"slices"
	"strings"
	"testing"

	"github.com/Ayush2966/Agent-Box/internal/config"
	"github.com/Ayush2966/Agent-Box/internal/testutil"
)

func detect(t *testing.T, files map[string]string) (Result, *config.Config) {
	t.Helper()
	dir := t.TempDir()
	testutil.WriteFiles(t, dir, files)
	res, err := Detect(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Parse([]byte(res.YAML))
	if err != nil {
		t.Fatalf("generated YAML does not parse: %v\n%s", err, res.YAML)
	}
	return res, cfg
}

func TestDetectNodePostgres(t *testing.T) {
	res, cfg := detect(t, map[string]string{
		"package.json":   `{"scripts": {"dev": "vite --port 5173"}}`,
		"pnpm-lock.yaml": "",
		".env.example":   "PORT=3000\nAPI_PORT=4000\nDATABASE_URL=postgres://u:p@localhost:5432/app\n",
	})
	// Vite preset, with the template's own port name and pnpm's frozen install.
	if cfg.Setup != "pnpm install --frozen-lockfile" || cfg.Dev != `pnpm run dev -- --port "$PORT" --strictPort` {
		t.Errorf("setup/dev = %q / %q", cfg.Setup, cfg.Dev)
	}
	if !slices.Equal(cfg.Ports, []string{"PORT", "API_PORT"}) {
		t.Errorf("ports = %v", cfg.Ports)
	}
	if cfg.DB == nil || cfg.DB.Driver != "postgres" || cfg.DB.URLEnv != "DATABASE_URL" || cfg.DB.Source != "postgres://u:p@localhost:5432/app" {
		t.Errorf("db = %+v", cfg.DB)
	}
	if cfg.Env.Template != ".env.example" {
		t.Errorf("template = %q", cfg.Env.Template)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "hard-codes a port") {
		t.Errorf("warnings = %v", res.Warnings)
	}
}

// This used to assert the opposite -- that init prefers the real .env value
// for db.source. That copied a live credential into a committed file, which
// spec §5 and §13 forbid. The template's placeholder is what ships.
func TestDetectTakesSourceFromTemplateNotRealEnv(t *testing.T) {
	_, cfg := detect(t, map[string]string{
		".env.example": "DATABASE_URL=postgres://user:pass@localhost/changeme\n",
		".env":         "DATABASE_URL=postgres://me@localhost:5432/realdb\n",
	})
	if cfg.DB == nil || cfg.DB.Source != "postgres://user:pass@localhost/changeme" {
		t.Errorf("db = %+v", cfg.DB)
	}
}

func TestDetectSQLite(t *testing.T) {
	_, cfg := detect(t, map[string]string{".env.example": "DATABASE_URL=file:./dev.db\n"})
	if cfg.DB == nil || cfg.DB.Driver != "sqlite" || cfg.DB.Path() != "dev.db" {
		t.Errorf("db = %+v", cfg.DB)
	}
}

func TestDetectGoAndEmpty(t *testing.T) {
	_, cfg := detect(t, map[string]string{"go.mod": "module x\n"})
	if cfg.Setup != "go mod download" || cfg.Dev != "go run ." || !slices.Equal(cfg.Ports, []string{"PORT"}) {
		t.Errorf("go: %+v", cfg)
	}
	_, cfg = detect(t, nil)
	if cfg.Setup != "" || cfg.DB != nil {
		t.Errorf("empty: %+v", cfg)
	}
}

// agentbox.yaml is committed (spec §5), and spec §13 has init read
// .env.example — not the developer's real, gitignored .env. Detecting the
// driver from .env is fine; copying a value out of it into the generated
// YAML would push a live credential to the repo.
func TestDetectNeverCopiesSecretsFromRealEnv(t *testing.T) {
	const secret = "sup3rs3cret"
	res, cfg := detect(t, map[string]string{
		"package.json": `{"scripts": {"dev": "next dev"}}`,
		".env.example": "APP_PORT=3000\nDATABASE_URL=postgres://user:pass@localhost:5432/myapp\n",
		".env":         "APP_PORT=3000\nDATABASE_URL=postgres://app:" + secret + "@prod.internal:5432/myapp\n",
	})
	for _, leak := range []string{secret, "prod.internal", "app:" + secret} {
		if strings.Contains(res.YAML, leak) {
			t.Fatalf("agentbox init copied %q out of .env into the committed agentbox.yaml:\n%s", leak, res.YAML)
		}
	}
	// Detection itself must still work: the driver is knowable from the
	// scheme alone, which carries no secret.
	if cfg.DB == nil || cfg.DB.Driver != "postgres" || cfg.DB.URLEnv != "DATABASE_URL" {
		t.Fatalf("driver detection regressed: %+v", cfg.DB)
	}
}

// A real .env that reveals a different driver than .env.example must still
// drive detection -- just without its value reaching the file.
func TestDetectUsesRealEnvForDriverOnly(t *testing.T) {
	res, cfg := detect(t, map[string]string{
		".env.example": "DATABASE_URL=\n",
		".env":         "DATABASE_URL=postgres://app:pw@db.internal:5432/myapp\n",
	})
	if strings.Contains(res.YAML, "pw@db.internal") {
		t.Fatalf("leaked .env value:\n%s", res.YAML)
	}
	if cfg.DB != nil && cfg.DB.Source != "" && strings.Contains(cfg.DB.Source, "db.internal") {
		t.Fatalf("db.source came from .env: %q", cfg.DB.Source)
	}
	if len(res.Warnings) == 0 {
		t.Fatal("expected a warning telling the user to fill in db.source by hand")
	}
}

func TestDetectHintsProxyWhenSeveralPorts(t *testing.T) {
	res, _ := detect(t, map[string]string{
		"package.json": `{"scripts": {"dev": "node server.js"}}`,
		".env.example": "PORT=3000\nAPI_PORT=4000\n",
	})
	if !strings.Contains(res.YAML, "# proxy: PORT") {
		t.Fatalf("no proxy hint:\n%s", res.YAML)
	}
	res, _ = detect(t, map[string]string{
		"package.json": `{"scripts": {"dev": "node server.js"}}`,
		".env.example": "PORT=3000\n",
	})
	if strings.Contains(res.YAML, "proxy") {
		t.Fatalf("proxy hint with one port:\n%s", res.YAML)
	}
}

func TestDetectMySQL(t *testing.T) {
	_, cfg := detect(t, map[string]string{
		"package.json": `{"scripts": {"dev": "node server.js"}}`,
		".env.example": "PORT=3000\nDATABASE_URL=mysql://app:secret@127.0.0.1:3306/app\n",
	})
	if cfg.DB == nil || cfg.DB.Driver != "mysql" || cfg.DB.Source != "mysql://app:secret@127.0.0.1:3306/app" {
		t.Fatalf("db = %+v", cfg.DB)
	}
}

func TestDetectComposeContainerName(t *testing.T) {
	res, _ := detect(t, map[string]string{
		"package.json": `{"scripts": {"dev": "node server.js"}}`,
		".env.example": "PORT=3000\n",
		"compose.yaml": "services:\n  redis:\n    image: redis:7\n    container_name: my-redis\n",
	})
	if !strings.Contains(strings.Join(res.Warnings, "\n"), "sets container_name") {
		t.Fatalf("want a container_name warning, got %v", res.Warnings)
	}
	res, _ = detect(t, map[string]string{
		"package.json": `{"scripts": {"dev": "node server.js"}}`,
		".env.example": "PORT=3000\n",
		"compose.yaml": "services:\n  redis:\n    image: redis:7\n",
	})
	if strings.Contains(strings.Join(res.Warnings, "\n"), "container_name") {
		t.Fatalf("unexpected warning: %v", res.Warnings)
	}
}

func TestDetectComposeFile(t *testing.T) {
	res, cfg := detect(t, map[string]string{
		"package.json": `{"scripts": {"dev": "node server.js"}}`,
		".env.example": "PORT=3000\n",
		"compose.yaml": "services:\n  redis:\n    image: redis:7\n    ports: [\"6379:6379\"]\n",
	})
	if cfg.Compose == nil || cfg.Compose.File != "compose.yaml" {
		t.Fatalf("compose = %+v\n%s", cfg.Compose, res.YAML)
	}
	if len(res.Warnings) == 0 || !strings.Contains(strings.Join(res.Warnings, "\n"), "6379:6379") {
		t.Fatalf("want a fixed-host-port warning, got %v", res.Warnings)
	}
	res, cfg = detect(t, map[string]string{
		"package.json":       `{"scripts": {"dev": "node server.js"}}`,
		".env.example":       "PORT=3000\nREDIS_PORT=6379\n",
		"docker-compose.yml": "services:\n  redis:\n    image: redis:7\n    ports: [\"${REDIS_PORT}:6379\"]\n",
	})
	if cfg.Compose == nil || cfg.Compose.File != "docker-compose.yml" {
		t.Fatalf("compose = %+v", cfg.Compose)
	}
	for _, w := range res.Warnings {
		if strings.Contains(w, "host port") {
			t.Fatalf("no warning expected for an interpolated port: %v", res.Warnings)
		}
	}
}

// Each framework gets the flag or env var that makes its dev server take the
// port agentbox allocates, and a lockfile makes setup reproducible.
func TestDetectStackPresets(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		setup string
		dev   string
		ports []string
		warn  string
	}{
		{"vite", map[string]string{"package.json": `{"scripts":{"dev":"vite"},"devDependencies":{"vite":"^6"}}`, "package-lock.json": "{}"},
			"npm ci", `npm run dev -- --port "$APP_PORT" --strictPort`, []string{"APP_PORT"}, ""},
		{"next", map[string]string{"package.json": `{"scripts":{"dev":"next dev --turbopack"},"dependencies":{"next":"15"}}`},
			"npm install", `npm run dev -- -p "$APP_PORT"`, []string{"APP_PORT"}, ""},
		{"webpack", map[string]string{"package.json": `{"scripts":{"start":"webpack serve --config scripts/webpack.dev.js"}}`, "yarn.lock": ""},
			"yarn install --frozen-lockfile", `yarn run start -- --port "$APP_PORT"`, []string{"APP_PORT"}, ""},
		{"cra", map[string]string{"package.json": `{"scripts":{"start":"react-scripts start"},"dependencies":{"react-scripts":"5"}}`},
			"npm install", "npm run start", []string{"PORT"}, ""},
		{"nest start:dev", map[string]string{"package.json": `{"scripts":{"start":"nest start","start:dev":"nest start --watch"},"dependencies":{"@nestjs/core":"10"}}`,
			"src/main.ts": "await app.listen(process.env.PORT);\n"},
			"npm install", "npm run start:dev", []string{"PORT"}, ""},
		{"nest fixed port", map[string]string{"package.json": `{"scripts":{"start":"nest start"},"dependencies":{"@nestjs/core":"10"}}`,
			"src/main.ts": "await app.listen(3000);\n"},
			"npm install", "npm run start", []string{"PORT"}, "src/main.ts listens on a fixed port"},
		{"express", map[string]string{"package.json": `{"scripts":{"dev":"nodemon server.js"}}`, "server.js": "app.listen(3000)\n"},
			"npm install", "npm run dev", []string{"PORT"}, "server.js listens on a fixed port"},
		{"go cmd", map[string]string{"go.mod": "module x\n", "cmd/server/main.go": "package main\nfunc main(){}\n"},
			"go mod download", "go run ./cmd/server", []string{"PORT"}, ""},
		{"rails", map[string]string{"Gemfile": "", "bin/rails": "#!/usr/bin/env ruby\n"},
			"bundle install", `bin/rails server -p "$APP_PORT"`, []string{"APP_PORT"}, ""},
		{"django", map[string]string{"manage.py": "", "requirements.txt": "django\n"},
			"pip install -r requirements.txt", `python manage.py runserver "$APP_PORT"`, []string{"APP_PORT"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, cfg := detect(t, c.files)
			if cfg.Setup != c.setup || cfg.Dev != c.dev || !slices.Equal(cfg.Ports, c.ports) {
				t.Fatalf("setup=%q dev=%q ports=%v\n%s", cfg.Setup, cfg.Dev, cfg.Ports, res.YAML)
			}
			if c.warn != "" && !slices.ContainsFunc(res.Warnings, func(w string) bool { return strings.Contains(w, c.warn) }) {
				t.Fatalf("warnings %v, want one containing %q", res.Warnings, c.warn)
			}
			if c.warn == "" && len(res.Warnings) != 0 {
				t.Fatalf("unexpected warnings %v", res.Warnings)
			}
		})
	}
}

func TestDetectHints(t *testing.T) {
	res, _ := detect(t, map[string]string{"package.json": `{"scripts":{"dev":"vite"}}`, ".env.local": "VITE_API=x\n"})
	for _, want := range []string{"# env: { template: .env.local, file: .env.local }", "# links:"} {
		if !strings.Contains(res.YAML, want) {
			t.Fatalf("missing hint %q:\n%s", want, res.YAML)
		}
	}
}
