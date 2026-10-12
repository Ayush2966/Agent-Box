package state

import (
	"errors"
	"path/filepath"
	"regexp"
	"sync"
	"testing"
	"time"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func always(int) bool { return true }

func TestRepoSlug(t *testing.T) {
	a := RepoSlug("/Users/me/My App")
	if !regexp.MustCompile(`^my-app-[0-9a-f]{6}$`).MatchString(a) {
		t.Fatalf("slug %q", a)
	}
	if a != RepoSlug("/Users/me/My App") {
		t.Fatal("slug not stable")
	}
	if a == RepoSlug("/Users/other/My App") {
		t.Fatal("different paths gave the same slug")
	}
}

func TestRepos(t *testing.T) {
	s := openTest(t)
	r, err := s.UpsertRepo("/x/app")
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := s.UpsertRepo("/x/app"); again != r {
		t.Fatalf("upsert not idempotent: %v vs %v", again, r)
	}
	got, err := s.RepoByID(r.ID)
	if err != nil || got.Path != "/x/app" {
		t.Fatalf("RepoByID = %v, %v", got, err)
	}
	if _, err := s.RepoByID("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestBoxCRUD(t *testing.T) {
	s := openTest(t)
	b := Box{RepoID: "r", Name: "a", Dir: "/d/a", Branch: "agentbox/a", State: Creating, CreatedAt: time.Now()}
	if err := s.InsertBox(b); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertBox(b); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate insert err = %v", err)
	}
	b.State, b.PID, b.DBName = Running, 42, "app_agentbox_a"
	if err := s.UpdateBox(b); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetBox("r", "a")
	if err != nil || got.State != Running || got.PID != 42 || got.DBName != "app_agentbox_a" || got.ID() != "r/a" {
		t.Fatalf("GetBox = %+v, %v", got, err)
	}
	list, _ := s.ListBoxes("r")
	all, _ := s.ListBoxes("")
	if len(list) != 1 || len(all) != 1 {
		t.Fatalf("list %d all %d", len(list), len(all))
	}
	if err := s.UpdateBox(Box{RepoID: "r", Name: "zz"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update missing err = %v", err)
	}
	if err := s.DeleteBox("r", "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetBox("r", "a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestAllocatePortsSkipsBusyAndReuses(t *testing.T) {
	s := openTest(t)
	busy := func(p int) bool { return p != 20000 }
	got, err := s.AllocatePorts("r", "a", []string{"APP_PORT", "API_PORT"}, 20000, 20100, busy)
	if err != nil {
		t.Fatal(err)
	}
	if got["APP_PORT"] != 20001 || got["API_PORT"] != 20002 {
		t.Fatalf("got %v", got)
	}
	b, _ := s.AllocatePorts("r", "b", []string{"APP_PORT"}, 20000, 20100, busy)
	if b["APP_PORT"] != 20003 {
		t.Fatalf("second box got %v", b)
	}
	if err := s.FreePorts("r", "a"); err != nil {
		t.Fatal(err)
	}
	c, _ := s.AllocatePorts("r", "c", []string{"P"}, 20000, 20100, busy)
	if c["P"] != 20001 {
		t.Fatalf("freed port not reused: %v", c)
	}
}

func TestAllocatePortsExhausted(t *testing.T) {
	s := openTest(t)
	if _, err := s.AllocatePorts("r", "a", []string{"A", "B", "C"}, 20000, 20001, always); err == nil {
		t.Fatal("expected exhaustion error")
	}
	if rows, _ := s.AllPorts(); len(rows) != 0 {
		t.Fatalf("partial allocation leaked: %v", rows)
	}
}

func TestAllocatePortsConcurrent(t *testing.T) {
	s := openTest(t)
	var wg sync.WaitGroup
	var mu sync.Mutex
	seen := map[int]bool{}
	for i := range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := s.AllocatePorts("r", string(rune('a'+i)), []string{"A", "B"}, 20000, 20100, always)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			for _, p := range got {
				if seen[p] {
					t.Errorf("port %d handed out twice", p)
				}
				seen[p] = true
			}
		}()
	}
	wg.Wait()
	if len(seen) != 20 {
		t.Fatalf("allocated %d ports, want 20", len(seen))
	}
}

func TestDeleteBoxFreesPorts(t *testing.T) {
	s := openTest(t)
	_ = s.InsertBox(Box{RepoID: "r", Name: "a", State: Ready, CreatedAt: time.Now()})
	_, _ = s.AllocatePorts("r", "a", []string{"A"}, 20000, 20100, always)
	b, _ := s.GetBox("r", "a")
	if b.Ports["A"] != 20000 {
		t.Fatalf("ports not loaded: %v", b.Ports)
	}
	_ = s.DeleteBox("r", "a")
	if rows, _ := s.AllPorts(); len(rows) != 0 {
		t.Fatalf("ports left: %v", rows)
	}
}

func TestBoxComposeRoundTrips(t *testing.T) {
	s := openTest(t) // whatever helper the file uses to open a temp store
	r, _ := s.UpsertRepo("/tmp/r")
	b := Box{RepoID: r.ID, Name: "a", Dir: "/tmp/boxes/a", Branch: "agentbox/a", State: Ready, Compose: "agentbox-" + r.ID + "-a"}
	if err := s.InsertBox(b); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetBox(r.ID, "a")
	if err != nil || got.Compose != b.Compose {
		t.Fatalf("%+v %v", got, err)
	}
	got.Compose = ""
	if err := s.UpdateBox(got); err != nil {
		t.Fatal(err)
	}
	if again, _ := s.GetBox(r.ID, "a"); again.Compose != "" {
		t.Fatalf("UpdateBox did not clear Compose: %+v", again)
	}
}
