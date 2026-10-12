// Package client talks to the local agentbox daemon over its Unix socket.
package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/Ayush2966/Agent-Box/internal/api"
	"github.com/Ayush2966/Agent-Box/internal/box"
)

const base = "http://agentbox"

type Client struct{ hc *http.Client }

type Event struct {
	Type string         `json:"type"`
	Box  string         `json:"box"`
	Data map[string]any `json:"data"`
}

func New(sock string) *Client {
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", sock)
	}}
	return &Client{hc: &http.Client{Transport: tr}}
}

func boxPath(repoID, name string) string {
	return "/v1/repos/" + url.PathEscape(repoID) + "/boxes/" + url.PathEscape(name)
}

func (c *Client) open(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, rd)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("talk to agentbox daemon: %w", err)
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		var eb api.ErrorBody
		if json.NewDecoder(resp.Body).Decode(&eb) == nil && eb.Error != nil {
			return nil, eb.Error
		}
		return nil, fmt.Errorf("%s %s: %s", method, path, resp.Status)
	}
	return resp, nil
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	resp, err := c.open(ctx, method, path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch o := out.(type) {
	case nil:
		return nil
	case *string:
		b, err := io.ReadAll(resp.Body)
		*o = string(b)
		return err
	default:
		return json.NewDecoder(resp.Body).Decode(out)
	}
}

func (c *Client) Health(ctx context.Context) error { return c.do(ctx, "GET", "/v1/health", nil, nil) }

func (c *Client) ResolveRepo(ctx context.Context, path string) (api.RepoView, error) {
	var rv api.RepoView
	return rv, c.do(ctx, "POST", "/v1/repos", map[string]string{"path": path}, &rv)
}

func (c *Client) CreateBox(ctx context.Context, opts box.NewOptions) (api.Created, error) {
	var out api.Created
	return out, c.do(ctx, "POST", "/v1/boxes", opts, &out)
}

func (c *Client) ListBoxes(ctx context.Context, repoID string) ([]api.BoxView, error) {
	var out []api.BoxView
	return out, c.do(ctx, "GET", "/v1/boxes?repo="+url.QueryEscape(repoID), nil, &out)
}

func (c *Client) GetBox(ctx context.Context, repoID, name string) (api.BoxView, error) {
	var out api.BoxView
	return out, c.do(ctx, "GET", boxPath(repoID, name), nil, &out)
}

func (c *Client) StartBox(ctx context.Context, repoID, name string) error {
	return c.do(ctx, "POST", boxPath(repoID, name)+"/start", nil, nil)
}

func (c *Client) StopBox(ctx context.Context, repoID, name string) error {
	return c.do(ctx, "POST", boxPath(repoID, name)+"/stop", nil, nil)
}

func (c *Client) Merge(ctx context.Context, repoID, name string, opts box.MergeOptions) (api.MergeResult, error) {
	var out api.MergeResult
	return out, c.do(ctx, "POST", boxPath(repoID, name)+"/merge", opts, &out)
}

func (c *Client) RemoveBox(ctx context.Context, repoID, name string, force, keepBranch bool) error {
	q := url.Values{}
	if force {
		q.Set("force", "true")
	}
	if keepBranch {
		q.Set("keep_branch", "true")
	}
	return c.do(ctx, "DELETE", boxPath(repoID, name)+"?"+q.Encode(), nil, nil)
}

func (c *Client) Diff(ctx context.Context, repoID, name, against string) (string, error) {
	var out string
	return out, c.do(ctx, "GET", boxPath(repoID, name)+"/diff?against="+url.QueryEscape(against), nil, &out)
}

func (c *Client) RefreshDB(ctx context.Context, repoID string, terminate bool) error {
	return c.do(ctx, "POST", "/v1/repos/"+url.PathEscape(repoID)+"/db/refresh?terminate="+strconv.FormatBool(terminate), nil, nil)
}

func (c *Client) Doctor(ctx context.Context, repoID string, fix bool) ([]box.Finding, error) {
	method := "GET"
	if fix {
		method = "POST"
	}
	var out []box.Finding
	return out, c.do(ctx, method, "/v1/repos/"+url.PathEscape(repoID)+"/doctor", nil, &out)
}

// Logs writes the last tail lines to w; with follow it keeps streaming until ctx is done.
func (c *Client) Logs(ctx context.Context, repoID, name string, tail int, follow bool, w io.Writer) error {
	path := fmt.Sprintf("%s/logs?tail=%d&follow=%t", boxPath(repoID, name), tail, follow)
	resp, err := c.open(ctx, "GET", path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if !follow {
		_, err := io.Copy(w, resp.Body)
		return err
	}
	err = readSSE(resp.Body, func(_ string, data []byte) error {
		var line string
		if err := json.Unmarshal(data, &line); err != nil {
			return err
		}
		_, err := fmt.Fprintln(w, line)
		return err
	})
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// Events streams daemon events until ctx is done. The subscription is live
// when Events returns, so actions taken afterwards can't be missed.
func (c *Client) Events(ctx context.Context) (<-chan Event, error) {
	resp, err := c.open(ctx, "GET", "/v1/events", nil)
	if err != nil {
		return nil, err
	}
	ch := make(chan Event, 64)
	go func() {
		defer close(ch)
		defer resp.Body.Close()
		_ = readSSE(resp.Body, func(_ string, data []byte) error {
			var ev Event
			if json.Unmarshal(data, &ev) != nil {
				return nil
			}
			select {
			case ch <- ev:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	return ch, nil
}

func readSSE(r io.Reader, fn func(event string, data []byte) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 4<<20)
	var event string
	var data []byte
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if data != nil {
				if err := fn(event, data); err != nil {
					return err
				}
			}
			event, data = "", nil
		case strings.HasPrefix(line, ":"):
		case strings.HasPrefix(line, "event: "):
			event = line[len("event: "):]
		case strings.HasPrefix(line, "data: "):
			data = append(data, line[len("data: "):]...)
		}
	}
	return sc.Err()
}

func (c *Client) HealthInfo(ctx context.Context) (api.HealthView, error) {
	var h api.HealthView
	return h, c.do(ctx, "GET", "/v1/health", nil, &h)
}

// LoginCode mints a one-shot dashboard sign-in link.
func (c *Client) LoginCode(ctx context.Context) (api.LoginCode, error) {
	var lc api.LoginCode
	return lc, c.do(ctx, "POST", "/v1/ui/login-codes", nil, &lc)
}

// Repos lists every repo the daemon knows.
func (c *Client) Repos(ctx context.Context) ([]api.RepoView, error) {
	var out []api.RepoView
	return out, c.do(ctx, "GET", "/v1/repos", nil, &out)
}

// LogLines streams a box's log: the last tail lines, then every new line
// until ctx is done. The channel is closed when the stream ends.
func (c *Client) LogLines(ctx context.Context, repoID, name string, tail int) (<-chan string, error) {
	path := fmt.Sprintf("%s/logs?tail=%d&follow=true", boxPath(repoID, name), tail)
	resp, err := c.open(ctx, "GET", path, nil)
	if err != nil {
		return nil, err
	}
	ch := make(chan string, 256)
	go func() {
		defer close(ch)
		defer resp.Body.Close()
		_ = readSSE(resp.Body, func(_ string, data []byte) error {
			var line string
			if json.Unmarshal(data, &line) != nil {
				return nil
			}
			select {
			case ch <- line:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	return ch, nil
}
