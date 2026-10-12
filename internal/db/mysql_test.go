package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/Ayush2966/Agent-Box/internal/errs"
)

func TestNewMySQLValidatesURL(t *testing.T) {
	m, err := NewMySQL("mysql://app:s%40cret@127.0.0.1:3306/myapp?tls=false")
	if err != nil || m.srcDB != "myapp" {
		t.Fatalf("%+v %v", m, err)
	}
	for _, bad := range []string{"mysql://app@127.0.0.1:3306/", "mysql:///db", "postgres://x@y/z", "mysql://x@y/bad-name"} {
		if _, err := NewMySQL(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestMySQLDSNAndURL(t *testing.T) {
	m, _ := NewMySQL("mysql://app:s%40cret@db.internal/myapp?tls=skip-verify")
	if got := m.dsn("myapp_agentbox_a"); got != "app:s@cret@tcp(db.internal:3306)/myapp_agentbox_a?tls=skip-verify" {
		t.Fatalf("dsn %q", got)
	}
	if got := m.dsn(""); got != "app:s@cret@tcp(db.internal:3306)/?tls=skip-verify" {
		t.Fatalf("admin dsn %q", got)
	}
	if got := m.urlFor("myapp_agentbox_a"); got != "mysql://app:s%40cret@db.internal/myapp_agentbox_a?tls=skip-verify" {
		t.Fatalf("url %q", got)
	}
	m2, _ := NewMySQL("mariadb://u@h:3307/d")
	if m2.dsn("d") != "u@tcp(h:3307)/d" || !strings.HasPrefix(m2.urlFor("x"), "mariadb://") {
		t.Fatalf("%q %q", m2.dsn("d"), m2.urlFor("x"))
	}
}

func TestMySQLNameTooLong(t *testing.T) {
	m, _ := NewMySQL("mysql://u@h/" + strings.Repeat("a", 50))
	_, err := m.Clone(context.Background(), strings.Repeat("b", 20), "")
	if err == nil || !strings.Contains(err.Error(), "64") {
		t.Fatalf("want a length error before any connection, got %v", err)
	}
}

func TestMySQLDropRefusesForeignDatabases(t *testing.T) {
	m, _ := NewMySQL("mysql://u@h/myapp")
	for _, name := range []string{"myapp", "other_agentbox_a", "myapp_agentbox", ""} {
		if err := m.Drop(context.Background(), name, ""); err == nil {
			t.Errorf("Drop(%q) must refuse", name)
		}
	}
}

func TestMySQLDefaultsFileKeepsSecretsOffArgv(t *testing.T) {
	m, _ := NewMySQL("mysql://app:hunter2@db.internal:3307/myapp")
	path, cleanup, err := m.defaultsFile()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode().Perm())
	}
	data, _ := os.ReadFile(path)
	for _, want := range []string{"[client]", "protocol=tcp", `host="db.internal"`, "port=3307", `user="app"`, `password="hunter2"`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("defaults file lacks %q:\n%s", want, data)
		}
	}
	args := m.dumpArgs(path, false)
	if strings.Contains(strings.Join(args, " "), "hunter2") || args[0] != "--defaults-file="+path {
		t.Fatalf("argv %v", args)
	}
	for _, want := range []string{"--single-transaction", "--no-tablespaces", "myapp"} {
		if !contains(args, want) {
			t.Fatalf("dump argv lacks %s: %v", want, args)
		}
	}
	rargs := m.restoreArgs(path, "myapp_agentbox_a")
	if len(rargs) != 2 || rargs[0] != "--defaults-file="+path || rargs[1] != "myapp_agentbox_a" || strings.Contains(strings.Join(rargs, " "), "hunter2") {
		t.Fatalf("restore argv %v", rargs)
	}
	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("cleanup must delete the defaults file")
	}
}

func TestMySQLCreateStatementAndPrefix(t *testing.T) {
	m, _ := NewMySQL("mysql://u@h/myapp")
	if got := m.createStatement("myapp_agentbox_a"); got != "CREATE DATABASE `myapp_agentbox_a`" {
		t.Fatalf("%q", got)
	}
	if got := m.clonePrefix(); got != "myapp_agentbox_" {
		t.Fatalf("prefix %q", got)
	}
}

func TestMySQLDumpArgsGTID(t *testing.T) {
	m, _ := NewMySQL("mysql://u@h/myapp")
	if contains(m.dumpArgs("/p", false), "--set-gtid-purged=OFF") {
		t.Fatal("flag present without gtid")
	}
	args := m.dumpArgs("/p", true)
	if !contains(args, "--set-gtid-purged=OFF") || args[0] != "--defaults-file=/p" || args[len(args)-1] != "myapp" {
		t.Fatalf("%v", args)
	}
}

func TestMySQLIsCloneFoldsCase(t *testing.T) {
	m, _ := NewMySQL("mysql://u@h/MyApp")
	if !m.isClone("myapp_agentbox_a") || !m.isClone("MyApp_agentbox_a") {
		t.Fatal("a lower_case_table_names=1 server stores the clone folded; Drop must still accept it")
	}
	for _, n := range []string{"MyApp_agentbox_", "other_agentbox_a", "MyApp_agentbox_a;x", ""} {
		if m.isClone(n) {
			t.Errorf("isClone(%q) must be false", n)
		}
	}
}

func TestMySQLCreateRaceMapsToExists(t *testing.T) {
	if !isDBExists(&mysql.MySQLError{Number: 1007, Message: "Can't create database 'x'; database exists"}) {
		t.Fatal("error 1007 must read as exists")
	}
	if isDBExists(&mysql.MySQLError{Number: 1044}) || isDBExists(errors.New("boom")) || isDBExists(nil) {
		t.Fatal("only 1007 is exists")
	}
}

func TestMySQLMatchesPrefix(t *testing.T) {
	if !matchesPrefix("MyApp_agentbox_a", "myapp_agentbox_") || !matchesPrefix("myapp_agentbox_a", "myapp_agentbox_") {
		t.Fatal("case-insensitive prefix must match")
	}
	if matchesPrefix("myapp_agentbo", "myapp_agentbox_") || matchesPrefix("other_agentbox_a", "myapp_agentbox_") {
		t.Fatal("must not match")
	}
}

func TestMySQLDefaultsFileQuotesSpecialCharacters(t *testing.T) {
	m, _ := NewMySQL("mysql://us%20er:p%23s%20s%22%5C@h/db")
	path, cleanup, err := m.defaultsFile()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	data, _ := os.ReadFile(path)
	for _, want := range []string{`user="us er"`, `password="p#s s\"\\"`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("defaults file lacks %q:\n%s", want, data)
		}
	}
	bad, _ := NewMySQL("mysql://u:a%0Ab@h/db")
	if p, c, err := bad.defaultsFile(); err == nil {
		c()
		t.Fatalf("control character accepted, wrote %s", p)
	}
}

func TestMySQLCloneNameMapsHyphens(t *testing.T) {
	m, _ := NewMySQL("mysql://u@127.0.0.1:1/myapp")
	got, err := m.cloneName("fix-login")
	if err != nil || got != "myapp_agentbox_fix_login" {
		t.Fatalf("%q %v", got, err)
	}
	err = m.Drop(context.Background(), got, "")
	if err == nil || strings.Contains(err.Error(), "refusing") {
		t.Fatalf("Drop must get as far as connecting, got %v", err)
	}
}

func TestMySQLIPv6Host(t *testing.T) {
	m, _ := NewMySQL("mysql://u@[::1]:3307/db")
	if got := m.dsn("db"); got != "u@tcp([::1]:3307)/db" {
		t.Fatalf("dsn %q", got)
	}
	path, cleanup, err := m.defaultsFile()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	data, _ := os.ReadFile(path)
	for _, want := range []string{`host="::1"`, "port=3307"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("defaults file lacks %q:\n%s", want, data)
		}
	}
}

// --- live tests: need AGENTBOX_TEST_MYSQL_URL (a mysql:// URL of a role that can CREATE DATABASE) ---

func mysqlAdminURL(t *testing.T) string {
	u := os.Getenv("AGENTBOX_TEST_MYSQL_URL")
	if u == "" {
		t.Skip("set AGENTBOX_TEST_MYSQL_URL (mysql:// URL of a role with CREATE privileges) to run MySQL tests")
	}
	if _, err := NewMySQL(u); err != nil {
		t.Fatalf("AGENTBOX_TEST_MYSQL_URL: %v", err)
	}
	return u
}

// newMySQLSource creates a throwaway source database with one row in items
// and returns its URL.
func newMySQLSource(t *testing.T) (string, *sql.DB) {
	t.Helper()
	admin := mysqlAdminURL(t)
	am, _ := NewMySQL(admin)
	name := fmt.Sprintf("abtest_%d", time.Now().UnixNano()%1_000_000_000)
	conn, err := sql.Open("mysql", am.dsn(""))
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"CREATE DATABASE " + mysqlIdent(name), "CREATE TABLE " + mysqlIdent(name) + ".items(name VARCHAR(20))", "INSERT INTO " + mysqlIdent(name) + ".items VALUES('one')"} {
		if _, err := conn.ExecContext(context.Background(), q); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		rows, _ := conn.Query("SHOW DATABASES")
		var dbs []string
		for rows != nil && rows.Next() {
			var d string
			rows.Scan(&d)
			if strings.HasPrefix(d, name) {
				dbs = append(dbs, d)
			}
		}
		for _, d := range dbs {
			conn.Exec("DROP DATABASE IF EXISTS " + mysqlIdent(d))
		}
		conn.Close()
	})
	return am.urlFor(name), conn
}

func mysqlCount(t *testing.T, conn *sql.DB, dbName string) int {
	t.Helper()
	var n int
	if err := conn.QueryRow("SELECT COUNT(*) FROM " + mysqlIdent(dbName) + ".items").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestMySQLCloneHasSourceRows(t *testing.T) {
	src, conn := newMySQLSource(t)
	m, err := NewMySQL(src)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := m.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	c, err := m.Clone(ctx, "a", "")
	if err != nil {
		t.Fatal(err)
	}
	if c.Name != m.srcDB+"_agentbox_a" || !strings.HasSuffix(c.URL, "/"+c.Name) {
		t.Fatalf("%+v", c)
	}
	if mysqlCount(t, conn, c.Name) != 1 {
		t.Fatal("clone lacks the source row")
	}
	conn.Exec("INSERT INTO " + mysqlIdent(c.Name) + ".items VALUES('two')")
	if mysqlCount(t, conn, m.srcDB) != 1 {
		t.Fatal("writing to the clone touched the source")
	}
	names, err := m.List(ctx)
	if err != nil || len(names) != 1 || names[0] != c.Name {
		t.Fatalf("list %v %v", names, err)
	}
	if err := m.Drop(ctx, c.Name, ""); err != nil {
		t.Fatal(err)
	}
	if names, _ := m.List(ctx); len(names) != 0 {
		t.Fatalf("after drop: %v", names)
	}
	if err := m.Drop(ctx, c.Name, ""); err != nil {
		t.Fatalf("dropping twice must succeed: %v", err)
	}
}

func TestMySQLCloneRefusesExistingLive(t *testing.T) {
	src, conn := newMySQLSource(t)
	m, _ := NewMySQL(src)
	ctx := context.Background()
	name := m.srcDB + "_agentbox_a"
	for _, q := range []string{"CREATE DATABASE " + mysqlIdent(name), "CREATE TABLE " + mysqlIdent(name) + ".items(name VARCHAR(20))", "INSERT INTO " + mysqlIdent(name) + ".items VALUES('live')"} {
		if _, err := conn.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	_, err := m.Clone(ctx, "a", "")
	se := errs.From(err)
	if err == nil || se.Code != errs.Exists || !strings.Contains(se.Hint, "doctor --fix") {
		t.Fatalf("want Exists with a doctor hint, got %v", err)
	}
	var row string
	if err := conn.QueryRow("SELECT name FROM " + mysqlIdent(name) + ".items").Scan(&row); err != nil || row != "live" {
		t.Fatalf("the existing database was touched: %q %v (a reclone would hold 'one')", row, err)
	}
	if err := m.Drop(ctx, name, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Clone(ctx, "a", ""); err != nil {
		t.Fatalf("clone after drop: %v", err)
	}
}

func TestMySQLGTIDProbeMatchesServer(t *testing.T) {
	src, conn := newMySQLSource(t)
	m, _ := NewMySQL(src)
	want := false
	var mode string
	if err := conn.QueryRow("SELECT @@global.gtid_mode").Scan(&mode); err == nil {
		want = strings.ToUpper(mode) != "OFF"
	}
	if got := m.gtidPurgedFlag(context.Background(), conn); got != want {
		t.Fatalf("gtidPurgedFlag = %v, want %v (gtid_mode %q)", got, want, mode)
	}
}

func TestMySQLCheckReportsMissingTools(t *testing.T) {
	src, _ := newMySQLSource(t)
	m, _ := NewMySQL(src)
	t.Setenv("PATH", t.TempDir())
	err := m.Check(context.Background())
	if err == nil || !strings.Contains(err.Error(), "mysql not found") {
		t.Fatalf("want a missing-tools error, got %v", err)
	}
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
