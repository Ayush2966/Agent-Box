package initcfg

import (
	"slices"
	"strings"
	"testing"

	"github.com/agentbox-dev/agentbox/internal/config"
	"github.com/agentbox-dev/agentbox/internal/testutil"
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
	if cfg.Setup != "pnpm install" || cfg.Dev != "pnpm run dev" {
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

func TestDetectPrefersRealEnvValue(t *testing.T) {
	_, cfg := detect(t, map[string]string{
		".env.example": "DATABASE_URL=postgres://user:pass@localhost/changeme\n",
		".env":         "DATABASE_URL=postgres://me@localhost:5432/realdb\n",
	})
	if cfg.DB == nil || cfg.DB.Source != "postgres://me@localhost:5432/realdb" {
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
	if cfg.Setup != "go mod download" || cfg.Dev != "go run ." || len(cfg.Ports) != 0 {
		t.Errorf("go: %+v", cfg)
	}
	_, cfg = detect(t, nil)
	if cfg.Setup != "" || cfg.DB != nil {
		t.Errorf("empty: %+v", cfg)
	}
}
