// Package config loads and validates a repo's agentbox.yaml.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// FileName is the per-repo config file, read from the main checkout's root.
const FileName = "agentbox.yaml"

type Config struct {
	Version int      `yaml:"version"`
	Setup   string   `yaml:"setup"`
	Dev     string   `yaml:"dev"`
	Ports   []string `yaml:"ports"`
	DB      *DB      `yaml:"db"`
	Env     Env      `yaml:"env"`
	Copy    []string `yaml:"copy"`
}

type DB struct {
	Driver string `yaml:"driver"`
	URLEnv string `yaml:"url_env"`
	Source string `yaml:"source"`
}

// Path returns a SQLite source as a clean relative file path, without any "file:" prefix.
func (d *DB) Path() string { return filepath.Clean(strings.TrimPrefix(d.Source, "file:")) }

type Env struct {
	Template string            `yaml:"template"`
	File     string            `yaml:"file"`
	Set      map[string]string `yaml:"set"`
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

var knownKeys = map[string][]string{
	"":    {"version", "setup", "dev", "ports", "db", "env", "copy"},
	"db":  {"driver", "url_env", "source"},
	"env": {"template", "file", "set"},
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
		if section == "" && (key.Value == "db" || key.Value == "env") {
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
	if c.DB != nil {
		switch c.DB.Driver {
		case "postgres", "sqlite":
		default:
			return fmt.Errorf(`%s: db.driver must be "postgres" or "sqlite" (got %q)`, FileName, c.DB.Driver)
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
	for i, p := range c.Copy {
		if err := localPath(fmt.Sprintf("copy[%d]", i), p); err != nil {
			return err
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
