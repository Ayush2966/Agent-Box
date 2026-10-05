package box

import (
	"errors"
	"fmt"
	"net"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/agentbox-dev/agentbox/internal/errs"
	"github.com/agentbox-dev/agentbox/internal/state"
	"github.com/agentbox-dev/agentbox/internal/testutil"
)

func kinds(fs []Finding) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Kind)
	}
	return out
}

func TestDoctorCleanRepo(t *testing.T) {
	m := newTestManager(t)
	b, err := m.New(ctx, NewOptions{RepoPath: newSQLiteRepo(t, sqliteConfig), Name: "a", NoDev: true})
	if err != nil {
		t.Fatal(err)
	}
	fs, err := m.Doctor(ctx, b.RepoID, false)
	if err != nil || len(fs) != 0 {
		t.Fatalf("findings %v, err %v", fs, err)
	}
}

func TestDoctorFixesMissingWorktree(t *testing.T) {
	m := newTestManager(t)
	b, _ := m.New(ctx, NewOptions{RepoPath: newSQLiteRepo(t, sqliteConfig), Name: "a", NoDev: true})
	os.RemoveAll(b.Dir)

	fs, _ := m.Doctor(ctx, b.RepoID, false)
	if !slices.Contains(kinds(fs), "missing_worktree") || fs[0].Fixed {
		t.Fatalf("report: %v", fs)
	}
	fs, _ = m.Doctor(ctx, b.RepoID, true)
	if len(fs) == 0 || !fs[0].Fixed {
		t.Fatalf("fix: %v", fs)
	}
	if _, err := m.d.Store.GetBox(b.RepoID, "a"); !errors.Is(err, state.ErrNotFound) {
		t.Fatal("box record not removed")
	}
	if rows, _ := m.d.Store.AllPorts(); len(rows) != 0 {
		t.Fatalf("ports not freed: %v", rows)
	}
}

func TestDoctorReportsOrphanBranchAndPortInUse(t *testing.T) {
	m := newTestManager(t)
	repo := newSQLiteRepo(t, sqliteConfig)
	b, _ := m.New(ctx, NewOptions{RepoPath: repo, Name: "a", NoDev: true})
	testutil.Git(t, repo, "branch", "agentbox/zzz")
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", b.Ports["APP_PORT"]))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	fs, _ := m.Doctor(ctx, b.RepoID, true)
	got := kinds(fs)
	if !slices.Contains(got, "orphan_branch") || !slices.Contains(got, "port_in_use") {
		t.Fatalf("findings %v", fs)
	}
	for _, f := range fs {
		if f.Kind == "orphan_branch" && (f.Fixed || !strings.Contains(f.Detail, "agentbox/zzz")) {
			t.Fatalf("orphan branch must only be reported: %+v", f)
		}
	}
}

func TestRefreshDBRequiresDB(t *testing.T) {
	m := newTestManager(t)
	r, _ := m.ResolveRepo(newPlainRepo(t, plainConfig))
	err := m.RefreshDB(ctx, r.ID, false)
	if e := errs.From(err); e.Code != errs.Precondition || !strings.Contains(e.Message, "no db section") {
		t.Fatalf("err = %v", err)
	}
	if err := m.RefreshDB(ctx, "nope", false); errs.From(err).Code != errs.NotFound {
		t.Fatalf("unknown repo err = %v", err)
	}
}
