package box

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	"github.com/Ayush2966/Agent-Box/internal/errs"
)

// Launcher starts a desktop program for the user and returns at once. nil in
// Deps means the real thing; tests record the calls instead.
type Launcher func(dir, name string, args ...string) error

// OpenApp names what Open launches on a box's directory.
type OpenApp string

const (
	OpenEditor   OpenApp = "editor"   // the user's code editor
	OpenTerminal OpenApp = "terminal" // a terminal window in the box
	OpenFiles    OpenApp = "files"    // the file manager
)

// Editors is the lookup order when no `editor:` is configured.
var Editors = []string{"code", "cursor", "windsurf", "zed", "subl", "idea"}

func launch(dir, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	return cmd.Start()
}

// Open opens the box's directory in the user's editor, a terminal or the
// file manager, and reports what it launched.
func (m *Manager) Open(repoID, name string, app OpenApp) (string, error) {
	b, err := m.get(repoID, name)
	if err != nil {
		return "", err
	}
	run := m.d.Launch
	if run == nil {
		run = launch
	}
	switch app {
	case OpenEditor:
		prog, args, err := editorCommand(m.d.Editor, b.Dir)
		if err != nil {
			return "", err
		}
		return prog, run(b.Dir, prog, args...)
	case OpenTerminal:
		prog, args, err := terminalCommand(m.d.Terminal, b.Dir)
		if err != nil {
			return "", err
		}
		return prog, run(b.Dir, prog, args...)
	case OpenFiles:
		if runtime.GOOS == "darwin" {
			return "Finder", run(b.Dir, "open", b.Dir)
		}
		return "file manager", run(b.Dir, "xdg-open", b.Dir)
	default:
		return "", errs.New(errs.Invalid, "open: app must be editor, terminal or files (got %q)", app)
	}
}

// editorCommand picks the editor: the configured one (a command on PATH, or
// on macOS a .app name for `open -a`), else the first of Editors on PATH.
func editorCommand(configured, dir string) (string, []string, error) {
	if configured != "" {
		if strings.HasSuffix(configured, ".app") || strings.Contains(configured, " ") {
			if runtime.GOOS != "darwin" {
				return "", nil, errs.New(errs.Invalid, "editor %q looks like a macOS app name; set a command on PATH", configured)
			}
			return "open", []string{"-a", strings.TrimSuffix(configured, ".app"), dir}, nil
		}
		if _, err := exec.LookPath(configured); err != nil {
			return "", nil, errs.New(errs.NotFound, "editor %q is not on the daemon's PATH", configured).
				WithHint("install its command-line launcher (e.g. “Shell Command: Install 'code' command” in VS Code) or set `editor:` in ~/.agentbox/config.yaml")
		}
		return configured, []string{dir}, nil
	}
	for _, e := range Editors {
		if _, err := exec.LookPath(e); err == nil {
			return e, []string{dir}, nil
		}
	}
	return "", nil, errs.New(errs.NotFound, "no editor found on the daemon's PATH (tried %s)", strings.Join(Editors, ", ")).
		WithHint("install your editor's command-line launcher, or set `editor:` in ~/.agentbox/config.yaml (a command, or on macOS an app name such as \"Visual Studio Code\")")
}

// terminalCommand opens a terminal window in dir: the configured app, else
// Terminal on macOS, else the first common Linux terminal on PATH.
func terminalCommand(configured, dir string) (string, []string, error) {
	if runtime.GOOS == "darwin" {
		app := configured
		if app == "" {
			app = "Terminal"
		}
		return "open", []string{"-a", strings.TrimSuffix(app, ".app"), dir}, nil
	}
	if configured != "" {
		if _, err := exec.LookPath(configured); err != nil {
			return "", nil, errs.New(errs.NotFound, "terminal %q is not on the daemon's PATH", configured)
		}
		return configured, terminalArgs(configured, dir), nil
	}
	for _, t := range []string{"x-terminal-emulator", "gnome-terminal", "konsole", "xfce4-terminal", "alacritty", "kitty", "xterm"} {
		if _, err := exec.LookPath(t); err == nil {
			return t, terminalArgs(t, dir), nil
		}
	}
	return "", nil, errs.New(errs.NotFound, "no terminal emulator found on the daemon's PATH").
		WithHint("set `terminal:` in ~/.agentbox/config.yaml to your terminal's command")
}

func terminalArgs(prog, dir string) []string {
	switch {
	case strings.Contains(prog, "gnome-terminal"), strings.Contains(prog, "xfce4-terminal"):
		return []string{"--working-directory=" + dir}
	case strings.Contains(prog, "konsole"):
		return []string{"--workdir", dir}
	case strings.Contains(prog, "alacritty"), strings.Contains(prog, "kitty"):
		return []string{"--working-directory", dir}
	default:
		return []string{"-e", fmt.Sprintf("sh -c 'cd %q && exec \"${SHELL:-sh}\"'", dir)}
	}
}
