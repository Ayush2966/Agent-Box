package desktop

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBetweenMarksIgnoresShellChatter(t *testing.T) {
	out := "Last login: Mon\n\033[1mwelcome\033[0m\n" + pathMark + "/opt/homebrew/bin:/usr/bin" + pathMark + "\n"
	if got := betweenMarks(out); got != "/opt/homebrew/bin:/usr/bin" {
		t.Fatalf("a banner must not reach the PATH: %q", got)
	}
	for _, bad := range []string{"", "no markers here", pathMark + "half"} {
		if got := betweenMarks(bad); got != "" {
			t.Fatalf("betweenMarks(%q) = %q, want empty", bad, got)
		}
	}
}

func TestLoginPATHSurvivesAChattyShell(t *testing.T) {
	// A stand-in for the user's login shell: it greets, prints its PATH
	// between the markers, and exits non-zero the way a shell with a failing
	// rc file does. The PATH must still come through, and the greeting must not.
	sh := filepath.Join(t.TempDir(), "sh")
	script := "#!/bin/sh\necho 'Last login: today'\nprintf %s '" + pathMark + "/opt/homebrew/bin:/usr/bin:/bin" + pathMark + "'\nexit 3\n"
	if err := os.WriteFile(sh, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHELL", sh)
	if got := LoginPATH(); got != "/opt/homebrew/bin:/usr/bin:/bin" {
		t.Fatalf("LoginPATH() = %q", got)
	}
}

// TestLoginPATHAgainstTheRealShell is the one test that proves the command
// line itself is right: a `$PATH` without braces would run into the marker and
// name a variable that does not exist, and every fake-shell test would still
// pass. It only runs when a POSIX-ish $SHELL is present.
func TestLoginPATHAgainstTheRealShell(t *testing.T) {
	sh := os.Getenv("SHELL")
	if sh == "" {
		t.Skip("no $SHELL")
	}
	switch filepath.Base(sh) {
	case "zsh", "bash", "sh", "dash", "ksh":
	default:
		t.Skipf("%s is not a shell this test knows", sh)
	}
	got := LoginPATH()
	if got == "" {
		t.Skipf("%s printed no PATH here (a locked-down or non-interactive environment)", sh)
	}
	if !strings.Contains(got, "/bin") || strings.Contains(got, pathMark) {
		t.Fatalf("LoginPATH() = %q", got)
	}
}

func TestLoginPATHIgnoresAShellThatSaysNothingUseful(t *testing.T) {
	sh := filepath.Join(t.TempDir(), "sh")
	if err := os.WriteFile(sh, []byte("#!/bin/sh\necho hello\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHELL", sh)
	if got := LoginPATH(); got != "" {
		t.Fatalf("unmarked output must be discarded, got %q", got)
	}
	t.Setenv("SHELL", filepath.Join(t.TempDir(), "does-not-exist"))
	if got := LoginPATH(); got != "" {
		t.Fatalf("a missing shell must give no PATH, got %q", got)
	}
}

func TestToolPATHMergesAndDeduplicates(t *testing.T) {
	got := ToolPATH("/usr/bin:/bin", "/opt/homebrew/bin:/usr/bin")
	if !strings.HasPrefix(got, "/usr/bin:/bin:/opt/homebrew/bin") {
		t.Fatalf("order wrong: %s", got)
	}
	count := map[string]int{}
	for _, p := range strings.Split(got, ":") {
		if p == "" {
			t.Fatalf("empty entry in %s", got)
		}
		count[p]++
	}
	for p, n := range count {
		if n != 1 {
			t.Fatalf("%s appears %d times", p, n)
		}
	}
	for _, d := range fallbackDirs {
		if count[d] != 1 {
			t.Fatalf("fallback %s count %d", d, count[d])
		}
	}
}

func TestToolPATHWithoutLogin(t *testing.T) {
	got := ToolPATH("/usr/bin:/bin", "")
	if !strings.HasPrefix(got, "/usr/bin:/bin:/opt/homebrew/bin") {
		t.Fatalf("got %s", got)
	}
	if !strings.Contains(got, "/usr/local/sbin") {
		t.Fatalf("fallbacks missing: %s", got)
	}
}

func TestLoginPATHWithoutShell(t *testing.T) {
	t.Setenv("SHELL", "")
	if got := LoginPATH(); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestGUILaunchIgnoresProcessSerialNumber(t *testing.T) {
	if !guiLaunch(nil) || !guiLaunch([]string{"-psn_0_12345"}) {
		t.Fatal("expected GUI launch")
	}
	if guiLaunch([]string{"ls"}) {
		t.Fatal("ls is the CLI")
	}
}
