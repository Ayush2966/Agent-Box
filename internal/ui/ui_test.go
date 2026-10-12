package ui

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func fakeBuild() fstest.MapFS {
	return fstest.MapFS{
		"index.html":           {Data: []byte("<!doctype html><title>agentbox</title><div id=\"root\"></div>")},
		"assets/app-abc123.js": {Data: []byte("console.log(1)")},
		".gitkeep":             {Data: nil},
	}
}

func newHandler(fsys fstest.MapFS) (http.Handler, *LoginCodes) {
	codes := NewLoginCodes()
	return Handler(Options{Token: "tok", Codes: codes, FS: fsys}), codes
}

func do(h http.Handler, method, path string, form url.Values, cookie string) *httptest.ResponseRecorder {
	var req *http.Request
	if form != nil {
		req = httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: SessionCookie, Value: cookie})
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func sessionCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == SessionCookie {
			return c
		}
	}
	t.Fatalf("no %s cookie in %v", SessionCookie, rec.Header())
	return nil
}

func TestServesAppWithSPAFallbackAndCaching(t *testing.T) {
	h, _ := newHandler(fakeBuild())
	for _, path := range []string{"/ui/", "/ui/r/myapp-1a2b3c/b/a", "/ui/does-not-exist.js"} {
		rec := do(h, "GET", path, nil, "")
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), `id="root"`) {
			t.Errorf("%s: %d %q", path, rec.Code, rec.Body.String())
		}
		if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
			t.Errorf("%s: Cache-Control %q, want no-cache", path, cc)
		}
	}
	rec := do(h, "GET", "/ui/assets/app-abc123.js", nil, "")
	if rec.Code != 200 || rec.Body.String() != "console.log(1)" || !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
		t.Errorf("asset: %d %q %q", rec.Code, rec.Body.String(), rec.Header().Get("Cache-Control"))
	}
	if !strings.Contains(rec.Header().Get("Content-Type"), "javascript") {
		t.Errorf("asset content-type %q", rec.Header().Get("Content-Type"))
	}
}

func TestNotBuiltExplains(t *testing.T) {
	h, _ := newHandler(fstest.MapFS{".gitkeep": {}})
	rec := do(h, "GET", "/ui/", nil, "")
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "built without the dashboard") {
		t.Fatalf("%d %q", rec.Code, rec.Body.String())
	}
	if Built(fakeBuild()) != true || Built(fstest.MapFS{".gitkeep": {}}) != false {
		t.Fatal("Built() wrong")
	}
}

func TestLoginCodeIsSingleUseAndExpires(t *testing.T) {
	h, codes := newHandler(fakeBuild())
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	codes.Now = func() time.Time { return now }

	code, exp := codes.Mint()
	if len(code) != 64 || !exp.Equal(now.Add(2*time.Minute)) {
		t.Fatalf("mint: %q %v", code, exp)
	}
	rec := do(h, "GET", "/ui/login?code="+code, nil, "")
	if rec.Code != 303 || rec.Header().Get("Location") != "/ui/" {
		t.Fatalf("redeem: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	c := sessionCookie(t, rec)
	if c.Value != "tok" || !c.HttpOnly || c.Path != "/" || c.SameSite != http.SameSiteLaxMode || c.Secure || c.Domain != "" {
		t.Fatalf("cookie attrs: %+v", c)
	}
	second := do(h, "GET", "/ui/login?code="+code, nil, "")
	if second.Code != 403 || !strings.Contains(second.Body.String(), "agentbox ui") {
		t.Fatalf("second use: %d %q", second.Code, second.Body.String())
	}
	for _, c := range second.Result().Cookies() {
		if c.Name == SessionCookie {
			t.Fatal("a failed login must not set a cookie")
		}
	}
	code2, _ := codes.Mint()
	now = now.Add(2*time.Minute + time.Second)
	if rec := do(h, "GET", "/ui/login?code="+code2, nil, ""); rec.Code != 403 {
		t.Fatalf("expired: %d", rec.Code)
	}
	if rec := do(h, "GET", "/ui/login?code=nope", nil, ""); rec.Code != 403 {
		t.Fatalf("unknown: %d", rec.Code)
	}
	if rec := do(h, "GET", "/ui/login", nil, ""); rec.Code != 403 {
		t.Fatalf("missing: %d", rec.Code)
	}
}

func TestTokenFormAndLogout(t *testing.T) {
	h, _ := newHandler(fakeBuild())
	rec := do(h, "POST", "/ui/login", url.Values{"token": {" tok \n"}}, "")
	if rec.Code != 303 || sessionCookie(t, rec).Value != "tok" {
		t.Fatalf("token form: %d", rec.Code)
	}
	if rec := do(h, "POST", "/ui/login", url.Values{"token": {"wrong"}}, ""); rec.Code != 403 {
		t.Fatalf("wrong token: %d", rec.Code)
	}
	rec = do(h, "POST", "/ui/logout", nil, "tok")
	if rec.Code != 303 || sessionCookie(t, rec).MaxAge >= 0 {
		t.Fatalf("logout: %d %+v", rec.Code, sessionCookie(t, rec))
	}
}

func TestValidSession(t *testing.T) {
	req := httptest.NewRequest("GET", "/v1/boxes", nil)
	if ValidSession(req, "tok") {
		t.Fatal("no cookie should be invalid")
	}
	req.AddCookie(&http.Cookie{Name: SessionCookie, Value: "tok"})
	if !ValidSession(req, "tok") || ValidSession(req, "other") || ValidSession(req, "") {
		t.Fatal("cookie must equal the token exactly")
	}
}

func TestTokenIsNotAcceptedFromTheQueryString(t *testing.T) {
	h, _ := newHandler(fakeBuild())
	rec := do(h, "POST", "/ui/login?token=tok", url.Values{}, "")
	if rec.Code != 403 {
		t.Fatalf("token in the URL must be refused: %d", rec.Code)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == SessionCookie {
			t.Fatal("a refused login must not set a cookie")
		}
	}
}

func TestDashboardPagesRefuseFraming(t *testing.T) {
	built, _ := newHandler(fakeBuild())
	unbuilt, _ := newHandler(fstest.MapFS{".gitkeep": {}})
	for name, rec := range map[string]*httptest.ResponseRecorder{
		"built":     do(built, "GET", "/ui/", nil, ""),
		"not built": do(unbuilt, "GET", "/ui/", nil, ""),
		"sign-in":   do(built, "GET", "/ui/login", nil, ""),
	} {
		if rec.Header().Get("X-Frame-Options") != "DENY" ||
			rec.Header().Get("Content-Security-Policy") != "frame-ancestors 'none'" ||
			rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s (%d): headers %v", name, rec.Code, rec.Header())
		}
	}
}
