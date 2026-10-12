package db

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ayush2966/Agent-Box/internal/config"
)

func countItems(t *testing.T, path string) int {
	t.Helper()
	c, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var n int
	if err := c.QueryRow(`SELECT count(*) FROM items`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSQLiteCloneIsIndependentAndIncludesWAL(t *testing.T) {
	ctx := context.Background()
	root, boxDir := t.TempDir(), t.TempDir()
	src := filepath.Join(root, "data", "dev.db")
	os.MkdirAll(filepath.Dir(src), 0o755)
	conn, err := sql.Open("sqlite", src)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close() // kept open so the row may still live only in the WAL
	for _, q := range []string{`PRAGMA journal_mode=WAL`, `CREATE TABLE items(name TEXT)`, `INSERT INTO items VALUES('one')`} {
		if _, err := conn.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	// A stale file that arrived with the worktree must be replaced, not appended to.
	os.MkdirAll(filepath.Join(boxDir, "data"), 0o755)
	os.WriteFile(filepath.Join(boxDir, "data", "dev.db"), []byte("stale"), 0o644)

	d, err := New(&config.DB{Driver: "sqlite", URLEnv: "DATABASE_URL", Source: "file:./data/dev.db"}, root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	c, err := d.Clone(ctx, "a", boxDir)
	if err != nil {
		t.Fatal(err)
	}
	if c.Name != "data/dev.db" || c.URL != "file:./data/dev.db" {
		t.Fatalf("clone = %+v", c)
	}
	dst := filepath.Join(boxDir, "data", "dev.db")
	if n := countItems(t, dst); n != 1 {
		t.Fatalf("clone has %d rows", n)
	}
	w, _ := sql.Open("sqlite", dst)
	w.Exec(`INSERT INTO items VALUES('box only')`)
	w.Close()
	if n := countItems(t, src); n != 1 {
		t.Fatalf("write to clone leaked into source (%d rows)", n)
	}
	if err := d.Drop(ctx, c.Name, boxDir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatal("clone file still exists after Drop")
	}
}

func TestSQLiteMissingSource(t *testing.T) {
	d, _ := New(&config.DB{Driver: "sqlite", URLEnv: "X", Source: "nope.db"}, t.TempDir(), Options{})
	_, err := d.Clone(context.Background(), "a", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "sqlite source nope.db") {
		t.Fatalf("err = %v", err)
	}
}
