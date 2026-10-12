// Package desktop holds the CGO-free parts of the desktop app: starting the
// daemon, proxying the dashboard with the API token, and bridging the
// daemon's streams into Wails events.
package desktop

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Ayush2966/Agent-Box/internal/api"
	"github.com/Ayush2966/Agent-Box/internal/daemon"
)

// bootstrapDeadline bounds how long Bootstrap waits for a daemon to answer.
var bootstrapDeadline = 5 * time.Second

// Bootstrap makes sure a daemon is serving home (ensure is daemon.EnsureRunning
// in production) and returns its HTTP API address and token. The address comes
// from the daemon's own health answer over the socket, which is authoritative;
// it waits a few seconds for a freshly spawned daemon to answer.
func Bootstrap(home string, ensure func(daemon.Paths) error, health func(ctx context.Context, sock string) (api.HealthView, error)) (apiAddr, token string, err error) {
	p := daemon.PathsFor(home)
	if err := ensure(p); err != nil {
		return "", "", err
	}
	deadline := time.Now().Add(bootstrapDeadline)
	for {
		// One second per attempt, but never past the overall deadline: a daemon
		// that accepts and then hangs must not stretch "within 5s" to 6s.
		callDeadline := time.Now().Add(time.Second)
		if callDeadline.After(deadline) {
			callDeadline = deadline
		}
		ctx, cancel := context.WithDeadline(context.Background(), callDeadline)
		hv, herr := health(ctx, p.Sock)
		cancel()
		if herr == nil {
			if hv.APIAddr == "" {
				return "", "", fmt.Errorf("the agentbox daemon is running but its HTTP API is disabled: the port in api_addr (%s) is probably in use; see %s", p.Config, p.Log)
			}
			t, terr := os.ReadFile(p.Token)
			if terr != nil {
				return "", "", fmt.Errorf("could not read the API token %s: %v", p.Token, terr)
			}
			return hv.APIAddr, strings.TrimSpace(string(t)), nil
		}
		if time.Now().After(deadline) {
			return "", "", fmt.Errorf("the agentbox daemon did not answer on %s within %s; see %s", p.Sock, bootstrapDeadline, p.Log)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
