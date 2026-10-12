package ui

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// LoginCodes are one-shot, short-lived codes that `agentbox ui` turns into a
// browser session without ever putting the API token in a URL.
type LoginCodes struct {
	TTL time.Duration
	Now func() time.Time

	mu    sync.Mutex
	codes map[string]time.Time // code → expiry
}

func NewLoginCodes() *LoginCodes {
	return &LoginCodes{TTL: 2 * time.Minute, Now: time.Now, codes: map[string]time.Time{}}
}

// Mint issues a new code. Expired codes are swept here, so the map stays small.
func (c *LoginCodes) Mint() (string, time.Time) {
	buf := make([]byte, 32)
	rand.Read(buf) // never fails on Go 1.24
	code := hex.EncodeToString(buf)
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.Now()
	for k, exp := range c.codes {
		if exp.Before(now) {
			delete(c.codes, k)
		}
	}
	exp := now.Add(c.TTL)
	c.codes[code] = exp
	return code, exp
}

// Redeem consumes a code. It is true only once per code, and only before expiry.
func (c *LoginCodes) Redeem(code string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	exp, ok := c.codes[code]
	if !ok {
		return false
	}
	delete(c.codes, code)
	return !exp.Before(c.Now())
}
