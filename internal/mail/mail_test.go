package mail

import (
	"bufio"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// ---------- Compose ----------

func TestCompose_PlainTextOnly(t *testing.T) {
	raw, err := Compose(MessageOpts{
		From:    "sender@example.com",
		To:      []string{"rcpt@example.com"},
		Subject: "Hello",
		Body:    "plain body",
	})
	if err != nil {
		t.Fatalf("Compose() error = %v", err)
	}
	out := string(raw)
	if !strings.Contains(out, "Content-Type: text/plain") {
		t.Errorf("output missing text/plain content type:\n%s", out)
	}
	if strings.Contains(out, "text/html") {
		t.Errorf("plain-only message unexpectedly contains text/html:\n%s", out)
	}
	if !strings.Contains(out, "plain body") {
		t.Errorf("output missing body text:\n%s", out)
	}
}

func TestCompose_HTMLOnly(t *testing.T) {
	raw, err := Compose(MessageOpts{
		From:     "sender@example.com",
		To:       []string{"rcpt@example.com"},
		Subject:  "Hello",
		BodyHTML: "<p>html body</p>",
	})
	if err != nil {
		t.Fatalf("Compose() error = %v", err)
	}
	out := string(raw)
	if !strings.Contains(out, "Content-Type: text/html") {
		t.Errorf("output missing text/html content type:\n%s", out)
	}
	if strings.Contains(out, "text/plain") {
		t.Errorf("html-only message unexpectedly contains text/plain:\n%s", out)
	}
}

func TestCompose_PlainAndHTML(t *testing.T) {
	raw, err := Compose(MessageOpts{
		From:     "sender@example.com",
		To:       []string{"rcpt@example.com"},
		Subject:  "Hello",
		Body:     "plain body",
		BodyHTML: "<p>html body</p>",
	})
	if err != nil {
		t.Fatalf("Compose() error = %v", err)
	}
	out := string(raw)
	if !strings.Contains(out, "Content-Type: text/plain") {
		t.Errorf("output missing text/plain content type (multipart/alternative):\n%s", out)
	}
	if !strings.Contains(out, "Content-Type: text/html") {
		t.Errorf("output missing text/html content type (multipart/alternative):\n%s", out)
	}
}

func TestCompose_WithAttachment(t *testing.T) {
	dir := t.TempDir()
	attPath := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(attPath, []byte("attachment content"), 0600); err != nil {
		t.Fatalf("write attachment: %v", err)
	}

	raw, err := Compose(MessageOpts{
		From:        "sender@example.com",
		To:          []string{"rcpt@example.com"},
		Subject:     "Hello",
		Body:        "plain body",
		Attachments: []string{attPath},
	})
	if err != nil {
		t.Fatalf("Compose() error = %v", err)
	}
	out := string(raw)
	if !strings.Contains(out, "notes.txt") {
		t.Errorf("output missing attachment filename:\n%s", out)
	}
}

func TestCompose_CcAndReplyTo(t *testing.T) {
	raw, err := Compose(MessageOpts{
		From:    "sender@example.com",
		To:      []string{"rcpt@example.com"},
		Cc:      []string{"cc1@example.com", "cc2@example.com"},
		ReplyTo: "replyto@example.com",
		Subject: "Hello",
		Body:    "plain body",
	})
	if err != nil {
		t.Fatalf("Compose() error = %v", err)
	}
	out := string(raw)
	if !strings.Contains(out, "Cc: cc1@example.com, cc2@example.com") {
		t.Errorf("output missing Cc header:\n%s", out)
	}
	if !strings.Contains(out, "Reply-To: replyto@example.com") {
		t.Errorf("output missing Reply-To header:\n%s", out)
	}
}

// ---------- key generation helpers ----------

func genRSAKeyPKCS1PEM(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	der := x509.MarshalPKCS1PrivateKey(key)
	block := &pem.Block{Type: "RSA PRIVATE KEY", Bytes: der}
	dir := t.TempDir()
	path := filepath.Join(dir, "pkcs1.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0600); err != nil {
		t.Fatalf("write pkcs1 key: %v", err)
	}
	return path
}

func genRSAKeyPKCS8PEM(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal pkcs8 key: %v", err)
	}
	block := &pem.Block{Type: "PRIVATE KEY", Bytes: der}
	dir := t.TempDir()
	path := filepath.Join(dir, "pkcs8.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0600); err != nil {
		t.Fatalf("write pkcs8 key: %v", err)
	}
	return path
}

// ---------- loadPrivateKey / Sign ----------

func TestLoadPrivateKey_PKCS1(t *testing.T) {
	path := genRSAKeyPKCS1PEM(t)
	signer, err := loadPrivateKey(path)
	if err != nil {
		t.Fatalf("loadPrivateKey() error = %v", err)
	}
	if signer == nil {
		t.Fatal("loadPrivateKey() returned nil signer")
	}
}

func TestLoadPrivateKey_PKCS8(t *testing.T) {
	path := genRSAKeyPKCS8PEM(t)
	signer, err := loadPrivateKey(path)
	if err != nil {
		t.Fatalf("loadPrivateKey() error = %v", err)
	}
	if signer == nil {
		t.Fatal("loadPrivateKey() returned nil signer")
	}
}

func TestLoadPrivateKey_NonexistentFile(t *testing.T) {
	dir := t.TempDir()
	_, err := loadPrivateKey(filepath.Join(dir, "missing.pem"))
	if err == nil {
		t.Fatal("loadPrivateKey() error = nil, want error for nonexistent file")
	}
}

func TestLoadPrivateKey_GarbageNonPEM(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "garbage.pem")
	if err := os.WriteFile(path, []byte("this is not a PEM file at all"), 0600); err != nil {
		t.Fatalf("write garbage file: %v", err)
	}
	_, err := loadPrivateKey(path)
	if err == nil {
		t.Fatal("loadPrivateKey() error = nil, want error for non-PEM content")
	}
	if !strings.Contains(err.Error(), "no PEM block") {
		t.Errorf("error = %v, want mention of missing PEM block", err)
	}
}

func TestLoadPrivateKey_PEMButNeitherPKCS1NorPKCS8(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "malformed.pem")
	block := &pem.Block{Type: "PRIVATE KEY", Bytes: []byte("not a valid DER-encoded key")}
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0600); err != nil {
		t.Fatalf("write malformed key: %v", err)
	}
	_, err := loadPrivateKey(path)
	if err == nil {
		t.Fatal("loadPrivateKey() error = nil, want error for malformed DER")
	}
	if !strings.Contains(err.Error(), "parse private key") {
		t.Errorf("error = %v, want mention of parse failure", err)
	}
}

func TestSign(t *testing.T) {
	for _, tc := range []struct {
		name    string
		keyPath func(t *testing.T) string
	}{
		{"pkcs1", genRSAKeyPKCS1PEM},
		{"pkcs8", genRSAKeyPKCS8PEM},
	} {
		t.Run(tc.name, func(t *testing.T) {
			keyPath := tc.keyPath(t)

			raw, err := Compose(MessageOpts{
				From:    "sender@example.com",
				To:      []string{"rcpt@example.com"},
				Subject: "Hello",
				Body:    "plain body",
			})
			if err != nil {
				t.Fatalf("Compose() error = %v", err)
			}

			signed, err := Sign(raw, "example.com", "mail", keyPath)
			if err != nil {
				t.Fatalf("Sign() error = %v", err)
			}
			if !strings.Contains(string(signed), "DKIM-Signature:") {
				t.Errorf("signed output missing DKIM-Signature header:\n%s", string(signed))
			}
		})
	}
}

func TestSign_LoadKeyError(t *testing.T) {
	raw, err := Compose(MessageOpts{
		From:    "sender@example.com",
		To:      []string{"rcpt@example.com"},
		Subject: "Hello",
		Body:    "plain body",
	})
	if err != nil {
		t.Fatalf("Compose() error = %v", err)
	}

	dir := t.TempDir()
	_, err = Sign(raw, "example.com", "mail", filepath.Join(dir, "missing.pem"))
	if err == nil {
		t.Fatal("Sign() error = nil, want error for missing key file")
	}
}

// ---------- fake SMTP server for Deliver / deliverStartTLS / sendData ----------

// fakeSMTPServer is a minimal hand-rolled SMTP server sufficient to drive
// one transaction through net/smtp's client (EHLO/HELO, MAIL FROM, RCPT TO,
// DATA + terminator, QUIT). No AUTH support: tests must call Deliver with
// SMTPDeliverConfig.Pass == "" so sendData skips client.Auth.
type fakeSMTPServer struct {
	ln net.Listener

	mu        sync.Mutex
	failRcpt  bool
	failData  bool
	dataLines []string
}

func startFakeSMTPServer(t *testing.T, failRcpt, failData bool) *fakeSMTPServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &fakeSMTPServer{ln: ln, failRcpt: failRcpt, failData: failData}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		conn, err := ln.Accept()
		if err != nil {
			// listener closed by test cleanup
			return
		}
		s.handleConn(conn)
	}()

	t.Cleanup(func() {
		ln.Close()
		wg.Wait()
	})

	return s
}

func (s *fakeSMTPServer) addr() string {
	return s.ln.Addr().String()
}

func (s *fakeSMTPServer) handleConn(conn net.Conn) {
	defer conn.Close()

	r := bufio.NewReader(conn)
	w := bufio.NewWriter(conn)
	writeLine := func(format string, args ...interface{}) {
		fmt.Fprintf(w, format+"\r\n", args...)
		w.Flush()
	}

	writeLine("220 fake.smtp ESMTP ready")

	inData := false
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")

		if inData {
			if line == "." {
				inData = false
				if s.failData {
					writeLine("554 transaction failed")
				} else {
					writeLine("250 OK: message accepted")
				}
				continue
			}
			s.mu.Lock()
			s.dataLines = append(s.dataLines, line)
			s.mu.Unlock()
			continue
		}

		upper := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(upper, "EHLO"), strings.HasPrefix(upper, "HELO"):
			writeLine("250 fake.smtp greets you")
		case strings.HasPrefix(upper, "MAIL FROM"):
			writeLine("250 OK")
		case strings.HasPrefix(upper, "RCPT TO"):
			if s.failRcpt {
				writeLine("550 no such user")
			} else {
				writeLine("250 OK")
			}
		case strings.HasPrefix(upper, "DATA"):
			if s.failRcpt {
				// shouldn't normally get here since client aborts after RCPT failure,
				// but respond sanely just in case.
				writeLine("503 bad sequence")
				continue
			}
			inData = true
			writeLine("354 End data with <CR><LF>.<CR><LF>")
		case strings.HasPrefix(upper, "QUIT"):
			writeLine("221 Bye")
			return
		case strings.HasPrefix(upper, "RSET"):
			writeLine("250 OK")
		default:
			writeLine("500 unrecognized command")
		}
	}
}

func TestDeliver_Success(t *testing.T) {
	srv := startFakeSMTPServer(t, false, false)
	host, portStr, err := net.SplitHostPort(srv.addr())
	if err != nil {
		t.Fatalf("split host/port: %v", err)
	}
	var port int
	fmt.Sscanf(portStr, "%d", &port)

	raw := []byte("Subject: test\r\n\r\nbody\r\n")
	resp, err := Deliver("sender@example.com", []string{"rcpt@example.com"}, raw, SMTPDeliverConfig{
		Host:     host,
		Port:     port,
		User:     "",
		Pass:     "", // no auth: exercises the `if pass != ""` skip in sendData
		UseTLS:   false,
		StartTLS: false,
	})
	if err != nil {
		t.Fatalf("Deliver() error = %v", err)
	}
	// NOTE: sendData's `resp` variable (internal/mail/mail.go) is declared as
	// resp := "" and returned unmodified after wc.Close() — it never captures
	// the server's actual 250 response text. This assertion documents the
	// current (likely unintended) behavior rather than a real response body.
	if resp != "" {
		t.Errorf("Deliver() resp = %q, want empty string per current sendData implementation (see NOTE)", resp)
	}
}

func TestDeliver_FailureOnRcpt(t *testing.T) {
	srv := startFakeSMTPServer(t, true, false)
	host, portStr, err := net.SplitHostPort(srv.addr())
	if err != nil {
		t.Fatalf("split host/port: %v", err)
	}
	var port int
	fmt.Sscanf(portStr, "%d", &port)

	raw := []byte("Subject: test\r\n\r\nbody\r\n")
	_, err = Deliver("sender@example.com", []string{"rcpt@example.com"}, raw, SMTPDeliverConfig{
		Host:     host,
		Port:     port,
		Pass:     "",
		UseTLS:   false,
		StartTLS: false,
	})
	if err == nil {
		t.Fatal("Deliver() error = nil, want error when server rejects RCPT TO")
	}
}

func TestDeliver_FailureOnData(t *testing.T) {
	srv := startFakeSMTPServer(t, false, true)
	host, portStr, err := net.SplitHostPort(srv.addr())
	if err != nil {
		t.Fatalf("split host/port: %v", err)
	}
	var port int
	fmt.Sscanf(portStr, "%d", &port)

	raw := []byte("Subject: test\r\n\r\nbody\r\n")
	_, err = Deliver("sender@example.com", []string{"rcpt@example.com"}, raw, SMTPDeliverConfig{
		Host:     host,
		Port:     port,
		Pass:     "",
		UseTLS:   false,
		StartTLS: false,
	})
	if err == nil {
		t.Fatal("Deliver() error = nil, want error when server rejects DATA")
	}
}

// sanity: ensure our fake server actually received the raw MIME bytes on
// the success path, giving the success test real signal beyond "no error".
func TestDeliver_Success_ServerReceivedData(t *testing.T) {
	srv := startFakeSMTPServer(t, false, false)
	host, portStr, err := net.SplitHostPort(srv.addr())
	if err != nil {
		t.Fatalf("split host/port: %v", err)
	}
	var port int
	fmt.Sscanf(portStr, "%d", &port)

	raw := []byte("Subject: test\r\nX-Marker: unique-marker-xyz\r\n\r\nbody\r\n")
	_, err = Deliver("sender@example.com", []string{"rcpt@example.com"}, raw, SMTPDeliverConfig{
		Host:     host,
		Port:     port,
		Pass:     "",
		UseTLS:   false,
		StartTLS: false,
	})
	if err != nil {
		t.Fatalf("Deliver() error = %v", err)
	}

	srv.mu.Lock()
	got := strings.Join(srv.dataLines, "\n")
	srv.mu.Unlock()
	if !strings.Contains(got, "unique-marker-xyz") {
		t.Errorf("server did not receive expected marker in DATA payload; got:\n%s", got)
	}
}
