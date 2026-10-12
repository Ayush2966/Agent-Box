// Package ui serves the embedded web dashboard and signs the browser in.
package ui

import (
	"crypto/subtle"
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var embedded embed.FS

// Dist is the dashboard build embedded in this binary. In a plain checkout it
// holds only .gitkeep; `make ui` fills it before `go build`.
func Dist() fs.FS {
	sub, err := fs.Sub(embedded, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}

// Built reports whether fsys holds a dashboard build.
func Built(fsys fs.FS) bool {
	st, err := fs.Stat(fsys, "index.html")
	return err == nil && !st.IsDir()
}

type Options struct {
	Token string      // the API token; a valid session cookie carries it
	Codes *LoginCodes // one-shot codes minted by `agentbox ui`
	FS    fs.FS       // the dashboard build: Dist() in production, a fake in tests
}

// Handler serves everything under /ui/: sign-in, sign-out and the single-page app.
func Handler(o Options) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ui/login", func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		if code == "" || o.Codes == nil || !o.Codes.Redeem(code) {
			forbidden(w, "This sign-in link has expired or was already used.")
			return
		}
		SetSession(w, o.Token)
		http.Redirect(w, r, "/ui/", http.StatusSeeOther)
	})
	mux.HandleFunc("POST /ui/login", func(w http.ResponseWriter, r *http.Request) {
		got := strings.TrimSpace(r.PostFormValue("token"))
		if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(o.Token)) != 1 {
			forbidden(w, "That is not this daemon's token.")
			return
		}
		SetSession(w, o.Token)
		http.Redirect(w, r, "/ui/", http.StatusSeeOther)
	})
	mux.HandleFunc("POST /ui/logout", func(w http.ResponseWriter, r *http.Request) {
		ClearSession(w)
		http.Redirect(w, r, "/ui/", http.StatusSeeOther)
	})
	mux.HandleFunc("/ui/", func(w http.ResponseWriter, r *http.Request) { serveApp(w, r, o.FS) })
	return mux
}

// serveApp serves a built file when the path names one, otherwise index.html
// so the app's own (hash) router can take over.
func serveApp(w http.ResponseWriter, r *http.Request, fsys fs.FS) {
	noFraming(w)
	if !Built(fsys) {
		page(w, http.StatusServiceUnavailable, notBuilt)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/ui/")
	if name != "" && name != "index.html" {
		if st, err := fs.Stat(fsys, name); err == nil && !st.IsDir() {
			if strings.HasPrefix(name, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			http.ServeFileFS(w, r, fsys, name)
			return
		}
	}
	w.Header().Set("Cache-Control", "no-cache")
	// ServeFileFS would redirect a path ending in /index.html; serve the bytes ourselves.
	data, err := fs.ReadFile(fsys, "index.html")
	if err != nil {
		page(w, http.StatusServiceUnavailable, notBuilt)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}
