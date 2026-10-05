package cli

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/agentbox-dev/agentbox/internal/client"
	"github.com/agentbox-dev/agentbox/internal/config"
	"github.com/agentbox-dev/agentbox/internal/daemon"
	"github.com/agentbox-dev/agentbox/internal/errs"
	"github.com/agentbox-dev/agentbox/internal/initcfg"
	"github.com/agentbox-dev/agentbox/internal/worktree"
)

func (a *app) initCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Write a starter agentbox.yaml for this repo",
		Args:  exactArgs(0, "no arguments"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := a.repoPath()
			if err != nil {
				return err
			}
			root, err := worktree.MainWorktree(p)
			if err != nil {
				return err
			}
			target := filepath.Join(root, config.FileName)
			if _, err := os.Stat(target); err == nil {
				return errs.New(errs.Precondition, "%s already exists", target)
			}
			res, err := initcfg.Detect(root)
			if err != nil {
				return err
			}
			if err := os.WriteFile(target, []byte(res.YAML), 0o644); err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "wrote %s\n", target)
			for _, w := range res.Warnings {
				fmt.Fprintf(out, "warning: %s\n", w)
			}
			fmt.Fprintln(out, "review it, then create a box with `agentbox new <name>`")
			return nil
		},
	}
}

func (a *app) dbCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "db", Short: "Database commands"}
	var terminate bool
	refresh := &cobra.Command{
		Use:   "refresh",
		Short: "Re-snapshot the source database that new boxes are cloned from",
		Args:  exactArgs(0, "no arguments"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			repo, err := a.setup(cmd.Context())
			if err != nil {
				return err
			}
			if err := a.c.RefreshDB(cmd.Context(), repo.ID, terminate); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "database snapshot refreshed")
			return nil
		},
	}
	refresh.Flags().BoolVar(&terminate, "terminate", false, "terminate stray connections to the template")
	cmd.AddCommand(refresh)
	return cmd
}

func (a *app) doctorCmd() *cobra.Command {
	var fix bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Find (and with --fix, safely repair) drift between agentbox and reality",
		Args:  exactArgs(0, "no arguments"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			problems := 0
			if _, err := exec.LookPath("git"); err != nil {
				fmt.Fprintln(out, "✗ git not found on PATH")
				problems++
			}
			repo, err := a.setup(cmd.Context())
			if err != nil {
				return err
			}
			fs, err := a.c.Doctor(cmd.Context(), repo.ID, fix)
			if err != nil {
				return err
			}
			for _, f := range fs {
				mark := "✗"
				if f.Fixed {
					mark = "✓ fixed"
				}
				fmt.Fprintf(out, "%s [%s] %s\n", mark, f.Kind, f.Detail)
				problems++
			}
			if problems == 0 {
				fmt.Fprintln(out, "✓ no problems found")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&fix, "fix", false, "repair what can be repaired safely")
	return cmd
}

func (a *app) daemonCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "daemon", Short: "Manage the background daemon (it starts automatically)"}
	paths := func() (daemon.Paths, error) {
		home, err := daemon.DefaultHome()
		return daemon.PathsFor(home), err
	}
	run := &cobra.Command{
		Use:    "run",
		Short:  "Run the daemon in the foreground",
		Hidden: true,
		Args:   exactArgs(0, "no arguments"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := paths()
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return daemon.Run(ctx, p)
		},
	}
	status := &cobra.Command{
		Use:   "status",
		Short: "Show whether the daemon is running",
		Args:  exactArgs(0, "no arguments"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := paths()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if !daemon.Ping(p.Sock) {
				fmt.Fprintln(out, "daemon not running (it starts automatically on the next command)")
				return nil
			}
			pid, _ := daemon.ReadPID(p.PID)
			boxes, err := client.New(p.Sock).ListBoxes(cmd.Context(), "")
			if err != nil {
				return err
			}
			running := 0
			for _, b := range boxes {
				if b.State == "running" {
					running++
				}
			}
			fmt.Fprintf(out, "daemon running (pid %d)\nsocket: %s\nboxes: %d total, %d running\n", pid, p.Sock, len(boxes), running)
			return nil
		},
	}
	stopDaemon := func(cmd *cobra.Command, p daemon.Paths) error {
		if daemon.Ping(p.Sock) {
			if boxes, err := client.New(p.Sock).ListBoxes(cmd.Context(), ""); err == nil {
				for _, b := range boxes {
					if b.State == "running" {
						fmt.Fprintf(cmd.OutOrStdout(), "stopping dev server of %s\n", b.Name)
					}
				}
			}
		}
		return daemon.Stop(p, 15*time.Second)
	}
	stop := &cobra.Command{
		Use:   "stop",
		Short: "Stop the daemon and every dev server",
		Args:  exactArgs(0, "no arguments"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := paths()
			if err != nil {
				return err
			}
			return stopDaemon(cmd, p)
		},
	}
	restart := &cobra.Command{
		Use:   "restart",
		Short: "Restart the daemon (dev servers are stopped, not restarted)",
		Args:  exactArgs(0, "no arguments"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := paths()
			if err != nil {
				return err
			}
			if err := stopDaemon(cmd, p); err != nil {
				return err
			}
			if err := daemon.EnsureRunning(p); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "daemon restarted")
			return nil
		},
	}
	cmd.AddCommand(run, status, stop, restart)
	return cmd
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Args:  exactArgs(0, "no arguments"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprintln(cmd.OutOrStdout(), "agentbox", Version)
			return nil
		},
	}
}
