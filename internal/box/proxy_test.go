package box

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ayush2966/Agent-Box/internal/proxy"
	"github.com/Ayush2966/Agent-Box/internal/testutil"
)

func lowestPort(ports map[string]int) int {
	lo := 0
	for _, p := range ports {
		if lo == 0 || p < lo {
			lo = p
		}
	}
	return lo
}

func TestProxyTargetDefaultsToFirstPort(t *testing.T) {
	m := newTestManager(t)
	repo := newSQLiteRepo(t, sqliteConfig) // ports: [APP_PORT, API_PORT]
	b, err := m.New(ctx, NewOptions{RepoPath: repo, Name: "a", NoDev: true})
	if err != nil {
		t.Fatal(err)
	}
	tg, err := m.ProxyTarget("a", "")
	if err != nil {
		t.Fatal(err)
	}
	if tg.Port != b.Ports["APP_PORT"] || tg.Repo != b.RepoID || tg.Name != "a" || tg.State != "ready" {
		t.Fatalf("%+v (ports %v)", tg, b.Ports)
	}
	if tg.Tail == nil || !strings.Contains(strings.Join(tg.Tail(50), "\n"), "setting up") {
		t.Errorf("Tail should read the box log: %v", tg.Tail(50))
	}
}

func TestProxyTargetHonoursProxyKeyAndQualifiedRepo(t *testing.T) {
	m := newTestManager(t)
	cfg := strings.Replace(sqliteConfig, "ports: [APP_PORT, API_PORT]\n", "ports: [APP_PORT, API_PORT]\nproxy: API_PORT\n", 1)
	repo := newSQLiteRepo(t, cfg)
	b, err := m.New(ctx, NewOptions{RepoPath: repo, Name: "a", NoDev: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, slug := range []string{"", b.RepoID} {
		tg, err := m.ProxyTarget("a", slug)
		if err != nil || tg.Port != b.Ports["API_PORT"] {
			t.Fatalf("slug %q: %+v %v (ports %v)", slug, tg, err, b.Ports)
		}
	}
	if _, err := m.ProxyTarget("a", "other-repo-000000"); err == nil {
		t.Fatal("wrong repo slug should not match")
	}
}

func TestProxyTargetFallsBackWhenConfigIsGone(t *testing.T) {
	m := newTestManager(t)
	repo := newSQLiteRepo(t, sqliteConfig)
	b, err := m.New(ctx, NewOptions{RepoPath: repo, Name: "a", NoDev: true})
	if err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(repo, "agentbox.yaml"))
	tg, err := m.ProxyTarget("a", "")
	if err != nil || tg.Port != lowestPort(b.Ports) {
		t.Fatalf("%+v %v (ports %v)", tg, err, b.Ports)
	}
}

func TestProxyTargetAmbiguousAcrossRepos(t *testing.T) {
	m := newTestManager(t)
	var slugs []string
	for i := 0; i < 2; i++ {
		repo := newSQLiteRepo(t, sqliteConfig)
		b, err := m.New(ctx, NewOptions{RepoPath: repo, Name: "a", NoDev: true})
		if err != nil {
			t.Fatal(err)
		}
		slugs = append(slugs, b.RepoID)
	}
	_, err := m.ProxyTarget("a", "")
	var amb *proxy.AmbiguousError
	if !errors.As(err, &amb) || len(amb.Hosts) != 2 {
		t.Fatalf("want AmbiguousError with 2 hosts, got %v", err)
	}
	for _, s := range slugs {
		if !strings.Contains(strings.Join(amb.Hosts, " "), "a."+s+".localhost") {
			t.Errorf("hosts %v lack %s", amb.Hosts, s)
		}
		if tg, err := m.ProxyTarget("a", s); err != nil || tg.Repo != s {
			t.Errorf("qualified lookup %s: %+v %v", s, tg, err)
		}
	}
}

func TestProxyTargetUnknownListsCandidates(t *testing.T) {
	m := newTestManager(t)
	repo := newSQLiteRepo(t, sqliteConfig)
	if _, err := m.New(ctx, NewOptions{RepoPath: repo, Name: "a", NoDev: true}); err != nil {
		t.Fatal(err)
	}
	_, err := m.ProxyTarget("zzz", "")
	var nsb *proxy.NoSuchBoxError
	if !errors.As(err, &nsb) || len(nsb.Candidates) != 1 || nsb.Candidates[0] != "a.localhost" {
		t.Fatalf("want NoSuchBoxError{Candidates: [a.localhost]}, got %v", err)
	}
}

func TestProxyTargetNoPorts(t *testing.T) {
	m := newTestManager(t)
	repo := testutil.NewRepo(t, map[string]string{"agentbox.yaml": "version: 1\n"})
	if _, err := m.New(ctx, NewOptions{RepoPath: repo, Name: "a", NoDev: true}); err != nil {
		t.Fatal(err)
	}
	_, err := m.ProxyTarget("a", "")
	var np *proxy.NoPortsError
	if !errors.As(err, &np) || np.Name != "a" {
		t.Fatalf("want NoPortsError, got %v", err)
	}
}
