package db

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/Ayush2966/Agent-Box/internal/errs"
)

func TestNewMongoValidatesURL(t *testing.T) {
	m, err := NewMongo("mongodb://app:s%40cret@127.0.0.1:27017/myapp?authSource=admin")
	if err != nil || m.srcDB != "myapp" {
		t.Fatalf("%+v %v", m, err)
	}
	if _, err := NewMongo("mongodb+srv://u:p@cluster0.example.mongodb.net/prod?retryWrites=true"); err != nil {
		t.Fatalf("srv: %v", err)
	}
	for _, bad := range []string{"mongodb://127.0.0.1:27017/", "mongodb:///db", "mysql://x@y/z", "mongodb://x@y/bad.name"} {
		if _, err := NewMongo(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestMongoURLsAndNames(t *testing.T) {
	m, _ := NewMongo("mongodb://app:pw@db.internal:27017/myapp")
	if got := m.urlFor("myapp_agentbox_a"); got != "mongodb://app:pw@db.internal:27017/myapp_agentbox_a" {
		t.Fatalf("url %q", got)
	}
	// Dropping the path must not move where the user authenticates.
	if got := m.adminURI(); got != "mongodb://app:pw@db.internal:27017/?authSource=myapp" {
		t.Fatalf("admin uri %q", got)
	}
	m2, _ := NewMongo("mongodb://app:pw@h/myapp?authSource=admin")
	if got := m2.adminURI(); got != "mongodb://app:pw@h/?authSource=admin" {
		t.Fatalf("admin uri keeps authSource: %q", got)
	}
	m3, _ := NewMongo("mongodb://h/myapp")
	if got := m3.adminURI(); got != "mongodb://h/" {
		t.Fatalf("no user, no authSource: %q", got)
	}
	name, err := m.cloneName("feature-x")
	if err != nil || name != "myapp_agentbox_feature_x" {
		t.Fatalf("%q %v", name, err)
	}
	if _, err := m.cloneName(strings.Repeat("b", 60)); err == nil {
		t.Fatal("63-byte limit not enforced")
	}
	for _, n := range []string{"myapp", "other_agentbox_a", "myapp_agentbox_", ""} {
		if m.isClone(n) {
			t.Errorf("isClone(%q) must be false", n)
		}
	}
	if err := m.Drop(context.Background(), "myapp", ""); errs.From(err).Code != errs.Invalid {
		t.Fatalf("Drop of the source must be refused before any connection: %v", err)
	}
}

func TestMongoConfigFileKeepsSecretsOffArgv(t *testing.T) {
	m, _ := NewMongo("mongodb://app:hunter2@db.internal:27017/myapp")
	path, cleanup, err := m.configFile()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode().Perm())
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), `uri: "mongodb://app:hunter2@db.internal:27017/?authSource=myapp"`) {
		t.Fatalf("config file:\n%s", data)
	}
	for _, args := range [][]string{m.dumpArgs(path), m.restoreArgs(path, "myapp_agentbox_a")} {
		if strings.Contains(strings.Join(args, " "), "hunter2") || args[0] != "--config="+path {
			t.Fatalf("argv %v", args)
		}
	}
	if !contains(m.dumpArgs(path), "--db=myapp") || !contains(m.restoreArgs(path, "myapp_agentbox_a"), "--nsTo=myapp_agentbox_a.*") {
		t.Fatalf("dump %v restore %v", m.dumpArgs(path), m.restoreArgs(path, "x"))
	}
	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("cleanup must delete the config file")
	}
}

// --- live tests: need AGENTBOX_TEST_MONGO_URL (a mongodb:// URL whose user may create and drop databases) ---

func mongoAdminURL(t *testing.T) string {
	u := os.Getenv("AGENTBOX_TEST_MONGO_URL")
	if u == "" {
		t.Skip("set AGENTBOX_TEST_MONGO_URL (mongodb:// URL with a database in the path) to run MongoDB tests")
	}
	if _, err := NewMongo(u); err != nil {
		t.Fatalf("AGENTBOX_TEST_MONGO_URL: %v", err)
	}
	return u
}

// newMongoSource creates a throwaway source database with one document and
// returns its URL and a client on the server.
func newMongoSource(t *testing.T) (string, *mongo.Client) {
	t.Helper()
	admin, _ := NewMongo(mongoAdminURL(t))
	name := fmt.Sprintf("abtest_%d", time.Now().UnixNano()%1_000_000_000)
	c, err := mongo.Connect(options.Client().ApplyURI(admin.adminURI()).SetServerSelectionTimeout(5 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := c.Database(name).Collection("items").InsertOne(ctx, bson.D{{Key: "name", Value: "one"}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		names, _ := c.ListDatabaseNames(ctx, bson.D{})
		for _, d := range names {
			if strings.HasPrefix(d, name) {
				_ = c.Database(d).Drop(ctx)
			}
		}
		_ = c.Disconnect(ctx)
	})
	return admin.urlFor(name), c
}

func TestMongoCloneListDrop(t *testing.T) {
	src, c := newMongoSource(t)
	m, err := NewMongo(src)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := m.Check(ctx); err != nil {
		t.Fatal(err)
	}
	cl, err := m.Clone(ctx, "feature-x", "")
	if err != nil {
		t.Fatal(err)
	}
	if cl.Name != m.srcDB+"_agentbox_feature_x" || !strings.HasSuffix(cl.URL, "/"+cl.Name) {
		t.Fatalf("clone %+v", cl)
	}
	n, err := c.Database(cl.Name).Collection("items").CountDocuments(ctx, bson.D{})
	if err != nil || n != 1 {
		t.Fatalf("clone has %d items (%v); the source's document must be there", n, err)
	}
	if names, _ := m.List(ctx); len(names) != 1 || names[0] != cl.Name {
		t.Fatalf("List = %v", names)
	}
	if _, err := m.Clone(ctx, "feature-x", ""); errs.From(err).Code != errs.Exists {
		t.Fatalf("second clone must refuse: %v", err)
	}
	if err := m.Drop(ctx, cl.Name, ""); err != nil {
		t.Fatal(err)
	}
	if names, _ := m.List(ctx); len(names) != 0 {
		t.Fatalf("after drop: %v", names)
	}
	if err := m.Drop(ctx, cl.Name, ""); err != nil {
		t.Fatalf("dropping a gone clone must succeed: %v", err)
	}
}
