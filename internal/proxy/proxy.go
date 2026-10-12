// Package proxy serves every running box at http://<box>.localhost:<port>/ by
// forwarding to the box's dev server, and explains every other state with a
// small HTML page. It knows nothing about boxes beyond the Lookup function.
package proxy

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
)

// Target is what Lookup resolves a host to.
type Target struct {
	Repo, Name string
	State      string // running | ready | stopped | failed | creating | removing
	LastError  string
	Port       int                  // the dev server's port on 127.0.0.1
	Tail       func(n int) []string // last n log lines, for the 502 page; may be nil
}

// NoSuchBoxError: no box matches. Candidates are the hosts that do exist.
type NoSuchBoxError struct {
	Name       string
	Candidates []string
}

func (e *NoSuchBoxError) Error() string { return fmt.Sprintf("no box called %q", e.Name) }

// AmbiguousError: several repos have a box with this name; Hosts are the
// fully qualified <name>.<repo>.localhost addresses.
type AmbiguousError struct {
	Name  string
	Hosts []string
}

func (e *AmbiguousError) Error() string {
	return fmt.Sprintf("%d repos have a box called %q", len(e.Hosts), e.Name)
}

// NoPortsError: the box exists but exposes no port.
type NoPortsError struct{ Name string }

func (e *NoPortsError) Error() string { return fmt.Sprintf("box %q has no ports", e.Name) }

type Options struct {
	// Lookup resolves <name>.localhost (repo == "") or <name>.<repo>.localhost.
	Lookup func(name, repo string) (Target, error)
	// Dashboard is the dashboard's base URL, e.g. "http://127.0.0.1:7777/ui/".
	Dashboard string
}

type Handler struct {
	o         Options
	transport http.RoundTripper
}

func New(o Options) *Handler {
	// One transport for every box, so idle connections are pooled and bounded.
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.MaxIdleConnsPerHost = 32
	return &Handler{o: o, transport: tr}
}

// IsProxyHost reports whether a Host header names a box (ends in ".localhost").
func IsProxyHost(host string) bool { return strings.HasSuffix(hostname(host), ".localhost") }

// hostname lowercases a Host header and drops its port and any trailing dot.
func hostname(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.ToLower(strings.TrimSuffix(host, "."))
}

// hostPort returns the ":port" part of a Host header ("" when absent), so the
// pages can build links that keep the port the browser used.
func hostPort(host string) string {
	if _, p, err := net.SplitHostPort(host); err == nil {
		return ":" + p
	}
	return ""
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host := hostname(r.Host)
	port := hostPort(r.Host)
	labels := strings.Split(strings.TrimSuffix(host, ".localhost"), ".")
	var name, repo string
	switch len(labels) {
	case 1:
		name = labels[0]
	case 2:
		name, repo = labels[0], labels[1]
	default:
		h.page(w, http.StatusNotFound, pageData{Title: "Not a box address",
			Message: fmt.Sprintf("%s is not a box address. Boxes live at <name>.localhost or <name>.<repo>.localhost.", host)})
		return
	}
	t, err := h.o.Lookup(name, repo)
	if err != nil {
		h.lookupError(w, err, name, port)
		return
	}
	switch t.State {
	case "running":
		h.proxy(t).ServeHTTP(w, r)
	case "creating":
		h.page(w, http.StatusServiceUnavailable, pageData{Title: "Box " + t.Name + " is being created",
			Message: "This page refreshes every two seconds until the box is up.", Refresh: true, Links: h.boxLinks(t)})
	case "removing":
		h.page(w, http.StatusServiceUnavailable, pageData{Title: "Box " + t.Name + " is being removed",
			Message: "This box is going away; its dev server has been stopped.", Refresh: true, Links: h.boxLinks(t)})
	default:
		h.page(w, http.StatusServiceUnavailable, pageData{Title: "Box " + t.Name + " is " + t.State,
			Message: "Its dev server is not running, so there is nothing to show yet.",
			Detail:  t.LastError, Command: "agentbox start " + t.Name, Links: h.boxLinks(t)})
	}
}

func (h *Handler) lookupError(w http.ResponseWriter, err error, name, port string) {
	var nsb *NoSuchBoxError
	var amb *AmbiguousError
	var np *NoPortsError
	switch {
	case errors.As(err, &nsb):
		d := pageData{Title: "No box called " + name, Message: "There is no box called " + name + "."}
		if len(nsb.Candidates) > 0 {
			d.Message += " These boxes exist:"
			for _, c := range nsb.Candidates {
				d.Links = append(d.Links, link{Href: "http://" + c + port + "/", Text: c})
			}
		} else {
			d.Message += " Create one with `agentbox new " + name + "`."
		}
		h.page(w, http.StatusNotFound, d)
	case errors.As(err, &amb):
		d := pageData{Title: "Which " + name + "?", Message: "Several repos have a box called " + name + ". Pick one:"}
		for _, host := range amb.Hosts {
			d.Links = append(d.Links, link{Href: "http://" + host + port + "/", Text: host})
		}
		h.page(w, http.StatusMultipleChoices, d)
	case errors.As(err, &np):
		h.page(w, http.StatusNotFound, pageData{Title: "Box " + name + " has no ports",
			Message: "Its agentbox.yaml lists no `ports`, so there is no dev server to forward to."})
	default:
		h.page(w, http.StatusInternalServerError, pageData{Title: "agentbox error", Message: err.Error()})
	}
}

// proxy builds the reverse proxy for one target. It is per request so the
// 502 page can show that box's logs; the transport (and its pool) is shared.
func (h *Handler) proxy(t Target) *httputil.ReverseProxy {
	target := &url.URL{Scheme: "http", Host: "127.0.0.1:" + strconv.Itoa(t.Port)}
	return &httputil.ReverseProxy{
		Transport:     h.transport,
		FlushInterval: -1, // dev servers stream (HMR, SSE); never buffer
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.SetXForwarded()
			pr.Out.Host = pr.In.Host // the app sees the address the browser used
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			var lines []string
			if t.Tail != nil {
				lines = t.Tail(20)
			}
			h.page(w, http.StatusBadGateway, pageData{Title: "Box " + t.Name + " is not answering",
				Message: fmt.Sprintf("agentbox could not reach the dev server on port %d: %v. It may be restarting; the last log lines:", t.Port, err),
				Lines:   lines, Command: "agentbox logs " + t.Name + " -f", Links: h.boxLinks(t)})
		},
	}
}

func (h *Handler) boxLinks(t Target) []link {
	if h.o.Dashboard == "" {
		return nil
	}
	return []link{{Href: h.o.Dashboard + "#/r/" + url.PathEscape(t.Repo) + "/b/" + url.PathEscape(t.Name), Text: "Open " + t.Name + " in the dashboard"}}
}
