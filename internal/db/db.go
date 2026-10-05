// Package db clones and drops the per-box copies of a project's database.
package db

import (
	"context"
	"fmt"

	"github.com/agentbox-dev/agentbox/internal/config"
)

// Clone identifies a box's database copy. URL is what goes into db.url_env.
type Clone struct {
	Name string
	URL  string
}

type Driver interface {
	// Prepare does one-time per-repo setup (e.g. building a template); cheap when already done.
	Prepare(ctx context.Context) error
	// Clone creates the copy for box. boxDir is the box's worktree (used by file-based drivers).
	Clone(ctx context.Context, box, boxDir string) (Clone, error)
	// Drop deletes the clone called name. It succeeds if the clone is already gone.
	Drop(ctx context.Context, name, boxDir string) error
	// Refresh re-snapshots the source for future clones.
	Refresh(ctx context.Context) error
	// Check verifies required tools and permissions.
	Check(ctx context.Context) error
	// List returns the names of the box clones that currently exist.
	List(ctx context.Context) ([]string, error)
}

type Options struct {
	// Terminate lets the driver kill stray connections to its template instead of refusing.
	Terminate bool
}

func New(cfg *config.DB, repoRoot string, opts Options) (Driver, error) {
	switch cfg.Driver {
	case "sqlite":
		return &SQLite{Root: repoRoot, Rel: cfg.Path(), Source: cfg.Source}, nil
	case "postgres":
		return NewPostgres(cfg.Source, opts)
	}
	return nil, fmt.Errorf("unknown db driver %q", cfg.Driver)
}

// BusyError means a database that must be idle has open connections.
type BusyError struct {
	DB   string
	PIDs []int32
}

func (e *BusyError) Error() string {
	return fmt.Sprintf("database %s has %d active connection(s) (pids %v)", e.DB, len(e.PIDs), e.PIDs)
}
