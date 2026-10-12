package tui

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/agentbox-dev/agentbox/internal/errs"
)

var (
	stBrand  = lipgloss.NewStyle().Bold(true)
	stHeader = lipgloss.NewStyle().Bold(true)
	stMuted  = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	stErr    = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	stOK     = lipgloss.NewStyle().Foreground(lipgloss.Color("78"))
	stAccent = lipgloss.NewStyle().Foreground(lipgloss.Color("75"))
	stAmber  = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	stTab    = lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Padding(0, 1)
	stTabOn  = lipgloss.NewStyle().Bold(true).Underline(true).Padding(0, 1)
	stDialog = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
)

// pill colours a box state; the text itself always carries the state.
func pill(state string) string {
	switch state {
	case "running":
		return stOK.Render(state)
	case "failed":
		return stErr.Render(state)
	case "creating":
		return stAccent.Render(state)
	case "removing":
		return stAmber.Render(state)
	}
	return stMuted.Render(state)
}

func connPill(c connState) string {
	switch c {
	case connLive:
		return stOK.Render("live")
	case connReconnecting:
		return stAmber.Render("reconnecting")
	}
	return stMuted.Render("connecting")
}

// describe formats any error the way the CLI does: "step failed: message — hint".
func describe(err error) string {
	e := errs.From(err)
	if e == nil {
		return ""
	}
	s := e.Message
	if e.Step != "" {
		s = e.Step + " failed: " + s
	}
	if e.Hint != "" {
		s += " — " + e.Hint
	}
	return s
}

func formatPorts(p map[string]int) string {
	if len(p) == 0 {
		return "-"
	}
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, p[k]))
	}
	return strings.Join(parts, " ")
}

func splitID(id string) (repo, name string) {
	if i := strings.Index(id, "/"); i >= 0 {
		return id[:i], id[i+1:]
	}
	return "", id
}

func truncate(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

func repoBase(path string) string { return filepath.Base(path) }

func envList(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+env[k])
	}
	return out
}
