package read

import (
	"bufio"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

type fakeIMAPServer struct {
	ln net.Listener

	mu           sync.Mutex
	failLogin    bool
	failSelect   bool
	noIDLE       bool
	noIMAP4rev2  bool
	msgCount     uint32
	hasMailbox   bool
	dataLines    []string
	selectedMbox string
}

func generateSelfSignedCert(t *testing.T) (certFile, keyFile string) {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
		DNSNames:     []string{"localhost", "127.0.0.1"},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})

	dir := t.TempDir()
	certFile = dir + "/cert.pem"
	keyFile = dir + "/key.pem"
	os.WriteFile(certFile, certPEM, 0600)
	os.WriteFile(keyFile, keyPEM, 0600)
	return
}

func startFakeIMAPServer(t *testing.T, opts ...func(*fakeIMAPServer)) *fakeIMAPServer {
	t.Helper()

	certFile, keyFile := generateSelfSignedCert(t)
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		t.Fatalf("load cert: %v", err)
	}

	tlsCfg := &tls.Config{Certificates: []tls.Certificate{cert}}

	ln, err := tls.Listen("tcp", "127.0.0.1:0", tlsCfg)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &fakeIMAPServer{ln: ln}

	for _, o := range opts {
		o(s)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		conn, err := ln.Accept()
		if err != nil {
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

func (s *fakeIMAPServer) addr() string {
	return s.ln.Addr().String()
}

func dialFakeIMAP(t *testing.T, addr string) (*Client, error) {
	t.Helper()
	tlsCfg := &tls.Config{InsecureSkipVerify: true}
	c, err := imapclient.DialTLS(addr, &imapclient.Options{TLSConfig: tlsCfg})
	if err != nil {
		return nil, err
	}
	return &Client{client: c}, nil
}

func withAuthFailure(s *fakeIMAPServer) { s.failLogin = true }
func withSelectFailure(s *fakeIMAPServer) { s.failSelect = true }
func withoutIDLE(s *fakeIMAPServer) { s.noIDLE = true; s.noIMAP4rev2 = true }
func withMessages(n uint32) func(*fakeIMAPServer) {
	return func(s *fakeIMAPServer) { s.msgCount = n }
}

func (s *fakeIMAPServer) handleConn(conn net.Conn) {
	defer conn.Close()

	r := bufio.NewReader(conn)
	w := bufio.NewWriter(conn)
	writeLine := func(f string, a ...any) {
		fmt.Fprintf(w, f+"\r\n", a...)
		w.Flush()
	}

	writeLine("* OK fake IMAP ready")

	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			continue
		}

		parts := strings.SplitN(line, " ", 3)
		tag := parts[0]

		rest := ""
		if len(parts) >= 3 {
			rest = parts[2]
		}

		cmd := parts[1]
		if len(parts) == 2 {
			cmd = parts[1]
		} else {
			cmd = parts[1]
		}
		_ = rest
		upper := strings.ToUpper(cmd)

		switch upper {
		case "CAPABILITY":
			if !s.noIMAP4rev2 {
				writeLine("* CAPABILITY IMAP4rev2")
			}
			if !s.noIDLE {
				writeLine("* CAPABILITY IDLE")
			}
			writeLine(tag + " OK CAPABILITY done")

		case "LOGIN":
			if s.failLogin {
				writeLine(tag + " NO LOGIN failed")
			} else {
				writeLine(tag + " OK LOGIN successful")
			}

		case "LIST":
			writeLine("* LIST (\\HasNoChildren) \"/\" \"INBOX\"")
			writeLine("* LIST (\\HasNoChildren) \"/\" \"Sent\"")
			writeLine(tag + " OK LIST done")

		case "SELECT", "EXAMINE":
			mbox := strings.Trim(parts[2], "\"")
			s.mu.Lock()
			s.hasMailbox = true
			s.selectedMbox = mbox
			s.mu.Unlock()
			if s.failSelect {
				writeLine(tag + " NO SELECT failed")
			} else {
				writeLine("* FLAGS (\\Seen \\Answered \\Flagged)")
				writeLine(fmt.Sprintf("* %d EXISTS", s.msgCount))
				writeLine("* OK [UIDVALIDITY 1]")
				writeLine(tag + " OK SELECT done")
			}

		case "STATUS":
			s.mu.Lock()
			cnt := s.msgCount
			s.mu.Unlock()
			m := strings.SplitN(line, "\"", 3)
			mbox := "INBOX"
			if len(m) >= 2 {
				mbox = m[1]
			}
			writeLine(fmt.Sprintf("* STATUS %s (MESSAGES %d)", mbox, cnt))
			writeLine(tag + " OK STATUS done")

		case "IDLE":
			writeLine("+ idling")
			s.mu.Lock()
			cnt := s.msgCount
			s.msgCount += 1
			s.mu.Unlock()
			writeLine(fmt.Sprintf("* %d EXISTS", cnt+1))
			writeLine(tag + " OK IDLE terminated")

		case "LOGOUT":
			writeLine("* BYE")
			writeLine(tag + " OK LOGOUT done")
			return

		default:
			if strings.HasPrefix(line, tag+" UID SEARCH") {
				s.handleSearch(w, tag)
			} else if strings.HasPrefix(line, tag+" UID FETCH") {
				s.handleFetch(w, tag)
			} else {
				writeLine(tag + " BAD unknown command: " + cmd)
			}
		}
	}
}

func (s *fakeIMAPServer) handleSearch(w *bufio.Writer, tag string) {
	s.mu.Lock()
	cnt := s.msgCount
	s.mu.Unlock()
	if cnt > 0 {
		var nums []string
		for i := uint32(1); i <= cnt; i++ {
			nums = append(nums, fmt.Sprintf("%d", i+100))
		}
		w.WriteString(fmt.Sprintf("* SEARCH %s\r\n", strings.Join(nums, " ")))
	} else {
		w.WriteString("* SEARCH\r\n")
	}
	w.WriteString(tag + " OK SEARCH done\r\n")
	w.Flush()
}

func (s *fakeIMAPServer) handleFetch(w *bufio.Writer, tag string) {
	s.mu.Lock()
	cnt := s.msgCount
	s.mu.Unlock()
	for i := uint32(1); i <= cnt; i++ {
		uid := i + 100
		w.WriteString(fmt.Sprintf("* %d FETCH (UID %d FLAGS ()", i, uid))
		w.WriteString(fmt.Sprintf(" ENVELOPE (\"\" \"subject %d\" NIL NIL NIL NIL NIL NIL NIL NIL)", uid))
		w.WriteString(fmt.Sprintf(" INTERNALDATE \"01-Jan-2026 00:00:00 +0000\""))
		w.WriteString(fmt.Sprintf(" BODY[] {%d}\r\n", len("body data\r\n")))
		w.WriteString("body data\r\n)\r\n")
	}
	w.WriteString(tag + " OK FETCH done\r\n")
	w.Flush()
}

func TestDialTLS_Connect(t *testing.T) {
	srv := startFakeIMAPServer(t)
	client, err := dialFakeIMAP(t, srv.addr())
	if err != nil {
		t.Fatalf("dialFakeIMAP() error = %v", err)
	}
	defer client.Close()

	if err := client.Login("user", "pass"); err != nil {
		t.Fatalf("Login() error = %v", err)
	}
}

func TestLogin_AuthFailure(t *testing.T) {
	srv := startFakeIMAPServer(t, withAuthFailure)

	client, err := dialFakeIMAP(t, srv.addr())
	if err != nil {
		t.Fatalf("dialFakeIMAP() error = %v", err)
	}
	defer client.Close()

	err = client.Login("user", "wrong")
	if err == nil {
		t.Fatal("Login() error = nil, want auth failure")
	}
}

func TestListMailboxes(t *testing.T) {
	srv := startFakeIMAPServer(t)

	client, err := dialFakeIMAP(t, srv.addr())
	if err != nil {
		t.Fatalf("dialFakeIMAP() error = %v", err)
	}
	defer client.Close()

	if err := client.Login("user", "pass"); err != nil {
		t.Fatalf("Login() error = %v", err)
	}

	mboxes, err := client.ListMailboxes()
	if err != nil {
		t.Fatalf("ListMailboxes() error = %v", err)
	}
	if len(mboxes) < 2 {
		t.Fatalf("ListMailboxes: got %d mailboxes, want >= 2", len(mboxes))
	}
}

func TestSelect_Success(t *testing.T) {
	srv := startFakeIMAPServer(t, withMessages(3))

	client, err := dialFakeIMAP(t, srv.addr())
	if err != nil {
		t.Fatalf("dialFakeIMAP() error = %v", err)
	}
	defer client.Close()

	if err := client.Login("user", "pass"); err != nil {
		t.Fatalf("Login() error = %v", err)
	}

	data, err := client.Select("INBOX")
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	if data.NumMessages != 3 {
		t.Errorf("NumMessages = %d, want 3", data.NumMessages)
	}
}

func TestSelect_Failure(t *testing.T) {
	srv := startFakeIMAPServer(t, withSelectFailure)

	client, err := dialFakeIMAP(t, srv.addr())
	if err != nil {
		t.Fatalf("dialFakeIMAP() error = %v", err)
	}
	defer client.Close()

	if err := client.Login("user", "pass"); err != nil {
		t.Fatalf("Login() error = %v", err)
	}

	_, err = client.Select("INBOX")
	if err == nil {
		t.Fatal("Select() error = nil, want select failure")
	}
}

func TestSearchAndFetch(t *testing.T) {
	srv := startFakeIMAPServer(t, withMessages(3))

	client, err := dialFakeIMAP(t, srv.addr())
	if err != nil {
		t.Fatalf("dialFakeIMAP() error = %v", err)
	}
	defer client.Close()

	if err := client.Login("user", "pass"); err != nil {
		t.Fatalf("Login() error = %v", err)
	}

	if _, err := client.Select("INBOX"); err != nil {
		t.Fatalf("Select() error = %v", err)
	}

	data, err := client.Search(&imap.SearchCriteria{})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}

	uidSet, ok := data.All.(imap.UIDSet)
	if !ok {
		t.Fatal("Search result is not a UIDSet")
	}
	uids, ok := uidSet.Nums()
	if !ok {
		t.Fatal("UIDSet.Nums() failed")
	}
	if len(uids) != 3 {
		t.Fatalf("Search returned %d UIDs, want 3", len(uids))
	}

	msgs, err := client.FetchMessages(uids)
	if err != nil {
		t.Fatalf("FetchMessages() error = %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("FetchMessages returned %d messages, want 3", len(msgs))
	}
}

func TestSearch_Empty(t *testing.T) {
	srv := startFakeIMAPServer(t, withMessages(0))

	client, err := dialFakeIMAP(t, srv.addr())
	if err != nil {
		t.Fatalf("dialFakeIMAP() error = %v", err)
	}
	defer client.Close()

	if err := client.Login("user", "pass"); err != nil {
		t.Fatalf("Login() error = %v", err)
	}

	if _, err := client.Select("INBOX"); err != nil {
		t.Fatalf("Select() error = %v", err)
	}

	data, err := client.Search(&imap.SearchCriteria{})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	uidSet, ok := data.All.(imap.UIDSet)
	if !ok {
		t.Fatal("Search result is not a UIDSet")
	}
	uids, ok := uidSet.Nums()
	if ok && len(uids) > 0 {
		t.Errorf("expected empty search, got %d UIDs", len(uids))
	}
}

func TestFetchMessages_Empty(t *testing.T) {
	srv := startFakeIMAPServer(t)

	client, err := dialFakeIMAP(t, srv.addr())
	if err != nil {
		t.Fatalf("dialFakeIMAP() error = %v", err)
	}
	defer client.Close()

	client.Login("user", "pass")

	msgs, err := client.FetchMessages(nil)
	if err != nil {
		t.Fatalf("FetchMessages() error = %v", err)
	}
	if msgs != nil {
		t.Errorf("FetchMessages(nil) = %v, want nil", msgs)
	}
}

func TestDialTLS_Unreachable(t *testing.T) {
	_, err := DialTLS("127.0.0.1", 19999)
	if err == nil {
		t.Fatal("DialTLS(unreachable) error = nil")
	}
}

func TestHasCapIDLE(t *testing.T) {
	srv := startFakeIMAPServer(t)

	client, err := dialFakeIMAP(t, srv.addr())
	if err != nil {
		t.Fatalf("dialFakeIMAP() error = %v", err)
	}
	defer client.Close()

	client.Login("user", "pass")

	if !hasCapIDLE(client.client) {
		t.Error("hasCapIDLE() = false, want true (server supports IDLE)")
	}
}

func TestHasCapIDLE_NoIDLE(t *testing.T) {
	srv := startFakeIMAPServer(t, withoutIDLE)

	client, err := dialFakeIMAP(t, srv.addr())
	if err != nil {
		t.Fatalf("dialFakeIMAP() error = %v", err)
	}
	defer client.Close()

	client.Login("user", "pass")

	if hasCapIDLE(client.client) {
		t.Error("hasCapIDLE() = true, want false (server has no IDLE capability)")
	}
}

func TestClient_Logout(t *testing.T) {
	srv := startFakeIMAPServer(t)

	client, err := dialFakeIMAP(t, srv.addr())
	if err != nil {
		t.Fatalf("dialFakeIMAP() error = %v", err)
	}

	client.Login("user", "pass")

	if err := client.Logout(); err != nil {
		t.Fatalf("Logout() error = %v", err)
	}
}

func TestFetchMessages_FullData(t *testing.T) {
	srv := startFakeIMAPServer(t, withMessages(1))

	client, err := dialFakeIMAP(t, srv.addr())
	if err != nil {
		t.Fatalf("dialFakeIMAP() error = %v", err)
	}
	defer client.Close()

	if err := client.Login("user", "pass"); err != nil {
		t.Fatalf("Login() error = %v", err)
	}

	if _, err := client.Select("INBOX"); err != nil {
		t.Fatalf("Select() error = %v", err)
	}

	data, _ := client.Search(&imap.SearchCriteria{})
	uidSet := data.All.(imap.UIDSet)
	uids, _ := uidSet.Nums()

	msgs, err := client.FetchMessages(uids)
	if err != nil {
		t.Fatalf("FetchMessages() error = %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("FetchMessages returned %d messages", len(msgs))
	}
	msg := msgs[0]
	if msg.Envelope == nil {
		t.Fatal("Envelope is nil")
	}
	if msg.Envelope.Subject != "subject 101" {
		t.Errorf("Subject = %q", msg.Envelope.Subject)
	}
	if len(msg.BodyData) == 0 {
		t.Error("BodyData is empty")
	}
}

func TestIMAPClientClose(t *testing.T) {
	srv := startFakeIMAPServer(t)

	client, err := dialFakeIMAP(t, srv.addr())
	if err != nil {
		t.Fatalf("dialFakeIMAP() error = %v", err)
	}

	client.Login("user", "pass")

	if err := client.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}
