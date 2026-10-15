package tui

import (
	"sort"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/Ayush2966/Agent-Box/internal/api"
)

// visibleBoxes applies the repo filter and the name query, sorted by repo then name.
func (m Model) visibleBoxes() []api.BoxView {
	out := make([]api.BoxView, 0, len(m.boxes))
	for _, b := range m.boxes {
		if m.filter != "" && b.Repo != m.filter {
			continue
		}
		if m.query != "" && !strings.Contains(b.Name, m.query) {
			continue
		}
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Repo != out[j].Repo {
			return out[i].Repo < out[j].Repo
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// stateCell is plain text: bubbles' table truncates cells without regard to ANSI codes.
func (m Model) stateCell(b api.BoxView) string {
	s := b.State
	if step, ok := m.steps[b.ID]; ok && step != "" {
		s += " " + step + "…"
	} else if b.State == "failed" && b.LastStep != "" {
		s += " (" + b.LastStep + ")"
	}
	return s
}

type column struct {
	title string
	width int
	show  bool
	cell  func(b api.BoxView) string
}

func (m Model) columns() []column {
	w := m.width
	return []column{
		{"NAME", 14, true, func(b api.BoxView) string { return b.Name }},
		{"STATE", 22, true, m.stateCell},
		{"AHEAD", 6, true, func(b api.BoxView) string { return strconv.Itoa(b.Ahead) }},
		{"BEHIND", 7, w >= 90, func(b api.BoxView) string { return strconv.Itoa(b.Behind) }},
		{"DIRTY", 6, true, func(b api.BoxView) string { return strconv.Itoa(b.Dirty) }},
		{"URL", 26, w >= 100, func(b api.BoxView) string {
			return strings.TrimSuffix(strings.TrimPrefix(b.URL, "http://"), "/")
		}},
		{"REPO", 18, w >= 120 && m.filter == "", func(b api.BoxView) string { return repoBase(b.RepoPath) }},
		{"PORTS", 24, w >= 145, func(b api.BoxView) string { return formatPorts(b.Ports) }},
	}
}

// refreshTable rebuilds the rows and keeps the cursor on the same box.
func (m *Model) refreshTable() {
	keep, _ := m.selectedID()
	oldIndex := m.table.Cursor()
	var cols []table.Column
	var shown []column
	for _, c := range m.columns() {
		if c.show {
			cols = append(cols, table.Column{Title: c.title, Width: c.width})
			shown = append(shown, c)
		}
	}
	boxes := m.visibleBoxes()
	rows := make([]table.Row, 0, len(boxes))
	ids := make([]string, 0, len(boxes))
	for _, b := range boxes {
		row := make(table.Row, 0, len(shown))
		for _, c := range shown {
			row = append(row, c.cell(b))
		}
		rows = append(rows, row)
		ids = append(ids, b.ID)
	}
	m.table.SetRows(nil) // old rows may have more cells than the new columns; SetColumns re-renders them
	m.table.SetColumns(cols)
	m.table.SetRows(rows)
	m.rowIDs = ids
	m.table.GotoTop()
	found := false
	for i, id := range ids {
		if id == keep {
			m.table.MoveDown(i)
			found = true
			break
		}
	}
	if !found && len(ids) > 0 {
		m.table.MoveDown(min(max(oldIndex, 0), len(ids)-1))
	}
}

func (m Model) selectedID() (string, bool) {
	i := m.table.Cursor()
	if i < 0 || i >= len(m.rowIDs) {
		return "", false
	}
	return m.rowIDs[i], true
}

func (m Model) selected() (api.BoxView, bool) {
	id, ok := m.selectedID()
	if !ok {
		return api.BoxView{}, false
	}
	b, ok := m.boxes[id]
	return b, ok
}

// repoFor picks the repo an action without a box should apply to: the
// filter, else the selected box's repo, else the only repo there is.
func (m Model) repoFor(b api.BoxView, ok bool) string {
	switch {
	case m.filter != "":
		return m.filter
	case ok:
		return b.Repo
	case len(m.repos) == 1:
		return m.repos[0].ID
	}
	return ""
}

func (m *Model) cycleRepo() {
	ids := []string{""}
	for _, r := range m.repos {
		ids = append(ids, r.ID)
	}
	next := 0
	for i, id := range ids {
		if id == m.filter {
			next = (i + 1) % len(ids)
			break
		}
	}
	m.filter = ids[next]
	m.refreshTable()
}

func (m Model) updateBoxes(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	b, ok := m.selected()
	keys := m.keys
	if k.String() == "g" && !ok {
		return m, m.loadAll()
	}
	switch {
	case key.Matches(k, keys.Back) && m.query != "":
		m.query = ""
		m.refreshTable()
	case key.Matches(k, keys.Open):
		if ok {
			cmd := m.openBox(b.ID)
			return m, cmd
		}
	case key.Matches(k, keys.New):
		return m.startNew()
	case key.Matches(k, keys.StartStop):
		if ok {
			return m, m.toggleRun(b)
		}
	case key.Matches(k, keys.Remove):
		if ok {
			m.askRemove(b)
		}
	case key.Matches(k, keys.Merge):
		if ok {
			m.askMerge(b)
		}
	case key.Matches(k, keys.Doctor):
		if repo := m.repoFor(b, ok); repo != "" {
			cmd := m.openDoctor(repo)
			return m, cmd
		}
		cmd := m.say(true, "pick a repo first: press r to filter, or select a box")
		return m, cmd
	case key.Matches(k, keys.RefreshDB):
		if repo := m.repoFor(b, ok); repo != "" {
			m.askRefresh(repo)
		} else {
			cmd := m.say(true, "pick a repo first: press r to filter, or select a box")
			return m, cmd
		}
	case key.Matches(k, keys.Repo):
		m.cycleRepo()
	case key.Matches(k, keys.OpenURL):
		if ok {
			return m, m.openURL(b)
		}
	case key.Matches(k, keys.Shell):
		if ok {
			return m, m.shell(b)
		}
	case key.Matches(k, keys.Editor):
		if ok {
			return m, m.openEditor(b)
		}
	case key.Matches(k, keys.Filter):
		m.querying, m.query = true, ""
		m.refreshTable()
	case key.Matches(k, keys.Reload):
		return m, m.loadAll()
	default:
		var cmd tea.Cmd
		m.table, cmd = m.table.Update(k)
		return m, cmd
	}
	return m, nil
}

func (m Model) viewBoxes(h int) string {
	if len(m.rowIDs) == 0 {
		msg := "No boxes yet."
		if m.query != "" {
			msg = "No box matches /" + m.query + "."
		} else if m.filter != "" {
			msg = "No boxes in " + repoBase(m.repoPath(m.filter)) + " yet."
		}
		return "\n  " + msg + "\n\n  " + stMuted.Render("create one with  agentbox new <name>  inside a repo")
	}
	return m.table.View()
}
