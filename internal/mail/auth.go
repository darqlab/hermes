package mail

import (
	"fmt"
	"net/smtp"
	"strings"
)

// loginAuth implements the classic (non-standardised but ubiquitous) SMTP
// AUTH LOGIN mechanism: the server issues a base64 "Username:" challenge,
// then a "Password:" challenge, and the client answers each with the raw
// credential. Go's net/smtp only ships PLAIN and CRAM-MD5, but some large
// providers — notably Office 365 (smtp.office365.com:587) — advertise
// "AUTH LOGIN XOAUTH2" and do not offer PLAIN at all, so PlainAuth fails
// against them outright.
//
// Like smtp.PlainAuth, this refuses to run over a cleartext connection: the
// password is sent only base64-encoded, which is encoding, not encryption.
type loginAuth struct {
	username string
	password string
	host     string
}

// LoginAuth returns an smtp.Auth implementing AUTH LOGIN for the given host.
func LoginAuth(username, password, host string) smtp.Auth {
	return &loginAuth{username: username, password: password, host: host}
}

func (a *loginAuth) Start(server *smtp.ServerInfo) (string, []byte, error) {
	if !server.TLS {
		return "", nil, fmt.Errorf("smtp: refusing to send LOGIN credentials over an unencrypted connection")
	}
	if server.Name != a.host {
		return "", nil, fmt.Errorf("smtp: server name %q does not match expected host %q", server.Name, a.host)
	}
	return "LOGIN", nil, nil
}

func (a *loginAuth) Next(fromServer []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	switch strings.ToLower(strings.TrimRight(string(fromServer), ": \r\n")) {
	case "username":
		return []byte(a.username), nil
	case "password":
		return []byte(a.password), nil
	default:
		return nil, fmt.Errorf("smtp: unexpected server challenge during AUTH LOGIN: %q", string(fromServer))
	}
}

// pickAuth chooses an auth mechanism based on what the server advertises in
// its EHLO response. PLAIN is preferred when offered; otherwise LOGIN is
// used. If the server advertises no AUTH extension at all (or lists neither),
// PLAIN is attempted so the failure mode matches the previous behaviour.
func pickAuth(client *smtp.Client, host, user, pass string) smtp.Auth {
	ok, mechs := client.Extension("AUTH")
	return chooseAuth(ok, mechs, host, user, pass)
}

// chooseAuth is the pure decision half of pickAuth, split out so it can be
// unit-tested without a live SMTP client.
func chooseAuth(advertised bool, mechs, host, user, pass string) smtp.Auth {
	if advertised {
		list := strings.Fields(strings.ToUpper(mechs))
		hasPlain, hasLogin := false, false
		for _, m := range list {
			switch m {
			case "PLAIN":
				hasPlain = true
			case "LOGIN":
				hasLogin = true
			}
		}
		if !hasPlain && hasLogin {
			return LoginAuth(user, pass, host)
		}
	}
	return smtp.PlainAuth("", user, pass, host)
}
