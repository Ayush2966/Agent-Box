package cli

import (
	"fmt"
	"github.com/Ayush2966/Agent-Box/internal/check"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/Ayush2966/Agent-Box/internal/client"
	"github.com/Ayush2966/Agent-Box/internal/config"
	"github.com/Ayush2966/Agent-Box/internal/daemon"
	"github.com/Ayush2966/Agent-Box/internal/errs"
	"github.com/Ayush2966/Agent-Box/internal/initcfg"
	"github.com/Ayush2966/Agent-Box/internal/worktree"
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

// checkCmd validates agentbox.yaml and can rehearse a box, without the daemon:
// what to run in CI after editing the config.
func (a *app) checkCmd() *cobra.Command {
	var setup bool
	var dev time.Duration
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Validate agentbox.yaml; with --setup/--dev, rehearse a box in a throwaway worktree",
		Long: `Checks agentbox.yaml and the files it names, then (optionally) creates a
temporary worktree of HEAD, gives it free ports like a box, runs setup and
starts dev until the port answers. Needs no daemon and leaves nothing
behind, so it runs in CI: agentbox check --setup --dev 60s`,
		Args: exactArgs(0, "no arguments"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := a.repoPath()
			if err != nil {
				return err
			}
			root, err := worktree.MainWorktree(p)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			findings := check.Run(cmd.Context(), root, check.Options{Setup: setup, Dev: dev, Out: cmd.ErrOrStderr()})
			for _, f := range findings {
				fmt.Fprintf(out, "%s: %s\n", f.Level, f.Msg)
			}
			if check.Failed(findings) {
				return errs.New(errs.Invalid, "%s has problems", config.FileName)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&setup, "setup", false, "run the setup command in a temporary worktree")
	cmd.Flags().DurationVar(&dev, "dev", 0, "after setup, start the dev server and wait this long for its port (e.g. 60s)")
	return cmd
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
			daemon.Version = Version
			// By-name compose commands inherit this cwd; keep it a directory
			// that holds no compose file. Created first: a hand-run daemon
			// on a fresh AGENTBOX_HOME has nothing to chdir into yet.
			if err := os.MkdirAll(p.Home, 0o700); err != nil {
				return err
			}
			if err := os.Chdir(p.Home); err != nil {
				return err
			}
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
				if pid, stale := daemon.StalePID(p); stale {
					_ = os.Remove(p.PID)
					fmt.Fprintf(out, "daemon not running; removed stale pid file (pid %d is dead)\n", pid)
				} else {
					fmt.Fprintln(out, "daemon not running (it starts automatically on the next command)")
				}
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
		out := cmd.OutOrStdout()
		if !daemon.Ping(p.Sock) {
			fmt.Fprintln(out, "daemon not running")
			return nil
		}
		pid, _ := daemon.ReadPID(p.PID)
		if boxes, err := client.New(p.Sock).ListBoxes(cmd.Context(), ""); err == nil {
			for _, b := range boxes {
				if b.State == "running" {
					fmt.Fprintf(out, "stopping dev server of %s\n", b.Name)
				}
			}
		}
		if err := daemon.Stop(p, 15*time.Second); err != nil {
			return err
		}
		fmt.Fprintf(out, "daemon stopped (pid %d)\n", pid)
		return nil
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
