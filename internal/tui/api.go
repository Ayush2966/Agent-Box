// Package tui is the terminal dashboard: a bubbletea program over the daemon API.
package tui

import (
	"context"

	"github.com/Ayush2966/Agent-Box/internal/api"
	"github.com/Ayush2966/Agent-Box/internal/box"
	"github.com/Ayush2966/Agent-Box/internal/client"
)

// API is the slice of the daemon client the TUI uses. *client.Client satisfies it;
// tests use an in-memory fake.
type API interface {
	Repos(ctx context.Context) ([]api.RepoView, error)
	ListBoxes(ctx context.Context, repoID string) ([]api.BoxView, error)
	GetBox(ctx context.Context, repoID, name string) (api.BoxView, error)
	CreateBox(ctx context.Context, opts box.NewOptions) (api.Created, error)
	StartBox(ctx context.Context, repoID, name string) error
	StopBox(ctx context.Context, repoID, name string) error
	RemoveBox(ctx context.Context, repoID, name string, force, keepBranch bool) error
	Merge(ctx context.Context, repoID, name string, opts box.MergeOptions) (api.MergeResult, error)
	RefreshDB(ctx context.Context, repoID string, terminate bool) error
	Doctor(ctx context.Context, repoID string, fix bool) ([]box.Finding, error)
	Diff(ctx context.Context, repoID, name, against string) (string, error)
	Events(ctx context.Context) (<-chan client.Event, error)
	LogLines(ctx context.Context, repoID, name string, tail int) (<-chan string, error)
}

var _ API = (*client.Client)(nil)
