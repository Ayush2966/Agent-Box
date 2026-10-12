package desktop

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"
)

// fallbackDirs are where the tools agentbox shells out to usually live when a
// GUI app does not inherit a shell's PATH.
var fallbackDirs = []string{"/opt/homebrew/bin", "/opt/homebrew/sbin", "/usr/local/bin", "/usr/local/sbin"}

// pathMark brackets the PATH in the login shell's output: an interactive
// shell also prints banners, "Last login" lines and whatever an rc file says,
// and those would otherwise be split into junk PATH entries.
const pathMark = "__agentbox_path__"

// betweenMarks returns what the shell printed between the two markers.
func betweenMarks(out string) string {
	i := strings.Index(out, pathMark)
	if i < 0 {
		return ""
	}
	rest := out[i+len(pathMark):]
	j := strings.Index(rest, pathMark)
	if j < 0 {
		return ""
	}
	return strings.TrimSpace(rest[:j])
}

// LoginPATH asks the user's login shell what PATH it sets. A GUI app is
// launched by launchd with /usr/bin:/bin:/usr/sbin:/sbin, and the daemon this
// app spawns inherits that, so pg_dump, mysqldump, docker and node would be
// missing from boxes created out of the Dock.
func LoginPATH() string {
	sh := os.Getenv("SHELL")
	if sh == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// ${PATH} with braces: a bare $PATH would run into the marker and name a
	// variable that does not exist, which prints nothing at all.
	cmd := exec.CommandContext(ctx, sh, "-ilc", `printf %s "`+pathMark+`${PATH}`+pathMark+`"`)
	// Killing the shell is not enough: Output() waits for everything holding
	// the pipe, and an rc file may have left a background child on it, which
	// would hang the app before its window ever opened.
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if err != nil && len(out) == 0 {
		return ""
	}
	return betweenMarks(string(out))
}

// ToolPATH merges the current PATH, the login shell's PATH and the fallback
// directories, keeping the first occurrence of each entry and the caller's own
// entries first. It never returns an empty string.
func ToolPATH(current, login string) string {
	var out []string
	seen := map[string]bool{}
	add := func(list string) {
		for _, p := range strings.Split(list, string(os.PathListSeparator)) {
			if p == "" || seen[p] {
				continue
			}
			seen[p] = true
			out = append(out, p)
		}
	}
	add(current)
	add(login)
	for _, d := range fallbackDirs {
		add(d)
	}
	return strings.Join(out, string(os.PathListSeparator))
}

// guiLaunch reports whether the process was started as the app rather than as
// the CLI: no arguments, or only the -psn_ Process Serial Number argument that
// older macOS versions pass on a Finder launch.
func guiLaunch(args []string) bool {
	return len(args) == 0 || (len(args) == 1 && strings.HasPrefix(args[0], "-psn_"))
}

// GUILaunch is guiLaunch for the exported surface used by the app's main.
func GUILaunch(args []string) bool { return guiLaunch(args) }
