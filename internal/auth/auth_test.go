package auth

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/JoshTeperman/interview-project-agent/internal/config"
)

// fakeMail records sent messages; set fail to simulate an SMTP error.
type fakeMail struct {
	sent []sent
	fail error
}

type sent struct{ to, subject, body string }

func (f *fakeMail) Send(_ context.Context, to, subject, body string) error {
	if f.fail != nil {
		return f.fail
	}
	f.sent = append(f.sent, sent{to, subject, body})
	return nil
}

var codeRe = regexp.MustCompile(`\b(\d{6})\b`)

func (f *fakeMail) lastCode(t *testing.T) string {
	t.Helper()
	if len(f.sent) == 0 {
		t.Fatal("no email sent")
	}
	m := codeRe.FindStringSubmatch(f.sent[len(f.sent)-1].body)
	if m == nil {
		t.Fatal("no 6-digit code in body")
	}
	return m[1]
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func testConfig() *config.Config {
	return &config.Config{
		AllowedDomains: []string{"example.com"}, AllowedEmails: []string{"friend@gmail.com"},
		CodeTTL: 10 * time.Minute, SessionTTL: time.Hour,
		CodeRequestsPerIPHour: 10, CodeRequestsPerEmailHour: 3, VerifyAttemptsPerCode: 3,
		VerifyPerIPHour: 30, SessionTurnCap: 2,
	}
}

func newSvc() (*Service, *fakeMail, *clock) {
	m, c := &fakeMail{}, &clock{time.Unix(1_700_000_000, 0)}
	return New(testConfig(), m, c.now), m, c
}

func TestHappyPath(t *testing.T) {
	s, m, _ := newSvc()
	if err := s.RequestCode(context.Background(), "  Alice@Example.com ", "1.1.1.1"); err != nil {
		t.Fatal(err)
	}
	if m.sent[0].to != "alice@example.com" || m.sent[0].subject != "Your sign-in code" {
		t.Errorf("sent = %+v", m.sent[0])
	}
	tok, err := s.Verify("alice@example.com", m.lastCode(t), "1.1.1.1")
	if err != nil {
		t.Fatal(err)
	}
	sess, ok := s.Session(tok)
	if !ok || sess.Email != "alice@example.com" {
		t.Fatalf("session = %+v %v", sess, ok)
	}
	s.Logout(tok)
	if _, ok := s.Session(tok); ok {
		t.Fatal("session survived logout")
	}
}

func TestExplicitAllowlist(t *testing.T) {
	s, m, _ := newSvc()
	if err := s.RequestCode(context.Background(), "friend@gmail.com", "ip"); err != nil {
		t.Fatal(err)
	}
	if len(m.sent) != 1 {
		t.Fatal("expected a send")
	}
}

func TestDisallowedEmailRejectedAndNoMail(t *testing.T) {
	s, m, _ := newSvc()
	for _, e := range []string{"eve@gmail.com", "eve@sub.example.com", "eve@example.com.evil.io"} {
		if err := s.RequestCode(context.Background(), e, "ip"); !errors.Is(err, ErrNotAllowed) {
			t.Errorf("%s: err = %v", e, err)
		}
	}
	for _, e := range []string{"", "not-an-email", "Eve <eve@example.com>"} {
		if err := s.RequestCode(context.Background(), e, "ip"); !errors.Is(err, ErrInvalidEmail) {
			t.Errorf("%q: err = %v", e, err)
		}
	}
	if len(m.sent) != 0 {
		t.Fatalf("sent %d emails to disallowed addresses", len(m.sent))
	}
	// A disallowed email can never redeem a code either.
	if _, err := s.Verify("eve@gmail.com", "000000", "ip"); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("verify err = %v", err)
	}
}

func TestCodeIsSingleUse(t *testing.T) {
	s, m, _ := newSvc()
	s.RequestCode(context.Background(), "a@example.com", "ip")
	code := m.lastCode(t)
	if _, err := s.Verify("a@example.com", code, "ip"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Verify("a@example.com", code, "ip"); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("reused code: err = %v", err)
	}
}

func TestCodeExpires(t *testing.T) {
	s, m, c := newSvc()
	s.RequestCode(context.Background(), "a@example.com", "ip")
	c.t = c.t.Add(10 * time.Minute)
	if _, err := s.Verify("a@example.com", m.lastCode(t), "ip"); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("expired code: err = %v", err)
	}
}

func TestNewCodeReplacesOld(t *testing.T) {
	s, m, _ := newSvc()
	s.RequestCode(context.Background(), "a@example.com", "ip")
	old := m.lastCode(t)
	s.RequestCode(context.Background(), "a@example.com", "ip")
	if m.lastCode(t) == old {
		t.Skip("random codes collided")
	}
	if _, err := s.Verify("a@example.com", old, "ip"); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("old code still valid: %v", err)
	}
}

func TestWrongGuessesBurnCode(t *testing.T) {
	s, m, _ := newSvc()
	s.RequestCode(context.Background(), "a@example.com", "ip")
	code := m.lastCode(t)
	wrong := "000000"
	if code == wrong {
		wrong = "111111"
	}
	for i := 0; i < 3; i++ {
		s.Verify("a@example.com", wrong, "ip")
	}
	if _, err := s.Verify("a@example.com", code, "ip"); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("code survived %d wrong guesses: %v", 3, err)
	}
}

func TestCodesStoredHashed(t *testing.T) {
	s, m, _ := newSvc()
	s.RequestCode(context.Background(), "a@example.com", "ip")
	code := m.lastCode(t)
	p := s.codes["a@example.com"]
	if string(p.mac) == code || len(p.mac) != 32 {
		t.Fatal("code not stored as a MAC")
	}
}

func TestRequestRateLimits(t *testing.T) {
	s, _, c := newSvc()
	for i := 0; i < 3; i++ {
		if err := s.RequestCode(context.Background(), "a@example.com", "ip1"); err != nil {
			t.Fatal(err)
		}
	}
	var rl *RateLimitedError
	if err := s.RequestCode(context.Background(), "a@example.com", "ip2"); !errors.As(err, &rl) {
		t.Fatalf("per-email limit: err = %v", err)
	}
	// Per-IP limit applies even to disallowed emails, so the endpoint can't be used to probe.
	for i := 0; i < 7; i++ {
		s.RequestCode(context.Background(), "x@gmail.com", "ip1")
	}
	if err := s.RequestCode(context.Background(), "b@example.com", "ip1"); !errors.As(err, &rl) {
		t.Fatalf("per-IP limit: err = %v", err)
	}
	c.t = c.t.Add(time.Hour)
	if err := s.RequestCode(context.Background(), "a@example.com", "ip1"); err != nil {
		t.Fatalf("after window: %v", err)
	}
}

func TestVerifyRateLimitPerIP(t *testing.T) {
	s, _, _ := newSvc()
	for i := 0; i < 30; i++ {
		s.Verify("a@example.com", "000000", "ip")
	}
	var rl *RateLimitedError
	if _, err := s.Verify("a@example.com", "000000", "ip"); !errors.As(err, &rl) {
		t.Fatalf("err = %v", err)
	}
}

func TestSendFailureDropsCode(t *testing.T) {
	s, m, _ := newSvc()
	m.fail = errors.New("smtp down")
	if err := s.RequestCode(context.Background(), "a@example.com", "ip"); !errors.Is(err, ErrSendFailed) {
		t.Fatalf("err = %v", err)
	}
	if len(s.codes) != 0 {
		t.Fatal("unsent code kept")
	}
}

func TestSessionExpiryAndTurnCap(t *testing.T) {
	s, m, c := newSvc()
	s.RequestCode(context.Background(), "a@example.com", "ip")
	tok, _ := s.Verify("a@example.com", m.lastCode(t), "ip")
	if !s.UseTurn(tok) || !s.UseTurn(tok) || s.UseTurn(tok) {
		t.Fatal("turn cap of 2 not enforced")
	}
	c.t = c.t.Add(time.Hour)
	if _, ok := s.Session(tok); ok {
		t.Fatal("expired session accepted")
	}
	if _, ok := s.Session("forged"); ok {
		t.Fatal("unknown token accepted")
	}
}

func TestEmailBodyIsMinimal(t *testing.T) {
	b := codeEmailBody("123456", 10*time.Minute)
	if !codeRe.MatchString(b) || regexp.MustCompile(`(?i)https?:|www\.`).MatchString(b) {
		t.Fatalf("body = %q", b)
	}
}
