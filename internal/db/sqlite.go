package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// SQLite copies the database file into the box with VACUUM INTO, which is
// consistent even while the source is open or in WAL mode.
type SQLite struct {
	Root   string // main checkout
	Rel    string // clean repo-relative path of the database file
	Source string // db.source as written in agentbox.yaml; used verbatim as the box URL
}

func (s *SQLite) src() string { return filepath.Join(s.Root, s.Rel) }

func (s *SQLite) Prepare(ctx context.Context) error { return s.Check(ctx) }

func (s *SQLite) Check(context.Context) error {
	if _, err := os.Stat(s.src()); err != nil {
		return fmt.Errorf("sqlite source %s: %w", s.Rel, err)
	}
	return nil
}

func (s *SQLite) Clone(ctx context.Context, _ string, boxDir string) (Clone, error) {
	if err := s.Check(ctx); err != nil {
		return Clone{}, err
	}
	dst := filepath.Join(boxDir, s.Rel)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return Clone{}, err
	}
	// A tracked database file arrives with the worktree; VACUUM INTO needs a fresh target.
	if err := s.Drop(ctx, s.Rel, boxDir); err != nil {
		return Clone{}, err
	}
	conn, err := sql.Open("sqlite", s.src())
	if err != nil {
		return Clone{}, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `VACUUM INTO ?`, dst); err != nil {
		return Clone{}, fmt.Errorf("copy sqlite database %s: %w", s.Rel, err)
	}
	return Clone{Name: s.Rel, URL: s.Source}, nil
}

func (s *SQLite) Drop(_ context.Context, _ string, boxDir string) error {
	if boxDir == "" {
		return nil
	}
	base := filepath.Join(boxDir, s.Rel)
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		if err := os.Remove(base + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (s *SQLite) Refresh(context.Context) error { return nil }

// List returns nothing: SQLite clones live inside box dirs and go away with them.
func (s *SQLite) List(context.Context) ([]string, error) { return nil, nil }
