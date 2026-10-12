package desktop

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
)

// NewProxy is the Wails asset handler: every request from the window goes to
// the daemon's HTTP API with the token injected, so the dashboard loads
// signed in and its relative fetches just work. The window's own origin
// (wails://) is dropped so the daemon's Origin allowlist is satisfied by the
// Bearer path, and the outgoing Host is the daemon's address for its Host
// allowlist.
func NewProxy(apiAddr, token string) (http.Handler, error) {
	host, _, err := net.SplitHostPort(apiAddr)
	if err != nil || host == "" {
		return nil, fmt.Errorf("bad api_addr %q: %v", apiAddr, err)
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return nil, fmt.Errorf("refusing to proxy to a non-loopback address %q", apiAddr)
	}
	target := &url.URL{Scheme: "http", Host: apiAddr}
	return &httputil.ReverseProxy{
		FlushInterval: -1,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Host = apiAddr
			pr.Out.Header.Del("Origin")
			pr.Out.Header.Del("Cookie")
			pr.Out.Header.Set("Authorization", "Bearer "+token)
			if pr.Out.URL.Path == "/" || pr.Out.URL.Path == "/index.html" {
				pr.Out.URL.Path, pr.Out.URL.RawPath = "/ui/", ""
			}
		},
	}, nil
}
