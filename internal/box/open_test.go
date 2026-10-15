package box

import (
	"runtime"
	"strings"
	"testing"

	"github.com/Ayush2966/Agent-Box/internal/errs"
)

type launched struct {
	dir, name string
	args      []string
}

func TestOpenLaunchesEditorTerminalAndFiles(t *testing.T) {
	m := newTestManager(t)
	var calls []launched
	m.d.Launch = func(dir, name string, args ...string) error {
		calls = append(calls, launched{dir, name, args})
		return nil
	}
	m.d.Editor = "sh" // something certainly on PATH
	repo := newSQLiteRepo(t, sqliteConfig)
	b, err := m.New(ctx, NewOptions{RepoPath: repo, Name: "a", NoDev: true})
	if err != nil {
		t.Fatal(err)
	}
	if what, err := m.Open(b.RepoID, "a", OpenEditor); err != nil || what != "sh" {
		t.Fatalf("editor: %q %v", what, err)
	}
	if _, err := m.Open(b.RepoID, "a", OpenTerminal); err != nil {
		t.Fatalf("terminal: %v", err)
	}
	if _, err := m.Open(b.RepoID, "a", OpenFiles); err != nil {
		t.Fatalf("files: %v", err)
	}
	if len(calls) != 3 || calls[0].name != "sh" || calls[0].args[0] != b.Dir || calls[0].dir != b.Dir {
		t.Fatalf("calls: %+v", calls)
	}
	if runtime.GOOS == "darwin" {
		if calls[1].name != "open" || calls[1].args[0] != "-a" || calls[1].args[2] != b.Dir || calls[2].name != "open" {
			t.Fatalf("macOS terminal/files: %+v", calls[1:])
		}
	}
	for _, c := range calls {
		if !strings.Contains(strings.Join(c.args, " "), b.Dir) {
			t.Fatalf("launch without the box dir: %+v", c)
		}
	}
	if _, err := m.Open(b.RepoID, "a", OpenApp("browser")); errs.From(err).Code != errs.Invalid {
		t.Fatalf("unknown app: %v", err)
	}
	if _, err := m.Open(b.RepoID, "nope", OpenEditor); errs.From(err).Code != errs.NotFound {
		t.Fatalf("missing box: %v", err)
	}
}

func TestOpenEditorErrorsAreActionable(t *testing.T) {
	m := newTestManager(t)
	m.d.Launch = func(string, string, ...string) error { return nil }
	repo := newSQLiteRepo(t, sqliteConfig)
	b, err := m.New(ctx, NewOptions{RepoPath: repo, Name: "a", NoDev: true})
	if err != nil {
		t.Fatal(err)
	}
	m.d.Editor = "definitely-not-an-editor-xyz"
	_, err = m.Open(b.RepoID, "a", OpenEditor)
	if e := errs.From(err); e == nil || e.Code != errs.NotFound || !strings.Contains(e.Hint, "config.yaml") {
		t.Fatalf("configured editor missing: %v", err)
	}
	m.d.Editor = ""
	old := Editors
	Editors = []string{"definitely-not-an-editor-xyz"}
	t.Cleanup(func() { Editors = old })
	_, err = m.Open(b.RepoID, "a", OpenEditor)
	if e := errs.From(err); e == nil || e.Code != errs.NotFound || !strings.Contains(e.Error(), "no editor found") {
		t.Fatalf("no editor: %v", err)
	}
}
