package compose

import (
	"context"
	"io"
	"sync"
)

// Recorder is an Executor for tests: it records every invocation and can be
// scripted to fail a verb (up/stop/down/ls/version) or to answer with stdout.
type Recorder struct {
	mu     sync.Mutex
	Calls  [][]string
	Dirs   []string
	Envs   [][]string
	Fail   map[string]error  // verb → error returned by Run/Output
	Out    string            // written to out by Run (e.g. docker's error text)
	Stdout map[string]string // verb → stdout returned by Output
}

func verb(args []string) string {
	for _, a := range args {
		switch a {
		case "up", "stop", "down", "ls", "version":
			return a
		}
	}
	return ""
}

func (r *Recorder) record(dir string, env []string, args []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Calls = append(r.Calls, append([]string(nil), args...))
	r.Dirs = append(r.Dirs, dir)
	r.Envs = append(r.Envs, append([]string(nil), env...))
}

func (r *Recorder) Run(_ context.Context, dir string, env []string, out io.Writer, args ...string) error {
	r.record(dir, env, args)
	if r.Out != "" && out != nil {
		io.WriteString(out, r.Out)
	}
	return r.Fail[verb(args)]
}

func (r *Recorder) Output(_ context.Context, args ...string) ([]byte, error) {
	r.record("", nil, args)
	if err := r.Fail[verb(args)]; err != nil {
		return nil, err
	}
	return []byte(r.Stdout[verb(args)]), nil
}

// Verbs returns the verb of every recorded call, in order (for assertions).
func (r *Recorder) Verbs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.Calls))
	for _, c := range r.Calls {
		out = append(out, verb(c))
	}
	return out
}
