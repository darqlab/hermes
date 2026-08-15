package mail

import (
	"net/smtp"
	"testing"
)

func TestLoginAuth_StartOverTLS(t *testing.T) {
	a := LoginAuth("alice", "hunter2", "smtp.office365.com")

	proto, resp, err := a.Start(&smtp.ServerInfo{Name: "smtp.office365.com", TLS: true})
	if err != nil {
		t.Fatalf("Start() error = %v, want nil", err)
	}
	if proto != "LOGIN" {
		t.Errorf("Start() proto = %q, want LOGIN", proto)
	}
	if len(resp) != 0 {
		t.Errorf("Start() resp = %q, want empty initial response", resp)
	}
}

func TestLoginAuth_RejectsCleartextConnection(t *testing.T) {
	a := LoginAuth("alice", "hunter2", "smtp.example.com")

	if _, _, err := a.Start(&smtp.ServerInfo{Name: "smtp.example.com", TLS: false}); err == nil {
		t.Fatal("Start() error = nil, want refusal to send credentials over an unencrypted connection")
	}
}

func TestLoginAuth_RejectsHostMismatch(t *testing.T) {
	a := LoginAuth("alice", "hunter2", "smtp.example.com")

	if _, _, err := a.Start(&smtp.ServerInfo{Name: "evil.example.net", TLS: true}); err == nil {
		t.Fatal("Start() error = nil, want refusal when the server name does not match the configured host")
	}
}

func TestLoginAuth_NextExchange(t *testing.T) {
	a := LoginAuth("alice", "hunter2", "smtp.example.com")
	if _, _, err := a.Start(&smtp.ServerInfo{Name: "smtp.example.com", TLS: true}); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	tests := []struct {
		challenge string
		want      string
	}{
		{"Username:", "alice"},
		{"username:", "alice"},
		{"USERNAME", "alice"},
		{"Password:", "hunter2"},
		{"password", "hunter2"},
	}
	for _, tt := range tests {
		got, err := a.Next([]byte(tt.challenge), true)
		if err != nil {
			t.Fatalf("Next(%q) error = %v", tt.challenge, err)
		}
		if string(got) != tt.want {
			t.Errorf("Next(%q) = %q, want %q", tt.challenge, got, tt.want)
		}
	}
}

func TestLoginAuth_NextUnknownChallenge(t *testing.T) {
	a := LoginAuth("alice", "hunter2", "smtp.example.com")

	if _, err := a.Next([]byte("Passphrase:"), true); err == nil {
		t.Fatal("Next() error = nil, want error on an unrecognized server challenge")
	}
}

func TestLoginAuth_NextNoMoreIsNoop(t *testing.T) {
	a := LoginAuth("alice", "hunter2", "smtp.example.com")

	got, err := a.Next(nil, false)
	if err != nil {
		t.Fatalf("Next(more=false) error = %v, want nil", err)
	}
	if got != nil {
		t.Errorf("Next(more=false) = %q, want nil", got)
	}
}

func TestPickAuth_PrefersPlainWhenAdvertised(t *testing.T) {
	tests := []struct {
		name      string
		mechs     string
		advertise bool
		wantLogin bool
	}{
		{"PLAIN and LOGIN offered", "PLAIN LOGIN", true, false},
		{"LOGIN only (Office 365)", "LOGIN XOAUTH2", true, true},
		{"lowercase login only", "login xoauth2", true, true},
		{"PLAIN only", "PLAIN", true, false},
		{"no AUTH extension advertised", "", false, false},
		{"AUTH advertised but neither mechanism", "XOAUTH2", true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := chooseAuth(tt.advertise, tt.mechs, "smtp.example.com", "u", "p")
			_, isLogin := got.(*loginAuth)
			if isLogin != tt.wantLogin {
				t.Errorf("chooseAuth(%q) returned loginAuth = %v, want %v", tt.mechs, isLogin, tt.wantLogin)
			}
		})
	}
}
