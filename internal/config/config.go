// Package config loads and validates a repo's agentbox.yaml.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// FileName is the per-repo config file, read from the main checkout's root.
const FileName = "agentbox.yaml"

type Config struct {
	Version int      `yaml:"version"`
	Setup   string   `yaml:"setup"`
	Dev     string   `yaml:"dev"`
	Ports   []string `yaml:"ports"`
	Proxy   string   `yaml:"proxy"`
	DB      *DB      `yaml:"db"`
	Env     Env      `yaml:"env"`
	Compose *Compose `yaml:"compose"`
	Copy    []string `yaml:"copy"`
	// Links hands a box the address of the same-named box in another repo:
	// env var name → "<repo>[:url|port|addr]" (see ParseLink).
	Links map[string]string `yaml:"links"`
}

// Link is one `links:` entry. Repo names another repo agentbox knows, by its
// folder name or its id; Kind selects what the env var receives: "url" (the
// proxy URL, http://<box>.<repo-id>.localhost:<port>/, stable even before the
// other box exists), "port" (the other box's proxy port) or "addr"
// (127.0.0.1:<port>, for server-to-server calls).
type Link struct {
	Repo string
	Kind string
}

// ParseLink reads "<repo>", "<repo>:url", "<repo>:port" or "<repo>:addr".
func ParseLink(spec string) (Link, error) {
	repo, kind, found := strings.Cut(spec, ":")
	if !found {
		kind = "url"
	}
	repo, kind = strings.TrimSpace(repo), strings.TrimSpace(kind)
	if repo == "" {
		return Link{}, fmt.Errorf("links: %q: the repo name is empty", spec)
	}
	switch kind {
	case "url", "port", "addr":
	default:
		return Link{}, fmt.Errorf("links: %q: must end in :url, :port or :addr (got %q)", spec, kind)
	}
	return Link{Repo: repo, Kind: kind}, nil
}

type DB struct {
	Driver string `yaml:"driver"`
	URLEnv string `yaml:"url_env"`
	Source string `yaml:"source"`
}

// Path returns a SQLite source as a clean relative file path, without any "file:" prefix.
func (d *DB) Path() string { return filepath.Clean(strings.TrimPrefix(d.Source, "file:")) }

// ProxyPort names the port env var that http://<box>.localhost forwards to:
// `proxy` when set, else the first entry of `ports`, else "" (no ports).
func (c *Config) ProxyPort() string {
	if c.Proxy != "" {
		return c.Proxy
	}
	if len(c.Ports) > 0 {
		return c.Ports[0]
	}
	return ""
}

type Env struct {
	Template string            `yaml:"template"`
	File     string            `yaml:"file"`
	Set      map[string]string `yaml:"set"`
}

// Compose describes the per-box Docker Compose stack.
type Compose struct {
	File     string   `yaml:"file"`     // repo-relative; "" = the first of compose.yaml, compose.yml, docker-compose.yml, docker-compose.yaml
	Services []string `yaml:"services"` // subset to bring up; empty = all
	Wait     string   `yaml:"wait"`     // `up --wait` timeout; "" = 60s
}

// WaitTimeout is how long `up --wait` may take before the step fails.
func (c *Compose) WaitTimeout() time.Duration {
	if c.Wait == "" {
		return 60 * time.Second
	}
	d, _ := time.ParseDuration(c.Wait) // validated in validate()
	return d
}

var serviceName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

var knownKeys = map[string][]string{
	"":        {"version", "setup", "dev", "ports", "proxy", "db", "env", "compose", "copy", "links"},
	"db":      {"driver", "url_env", "source"},
	"env":     {"template", "file", "set"},
	"compose": {"file", "services", "wait"},
}

// Load reads and validates <repoRoot>/agentbox.yaml.
func Load(repoRoot string) (*Config, error) {
	data, err := os.ReadFile(filepath.Join(repoRoot, FileName))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%s not found in %s (run `agentbox init`)", FileName, repoRoot)
		}
		return nil, err
	}
	return Parse(data)
}

// Parse validates raw agentbox.yaml contents.
func Parse(data []byte) (*Config, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("%s: %w", FileName, err)
	}
	if len(root.Content) == 0 {
		return nil, fmt.Errorf("%s is empty", FileName)
	}
	doc := root.Content[0]
	if doc.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s: top level must be a mapping", FileName)
	}
	if err := checkKeys(doc, ""); err != nil {
		return nil, err
	}
	var c Config
	if err := doc.Decode(&c); err != nil {
		return nil, fmt.Errorf("%s: %w", FileName, err)
	}
	if c.Env.File == "" {
		c.Env.File = ".env"
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func checkKeys(n *yaml.Node, section string) error {
	if n.Kind != yaml.MappingNode {
		return nil // Decode reports the type error
	}
	known := knownKeys[section]
	for i := 0; i+1 < len(n.Content); i += 2 {
		key := n.Content[i]
		if !slices.Contains(known, key.Value) {
			name := key.Value
			if section != "" {
				name = section + "." + key.Value
			}
			msg := fmt.Sprintf("%s:%d: unknown key %q", FileName, key.Line, name)
			if s := suggest(key.Value, known); s != "" {
				msg += fmt.Sprintf(" (did you mean %q?)", s)
			}
			return errors.New(msg)
		}
		if section == "" && (key.Value == "db" || key.Value == "env" || key.Value == "compose") {
			if err := checkKeys(n.Content[i+1], key.Value); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *Config) validate() error {
	if c.Version != 1 {
		return fmt.Errorf("%s: version must be 1 (got %d)", FileName, c.Version)
	}
	seen := map[string]bool{}
	for _, p := range c.Ports {
		if !envName.MatchString(p) {
			return fmt.Errorf("%s: ports: %q is not a valid env var name", FileName, p)
		}
		if seen[p] {
			return fmt.Errorf("%s: ports: %q listed twice", FileName, p)
		}
		seen[p] = true
	}
	if c.Proxy != "" && !seen[c.Proxy] {
		return fmt.Errorf("%s: proxy: %q is not listed in ports", FileName, c.Proxy)
	}
	if c.DB != nil {
		switch c.DB.Driver {
		case "postgres", "sqlite", "mysql", "mongo", "mongodb":
		default:
			return fmt.Errorf(`%s: db.driver must be "postgres", "mysql", "mongo" or "sqlite" (got %q)`, FileName, c.DB.Driver)
		}
		if !envName.MatchString(c.DB.URLEnv) {
			return fmt.Errorf("%s: db.url_env must be a valid env var name (got %q)", FileName, c.DB.URLEnv)
		}
		if seen[c.DB.URLEnv] {
			return fmt.Errorf("%s: db.url_env %q is also listed in ports", FileName, c.DB.URLEnv)
		}
		if c.DB.Source == "" {
			return fmt.Errorf("%s: db.source is required", FileName)
		}
		if c.DB.Driver == "sqlite" {
			if err := localPath("db.source", c.DB.Path()); err != nil {
				return err
			}
		}
		if c.DB.Driver == "mysql" {
			u, err := url.Parse(c.DB.Source)
			if err != nil || (u.Scheme != "mysql" && u.Scheme != "mariadb") || u.Host == "" || len(strings.Trim(u.Path, "/")) == 0 {
				return fmt.Errorf("%s: db.source for mysql must look like mysql://user:pass@host:3306/database (got %q)", FileName, c.DB.Source)
			}
		}
		if c.DB.Driver == "mongo" || c.DB.Driver == "mongodb" {
			u, err := url.Parse(c.DB.Source)
			if err != nil || (u.Scheme != "mongodb" && u.Scheme != "mongodb+srv") || u.Host == "" || len(strings.Trim(u.Path, "/")) == 0 {
				return fmt.Errorf("%s: db.source for mongo must look like mongodb://user:pass@host:27017/database (got %q)", FileName, c.DB.Source)
			}
		}
	}
	for k := range c.Env.Set {
		if !envName.MatchString(k) {
			return fmt.Errorf("%s: env.set: %q is not a valid env var name", FileName, k)
		}
	}
	if c.Env.Template != "" {
		if err := localPath("env.template", c.Env.Template); err != nil {
			return err
		}
	}
	if err := localPath("env.file", c.Env.File); err != nil {
		return err
	}
	if c.Compose != nil {
		if c.Compose.File != "" {
			if err := localPath("compose.file", c.Compose.File); err != nil {
				return err
			}
		}
		for _, s := range c.Compose.Services {
			if !serviceName.MatchString(s) {
				return fmt.Errorf("%s: compose.services: %q is not a valid service name", FileName, s)
			}
		}
		if c.Compose.Wait != "" {
			d, err := time.ParseDuration(c.Compose.Wait)
			if err != nil || d < time.Second {
				return fmt.Errorf("%s: compose.wait must be a duration of at least 1s (got %q)", FileName, c.Compose.Wait)
			}
		}
	}
	for i, p := range c.Copy {
		if err := localPath(fmt.Sprintf("copy[%d]", i), p); err != nil {
			return err
		}
	}
	for k, spec := range c.Links {
		if !envName.MatchString(k) {
			return fmt.Errorf("%s: links: %q is not a valid env var name", FileName, k)
		}
		if seen[k] || (c.DB != nil && c.DB.URLEnv == k) {
			return fmt.Errorf("%s: links: %q is already a port or the db url env", FileName, k)
		}
		if _, ok := c.Env.Set[k]; ok {
			return fmt.Errorf("%s: links: %q is also in env.set", FileName, k)
		}
		if _, err := ParseLink(spec); err != nil {
			return fmt.Errorf("%s: %w", FileName, err)
		}
	}
	return nil
}

func localPath(field, p string) error {
	if !filepath.IsLocal(p) {
		return fmt.Errorf("%s: %s: %q must be a relative path inside the repo", FileName, field, p)
	}
	return nil
}

func suggest(s string, options []string) string {
	best, bestDist := "", 3
	for _, o := range options {
		if d := levenshtein(s, o); d < bestDist {
			best, bestDist = o, d
		}
	}
	return best
}

func levenshtein(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}
