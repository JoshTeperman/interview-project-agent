// Package server wires the HTTP routes, security headers, and auth gate.
//
// Routing is default-deny: only the routes registered on the public mux are
// reachable without a session. Everything else goes through requireAuth (R1, R14).
package server

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/JoshTeperman/interview-project-agent/internal/auth"
	"github.com/JoshTeperman/interview-project-agent/internal/config"
	"github.com/JoshTeperman/interview-project-agent/web"
)

// CookieName uses the __Host- prefix, so browsers require Secure, Path=/, and no Domain.
const CookieName = "__Host-session"

const maxAuthBody = 4 << 10

// Server holds the dependencies for HTTP handlers.
type Server struct {
	cfg  *config.Config
	auth *auth.Service
	log  *slog.Logger

	// Protected is the mux for routes that need a session. Later milestones register chat routes here.
	Protected *http.ServeMux
}

// New builds the server. Call Handler to get the root http.Handler.
func New(cfg *config.Config, a *auth.Service, log *slog.Logger) *Server {
	s := &Server{cfg: cfg, auth: a, log: log, Protected: http.NewServeMux()}
	s.Protected.HandleFunc("GET /{$}", s.page("app.html"))
	s.Protected.HandleFunc("GET /api/me", s.handleMe)
	return s
}

// Handler returns the root handler: security headers, public routes, then the auth gate.
func (s *Server) Handler() http.Handler {
	static, _ := fs.Sub(web.FS, "static")

	public := http.NewServeMux()
	public.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok\n")) })
	public.HandleFunc("GET /robots.txt", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte("User-agent: *\nDisallow: /\n"))
	})
	public.HandleFunc("GET /login", s.handleLoginPage)
	public.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	public.HandleFunc("POST /api/auth/request", s.handleRequestCode)
	public.HandleFunc("POST /api/auth/verify", s.handleVerify)
	public.HandleFunc("POST /api/auth/logout", s.handleLogout)
	public.Handle("/", s.requireAuth(s.Protected))

	return securityHeaders(public)
}

// securityHeaders sets headers on every response, including errors and redirects (R12).
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Robots-Tag", "noindex, nofollow")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		h.Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

type ctxKey struct{}

type sessionInfo struct {
	token string
	auth.Session
}

// requireAuth rejects requests without a live session. API calls get 401 JSON;
// page requests are redirected to /login. No protected handler runs unauthenticated.
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(CookieName)
		var sess auth.Session
		ok := err == nil
		if ok {
			sess, ok = s.auth.Session(c.Value)
		}
		if !ok {
			if strings.HasPrefix(r.URL.Path, "/api/") || r.Method != http.MethodGet {
				writeError(w, http.StatusUnauthorized, "unauthenticated", "Please sign in.")
				return
			}
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		ctx := context.WithValue(r.Context(), ctxKey{}, sessionInfo{token: c.Value, Session: sess})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// SessionFrom returns the session token and session for an authenticated request.
func SessionFrom(ctx context.Context) (token string, sess auth.Session, ok bool) {
	si, ok := ctx.Value(ctxKey{}).(sessionInfo)
	return si.token, si.Session, ok
}

func (s *Server) page(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		b, err := web.FS.ReadFile(name)
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(b)
	}
}

func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(CookieName); err == nil {
		if _, ok := s.auth.Session(c.Value); ok {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
	}
	s.page("login.html")(w, r)
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	_, sess, _ := SessionFrom(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"email":           sess.Email,
		"turns_used":      sess.Turns,
		"turns_remaining": max(0, s.cfg.SessionTurnCap-sess.Turns),
	})
}

func (s *Server) handleRequestCode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	ip := s.clientIP(r)
	err := s.auth.RequestCode(r.Context(), req.Email, ip)
	var rl *auth.RateLimitedError
	switch {
	case err == nil:
		// Same answer for allowed and disallowed emails (001-D2).
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "expires_in_seconds": int(s.cfg.CodeTTL.Seconds())})
	case errors.As(err, &rl):
		s.log.Warn("code_rate_limited", "ip", ip)
		writeRateLimited(w, rl.RetryAfter)
	case errors.Is(err, auth.ErrInvalidEmail):
		writeError(w, http.StatusBadRequest, "invalid_email", "That doesn't look like an email address.")
	default:
		s.log.Error("code_request_error", "error", err.Error())
		writeError(w, http.StatusInternalServerError, "internal", "Something went wrong.")
	}
}

func (s *Server) handleVerify(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
		Code  string `json:"code"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	ip := s.clientIP(r)
	token, err := s.auth.Verify(req.Email, req.Code, ip)
	var rl *auth.RateLimitedError
	switch {
	case err == nil:
		http.SetCookie(w, &http.Cookie{
			Name: CookieName, Value: token, Path: "/",
			MaxAge: int(s.cfg.SessionTTL.Seconds()), HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode,
		})
		s.log.Info("signed_in", "email", strings.ToLower(strings.TrimSpace(req.Email)))
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case errors.As(err, &rl):
		s.log.Warn("verify_rate_limited", "ip", ip)
		writeRateLimited(w, rl.RetryAfter)
	case errors.Is(err, auth.ErrInvalidCode):
		writeError(w, http.StatusUnauthorized, "invalid_code", "That code is wrong or has expired. Request a new one if needed.")
	default:
		s.log.Error("verify_error", "error", err.Error())
		writeError(w, http.StatusInternalServerError, "internal", "Something went wrong.")
	}
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(CookieName); err == nil {
		s.auth.Logout(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// clientIP returns the caller's IP. With TrustForwardedFor it uses the last
// X-Forwarded-For entry, which the trusted proxy appends.
func (s *Server) clientIP(r *http.Request) string {
	if s.cfg.TrustForwardedFor {
		if xff := r.Header.Values("X-Forwarded-For"); len(xff) > 0 {
			parts := strings.Split(xff[len(xff)-1], ",")
			if ip := strings.TrimSpace(parts[len(parts)-1]); net.ParseIP(ip) != nil {
				return ip
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// decodeJSON requires a JSON content type (a simple CSRF defence, since cross-site
// forms can't send it without a CORS preflight) and a small body.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mt != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Expected JSON.")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxAuthBody)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Malformed request.")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"error": code, "message": msg})
}

func writeRateLimited(w http.ResponseWriter, retry time.Duration) {
	secs := int(retry.Seconds()) + 1
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	writeJSON(w, http.StatusTooManyRequests, map[string]any{
		"error": "rate_limited", "message": "Too many attempts. Please wait and try again.", "retry_after_seconds": secs,
	})
}
