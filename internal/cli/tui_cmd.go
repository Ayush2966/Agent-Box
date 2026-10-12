package cli

import (
	"errors"
	"os"

	"github.com/spf13/cobra"

	"github.com/agentbox-dev/agentbox/internal/tui"
)

func (a *app) tuiCmd() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "tui",
		Short: "Open the terminal dashboard",
		Args:  exactArgs(0, "no arguments"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !isTerminal(os.Stdout) || !isTerminal(os.Stdin) {
				return errors.New("agentbox tui needs an interactive terminal (try `agentbox ls --json` for scripts)")
			}
			if err := a.connect(); err != nil {
				return err
			}
			opts := tui.Options{OpenBrowser: openBrowser}
			if !all {
				if p, err := a.repoPath(); err == nil {
					if rv, err := a.c.ResolveRepo(cmd.Context(), p); err == nil {
						opts.Repo = rv.ID // start on the repo we were launched in; r cycles to the others
					}
				}
			}
			return tui.Run(cmd.Context(), a.c, opts)
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "start with every repo shown instead of the current one")
	return cmd
}
