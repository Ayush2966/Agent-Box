// Package ports checks whether local TCP ports are available.
package ports

import (
	"errors"
	"fmt"
	"net"
	"syscall"
	"time"
)

const (
	DefaultLo = 20000
	DefaultHi = 29999
)

// IsFree reports whether nothing is listening on port on this machine. It
// both dials and binds, because on macOS a bind can succeed next to an
// existing wildcard listener.
func IsFree(port int) bool {
	if c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond); err == nil {
		c.Close()
		return false
	}
	for _, addr := range []string{fmt.Sprintf("127.0.0.1:%d", port), fmt.Sprintf(":%d", port), fmt.Sprintf("[::1]:%d", port)} {
		l, err := net.Listen("tcp", addr)
		if err != nil {
			if errors.Is(err, syscall.EADDRINUSE) {
				return false
			}
			continue // e.g. no IPv6 loopback on this host
		}
		l.Close()
	}
	return true
}
