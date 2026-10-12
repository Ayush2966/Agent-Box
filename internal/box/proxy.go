package box

import (
	"slices"

	"github.com/Ayush2966/Agent-Box/internal/config"
	"github.com/Ayush2966/Agent-Box/internal/proxy"
	"github.com/Ayush2966/Agent-Box/internal/state"
)

// ProxyTarget resolves a proxy host to a box: <name>.localhost when repoSlug
// is "" (the name must then be unique across repos), or
// <name>.<repoSlug>.localhost. It is the proxy package's Lookup.
func (m *Manager) ProxyTarget(name, repoSlug string) (proxy.Target, error) {
	boxes, err := m.d.Store.ListBoxes("")
	if err != nil {
		return proxy.Target{}, err
	}
	counts := map[string]int{}
	for _, b := range boxes {
		counts[b.Name]++
	}
	hostOf := func(b state.Box) string {
		if counts[b.Name] == 1 {
			return b.Name + ".localhost"
		}
		return b.Name + "." + b.RepoID + ".localhost"
	}
	var matches []state.Box
	for _, b := range boxes {
		if b.Name == name && (repoSlug == "" || b.RepoID == repoSlug) {
			matches = append(matches, b)
		}
	}
	switch len(matches) {
	case 0:
		cands := make([]string, 0, len(boxes))
		for _, b := range boxes {
			cands = append(cands, hostOf(b))
		}
		slices.Sort(cands)
		return proxy.Target{}, &proxy.NoSuchBoxError{Name: name, Candidates: cands}
	case 1:
	default:
		hosts := make([]string, 0, len(matches))
		for _, b := range matches {
			hosts = append(hosts, b.Name+"."+b.RepoID+".localhost")
		}
		slices.Sort(hosts)
		return proxy.Target{}, &proxy.AmbiguousError{Name: name, Hosts: hosts}
	}
	b := matches[0]
	if len(b.Ports) == 0 {
		return proxy.Target{}, &proxy.NoPortsError{Name: b.Name}
	}
	return proxy.Target{
		Repo: b.RepoID, Name: b.Name, State: string(b.State), LastError: b.LastError,
		Port: m.proxyPort(b),
		Tail: func(n int) []string {
			lines, _ := m.LogTail(b.RepoID, b.Name, n)
			return lines
		},
	}, nil
}

// proxyPort is the port named by the repo's `proxy:` key (default: the first
// of `ports`), or the box's lowest port when the config can't say.
func (m *Manager) proxyPort(b state.Box) int {
	if repo, err := m.d.Store.RepoByID(b.RepoID); err == nil {
		if cfg, err := config.Load(repo.Path); err == nil {
			if p := b.Ports[cfg.ProxyPort()]; p != 0 {
				return p
			}
		}
	}
	lowest := 0
	for _, p := range b.Ports {
		if lowest == 0 || p < lowest {
			lowest = p
		}
	}
	return lowest
}
