package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Ayush2966/Agent-Box/internal/worktree"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Ayush2966/Agent-Box/internal/box"
	"github.com/Ayush2966/Agent-Box/internal/client"
	"github.com/Ayush2966/Agent-Box/internal/errs"
	"github.com/Ayush2966/Agent-Box/internal/events"
)

func (a *app) newCmd() *cobra.Command {
	var opts box.NewOptions
	cmd := &cobra.Command{
		Use:   "new <name>",
		Short: "Create a box: worktree, ports, database copy, env file and dev server",
		Args:  exactArgs(1, "a box name"),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()
			repo, err := a.setup(ctx)
			if err != nil {
				return err
			}
			opts.RepoPath, opts.Name = repo.Path, args[0]
			evs, err := a.c.Events(ctx) // subscribe before creating so no event is missed
			if err != nil {
				return err
			}
			created, err := a.c.CreateBox(ctx, opts)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "creating box %s\n", opts.Name)
			if err := waitCreated(evs, created.ID, out); err != nil {
				return err
			}
			b, err := a.c.GetBox(ctx, repo.ID, opts.Name)
			if err != nil {
				return err
			}
			printBox(out, b)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&opts.From, "from", "", "branch or commit to start from (default: current HEAD)")
	f.BoolVar(&opts.NoDev, "no-dev", false, "don't start the dev server")
	f.BoolVar(&opts.KeepFailed, "keep-failed", false, "keep a box that failed to create, for debugging")
	f.BoolVar(&opts.Terminate, "terminate", false, "terminate stray connections to the Postgres template")
	return cmd
}

// prCmd creates a box from a pull request: `agentbox pr 123` fetches the
// request's head from origin and starts a box named pr-123 on it.
func (a *app) prCmd() *cobra.Command {
	var opts box.NewOptions
	var name string
	cmd := &cobra.Command{
		Use:   "pr <number>",
		Short: "Create a box from a pull request on origin (GitHub, GitLab, Gitea)",
		Args:  exactArgs(1, "a pull request number"),
		RunE: func(cmd *cobra.Command, args []string) error {
			n, err := strconv.Atoi(args[0])
			if err != nil || n <= 0 {
				return usage(fmt.Errorf("%q is not a pull request number", args[0]))
			}
			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()
			repo, err := a.setup(ctx)
			if err != nil {
				return err
			}
			sha, webURL, err := worktree.FetchPullRequest(repo.Path, n)
			if err != nil {
				return errs.New(errs.NotFound, "%v", err).
					WithHint("the number is the one in the pull request's URL; the repo's origin must be where the request was opened")
			}
			opts.RepoPath, opts.From = repo.Path, sha
			opts.Name = name
			if opts.Name == "" {
				opts.Name = fmt.Sprintf("pr-%d", n)
			}
			evs, err := a.c.Events(ctx)
			if err != nil {
				return err
			}
			created, err := a.c.CreateBox(ctx, opts)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "creating box %s from pull request %d (%s)\n", opts.Name, n, sha[:12])
			if err := waitCreated(evs, created.ID, out); err != nil {
				return err
			}
			b, err := a.c.GetBox(ctx, repo.ID, opts.Name)
			if err != nil {
				return err
			}
			printBox(out, b)
			if webURL != "" {
				fmt.Fprintf(out, "\npull request:\n  %s\n", webURL)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&name, "name", "", "box name (default: pr-<number>)")
	f.BoolVar(&opts.NoDev, "no-dev", false, "don't start the dev server")
	f.BoolVar(&opts.KeepFailed, "keep-failed", false, "keep a box that failed to create, for debugging")
	f.BoolVar(&opts.Terminate, "terminate", false, "terminate stray connections to the Postgres template")
	return cmd
}

func waitCreated(evs <-chan client.Event, id string, out io.Writer) error {
	for ev := range evs {
		if ev.Box != id {
			continue
		}
		switch ev.Type {
		case events.BoxStep:
			fmt.Fprintf(out, "  • %v\n", ev.Data["step"])
		case events.BoxState:
			if e, ok := ev.Data["error"]; ok {
				return errors.New(fmt.Sprint(e))
			}
			switch ev.Data["state"] {
			case "ready", "running":
				return nil
			case "stopped":
				return fmt.Errorf("box created, but its dev server keeps crashing: %v\nhint: see `agentbox logs %s`", ev.Data["last_error"], id[strings.LastIndex(id, "/")+1:])
			}
		case events.BoxRemoved:
			if e, ok := ev.Data["error"]; ok {
				return errors.New(fmt.Sprint(e))
			}
			return errors.New("the box was removed while it was being created")
		}
	}
	return errors.New("lost the connection to the agentbox daemon")
}

func (a *app) lsCmd() *cobra.Command {
	var all, asJSON bool
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List boxes",
		Args:  exactArgs(0, "no arguments"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			repoID := ""
			if all {
				if err := a.connect(); err != nil {
					return err
				}
			} else {
				repo, err := a.setup(ctx)
				if err != nil {
					return err
				}
				repoID = repo.ID
			}
			boxes, err := a.c.ListBoxes(ctx, repoID)
			if err != nil {
				return err
			}
			if asJSON {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(boxes)
			}
			printTable(cmd.OutOrStdout(), boxes, all)
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "list boxes of every repo")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

func (a *app) logsCmd() *cobra.Command {
	var follow bool
	var tail int
	cmd := &cobra.Command{
		Use:   "logs <name>",
		Short: "Show a box's setup and dev-server output",
		Args:  exactArgs(1, "a box name"),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			repo, err := a.setup(ctx)
			if err != nil {
				return err
			}
			err = a.c.Logs(ctx, repo.ID, args[0], tail, follow, cmd.OutOrStdout())
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "keep streaming new output")
	cmd.Flags().IntVar(&tail, "tail", 200, "number of lines to show")
	return cmd
}

func (a *app) simpleBoxCmd(use, short, done string, fn func(ctx context.Context, repoID, name string) error) *cobra.Command {
	return &cobra.Command{
		Use:   use + " <name>",
		Short: short,
		Args:  exactArgs(1, "a box name"),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, err := a.setup(cmd.Context())
			if err != nil {
				return err
			}
			if err := fn(cmd.Context(), repo.ID, args[0]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", done, args[0])
			return nil
		},
	}
}

func (a *app) startCmd() *cobra.Command {
	return a.simpleBoxCmd("start", "Start a box's dev server", "started", func(ctx context.Context, r, n string) error { return a.c.StartBox(ctx, r, n) })
}

func (a *app) stopCmd() *cobra.Command {
	return a.simpleBoxCmd("stop", "Stop a box's dev server (keeps its files, ports and database)", "stopped", func(ctx context.Context, r, n string) error { return a.c.StopBox(ctx, r, n) })
}

func (a *app) execCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "exec <name> -- <command> [args...]",
		Short: "Run a command inside a box with its env",
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) < 2 {
				return usage(errors.New("expected a box name and a command"))
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, err := a.setup(cmd.Context())
			if err != nil {
				return err
			}
			b, err := a.c.GetBox(cmd.Context(), repo.ID, args[0])
			if err != nil {
				return err
			}
			c := exec.Command(args[1], args[2:]...)
			c.Dir = b.Dir
			c.Env = os.Environ()
			for k, v := range b.Env {
				c.Env = append(c.Env, k+"="+v)
			}
			c.Stdin, c.Stdout, c.Stderr = os.Stdin, cmd.OutOrStdout(), cmd.ErrOrStderr()
			if err := c.Run(); err != nil {
				var ee *exec.ExitError
				if errors.As(err, &ee) {
					return &exitError{code: ee.ExitCode()}
				}
				return err
			}
			return nil
		},
	}
	cmd.Flags().SetInterspersed(false) // everything after the box name belongs to the command
	return cmd
}

func (a *app) mergeCmd() *cobra.Command {
	var opts box.MergeOptions
	cmd := &cobra.Command{
		Use:   "merge <name>",
		Short: "Merge a box's branch into the main checkout's current branch",
		Args:  exactArgs(1, "a box name"),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, err := a.setup(cmd.Context())
			if err != nil {
				return err
			}
			res, err := a.c.Merge(cmd.Context(), repo.ID, args[0], opts)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "merged agentbox/%s into %s\nremove the box with `agentbox rm %s`\n", args[0], res.Into, args[0])
			return nil
		},
	}
	cmd.Flags().StringVar(&opts.Into, "into", "", "target branch (must be checked out in the main checkout)")
	cmd.Flags().BoolVar(&opts.Squash, "squash", false, "squash the box's commits into one")
	return cmd
}

func (a *app) rmCmd() *cobra.Command {
	var force, keepBranch bool
	cmd := &cobra.Command{
		Use:   "rm <name>",
		Short: "Remove a box: stop it, drop its database, free its ports, delete its worktree",
		Args:  exactArgs(1, "a box name"),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, out := cmd.Context(), cmd.OutOrStdout()
			repo, err := a.setup(ctx)
			if err != nil {
				return err
			}
			err = a.c.RemoveBox(ctx, repo.ID, args[0], force, keepBranch)
			if err != nil && errs.From(err).Code == errs.UnmergedWork && isTerminal(os.Stdin) {
				fmt.Fprintf(out, "%s\nRemove anyway and discard it? [y/N] ", errs.From(err).Message)
				ans, _ := bufio.NewReader(os.Stdin).ReadString('\n')
				if strings.ToLower(strings.TrimSpace(ans)) != "y" {
					return &exitError{code: 3, err: errors.New("aborted")}
				}
				err = a.c.RemoveBox(ctx, repo.ID, args[0], true, keepBranch)
			}
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "removed box %s\n", args[0])
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "discard uncommitted or unmerged work without asking")
	cmd.Flags().BoolVar(&keepBranch, "keep-branch", false, "keep the agentbox/<name> branch")
	return cmd
}

func isTerminal(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}
