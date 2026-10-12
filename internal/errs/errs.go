// Package errs defines the user-facing error type shared by the daemon, API and CLI.
package errs

import (
	"errors"
	"fmt"
)

type Code string

const (
	NotFound     Code = "not_found"
	Exists       Code = "exists"
	Invalid      Code = "invalid"
	UnmergedWork Code = "unmerged_work"
	Precondition Code = "precondition"
	Conflict     Code = "conflict"
	Internal     Code = "internal"
)

// E says which step failed, why, and what the user can do about it.
type E struct {
	Code    Code   `json:"code"`
	Message string `json:"message"`
	Step    string `json:"step,omitempty"`
	Hint    string `json:"hint,omitempty"`
}

func New(code Code, format string, a ...any) *E {
	return &E{Code: code, Message: fmt.Sprintf(format, a...)}
}

func (e *E) Error() string {
	s := e.Message
	if e.Step != "" {
		s = e.Step + " failed: " + s
	}
	if e.Hint != "" {
		s += "\nhint: " + e.Hint
	}
	return s
}

func (e *E) WithHint(h string) *E { c := *e; c.Hint = h; return &c }

func (e *E) WithStep(s string) *E { c := *e; c.Step = s; return &c }

// From returns the *E in err's chain, or wraps err as an Internal error.
// From(nil) is nil, so callers may convert before checking for an error.
func From(err error) *E {
	if err == nil {
		return nil
	}
	var e *E
	if errors.As(err, &e) {
		return e
	}
	return &E{Code: Internal, Message: err.Error()}
}
