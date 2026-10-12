package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeDaemon serves a tiny slice of the v1 API on a unix socket.
func fakeDaemon(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "abcl") // short: unix socket path limit
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "d.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/repos", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[{"id":"app-1a2b3c","path":"/tmp/app"}]`)
	})
	mux.HandleFunc("GET /v1/repos/{repo}/boxes/{name}/logs", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("follow") != "true" || r.URL.Query().Get("tail") != "10" {
			http.Error(w, "expected follow=true&tail=10, got "+r.URL.RawQuery, 400)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		f := w.(http.Flusher)
		for _, line := range []string{"listening on 20000", "GET / 200"} {
			b, _ := json.Marshal(line)
			fmt.Fprintf(w, "event: line\ndata: %s\n\n", b)
			f.Flush()
		}
		<-r.Context().Done() // hold the stream open until the client goes away
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close(); os.RemoveAll(dir) })
	return sock
}

func TestRepos(t *testing.T) {
	c := New(fakeDaemon(t))
	repos, err := c.Repos(context.Background())
	if err != nil || len(repos) != 1 || repos[0].ID != "app-1a2b3c" || repos[0].Path != "/tmp/app" {
		t.Fatalf("%+v %v", repos, err)
	}
}

func TestLogLinesStreamsThenClosesOnCancel(t *testing.T) {
	c := New(fakeDaemon(t))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := c.LogLines(ctx, "app-1a2b3c", "a", 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"listening on 20000", "GET / 200"} {
		select {
		case got := <-ch:
			if got != want {
				t.Fatalf("got %q, want %q", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for %q", want)
		}
	}
	cancel()
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("unexpected extra line")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("channel did not close after cancel")
	}
}
