package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/Ayush2966/Agent-Box/internal/box"
)

type doctorState struct {
	repo     string
	findings []box.Finding
	busy     bool
	loaded   bool
	err      string
}

func (m *Model) openDoctor(repo string) tea.Cmd {
	m.mode = modeDoctor
	m.doctor = doctorState{repo: repo, busy: true}
	return m.runDoctor(repo, false)
}

func (m Model) updateDoctor(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	d := &m.doctor
	switch {
	case key.Matches(k, m.keys.Back):
		m.mode = modeBoxes
	case key.Matches(k, m.keys.Fix):
		if !d.busy {
			d.busy = true
			return m, m.runDoctor(d.repo, true)
		}
	case key.Matches(k, m.keys.Recheck):
		if !d.busy {
			d.busy = true
			return m, m.runDoctor(d.repo, false)
		}
	}
	return m, nil
}

func (m Model) onFindings(msg findingsMsg) (tea.Model, tea.Cmd) {
	if msg.repo != m.doctor.repo {
		return m, nil
	}
	m.doctor.busy, m.doctor.loaded = false, true
	if msg.err != nil {
		m.doctor.err = describe(msg.err)
		return m, nil
	}
	m.doctor.err, m.doctor.findings = "", msg.findings
	return m, nil
}

func (m Model) viewDoctor(h int) string {
	d := m.doctor
	lines := []string{stHeader.Render("Doctor") + stMuted.Render("  "+m.repoPath(d.repo)), ""}
	switch {
	case d.err != "":
		lines = append(lines, stErr.Render(d.err))
	case d.busy && !d.loaded:
		lines = append(lines, stMuted.Render("checking…"))
	case len(d.findings) == 0:
		lines = append(lines, stOK.Render("✓ no problems found"))
	default:
		for _, f := range d.findings {
			mark := stErr.Render("✗")
			if f.Fixed {
				mark = stOK.Render("✓ fixed")
			}
			lines = append(lines, fmt.Sprintf("%s %s %s", mark, stAccent.Render("["+f.Kind+"]"), f.Detail))
		}
	}
	if d.busy && d.loaded {
		lines = append(lines, "", stMuted.Render("working…"))
	}
	return strings.Join(lines, "\n")
}
