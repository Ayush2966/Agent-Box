package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"slices"
	"strconv"
	"time"

	"github.com/Ayush2966/Agent-Box/internal/box"
	"github.com/Ayush2966/Agent-Box/internal/errs"
	"github.com/Ayush2966/Agent-Box/internal/events"
	"github.com/Ayush2966/Agent-Box/internal/ui"
)

type Server struct {
	M       *box.Manager
	Bus     *events.Bus
	Token   string         // required on the TCP listener (Bearer header or session cookie)
	Origins []string       // browser origins allowed on the TCP listener
	Codes   *ui.LoginCodes // nil disables login codes
	UI      http.Handler   // the dashboard; nil disables /ui/
	APIAddr string         // effective TCP address, "" when the HTTP API is disabled
	Version string
	UIBuilt bool
}

// Handler serves the v1 API. requireToken is true for the TCP listener, which
// also gets the dashboard, and false for the Unix socket, which file
// permissions already protect and which serves the API alone.
func (s *Server) Handler(requireToken bool) http.Handler {
	mux := http.NewServeMux()
	const b = "/v1/repos/{repo}/boxes/{name}"
	mux.HandleFunc("GET /v1/health", s.health)
	mux.HandleFunc("GET /v1/repos", s.listRepos)
	mux.HandleFunc("POST /v1/repos", s.resolveRepo)
	mux.HandleFunc("GET /v1/boxes", s.listBoxes)
	mux.HandleFunc("POST /v1/boxes", s.createBox)
	mux.HandleFunc("GET "+b, s.getBox)
	mux.HandleFunc("POST "+b+"/start", s.startBox)
	mux.HandleFunc("POST "+b+"/stop", s.stopBox)
	mux.HandleFunc("POST "+b+"/merge", s.mergeBox)
	mux.HandleFunc("DELETE "+b, s.removeBox)
	mux.HandleFunc("GET "+b+"/logs", s.logs)
	mux.HandleFunc("GET "+b+"/diff", s.diff)
	mux.HandleFunc("POST /v1/repos/{repo}/db/refresh", s.refreshDB)
	mux.HandleFunc("GET /v1/repos/{repo}/doctor", s.doctor(false))
	mux.HandleFunc("POST /v1/repos/{repo}/doctor", s.doctor(true))
	mux.HandleFunc("GET /v1/events", s.events)
	mux.HandleFunc("POST /v1/ui/login-codes", s.loginCode)
	if !requireToken {
		return mux
	}
	root := http.NewServeMux()
	root.Handle("/v1/", s.auth(mux))
	if s.UI != nil {
		root.Handle("/ui/", s.UI)
		root.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/ui/", http.StatusFound)
		})
	}
	return s.checkOrigin(root)
}

// checkOrigin enforces spec §6.3 rule 3 on every TCP request: a browser that
// names a foreign origin is refused before anything else happens, dashboard
// pages included.
func (s *Server) checkOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if o := r.Header.Get("Origin"); o != "" && !slices.Contains(s.Origins, o) {
			writeJSON(w, http.StatusForbidden, ErrorBody{errs.New(errs.Invalid, "origin %q is not allowed", o)})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// auth admits a Bearer token, or a browser session cookie. A cookie may only
// change state from the dashboard's own origin: browsers send Origin on every
// cross-origin request and on same-origin fetch POSTs, and Sec-Fetch-Site
// names a cross-site caller even when Origin looks right.
func (s *Server) auth(next http.Handler) http.Handler {
	want := []byte("Bearer " + s.Token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) == 1 {
			next.ServeHTTP(w, r)
			return
		}
		if !ui.ValidSession(r, s.Token) {
			writeJSON(w, http.StatusUnauthorized, ErrorBody{errs.New(errs.Invalid, "missing or wrong API token").
				WithHint("send `Authorization: Bearer <token>` using the token in ~/.agentbox/token, or sign in with `agentbox ui`")})
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if origin == "" || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
				writeJSON(w, http.StatusForbidden, ErrorBody{errs.New(errs.Invalid, "a browser session must send an allowed Origin header to change state")})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, HealthView{OK: true, PID: os.Getpid(), Version: s.Version, APIAddr: s.APIAddr, UI: s.UIBuilt})
}

func (s *Server) loginCode(w http.ResponseWriter, r *http.Request) {
	if s.Codes == nil || s.APIAddr == "" {
		writeError(w, errs.New(errs.Precondition, "the daemon's HTTP API is disabled, so the dashboard cannot be opened").
			WithHint("the port in api_addr (~/.agentbox/config.yaml, default 127.0.0.1:7777) is probably in use; see ~/.agentbox/agentbox.log"))
		return
	}
	code, exp := s.Codes.Mint()
	writeJSON(w, http.StatusCreated, LoginCode{Code: code, URL: "http://" + s.APIAddr + "/ui/login?code=" + code, ExpiresAt: exp})
}

// view adds what only the server knows to a box: its proxy URL.
func (s *Server) view(i box.Info) BoxView {
	v := viewOf(i)
	if len(i.Ports) > 0 && s.APIAddr != "" {
		if _, port, err := net.SplitHostPort(s.APIAddr); err == nil {
			v.URL = "http://" + i.Name + ".localhost:" + port + "/"
		}
	}
	return v
}

func (s *Server) listRepos(w http.ResponseWriter, r *http.Request) {
	repos, err := s.M.Repos()
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]RepoView, 0, len(repos))
	for _, rp := range repos {
		out = append(out, RepoView{ID: rp.ID, Path: rp.Path})
	}
	writeJSON(w, 200, out)
}

func (s *Server) resolveRepo(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
	}
	if !decode(w, r, &body) {
		return
	}
	repo, err := s.M.ResolveRepo(body.Path)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, RepoView{ID: repo.ID, Path: repo.Path})
}

func (s *Server) listBoxes(w http.ResponseWriter, r *http.Request) {
	infos, err := s.M.List(r.URL.Query().Get("repo"))
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]BoxView, 0, len(infos))
	for _, i := range infos {
		out = append(out, s.view(i))
	}
	writeJSON(w, 200, out)
}

func (s *Server) createBox(w http.ResponseWriter, r *http.Request) {
	var opts box.NewOptions
	if !decode(w, r, &opts) {
		return
	}
	// Claim synchronously, so a name that is taken (or being taken right now
	// by a concurrent request) is refused here instead of being accepted and
	// then losing a race the client would never hear about.
	b, cfg, err := s.M.Claim(opts)
	if err != nil {
		writeError(w, err)
		return
	}
	go func() {
		// Detached from the request: creation must finish even if the client goes away.
		if _, err := s.M.Build(context.Background(), b, cfg, opts); err != nil {
			slog.Warn("box creation failed", "repo", b.RepoID, "box", b.Name, "err", err)
		}
	}()
	writeJSON(w, http.StatusAccepted, Created{ID: b.ID(), Repo: b.RepoID, Name: b.Name})
}

func (s *Server) getBox(w http.ResponseWriter, r *http.Request) {
	info, err := s.M.Info(r.PathValue("repo"), r.PathValue("name"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, s.view(info))
}

func (s *Server) startBox(w http.ResponseWriter, r *http.Request) {
	noContent(w, s.M.Start(r.Context(), r.PathValue("repo"), r.PathValue("name")))
}

func (s *Server) stopBox(w http.ResponseWriter, r *http.Request) {
	noContent(w, s.M.Stop(r.Context(), r.PathValue("repo"), r.PathValue("name")))
}

func (s *Server) mergeBox(w http.ResponseWriter, r *http.Request) {
	var opts box.MergeOptions
	if r.ContentLength != 0 && !decode(w, r, &opts) {
		return
	}
	into, err := s.M.Merge(r.Context(), r.PathValue("repo"), r.PathValue("name"), opts)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, MergeResult{Into: into})
}

func (s *Server) removeBox(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	noContent(w, s.M.Remove(r.Context(), r.PathValue("repo"), r.PathValue("name"),
		box.RemoveOptions{Force: q.Get("force") == "true", KeepBranch: q.Get("keep_branch") == "true"}))
}

func (s *Server) logs(w http.ResponseWriter, r *http.Request) {
	repo, name, q := r.PathValue("repo"), r.PathValue("name"), r.URL.Query()
	tail := 200
	if v := q.Get("tail"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeError(w, errs.New(errs.Invalid, "tail must be a non-negative number"))
			return
		}
		tail = n
	}
	if q.Get("follow") != "true" {
		lines, err := s.M.LogTail(repo, name, tail)
		if err != nil {
			writeError(w, err)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		for _, l := range lines {
			fmt.Fprintln(w, l)
		}
		return
	}
	ch, cancel, err := s.M.LogSubscribe(repo, name) // subscribe first so no line falls between tail and follow
	if err != nil {
		writeError(w, err)
		return
	}
	defer cancel()
	lines, err := s.M.LogTail(repo, name, tail)
	if err != nil {
		writeError(w, err)
		return
	}
	sse, ok := newSSE(w)
	if !ok {
		return
	}
	for _, l := range lines {
		if sse.send("line", l) != nil {
			return
		}
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case l, ok := <-ch:
			if !ok || sse.send("line", l) != nil {
				return
			}
		}
	}
}

func (s *Server) diff(w http.ResponseWriter, r *http.Request) {
	d, err := s.M.Diff(r.PathValue("repo"), r.PathValue("name"), r.URL.Query().Get("against"))
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprint(w, d)
}

func (s *Server) refreshDB(w http.ResponseWriter, r *http.Request) {
	noContent(w, s.M.RefreshDB(r.Context(), r.PathValue("repo"), r.URL.Query().Get("terminate") == "true"))
}

func (s *Server) doctor(fix bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		fs, err := s.M.Doctor(r.Context(), r.PathValue("repo"), fix)
		if err != nil {
			writeError(w, err)
			return
		}
		if fs == nil {
			fs = []box.Finding{}
		}
		writeJSON(w, 200, fs)
	}
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	ch, cancel := s.Bus.Subscribe() // before headers go out, so clients can't miss events
	defer cancel()
	sse, ok := newSSE(w)
	if !ok {
		return
	}
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			if sse.comment("ping") != nil {
				return
			}
		case ev, ok := <-ch:
			if !ok || sse.send(ev.Type, ev) != nil {
				return
			}
		}
	}
}

// --- helpers ---

type sseWriter struct {
	w http.ResponseWriter
	f http.Flusher
}

func newSSE(w http.ResponseWriter) (*sseWriter, bool) {
	f, ok := w.(http.Flusher)
	if !ok {
		writeError(w, errs.New(errs.Internal, "streaming not supported"))
		return nil, false
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	w.WriteHeader(200)
	f.Flush()
	return &sseWriter{w: w, f: f}, true
}

func (s *sseWriter) send(event string, data any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", event, b); err != nil {
		return err
	}
	s.f.Flush()
	return nil
}

func (s *sseWriter) comment(text string) error {
	if _, err := fmt.Fprintf(s.w, ": %s\n\n", text); err != nil {
		return err
	}
	s.f.Flush()
	return nil
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeError(w, errs.New(errs.Invalid, "bad request body: %v", err))
		return false
	}
	return true
}

func statusFor(c errs.Code) int {
	switch c {
	case errs.NotFound:
		return http.StatusNotFound
	case errs.Exists, errs.UnmergedWork, errs.Conflict:
		return http.StatusConflict
	case errs.Invalid:
		return http.StatusBadRequest
	case errs.Precondition:
		return http.StatusPreconditionFailed
	}
	return http.StatusInternalServerError
}

func writeError(w http.ResponseWriter, err error) {
	e := errs.From(err)
	writeJSON(w, statusFor(e.Code), ErrorBody{Error: e})
}

func noContent(w http.ResponseWriter, err error) {
	if err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
