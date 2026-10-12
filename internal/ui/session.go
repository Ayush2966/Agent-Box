package ui

import (
	"crypto/subtle"
	"net/http"
)

// SessionCookie carries the API token for the browser. HttpOnly keeps page
// scripts (and the box apps, which never see this host anyway) away from it.
const SessionCookie = "agentbox_session"

// ValidSession reports whether the request carries a session cookie equal to the token.
func ValidSession(r *http.Request, token string) bool {
	c, err := r.Cookie(SessionCookie)
	return err == nil && token != "" && subtle.ConstantTimeCompare([]byte(c.Value), []byte(token)) == 1
}

func SetSession(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{Name: SessionCookie, Value: token, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, MaxAge: 365 * 24 * 3600})
}

func ClearSession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: SessionCookie, Value: "", Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, MaxAge: -1})
}
