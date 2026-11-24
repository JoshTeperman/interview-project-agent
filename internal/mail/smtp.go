// Package mail sends plain-text magic-code emails over SMTP with STARTTLS (S-D10).
package mail

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// Sender sends one plain-text email.
type Sender interface {
	Send(ctx context.Context, to, subject, body string) error
}

// SMTP sends mail through an SMTP server that must support STARTTLS.
type SMTP struct {
	Host     string
	Port     int
	User     string
	Pass     string
	FromName string

	// TLSConfig overrides the STARTTLS config (tests only). Nil means verify Host.
	TLSConfig *tls.Config
	// Timeout bounds the whole exchange. Zero means 30 seconds.
	Timeout time.Duration
}

// ErrNoStartTLS is returned when the server does not offer STARTTLS.
// Credentials are never sent over an unencrypted connection.
var ErrNoStartTLS = errors.New("smtp server does not support STARTTLS")

// Send delivers one message. Errors never include the password or the body.
func (s *SMTP) Send(ctx context.Context, to, subject, body string) error {
	timeout := s.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	addr := net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("smtp dial: %w", err)
	}
	deadline, _ := ctx.Deadline()
	conn.SetDeadline(deadline)

	c, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("smtp greeting: %w", err)
	}
	defer c.Close()

	if ok, _ := c.Extension("STARTTLS"); !ok {
		return ErrNoStartTLS
	}
	tlsCfg := s.TLSConfig
	if tlsCfg == nil {
		tlsCfg = &tls.Config{ServerName: s.Host, MinVersion: tls.VersionTLS12}
	}
	if err := c.StartTLS(tlsCfg); err != nil {
		return fmt.Errorf("smtp starttls: %w", err)
	}
	if err := c.Auth(smtp.PlainAuth("", s.User, s.Pass, s.Host)); err != nil {
		return errors.New("smtp auth failed")
	}
	if err := c.Mail(s.User); err != nil {
		return fmt.Errorf("smtp MAIL FROM: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("smtp RCPT TO: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp DATA: %w", err)
	}
	if _, err := w.Write(s.message(to, subject, body, time.Now())); err != nil {
		return fmt.Errorf("smtp write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp end of data: %w", err)
	}
	return c.Quit()
}

// message builds a minimal RFC 5322 plain-text message with CRLF line endings.
func (s *SMTP) message(to, subject, body string, now time.Time) []byte {
	from := mail.Address{Name: s.FromName, Address: s.User}
	domain := s.User[strings.LastIndexByte(s.User, '@')+1:]
	id := make([]byte, 12)
	rand.Read(id)

	var b strings.Builder
	header := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	header("From", from.String())
	header("To", to)
	header("Subject", mime.QEncoding.Encode("utf-8", subject))
	header("Date", now.UTC().Format(time.RFC1123Z))
	header("Message-ID", "<"+hex.EncodeToString(id)+"@"+domain+">")
	header("MIME-Version", "1.0")
	header("Content-Type", `text/plain; charset="utf-8"`)
	header("Content-Transfer-Encoding", "8bit")
	b.WriteString("\r\n")
	b.WriteString(strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\n", "\r\n"))
	return []byte(b.String())
}
