package mail

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"errors"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"
)

// fakeServer is a minimal SMTP server. It records the AUTH credentials and the DATA payload.
type fakeServer struct {
	ln       net.Listener
	startTLS bool
	tlsCfg   *tls.Config
	auth     chan string
	data     chan string
}

func newFakeServer(t *testing.T, startTLS bool) (*fakeServer, *x509.CertPool) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "127.0.0.1"},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:   time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(cert)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	s := &fakeServer{ln: ln, startTLS: startTLS,
		tlsCfg: &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}},
		auth:   make(chan string, 1), data: make(chan string, 1)}
	go s.serve()
	return s, pool
}

func (s *fakeServer) port() int { return s.ln.Addr().(*net.TCPAddr).Port }

func (s *fakeServer) serve() {
	conn, err := s.ln.Accept()
	if err != nil {
		return
	}
	defer conn.Close()
	r, w := bufio.NewReader(conn), conn
	reply := func(l string) { w.Write([]byte(l + "\r\n")) }
	reply("220 fake ESMTP")
	tlsOn := false
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.TrimSpace(line)
		switch up := strings.ToUpper(cmd); {
		case strings.HasPrefix(up, "EHLO"):
			if s.startTLS && !tlsOn {
				reply("250-fake")
				reply("250 STARTTLS")
			} else {
				reply("250-fake")
				reply("250 AUTH PLAIN")
			}
		case up == "STARTTLS":
			reply("220 go ahead")
			tc := tls.Server(conn, s.tlsCfg)
			if tc.Handshake() != nil {
				return
			}
			conn, tlsOn = tc, true
			r, w = bufio.NewReader(tc), tc
		case strings.HasPrefix(up, "AUTH PLAIN"):
			raw, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(cmd[len("AUTH PLAIN"):]))
			s.auth <- string(raw)
			reply("235 ok")
		case strings.HasPrefix(up, "MAIL"), strings.HasPrefix(up, "RCPT"):
			reply("250 ok")
		case up == "DATA":
			reply("354 send")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil || l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			s.data <- b.String()
			reply("250 queued")
		case up == "QUIT":
			reply("221 bye")
			return
		default:
			reply("502 unknown")
		}
	}
}

func TestSendOverStartTLS(t *testing.T) {
	srv, pool := newFakeServer(t, true)
	m := &SMTP{Host: "127.0.0.1", Port: srv.port(), User: "agent@example.com", Pass: "pw-secret",
		FromName: "Interview Agent", TLSConfig: &tls.Config{RootCAs: pool, ServerName: "127.0.0.1"}}
	if err := m.Send(context.Background(), "rev@example.org", "Your sign-in code", "Code: 123456\nBye"); err != nil {
		t.Fatal(err)
	}
	if got := <-srv.auth; got != "\x00agent@example.com\x00pw-secret" {
		t.Errorf("auth = %q", got)
	}
	data := <-srv.data
	for _, want := range []string{
		"From: \"Interview Agent\" <agent@example.com>\r\n", "To: rev@example.org\r\n",
		"Subject: Your sign-in code\r\n", "Content-Type: text/plain; charset=\"utf-8\"\r\n",
		"\r\n\r\nCode: 123456\r\nBye",
	} {
		if !strings.Contains(data, want) {
			t.Errorf("message missing %q:\n%s", want, data)
		}
	}
	if strings.Contains(strings.ToLower(data), "http") {
		t.Error("message must not contain links")
	}
}

func TestRefusesWithoutStartTLS(t *testing.T) {
	srv, _ := newFakeServer(t, false)
	m := &SMTP{Host: "127.0.0.1", Port: srv.port(), User: "a@example.com", Pass: "pw-secret"}
	err := m.Send(context.Background(), "b@example.org", "s", "b")
	if !errors.Is(err, ErrNoStartTLS) {
		t.Fatalf("err = %v, want ErrNoStartTLS", err)
	}
	select {
	case <-srv.auth:
		t.Fatal("credentials were sent without TLS")
	default:
	}
}

func TestDialErrorHidesSecrets(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	m := &SMTP{Host: "127.0.0.1", Port: port, User: "a@example.com", Pass: "pw-secret", Timeout: time.Second}
	err := m.Send(context.Background(), "b@example.org", "s", "body-123456")
	if err == nil || strings.Contains(err.Error(), "pw-secret") || strings.Contains(err.Error(), "123456") {
		t.Fatalf("err = %v", err)
	}
}
