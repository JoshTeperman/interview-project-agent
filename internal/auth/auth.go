// Package auth implements email magic-code login and server-side sessions (S-D4).
//
// Codes are 6 digits, short-lived, single-use, and stored only as an HMAC
// keyed with a per-process secret. Session tokens are random and stored only
// as a SHA-256 hash. Nothing here logs a code or token.
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"net/mail"
	"strings"
	"sync"
	"time"

	"github.com/JoshTeperman/interview-project-agent/internal/config"
	mailer "github.com/JoshTeperman/interview-project-agent/internal/mail"
	"github.com/JoshTeperman/interview-project-agent/internal/ratelimit"
)

// Errors returned by Service. Handlers map them to UI states (U2).
var (
	ErrInvalidEmail = errors.New("invalid email address")
	ErrNotAllowed   = errors.New("email not allowed")
	ErrInvalidCode  = errors.New("invalid or expired code")
	ErrSendFailed   = errors.New("could not send email")
)

// RateLimitedError reports that a limit was hit and when to retry.
type RateLimitedError struct{ RetryAfter time.Duration }

func (e *RateLimitedError) Error() string { return "rate limited" }

// Session is a signed-in interviewer.
type Session struct {
	Email   string
	Expires time.Time
	Turns   int // chat turns used, for the per-session turn cap
}

type pendingCode struct {
	mac      []byte
	expires  time.Time
	attempts int
}

// Service holds pending codes and sessions in memory.
type Service struct {
	cfg  *config.Config
	mail mailer.Sender
	now  func() time.Time
	key  []byte // HMAC key for codes; random per process

	codeIP, codeEmail, verifyIP *ratelimit.Limiter

	mu       sync.Mutex
	codes    map[string]*pendingCode // by normalized email
	sessions map[string]*Session     // by SHA-256 of token
}

// New returns a Service. now may be nil (time.Now).
func New(cfg *config.Config, sender mailer.Sender, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic(err)
	}
	return &Service{
		cfg: cfg, mail: sender, now: now, key: key,
		codeIP:    ratelimit.New(cfg.CodeRequestsPerIPHour, time.Hour).WithClock(now),
		codeEmail: ratelimit.New(cfg.CodeRequestsPerEmailHour, time.Hour).WithClock(now),
		verifyIP:  ratelimit.New(cfg.VerifyPerIPHour, time.Hour).WithClock(now),
		codes:     map[string]*pendingCode{},
		sessions:  map[string]*Session{},
	}
}

// NormalizeEmail trims and lower-cases a bare address, rejecting display names and malformed input.
func NormalizeEmail(raw string) (string, error) {
	e := strings.ToLower(strings.TrimSpace(raw))
	if len(e) == 0 || len(e) > 254 {
		return "", ErrInvalidEmail
	}
	a, err := mail.ParseAddress(e)
	if err != nil || a.Address != e || a.Name != "" {
		return "", ErrInvalidEmail
	}
	return e, nil
}

// RequestCode emails a fresh code to an allowed address. Any earlier code for it is replaced.
func (s *Service) RequestCode(ctx context.Context, rawEmail, ip string) error {
	if ok, retry := s.codeIP.Allow(ip); !ok {
		return &RateLimitedError{retry}
	}
	email, err := NormalizeEmail(rawEmail)
	if err != nil {
		return err
	}
	if !s.cfg.EmailAllowed(email) {
		return ErrNotAllowed
	}
	if ok, retry := s.codeEmail.Allow(email); !ok {
		return &RateLimitedError{retry}
	}

	code, err := randomCode()
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.codes[email] = &pendingCode{mac: s.mac(email, code), expires: s.now().Add(s.cfg.CodeTTL)}
	s.mu.Unlock()

	if err := s.mail.Send(ctx, email, "Your sign-in code", codeEmailBody(code, s.cfg.CodeTTL)); err != nil {
		s.mu.Lock()
		delete(s.codes, email)
		s.mu.Unlock()
		return fmt.Errorf("%w: %v", ErrSendFailed, err)
	}
	return nil
}

// Verify redeems a code. On success it returns a new session token; the code is spent.
// A code is also discarded after VerifyAttemptsPerCode wrong guesses or once it expires.
func (s *Service) Verify(rawEmail, code, ip string) (string, error) {
	if ok, retry := s.verifyIP.Allow(ip); !ok {
		return "", &RateLimitedError{retry}
	}
	email, err := NormalizeEmail(rawEmail)
	if err != nil {
		return "", ErrInvalidCode
	}
	code = strings.TrimSpace(code)

	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.codes[email]
	if !ok {
		return "", ErrInvalidCode
	}
	if !s.now().Before(p.expires) {
		delete(s.codes, email)
		return "", ErrInvalidCode
	}
	if !hmac.Equal(p.mac, s.mac(email, code)) {
		p.attempts++
		if p.attempts >= s.cfg.VerifyAttemptsPerCode {
			delete(s.codes, email)
		}
		return "", ErrInvalidCode
	}
	delete(s.codes, email)

	tok := make([]byte, 32)
	if _, err := rand.Read(tok); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(tok)
	s.sessions[hashToken(token)] = &Session{Email: email, Expires: s.now().Add(s.cfg.SessionTTL)}
	return token, nil
}

// Session returns a copy of the live session for token, if any.
func (s *Service) Session(token string) (Session, bool) {
	if token == "" {
		return Session{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	h := hashToken(token)
	sess, ok := s.sessions[h]
	if !ok {
		return Session{}, false
	}
	if !s.now().Before(sess.Expires) {
		delete(s.sessions, h)
		return Session{}, false
	}
	return *sess, true
}

// UseTurn counts one chat turn against the session's cap. It returns false when the cap is reached.
func (s *Service) UseTurn(token string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[hashToken(token)]
	if !ok || sess.Turns >= s.cfg.SessionTurnCap {
		return false
	}
	sess.Turns++
	return true
}

// Logout ends the session for token.
func (s *Service) Logout(token string) {
	s.mu.Lock()
	delete(s.sessions, hashToken(token))
	s.mu.Unlock()
}

// Prune removes expired codes, sessions, and rate-limit buckets.
func (s *Service) Prune() {
	now := s.now()
	s.mu.Lock()
	for k, p := range s.codes {
		if !now.Before(p.expires) {
			delete(s.codes, k)
		}
	}
	for k, v := range s.sessions {
		if !now.Before(v.Expires) {
			delete(s.sessions, k)
		}
	}
	s.mu.Unlock()
	s.codeIP.Prune()
	s.codeEmail.Prune()
	s.verifyIP.Prune()
}

// RunPruner calls Prune every interval until ctx ends.
func (s *Service) RunPruner(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Prune()
		}
	}
}

func (s *Service) mac(email, code string) []byte {
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte(email + "\x00" + code))
	return m.Sum(nil)
}

func hashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return string(h[:])
}

func randomCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

func codeEmailBody(code string, ttl time.Duration) string {
	return fmt.Sprintf("Your sign-in code is %s\n\nIt expires in %d minutes and can be used once.\nIf you did not ask for it, you can ignore this email.\n",
		code, int(ttl.Minutes()))
}
