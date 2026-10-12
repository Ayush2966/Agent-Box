package cli

import (
	"errors"
	"fmt"
	"os/exec"
	"runtime"

	"github.com/spf13/cobra"
)

func (a *app) uiCmd() *cobra.Command {
	var noOpen bool
	cmd := &cobra.Command{
		Use:   "ui",
		Short: "Open the web dashboard in your browser",
		Args:  exactArgs(0, "no arguments"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			if err := a.connect(); err != nil {
				return err
			}
			h, err := a.c.HealthInfo(ctx)
			if err != nil {
				return err
			}
			if h.APIAddr == "" {
				return fmt.Errorf("the daemon's HTTP API is disabled (its port is probably in use); see %s and api_addr in %s", a.paths.Log, a.paths.Config)
			}
			if !h.UI {
				return errors.New("this agentbox binary was built without the dashboard; install a release build, or run `make ui` (needs Node 20+) and rebuild")
			}
			lc, err := a.c.LoginCode(ctx)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "dashboard: %s\n", lc.URL)
			if noOpen {
				return nil
			}
			if err := openBrowser(lc.URL); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "could not open a browser (%v); open the link above yourself\n", err)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&noOpen, "no-open", false, "print the sign-in link instead of opening a browser")
	return cmd
}

func openBrowser(u string) error {
	var c *exec.Cmd
	if runtime.GOOS == "darwin" {
		c = exec.Command("open", u)
	} else {
		c = exec.Command("xdg-open", u)
	}
	if err := c.Start(); err != nil {
		return err
	}
	go c.Wait() // reap the opener
	return nil
}
