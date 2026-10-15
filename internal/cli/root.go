// Package cli implements the agentbox command line. Every command except
// init, version and daemon run is a thin client of the daemon's API.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/Ayush2966/Agent-Box/internal/api"
	"github.com/Ayush2966/Agent-Box/internal/client"
	"github.com/Ayush2966/Agent-Box/internal/daemon"
	"github.com/Ayush2966/Agent-Box/internal/errs"
)

var Version = "dev"

// exitError carries a specific exit code; a nil err prints nothing.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string {
	if e.err == nil {
		return ""
	}
	return e.err.Error()
}

func usage(err error) error { return &exitError{code: 2, err: err} }

func Execute() int { return Run(os.Args[1:], os.Stdout, os.Stderr) }

func Run(args []string, stdout, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	root := newRoot()
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)
	err := root.ExecuteContext(ctx)
	if err == nil {
		return 0
	}
	var ee *exitError
	if errors.As(err, &ee) {
		if ee.err != nil {
			fmt.Fprintln(stderr, "error:", ee.err)
		}
		return ee.code
	}
	fmt.Fprintln(stderr, "error:", err)
	if strings.HasPrefix(err.Error(), "unknown command") {
		return 2
	}
	switch errs.From(err).Code {
	case errs.Precondition, errs.UnmergedWork, errs.Conflict:
		return 3
	}
	return 1
}

type app struct {
	repoFlag string
	paths    daemon.Paths
	c        *client.Client
}

func newRoot() *cobra.Command {
	a := &app{}
	root := &cobra.Command{
		Use:           "agentbox",
		Short:         "Isolated dev environments for parallel AI coding agents",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&a.repoFlag, "repo", "", "path to the repo (default: current directory)")
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return usage(err) })
	root.AddCommand(
		a.initCmd(), a.checkCmd(), a.newCmd(), a.prCmd(), a.openCmd(), a.lsCmd(), a.logsCmd(), a.startCmd(), a.stopCmd(), a.execCmd(),
		a.mergeCmd(), a.rmCmd(), a.dbCmd(), a.doctorCmd(), a.uiCmd(), a.tuiCmd(), a.daemonCmd(), versionCmd(),
	)
	return root
}

func exactArgs(n int, what string) cobra.PositionalArgs {
	return func(_ *cobra.Command, args []string) error {
		if len(args) != n {
			return usage(fmt.Errorf("expected %s", what))
		}
		return nil
	}
}

func (a *app) connect() error {
	home, err := daemon.DefaultHome()
	if err != nil {
		return err
	}
	a.paths = daemon.PathsFor(home)
	if err := daemon.EnsureRunning(a.paths); err != nil {
		return err
	}
	a.c = client.New(a.paths.Sock)
	return nil
}

func (a *app) repoPath() (string, error) {
	p := a.repoFlag
	if p == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		p = wd
	}
	return filepath.Abs(p)
}

// setup connects to the daemon and resolves the current repo.
func (a *app) setup(ctx context.Context) (api.RepoView, error) {
	if err := a.connect(); err != nil {
		return api.RepoView{}, err
	}
	p, err := a.repoPath()
	if err != nil {
		return api.RepoView{}, err
	}
	return a.c.ResolveRepo(ctx, p)
}
