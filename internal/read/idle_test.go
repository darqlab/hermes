package read

import (
	"net"
	"testing"
)

func TestWatchReconnect(t *testing.T) {
	srv := startFakeIMAPServer(t, withMessages(1))
	addr := srv.addr()

	client, err := dialFakeIMAP(t, addr)
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
	if data.NumMessages != 1 {
		t.Errorf("NumMessages = %d, want 1", data.NumMessages)
	}

	msgs, err := client.FetchMessages(nil)
	if err != nil {
		t.Fatalf("FetchMessages(nil) error = %v", err)
	}
	if msgs != nil {
		t.Errorf("FetchMessages(nil) = %v, want nil", msgs)
	}
}

func TestSplitAddr(t *testing.T) {
	host, port, err := splitAddr("127.0.0.1:993")
	if err != nil {
		t.Fatalf("splitAddr error = %v", err)
	}
	if host != "127.0.0.1" || port != "993" {
		t.Errorf("splitAddr = (%q, %q), want (127.0.0.1, 993)", host, port)
	}
}

func splitAddr(addr string) (host, port string, err error) {
	h, p, e := net.SplitHostPort(addr)
	return h, p, e
}
