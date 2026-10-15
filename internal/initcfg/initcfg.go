// Package initcfg inspects a repo and drafts an agentbox.yaml for `agentbox init`.
package initcfg

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/Ayush2966/Agent-Box/internal/compose"
	"github.com/Ayush2966/Agent-Box/internal/envfile"
)

type Result struct {
	YAML     string
	Warnings []string
}

var (
	portKey       = regexp.MustCompile(`(^|_)PORT$`)
	dbKey         = regexp.MustCompile(`^(DATABASE_URL|DB_URL|POSTGRES_URL|DATABASE_PATH|SQLITE_PATH|MONGO_URI|MONGODB_URI|MONGO_URL|MONGODB_URL)$`)
	fixedHostPort = regexp.MustCompile(`"?\b(\d{2,5}):(\d{2,5})\b"?`)
	containerName = regexp.MustCompile(`(?m)^\s*container_name:`)
	hardPort      = regexp.MustCompile(`(--port[ =]|-p ?|PORT=)\d{2,5}`)
)

func exists(root, name string) bool {
	_, err := os.Stat(filepath.Join(root, name))
	return err == nil
}

func nodePM(root string) string {
	switch {
	case exists(root, "pnpm-lock.yaml"):
		return "pnpm"
	case exists(root, "yarn.lock"):
		return "yarn"
	case exists(root, "bun.lockb"), exists(root, "bun.lock"):
		return "bun"
	}
	return "npm"
}

// driverOf names the agentbox driver a connection string belongs to. It looks
// only at the scheme or the file suffix, never at anything secret.
func driverOf(v string) string {
	switch {
	case strings.HasPrefix(v, "postgres://"), strings.HasPrefix(v, "postgresql://"):
		return "postgres"
	case strings.HasPrefix(v, "mysql://"), strings.HasPrefix(v, "mariadb://"):
		return "mysql"
	case strings.HasPrefix(v, "mongodb://"), strings.HasPrefix(v, "mongodb+srv://"):
		return "mongo"
	case strings.HasPrefix(v, "file:"), strings.HasSuffix(v, ".db"), strings.HasSuffix(v, ".sqlite"), strings.HasSuffix(v, ".sqlite3"):
		return "sqlite"
	}
	return ""
}

// stack is what Detect learned about how the repo is run.
type stack struct {
	setup, dev string
	portVar    string // the env var the dev command passes the port in, when the template names none
	devNote    string // one-line comment printed under dev
	warnings   []string
}

type pkgJSON struct {
	Scripts         map[string]string `json:"scripts"`
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
}

func (p pkgJSON) has(dep string) bool {
	_, a := p.Dependencies[dep]
	_, b := p.DevDependencies[dep]
	return a || b
}

var listenLiteral = regexp.MustCompile(`\.listen\(\s*\d{2,5}\b`)

// detectNode picks setup and dev for a package.json repo. Each framework gets
// the flag that makes its dev server take the port agentbox allocates.
func detectNode(root string, pkg pkgJSON) stack {
	pm := nodePM(root)
	st := stack{setup: pm + " install", portVar: "PORT"}
	switch {
	case pm == "npm" && exists(root, "package-lock.json"):
		st.setup = "npm ci"
	case pm == "pnpm" && exists(root, "pnpm-lock.yaml"):
		st.setup = "pnpm install --frozen-lockfile"
	case pm == "yarn" && exists(root, "yarn.lock"):
		st.setup = "yarn install --frozen-lockfile"
	case pm == "bun":
		st.setup = "bun install --frozen-lockfile"
	}
	name, script := "", ""
	for _, n := range []string{"dev", "start:dev", "start", "serve"} {
		if s, ok := pkg.Scripts[n]; ok {
			name, script = n, s
			break
		}
	}
	if name == "" {
		return st
	}
	run := pm + " run " + name
	if hardPort.MatchString(script) {
		st.warnings = append(st.warnings, fmt.Sprintf("the %s script hard-codes a port (%q); remove it, agentbox passes the port", name, script))
	}
	switch {
	case pkg.has("vite") || strings.Contains(script, "vite"):
		st.dev, st.portVar = run+` -- --port "$APP_PORT" --strictPort`, "APP_PORT"
		st.devNote = "Vite takes the port from the flag; the proxy reaches it on 127.0.0.1 or [::1]"
	case pkg.has("next") || strings.Contains(script, "next "):
		st.dev, st.portVar = run+` -- -p "$APP_PORT"`, "APP_PORT"
	case strings.Contains(script, "webpack serve") || strings.Contains(script, "webpack-dev-server"):
		st.dev, st.portVar = run+` -- --port "$APP_PORT"`, "APP_PORT"
		st.devNote = "the flag overrides devServer.port in the webpack config"
	case strings.Contains(script, "react-scripts start"):
		st.dev, st.portVar = run, "PORT"
		st.devNote = "react-scripts reads PORT from the env"
	case pkg.has("@angular/core") || strings.Contains(script, "ng serve"):
		st.dev, st.portVar = run+` -- --port "$APP_PORT"`, "APP_PORT"
	case pkg.has("nuxt") || pkg.has("astro") || pkg.has("@remix-run/dev"):
		st.dev, st.portVar = run+` -- --port "$APP_PORT"`, "APP_PORT"
	case pkg.has("@nestjs/core"):
		if _, ok := pkg.Scripts["start:dev"]; ok && name != "start:dev" {
			run = pm + " run start:dev"
		}
		st.dev, st.portVar = run, "PORT"
		st.devNote = "NestJS: main.ts must call app.listen(process.env.PORT)"
		st.warnings = append(st.warnings, listenWarnings(root, "src/main.ts")...)
	default:
		st.dev, st.portVar = run, "PORT"
		st.devNote = "the server must read its port from the env (process.env.PORT)"
		st.warnings = append(st.warnings, listenWarnings(root, "server.js", "index.js", "app.js", "src/index.ts", "src/server.ts", "src/index.js", "src/server.js")...)
	}
	return st
}

// listenWarnings flags entry files that bind a literal port.
func listenWarnings(root string, files ...string) []string {
	for _, f := range files {
		data, err := os.ReadFile(filepath.Join(root, f))
		if err != nil {
			continue
		}
		if listenLiteral.Match(data) && !strings.Contains(string(data), "process.env") {
			return []string{fmt.Sprintf("%s listens on a fixed port; use process.env.PORT so boxes don't collide", f)}
		}
	}
	return nil
}

// detectGo prefers a single cmd/<name> entrypoint, then a root main package.
func detectGo(root string) stack {
	st := stack{setup: "go mod download", dev: "go run .", portVar: "PORT",
		devNote: "the server must read its port from the env (os.Getenv(\"PORT\"))"}
	if exists(root, "main.go") {
		return st
	}
	if exists(root, filepath.Join("cmd", "main.go")) {
		st.dev = "go run ./cmd"
		return st
	}
	cmds, _ := filepath.Glob(filepath.Join(root, "cmd", "*", "main.go"))
	var names []string
	for _, c := range cmds {
		names = append(names, filepath.Base(filepath.Dir(c)))
	}
	switch {
	case len(names) == 1:
		st.dev = "go run ./cmd/" + names[0]
	case len(names) > 1:
		pick := names[0]
		for _, n := range names { // the server-looking one wins
			if slices.Contains([]string{"server", "serve", "api", "daemon", "app", "web", "main"}, n) {
				pick = n
				break
			}
		}
		st.dev = "go run ./cmd/" + pick
		if !slices.Contains([]string{"server", "serve", "api", "daemon", "app", "web", "main"}, pick) {
			st.warnings = append(st.warnings, fmt.Sprintf("several commands under cmd/ (%s); dev runs %s, change it if that is not the server", strings.Join(names, ", "), pick))
		}
	}
	return st
}

func detectStack(root string, r *Result) stack {
	if data, err := os.ReadFile(filepath.Join(root, "package.json")); err == nil {
		var pkg pkgJSON
		if err := json.Unmarshal(data, &pkg); err != nil {
			r.Warnings = append(r.Warnings, fmt.Sprintf("package.json is not valid JSON: %v", err))
		}
		return detectNode(root, pkg)
	}
	if exists(root, "go.mod") {
		return detectGo(root)
	}
	if exists(root, "Gemfile") && exists(root, "bin/rails") {
		return stack{setup: "bundle install", dev: `bin/rails server -p "$APP_PORT"`, portVar: "APP_PORT"}
	}
	if exists(root, "manage.py") {
		st := stack{setup: "pip install -e .", dev: `python manage.py runserver "$APP_PORT"`, portVar: "APP_PORT"}
		if exists(root, "requirements.txt") {
			st.setup = "pip install -r requirements.txt"
		}
		return st
	}
	if exists(root, "pyproject.toml") || exists(root, "requirements.txt") {
		st := stack{setup: "pip install -e .", portVar: "PORT"}
		if exists(root, "requirements.txt") {
			st.setup = "pip install -r requirements.txt"
		}
		return st
	}
	return stack{}
}

func Detect(root string) (Result, error) {
	var r Result
	st := detectStack(root, &r)
	setup, dev := st.setup, st.dev
	r.Warnings = append(r.Warnings, st.warnings...)

	tmpl := ""
	for _, c := range []string{".env.example", ".env.sample", ".env.template"} {
		if exists(root, c) {
			tmpl = c
			break
		}
	}
	var vars []envfile.KV
	if tmpl != "" {
		data, err := os.ReadFile(filepath.Join(root, tmpl))
		if err != nil {
			return r, err
		}
		vars = envfile.Parse(data)
	}
	// The developer's real .env is read ONLY to recognise which database they
	// use. agentbox.yaml is committed (spec §5) and spec §13 has init read
	// .env.example, so no value out of a gitignored .env may ever reach the
	// generated file -- that would push a live credential to the repo.
	real := map[string]string{}
	if data, err := os.ReadFile(filepath.Join(root, ".env")); err == nil {
		for _, kv := range envfile.Parse(data) {
			real[kv.Key] = kv.Value
		}
	}

	var portKeys []string
	urlEnv, source := "", ""
	for _, kv := range vars {
		if portKey.MatchString(kv.Key) {
			portKeys = append(portKeys, kv.Key)
		}
		if urlEnv == "" && dbKey.MatchString(kv.Key) {
			urlEnv, source = kv.Key, kv.Value
		}
	}
	// source comes from the template; the real value only breaks a tie on the
	// driver, and is then deliberately left out of the generated YAML.
	driver := driverOf(source)
	if driver == "" && urlEnv != "" {
		if driver = driverOf(real[urlEnv]); driver != "" {
			source = ""
		}
	}
	if driver == "" && urlEnv != "" {
		r.Warnings = append(r.Warnings, fmt.Sprintf("found %s but couldn't tell which database it points to; add a db section by hand", urlEnv))
	}

	var b strings.Builder
	b.WriteString("# Generated by `agentbox init`. Review it before committing.\n")
	b.WriteString("# Reference: https://github.com/Ayush2966/Agent-Box#configuration\n")
	b.WriteString("version: 1\n")
	line := func(key, val, hint string) {
		if val != "" {
			fmt.Fprintf(&b, "%s: %s\n", key, strconv.Quote(val))
		} else {
			fmt.Fprintf(&b, "# %s: %s\n", key, hint)
		}
	}
	line("setup", setup, "<command that installs dependencies, e.g. npm install>")
	if len(portKeys) > 0 && st.portVar != "" && !slices.Contains(portKeys, st.portVar) {
		// The template names the port differently; use its name in the dev command.
		dev = strings.ReplaceAll(dev, "$"+st.portVar, "$"+portKeys[0])
		st.portVar = portKeys[0]
	}
	line("dev", dev, "<command that starts the dev server, e.g. npm run dev>")
	if dev != "" && st.devNote != "" {
		fmt.Fprintf(&b, "# %s\n", st.devNote)
	}
	switch {
	case len(portKeys) > 0:
		fmt.Fprintf(&b, "ports: [%s]\n", strings.Join(portKeys, ", "))
		if len(portKeys) > 1 {
			fmt.Fprintf(&b, "# proxy: %s  # which port http://<box>.localhost:7777 serves (default: the first)\n", portKeys[0])
		}
	case dev != "" && st.portVar != "":
		fmt.Fprintf(&b, "ports: [%s]  # each box gets a free port in this env var\n", st.portVar)
	default:
		b.WriteString("ports: []  # env var names that get a free port per box, e.g. [PORT]\n")
	}
	for _, name := range compose.DefaultFiles {
		if !exists(root, name) {
			continue
		}
		fmt.Fprintf(&b, "compose:\n  file: %s\n", name)
		if data, err := os.ReadFile(filepath.Join(root, name)); err == nil {
			if m := fixedHostPort.FindString(string(data)); m != "" {
				r.Warnings = append(r.Warnings, fmt.Sprintf(
					"%s publishes a fixed host port (%s); write it as \"${VAR}:containerPort\" and list VAR under ports so boxes don't collide", name, m))
			}
			if containerName.Match(data) {
				r.Warnings = append(r.Warnings, fmt.Sprintf(
					"%s sets container_name, which Docker requires to be unique on the host; remove it so every box can run its own copy", name))
			}
		}
		break
	}
	switch {
	case driver != "" && source != "":
		fmt.Fprintf(&b, "db:\n  driver: %s\n  url_env: %s\n  source: %s\n", driver, urlEnv, strconv.Quote(source))
	case driver != "":
		// Only the gitignored .env told us the connection string, and this
		// file is committed, so it is left for the user to fill in.
		fmt.Fprintf(&b, "# db:\n#   driver: %s\n#   url_env: %s\n#   source: <%s connection string for your dev database>\n", driver, urlEnv, driver)
		r.Warnings = append(r.Warnings, fmt.Sprintf(
			"only your .env had a value for %s, and agentbox.yaml is committed; fill in db.source yourself and keep real credentials out of it", urlEnv))
	}
	if tmpl != "" {
		fmt.Fprintf(&b, "env:\n  template: %s\n", tmpl)
	} else if exists(root, ".env.local") {
		b.WriteString("# env: { template: .env.local, file: .env.local }  # if every developer keeps a local env file\n")
	}
	b.WriteString("# links:                 # the same-named box in another repo, e.g.\n#   API_URL: api-service   #   http://<box>.<repo-id>.localhost:7777/\n")
	r.YAML = b.String()
	return r, nil
}
