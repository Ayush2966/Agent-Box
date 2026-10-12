package tui

import (
	"context"
	"errors"

	tea "github.com/charmbracelet/bubbletea"
)

// Run drives the dashboard until the user quits or ctx is cancelled.
func Run(ctx context.Context, a API, opts Options) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	p := tea.NewProgram(New(ctx, a, opts), tea.WithAltScreen(), tea.WithContext(ctx))
	_, err := p.Run()
	if err != nil && (errors.Is(err, tea.ErrProgramKilled) || ctx.Err() != nil) {
		return nil // ctrl+c / cancelled: not an error
	}
	return err
}
