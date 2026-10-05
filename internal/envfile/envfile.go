// Package envfile renders a box's .env file from a template and agentbox-managed variables.
package envfile

import (
	"bytes"
	"regexp"
	"strings"
)

type KV struct{ Key, Value string }

var lineKey = regexp.MustCompile(`^(\s*)(export\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*=`)

// Render rewrites managed keys in the template and appends the ones it doesn't contain.
func Render(template []byte, vars []KV) []byte {
	var order []string
	vals := map[string]string{}
	for _, kv := range vars {
		if _, ok := vals[kv.Key]; !ok {
			order = append(order, kv.Key)
		}
		vals[kv.Key] = kv.Value
	}

	var out bytes.Buffer
	written := map[string]bool{}
	if text := string(template); text != "" {
		for _, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
			if m := lineKey.FindStringSubmatch(line); m != nil {
				if v, ok := vals[m[3]]; ok {
					out.WriteString(m[1] + m[2] + m[3] + "=" + Quote(v) + "\n")
					written[m[3]] = true
					continue
				}
			}
			out.WriteString(line + "\n")
		}
	}

	var missing []string
	for _, k := range order {
		if !written[k] {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		if out.Len() > 0 {
			out.WriteString("\n")
		}
		out.WriteString("# added by agentbox\n")
		for _, k := range missing {
			out.WriteString(k + "=" + Quote(vals[k]) + "\n")
		}
	}
	return out.Bytes()
}

// Quote formats a value so dotenv loaders, Docker and shells all read it back unchanged.
func Quote(v string) string {
	if v == "" {
		return `""`
	}
	if !strings.ContainsAny(v, " \t\"'#$\\`\n") {
		return v
	}
	if !strings.ContainsAny(v, "'\n") {
		return "'" + v + "'"
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return `"` + r.Replace(v) + `"`
}

// Parse reads KEY=VALUE lines, ignoring comments and anything that isn't an assignment.
func Parse(data []byte) []KV {
	var out []KV
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimRight(line, "\r")
		m := lineKey.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		v := strings.TrimSpace(line[len(m[0]):])
		if len(v) >= 2 && ((v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'')) {
			v = v[1 : len(v)-1]
		} else if i := strings.Index(v, " #"); i >= 0 {
			v = strings.TrimSpace(v[:i])
		}
		out = append(out, KV{m[3], v})
	}
	return out
}

// Pairs returns KEY=VALUE strings for exec.Cmd.Env.
func Pairs(vars []KV) []string {
	out := make([]string, 0, len(vars))
	for _, kv := range vars {
		out = append(out, kv.Key+"="+kv.Value)
	}
	return out
}
