package compose

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func requireDocker(t *testing.T) {
	t.Helper()
	if os.Getenv("AGENTBOX_TEST_DOCKER") != "1" {
		t.Skip("set AGENTBOX_TEST_DOCKER=1 with a running Docker daemon")
	}
}

func randHex(t *testing.T) string {
	t.Helper()
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

func dockerLines(t *testing.T, args ...string) []string {
	t.Helper()
	out, err := Docker{}.Output(ctx, args...)
	if err != nil {
		t.Fatalf("docker %s: %v", strings.Join(args, " "), err)
	}
	return strings.Fields(string(out))
}

func TestDockerCheck(t *testing.T) {
	requireDocker(t)
	if err := Check(ctx, Docker{}); err != nil {
		t.Fatal(err)
	}
}

// volumeCompose is one long-lived container with a named volume.
const volumeCompose = `services:
  box:
    image: alpine:3.20
    command: ["sh", "-c", "echo hi > /data/x && sleep 3600"]
    volumes: ["data:/data"]
volumes:
  data: {}
`

func TestDockerUpStopDownRemovesNamedVolume(t *testing.T) {
	requireDocker(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(volumeCompose), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	project := "agentbox-test-" + randHex(t)
	s := &Stack{Project: project, Dir: dir, File: "compose.yaml", Wait: 120 * time.Second, Exec: Docker{}, Log: &buf}
	t.Cleanup(func() { _ = s.Down(ctx) })
	if err := s.Up(ctx); err != nil {
		t.Fatalf("up: %v\n%s", err, buf.String())
	}
	vol := project + "_data"
	if got := dockerLines(t, "volume", "ls", "-q", "--filter", "name="+vol); !slices.Equal(got, []string{vol}) {
		t.Fatalf("volumes after up: %v", got)
	}
	if got, err := Projects(ctx, Docker{}, "agentbox-test-"); err != nil || !slices.Contains(got, project) {
		t.Fatalf("projects %v %v", got, err)
	}
	if err := s.Stop(ctx); err != nil {
		t.Fatalf("stop: %v\n%s", err, buf.String())
	}
	label := "label=com.docker.compose.project=" + project
	if got := dockerLines(t, "ps", "-q", "--filter", label); len(got) != 0 {
		t.Fatalf("running containers after stop: %v", got)
	}
	if got := dockerLines(t, "ps", "-aq", "--filter", label); len(got) == 0 {
		t.Fatal("stop must keep the containers")
	}
	if err := s.Down(ctx); err != nil {
		t.Fatalf("down: %v\n%s", err, buf.String())
	}
	if got, err := Projects(ctx, Docker{}, "agentbox-test-"); err != nil || slices.Contains(got, project) {
		t.Fatalf("project still listed after down: %v %v", got, err)
	}
	if got := dockerLines(t, "volume", "ls", "-q", "--filter", "name="+vol); len(got) != 0 {
		t.Fatalf("volume left after down: %v", got)
	}
}

func TestDockerDownProjectOfNothing(t *testing.T) {
	requireDocker(t)
	var buf bytes.Buffer
	if err := DownProject(ctx, Docker{}, t.TempDir(), "agentbox-test-nothing-"+randHex(t), &buf); err != nil {
		t.Fatalf("%v\n%s", err, buf.String())
	}
}

// DownProject is what rm and doctor use once a box directory (and its compose
// file) is gone, so it must remove the project's named volumes by label, and a
// stray compose file in a PARENT of the directory it runs in must not derail
// it: Compose walks parent directories looking for one.
func TestDockerDownProjectRemovesVolumesDespiteAStrayParentFile(t *testing.T) {
	requireDocker(t)
	project := "agentbox-test-" + randHex(t)
	dir := t.TempDir()
	parent := filepath.Dir(dir)
	stray := filepath.Join(parent, "compose.yaml")
	if _, err := os.Stat(stray); err == nil {
		t.Skip("the parent of t.TempDir() already holds a compose file")
	}
	if err := os.WriteFile(stray, []byte("services:\n  nope:\n    image: alpine:3.20\n    command: [\"true\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(stray)
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(volumeCompose), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	st := &Stack{Project: project, Dir: dir, File: "compose.yaml", Wait: 120 * time.Second, Exec: Docker{}, Log: &buf}
	t.Cleanup(func() { _ = DownProject(ctx, Docker{}, dir, project, &buf) })
	if err := st.Up(ctx); err != nil {
		t.Fatalf("up: %v\n%s", err, buf.String())
	}
	if vols := dockerLines(t, "volume", "ls", "-q", "--filter", "name="+project+"_data"); len(vols) != 1 {
		t.Fatalf("volume not created: %v", vols)
	}
	os.Remove(filepath.Join(dir, "compose.yaml")) // the box directory is gone; only the name is left
	if err := DownProject(ctx, Docker{}, dir, project, &buf); err != nil {
		t.Fatalf("down by name: %v\n%s", err, buf.String())
	}
	if vols := dockerLines(t, "volume", "ls", "-q", "--filter", "name="+project+"_data"); len(vols) != 0 {
		t.Fatalf("down -v by project name left the named volume behind: %v", vols)
	}
	if ps := dockerLines(t, "ps", "-aq", "--filter", "label=com.docker.compose.project="+project); len(ps) != 0 {
		t.Fatalf("containers left: %v", ps)
	}
}
