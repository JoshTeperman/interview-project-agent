package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/JoshTeperman/interview-project-agent/internal/auth"
	"github.com/JoshTeperman/interview-project-agent/internal/config"
)

type fakeMail struct{ bodies []string }

func (f *fakeMail) Send(_ context.Context, _, _, body string) error {
	f.bodies = append(f.bodies, body)
	return nil
}

func (f *fakeMail) code(t *testing.T) string {
	t.Helper()
	m := regexp.MustCompile(`\d{6}`).FindString(f.bodies[len(f.bodies)-1])
	if m == "" {
		t.Fatal("no code")
	}
	return m
}

type harness struct {
	h    http.Handler
	mail *fakeMail
	logs *bytes.Buffer
	srv  *Server
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	cfg := &config.Config{
		AllowedDomains: []string{"example.com"}, CodeTTL: 10 * time.Minute, SessionTTL: time.Hour,
		CodeRequestsPerIPHour: 5, CodeRequestsPerEmailHour: 3, VerifyAttemptsPerCode: 5, VerifyPerIPHour: 30,
		SessionTurnCap: 40,
	}
	m, logs := &fakeMail{}, &bytes.Buffer{}
	s := New(cfg, auth.New(cfg, m, nil), slog.New(slog.NewJSONHandler(logs, nil)))
	return &harness{h: s.Handler(), mail: m, logs: logs, srv: s}
}

func (h *harness) do(method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	h.h.ServeHTTP(w, r)
	return w
}

func (h *harness) login(t *testing.T, email string) *http.Cookie {
	t.Helper()
	if w := h.do("POST", "/api/auth/request", `{"email":"`+email+`"}`, nil); w.Code != 200 {
		t.Fatalf("request: %d %s", w.Code, w.Body)
	}
	w := h.do("POST", "/api/auth/verify", `{"email":"`+email+`","code":"`+h.mail.code(t)+`"}`, nil)
	if w.Code != 200 {
		t.Fatalf("verify: %d %s", w.Code, w.Body)
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == CookieName {
			return c
		}
	}
	t.Fatal("no session cookie")
	return nil
}

func TestUnauthenticatedGetsNoProtectedContent(t *testing.T) {
	h := newHarness(t)
	h.srv.Protected.HandleFunc("POST /api/chat", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("PROJECT DATA")) })
	h.srv.Protected.HandleFunc("GET /api/starters", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("PROJECT DATA")) })

	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/me"}, {"POST", "/api/chat"}, {"GET", "/api/starters"}, {"GET", "/api/anything"},
	} {
		w := h.do(tc.method, tc.path, "", nil)
		if w.Code != http.StatusUnauthorized || strings.Contains(w.Body.String(), "PROJECT DATA") {
			t.Errorf("%s %s: %d %q", tc.method, tc.path, w.Code, w.Body)
		}
	}
	for _, p := range []string{"/", "/some/page"} {
		w := h.do("GET", p, "", nil)
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/login" {
			t.Errorf("GET %s: %d -> %q", p, w.Code, w.Header().Get("Location"))
		}
	}
	// A forged cookie is no better than none.
	if w := h.do("GET", "/api/me", "", &http.Cookie{Name: CookieName, Value: "forged"}); w.Code != 401 {
		t.Errorf("forged cookie: %d", w.Code)
	}
}

func TestPublicRoutesCarryNoProjectData(t *testing.T) {
	h := newHarness(t)
	for _, p := range []string{"/login", "/healthz", "/robots.txt", "/static/login.js", "/static/style.css"} {
		if w := h.do("GET", p, "", nil); w.Code != 200 {
			t.Errorf("GET %s: %d", p, w.Code)
		}
	}
}

func TestLoginFlowAndCookie(t *testing.T) {
	h := newHarness(t)
	c := h.login(t, "Rev@Example.com")
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode || c.Path != "/" {
		t.Errorf("cookie flags: %+v", c)
	}
	w := h.do("GET", "/api/me", "", c)
	var me struct{ Email string }
	json.NewDecoder(w.Body).Decode(&me)
	if w.Code != 200 || me.Email != "rev@example.com" {
		t.Fatalf("me: %d %+v", w.Code, me)
	}
	if w := h.do("GET", "/", "", c); w.Code != 200 || !strings.Contains(w.Body.String(), "Signed in") {
		t.Errorf("app page: %d", w.Code)
	}
	if w := h.do("GET", "/login", "", c); w.Code != http.StatusSeeOther {
		t.Errorf("login while signed in: %d", w.Code)
	}
	h.do("POST", "/api/auth/logout", "", c)
	if w := h.do("GET", "/api/me", "", c); w.Code != 401 {
		t.Errorf("after logout: %d", w.Code)
	}
}

func TestErrorStates(t *testing.T) {
	h := newHarness(t)
	cases := []struct {
		path, body string
		status     int
		code       string
	}{
		{"/api/auth/request", `{"email":"eve@gmail.com"}`, 403, "email_not_allowed"},
		{"/api/auth/request", `{"email":"nope"}`, 400, "invalid_email"},
		{"/api/auth/request", `{bad json`, 400, "bad_request"},
		{"/api/auth/verify", `{"email":"a@example.com","code":"123456"}`, 401, "invalid_code"},
	}
	for _, tc := range cases {
		w := h.do("POST", tc.path, tc.body, nil)
		var e struct{ Error, Message string }
		json.NewDecoder(w.Body).Decode(&e)
		if w.Code != tc.status || e.Error != tc.code || e.Message == "" {
			t.Errorf("%s %s: %d %+v", tc.path, tc.body, w.Code, e)
		}
	}
	// Non-JSON posts are refused (CSRF defence).
	r := httptest.NewRequest("POST", "/api/auth/request", strings.NewReader("email=a@example.com"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.h.ServeHTTP(w, r)
	if w.Code != http.StatusUnsupportedMediaType {
		t.Errorf("form post: %d", w.Code)
	}
}

func TestRateLimitResponse(t *testing.T) {
	h := newHarness(t)
	var w *httptest.ResponseRecorder
	for i := 0; i < 6; i++ {
		w = h.do("POST", "/api/auth/request", `{"email":"x@gmail.com"}`, nil)
	}
	if w.Code != 429 || w.Header().Get("Retry-After") == "" {
		t.Fatalf("got %d, Retry-After=%q", w.Code, w.Header().Get("Retry-After"))
	}
}

func TestSecurityHeadersEverywhere(t *testing.T) {
	h := newHarness(t)
	for _, tc := range []struct{ method, path string }{
		{"GET", "/"}, {"GET", "/login"}, {"GET", "/healthz"}, {"GET", "/api/me"}, {"POST", "/api/auth/request"},
	} {
		w := h.do(tc.method, tc.path, "", nil)
		if got := w.Header().Get("X-Robots-Tag"); got != "noindex, nofollow" {
			t.Errorf("%s %s: X-Robots-Tag=%q", tc.method, tc.path, got)
		}
		if w.Header().Get("Content-Security-Policy") == "" {
			t.Errorf("%s %s: no CSP", tc.method, tc.path)
		}
	}
}

func TestLogsNeverContainCodesOrTokens(t *testing.T) {
	h := newHarness(t)
	c := h.login(t, "a@example.com")
	code := h.mail.code(t)
	logs, _ := io.ReadAll(h.logs)
	if strings.Contains(string(logs), code) || strings.Contains(string(logs), c.Value) {
		t.Fatalf("secret in logs: %s", logs)
	}
}

func TestClientIP(t *testing.T) {
	h := newHarness(t)
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:5555"
	r.Header.Set("X-Forwarded-For", "9.9.9.9, 203.0.113.7")
	if got := h.srv.clientIP(r); got != "127.0.0.1" {
		t.Errorf("untrusted: %s", got)
	}
	h.srv.cfg.TrustForwardedFor = true
	if got := h.srv.clientIP(r); got != "203.0.113.7" {
		t.Errorf("trusted: %s", got)
	}
}
