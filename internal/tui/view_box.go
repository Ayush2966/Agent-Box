package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/agentbox-dev/agentbox/internal/api"
)

type boxTab int

const (
	tabOverview boxTab = iota
	tabLogs
	tabDiff
)

const maxLogLines = 5000

func (m *Model) openBox(id string) tea.Cmd {
	if m.cur != id {
		m.stopLogs()
		m.logs = nil
		m.logView.SetContent("")
		m.diff, m.diffLoaded, m.diffLoading, m.diffAgainst = "", false, false, ""
	}
	m.mode, m.cur, m.tab = modeBox, id, tabOverview
	m.logFollow = true
	m.refreshInfo()
	m.infoView.GotoTop()
	return nil
}

// refreshInfo re-renders the overview into its viewport.
func (m *Model) refreshInfo() {
	if b, ok := m.boxes[m.cur]; ok {
		m.infoView.SetContent(m.viewOverview(b))
	}
}

func (m *Model) leaveBox() {
	m.stopLogs()
	m.mode = modeBoxes
}

// stopLogs ends the log stream, if any. logID is cleared so the next visit
// to the logs tab opens a fresh stream.
func (m *Model) stopLogs() {
	if m.logStop != nil {
		m.logStop()
		m.logStop = nil
	}
	m.logID, m.logCh = "", nil
}

// setTab switches tabs and starts what the tab needs: the log stream, or the diff.
func (m *Model) setTab(t boxTab) tea.Cmd {
	m.tab = t
	switch t {
	case tabOverview:
		m.refreshInfo()
	case tabLogs:
		if m.logID != m.cur {
			m.stopLogs()
			m.logs = nil
			m.logFollow = true
			m.logView.SetContent("")
			m.logID = m.cur // claimed now, so a second keypress can't open two streams
			m.logSeq++
			return m.openLogs(m.cur, m.logSeq)
		}
	case tabDiff:
		if !m.diffLoaded && !m.diffLoading {
			m.diffLoading = true
			return m.loadDiff(m.cur, m.diffAgainst)
		}
	}
	return nil
}

func (m Model) updateBox(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	b, ok := m.boxes[m.cur]
	if !ok {
		m.leaveBox()
		return m, nil
	}
	keys := m.keys
	switch {
	case key.Matches(k, keys.Back):
		m.leaveBox()
		return m, nil
	case key.Matches(k, keys.Tab):
		cmd := m.setTab((m.tab + 1) % 3)
		return m, cmd
	case key.Matches(k, keys.Tab1):
		cmd := m.setTab(tabOverview)
		return m, cmd
	case key.Matches(k, keys.Tab2):
		cmd := m.setTab(tabLogs)
		return m, cmd
	case key.Matches(k, keys.Tab3):
		cmd := m.setTab(tabDiff)
		return m, cmd
	case key.Matches(k, keys.StartStop):
		return m, m.toggleRun(b)
	case key.Matches(k, keys.Remove):
		m.askRemove(b)
		return m, nil
	case key.Matches(k, keys.Merge):
		m.askMerge(b)
		return m, nil
	case key.Matches(k, keys.OpenURL):
		return m, m.openURL(b)
	case key.Matches(k, keys.Shell):
		return m, m.shell(b)
	}
	switch m.tab {
	case tabLogs:
		switch {
		case key.Matches(k, keys.Follow):
			m.logView.GotoBottom()
			m.logFollow = true
		case key.Matches(k, keys.Clear):
			m.logs = nil
			m.logView.SetContent("")
		default:
			var cmd tea.Cmd
			m.logView, cmd = m.logView.Update(k)
			m.logFollow = m.logView.AtBottom()
			return m, cmd
		}
	case tabOverview:
		var cmd tea.Cmd
		m.infoView, cmd = m.infoView.Update(k)
		return m, cmd
	case tabDiff:
		if key.Matches(k, keys.Against) {
			m.overlay = newPrompt("Diff against…", m.against(b), nil, m.opts.NoBlink,
				func(m *Model, ref string, _ bool) tea.Cmd {
					m.diffAgainst, m.diff, m.diffLoaded, m.diffLoading = ref, "", false, true
					return m.loadDiff(m.cur, ref)
				})
			return m, nil
		}
		var cmd tea.Cmd
		m.diffView, cmd = m.diffView.Update(k)
		return m, cmd
	}
	return m, nil
}

func (m Model) onLogsOpen(msg logsOpenMsg) (tea.Model, tea.Cmd) {
	if m.mode != modeBox || msg.id != m.logID || msg.seq != m.logSeq {
		msg.stop() // the user moved on before the stream opened
		return m, nil
	}
	m.logStop, m.logCh = msg.stop, msg.ch
	return m, waitLine(msg.id, msg.seq, msg.ch)
}

func (m Model) onLogLine(msg logLineMsg) (tea.Model, tea.Cmd) {
	if msg.id != m.logID || msg.seq != m.logSeq {
		return m, nil // a stale stream's line
	}
	m.logs = append(m.logs, msg.line)
	if len(m.logs) > maxLogLines {
		m.logs = m.logs[len(m.logs)-maxLogLines:]
	}
	m.logView.SetContent(strings.Join(m.logs, "\n"))
	if m.logFollow {
		m.logView.GotoBottom()
	}
	if m.logCh == nil {
		return m, nil
	}
	return m, waitLine(msg.id, msg.seq, m.logCh)
}

func (m Model) onLogsDone(msg logsDoneMsg) (tea.Model, tea.Cmd) {
	if msg.id != m.logID || msg.seq != m.logSeq {
		return m, nil
	}
	m.logStop, m.logCh, m.logID = nil, nil, ""
	if msg.err != nil {
		cmd := m.say(true, "logs: "+describe(msg.err))
		return m, cmd
	}
	return m, nil
}

func (m Model) onDiff(msg diffMsg) (tea.Model, tea.Cmd) {
	if msg.id != m.cur {
		return m, nil
	}
	m.diffLoading = false
	if msg.err != nil {
		cmd := m.say(true, "diff: "+describe(msg.err))
		return m, cmd
	}
	m.diff, m.diffLoaded = msg.text, true
	m.diffView.SetContent(colorDiff(msg.text))
	m.diffView.GotoTop()
	return m, nil
}

func colorDiff(d string) string {
	lines := strings.Split(strings.TrimRight(d, "\n"), "\n")
	for i, l := range lines {
		switch {
		case strings.HasPrefix(l, "+++"), strings.HasPrefix(l, "---"), strings.HasPrefix(l, "diff --git"):
			lines[i] = stHeader.Render(l)
		case strings.HasPrefix(l, "@@"):
			lines[i] = stAccent.Render(l)
		case strings.HasPrefix(l, "+"):
			lines[i] = stOK.Render(l)
		case strings.HasPrefix(l, "-"):
			lines[i] = stErr.Render(l)
		}
	}
	return strings.Join(lines, "\n")
}

func (m Model) against(b api.BoxView) string {
	switch {
	case m.diffAgainst != "":
		return m.diffAgainst
	case b.Base != "":
		return b.Base
	}
	return "the main checkout's branch"
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func (m Model) viewBox(h int) string {
	b, ok := m.boxes[m.cur]
	if !ok {
		return ""
	}
	head := stMuted.Render("← boxes") + "  " + stHeader.Render(b.Name) + "  " + pill(b.State)
	if step, ok := m.steps[b.ID]; ok && step != "" {
		head += "  " + stMuted.Render(step+"…")
	}
	var tabs string
	for i, name := range []string{"1 overview", "2 logs", "3 diff"} {
		if boxTab(i) == m.tab {
			tabs += stTabOn.Render(name)
		} else {
			tabs += stTab.Render(name)
		}
	}
	var body string
	switch m.tab {
	case tabLogs:
		status := fmt.Sprintf("%d lines", len(m.logs))
		if !m.logFollow {
			status += " · scrolled up (G to follow)"
		}
		if m.logID == "" {
			status += " · stream ended (2 to reopen)"
		}
		body = stMuted.Render(status) + "\n" + m.logView.View()
	case tabDiff:
		switch {
		case m.diffLoading:
			body = stMuted.Render("loading diff…")
		case m.diffLoaded && strings.TrimSpace(m.diff) == "":
			body = stMuted.Render("No changes against " + m.against(b) + ".")
		default:
			body = stMuted.Render("against "+m.against(b)+"  (a to change)") + "\n" + m.diffView.View()
		}
	default:
		body = m.infoView.View()
	}
	return lipgloss.JoinVertical(lipgloss.Left, head, tabs, body)
}

func (m Model) viewOverview(b api.BoxView) string {
	row := func(k, v string) string { return "  " + stMuted.Render(fmt.Sprintf("%-15s", k)) + v }
	created := ""
	if !b.CreatedAt.IsZero() {
		created = b.CreatedAt.Local().Format(time.DateTime)
	}
	lines := []string{
		row("dir", b.Dir),
		row("branch", b.Branch),
		row("base", dash(b.Base)),
		row("ahead / behind", fmt.Sprintf("%d / %d", b.Ahead, b.Behind)),
		row("dirty files", strconv.Itoa(b.Dirty)),
		row("ports", formatPorts(b.Ports)),
		row("url", dash(b.URL)),
		row("database", dash(b.DBName)),
		row("pid", dash(nonZero(b.PID))),
		row("created", dash(created)),
	}
	if b.LastError != "" {
		lines = append(lines, row("last error", stErr.Render(b.LastError)))
	}
	if f, ok := m.failures[b.ID]; ok {
		lines = append(lines, row("failure", stErr.Render(f)))
	}
	lines = append(lines, "", "  "+stHeader.Render("Point your agent at it"), "    cd "+strconv.Quote(b.Dir))
	if len(b.Env) > 0 {
		lines = append(lines, "", "  "+stHeader.Render("Managed env"))
		for _, kv := range envList(b.Env) {
			lines = append(lines, "    "+kv)
		}
	}
	return strings.Join(lines, "\n")
}

func nonZero(n int) string {
	if n == 0 {
		return ""
	}
	return strconv.Itoa(n)
}
