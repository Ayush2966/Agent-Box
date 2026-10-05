// Package state persists repos, boxes and port reservations in ~/.agentbox/state.db.
package state

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type BoxState string

const (
	Creating BoxState = "creating"
	Ready    BoxState = "ready"
	Running  BoxState = "running"
	Stopped  BoxState = "stopped"
	Removing BoxState = "removing"
	Failed   BoxState = "failed"
)

var (
	ErrNotFound = errors.New("not found")
	ErrExists   = errors.New("already exists")
)

type Repo struct {
	ID   string
	Path string
}

type Box struct {
	RepoID    string
	Name      string
	Dir       string
	Branch    string
	State     BoxState
	DBName    string
	DBURL     string
	PID       int
	LastStep  string
	LastError string
	CreatedAt time.Time
	Ports     map[string]int
	// Managed lists the repo-relative paths agentbox itself generated in this
	// box. It is recorded at creation so that later edits to agentbox.yaml
	// cannot reclassify agentbox's own files as the agent's work.
	Managed []string
}

func (b Box) ID() string { return b.RepoID + "/" + b.Name }

type PortRow struct {
	Port    int
	RepoID  string
	Box     string
	EnvName string
}

const schema = `
CREATE TABLE IF NOT EXISTS repos (
	id   TEXT PRIMARY KEY,
	path TEXT NOT NULL UNIQUE
);
CREATE TABLE IF NOT EXISTS boxes (
	repo_id    TEXT NOT NULL,
	name       TEXT NOT NULL,
	dir        TEXT NOT NULL,
	branch     TEXT NOT NULL,
	state      TEXT NOT NULL,
	db_name    TEXT NOT NULL DEFAULT '',
	db_url     TEXT NOT NULL DEFAULT '',
	pid        INTEGER NOT NULL DEFAULT 0,
	last_step  TEXT NOT NULL DEFAULT '',
	last_error TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL,
	managed    TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (repo_id, name)
);
CREATE TABLE IF NOT EXISTS ports (
	port     INTEGER PRIMARY KEY,
	repo_id  TEXT NOT NULL,
	box      TEXT NOT NULL,
	env_name TEXT NOT NULL
);`

// Store is safe for concurrent use. It uses a single connection, so every
// query must close its rows before issuing the next one.
type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("init state db %s: %w", path, err)
	}
	// Idempotent migration for state DBs created before `managed` existed.
	if _, err := db.Exec(`ALTER TABLE boxes ADD COLUMN managed TEXT NOT NULL DEFAULT ''`); err != nil &&
		!strings.Contains(err.Error(), "duplicate column name") {
		db.Close()
		return nil, fmt.Errorf("migrate state db %s: %w", path, err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// RepoSlug is the stable, filesystem-safe ID of the repo at path.
func RepoSlug(path string) string {
	base := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(filepath.Base(path)), "-"), "-")
	if base == "" {
		base = "repo"
	}
	sum := sha256.Sum256([]byte(path))
	return base + "-" + hex.EncodeToString(sum[:])[:6]
}

func (s *Store) UpsertRepo(path string) (Repo, error) {
	r := Repo{ID: RepoSlug(path), Path: path}
	_, err := s.db.Exec(`INSERT INTO repos(id, path) VALUES(?, ?) ON CONFLICT(id) DO NOTHING`, r.ID, r.Path)
	return r, err
}

func (s *Store) RepoByID(id string) (Repo, error) {
	var r Repo
	err := s.db.QueryRow(`SELECT id, path FROM repos WHERE id = ?`, id).Scan(&r.ID, &r.Path)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

func (s *Store) Repos() ([]Repo, error) {
	rows, err := s.db.Query(`SELECT id, path FROM repos ORDER BY path`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Repo
	for rows.Next() {
		var r Repo
		if err := rows.Scan(&r.ID, &r.Path); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

const boxCols = `repo_id, name, dir, branch, state, db_name, db_url, pid, last_step, last_error, created_at, managed`

func (s *Store) InsertBox(b Box) error {
	_, err := s.db.Exec(`INSERT INTO boxes(`+boxCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		b.RepoID, b.Name, b.Dir, b.Branch, string(b.State), b.DBName, b.DBURL, b.PID, b.LastStep, b.LastError,
		b.CreatedAt.Unix(), encodeManaged(b.Managed))
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed") {
		return ErrExists
	}
	return err
}

func (s *Store) UpdateBox(b Box) error {
	res, err := s.db.Exec(`UPDATE boxes SET dir=?, branch=?, state=?, db_name=?, db_url=?, pid=?, last_step=?, last_error=?, managed=?
		WHERE repo_id=? AND name=?`,
		b.Dir, b.Branch, string(b.State), b.DBName, b.DBURL, b.PID, b.LastStep, b.LastError,
		encodeManaged(b.Managed), b.RepoID, b.Name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

type scanner interface{ Scan(dest ...any) error }

func scanBox(sc scanner) (Box, error) {
	var b Box
	var st, managed string
	var created int64
	err := sc.Scan(&b.RepoID, &b.Name, &b.Dir, &b.Branch, &st, &b.DBName, &b.DBURL, &b.PID, &b.LastStep, &b.LastError, &created, &managed)
	b.State = BoxState(st)
	b.CreatedAt = time.Unix(created, 0)
	b.Managed = decodeManaged(managed)
	return b, err
}

func encodeManaged(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	out, err := json.Marshal(paths)
	if err != nil {
		return ""
	}
	return string(out)
}

func decodeManaged(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil
	}
	return out
}

func (s *Store) GetBox(repoID, name string) (Box, error) {
	b, err := scanBox(s.db.QueryRow(`SELECT `+boxCols+` FROM boxes WHERE repo_id=? AND name=?`, repoID, name))
	if errors.Is(err, sql.ErrNoRows) {
		return Box{}, ErrNotFound
	}
	if err != nil {
		return Box{}, err
	}
	b.Ports, err = s.boxPorts(repoID, name)
	return b, err
}

func (s *Store) ListBoxes(repoID string) ([]Box, error) {
	q := `SELECT ` + boxCols + ` FROM boxes`
	var args []any
	if repoID != "" {
		q += ` WHERE repo_id=?`
		args = append(args, repoID)
	}
	rows, err := s.db.Query(q+` ORDER BY repo_id, name`, args...)
	if err != nil {
		return nil, err
	}
	var out []Box
	for rows.Next() {
		b, err := scanBox(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, b)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].Ports, err = s.boxPorts(out[i].RepoID, out[i].Name); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) DeleteBox(repoID, name string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM boxes WHERE repo_id=? AND name=?`, repoID, name); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM ports WHERE repo_id=? AND box=?`, repoID, name); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) boxPorts(repoID, name string) (map[string]int, error) {
	rows, err := s.db.Query(`SELECT env_name, port FROM ports WHERE repo_id=? AND box=?`, repoID, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var n string
		var p int
		if err := rows.Scan(&n, &p); err != nil {
			return nil, err
		}
		out[n] = p
	}
	return out, rows.Err()
}

// AllocatePorts reserves one free port per name, atomically. isFree is
// consulted for ports not already reserved, so ports in use by other
// programs are skipped without being recorded.
func (s *Store) AllocatePorts(repoID, box string, names []string, lo, hi int, isFree func(int) bool) (map[string]int, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	used := map[int]bool{}
	rows, err := tx.Query(`SELECT port FROM ports`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var p int
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			return nil, err
		}
		used[p] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}

	out := map[string]int{}
	p := lo
	for _, name := range names {
		for p <= hi && (used[p] || !isFree(p)) {
			p++
		}
		if p > hi {
			return nil, fmt.Errorf("no free ports left in range %d-%d", lo, hi)
		}
		if _, err := tx.Exec(`INSERT INTO ports(port, repo_id, box, env_name) VALUES(?,?,?,?)`, p, repoID, box, name); err != nil {
			return nil, err
		}
		out[name] = p
		used[p] = true
		p++
	}
	return out, tx.Commit()
}

func (s *Store) FreePorts(repoID, box string) error {
	_, err := s.db.Exec(`DELETE FROM ports WHERE repo_id=? AND box=?`, repoID, box)
	return err
}

func (s *Store) AllPorts() ([]PortRow, error) {
	rows, err := s.db.Query(`SELECT port, repo_id, box, env_name FROM ports ORDER BY port`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PortRow
	for rows.Next() {
		var r PortRow
		if err := rows.Scan(&r.Port, &r.RepoID, &r.Box, &r.EnvName); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
