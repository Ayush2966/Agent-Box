package tui

import "github.com/charmbracelet/bubbles/key"

type keyMap struct {
	Up, Down, Open, Back, New, StartStop, Remove, Merge, Doctor, RefreshDB, Repo, OpenURL, Shell,
	Filter, Reload, Help, Quit, Tab, Tab1, Tab2, Tab3, Follow, Clear, Against, Fix, Recheck, Editor key.Binding
}

func newKeyMap() keyMap {
	b := func(help, desc string, keys ...string) key.Binding {
		return key.NewBinding(key.WithKeys(keys...), key.WithHelp(help, desc))
	}
	return keyMap{
		Up:        b("↑/k", "up", "up", "k"),
		Down:      b("↓/j", "down", "down", "j"),
		Open:      b("enter", "open", "enter", "l"),
		Back:      b("esc", "back", "esc", "h"),
		New:       b("n", "new box", "n"),
		StartStop: b("s", "start/stop", "s"),
		Remove:    b("x", "remove", "x"),
		Merge:     b("m", "merge", "m"),
		Doctor:    b("d", "doctor", "d"),
		RefreshDB: b("R", "refresh db", "R"),
		Repo:      b("r", "repo filter", "r"),
		OpenURL:   b("o", "open url", "o"),
		Shell:     b("e", "shell", "e"),
		Editor:    b("E", "editor", "E"),
		Filter:    b("/", "filter", "/"),
		Reload:    b("ctrl+r", "reload", "ctrl+r", "f5"),
		Help:      b("?", "help", "?"),
		Quit:      b("q", "quit", "q", "ctrl+c"),
		Tab:       b("tab", "next tab", "tab"),
		Tab1:      b("1", "overview", "1"),
		Tab2:      b("2", "logs", "2"),
		Tab3:      b("3", "diff", "3"),
		Follow:    b("G", "follow", "G"),
		Clear:     b("c", "clear", "c"),
		Against:   b("a", "against…", "a"),
		Fix:       b("f", "fix", "f"),
		Recheck:   b("r", "re-check", "r"),
	}
}

// hb is a help-only binding. It needs a key to count as enabled, or the help
// view skips it.
func hb(h, d string) key.Binding {
	return key.NewBinding(key.WithKeys(h), key.WithHelp(h, d))
}

func (m Model) shortHelp() []key.Binding {
	k := m.keys
	switch m.mode {
	case modeBox:
		return []key.Binding{k.Back, k.Tab, k.StartStop, k.Remove, k.Merge, k.Shell, k.Editor, k.Help}
	case modeNew:
		return []key.Binding{hb("enter", "create"), hb("tab", "next field"), hb("esc", "cancel")}
	case modeDoctor:
		return []key.Binding{k.Fix, k.Recheck, k.Back}
	}
	return []key.Binding{hb("enter", "open"), hb("n", "new"), hb("s", "start/stop"), hb("x", "remove"), hb("m", "merge"), hb("r", "repo"), hb("?", "help"), hb("q", "quit")}
}

func (m Model) fullHelp() [][]key.Binding {
	k := m.keys
	return [][]key.Binding{
		{k.Up, k.Down, k.Open, k.Back, k.Tab, k.Filter, k.Repo},
		{k.New, k.StartStop, k.Remove, k.Merge, k.Doctor, k.RefreshDB},
		{k.OpenURL, k.Shell, k.Editor, k.Follow, k.Clear, k.Against, k.Reload, k.Quit},
		{k.Tab1, k.Tab2, k.Tab3, k.Fix, k.Recheck, k.Help, key.NewBinding(key.WithKeys("g", "G"), key.WithHelp("g/G", "top/bottom"))},
	}
}
