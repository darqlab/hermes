package mail

import (
	"bytes"
	"crypto"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"net/smtp"
	"os"

	"github.com/emersion/go-msgauth/dkim"
	gomail "gopkg.in/mail.v2"
)

type MessageOpts struct {
	From        string
	To          []string
	Cc          []string
	Bcc         []string
	ReplyTo     string
	Subject     string
	Body        string
	BodyHTML    string
	Attachments []string
}

type SMTPDeliverConfig struct {
	Host     string
	Port     int
	User     string
	Pass     string
	UseTLS   bool
	StartTLS bool
}

func Compose(opts MessageOpts) ([]byte, error) {
	m := gomail.NewMessage()
	m.SetHeader("From", opts.From)
	m.SetHeader("To", opts.To...)
	if len(opts.Cc) > 0 {
		m.SetHeader("Cc", opts.Cc...)
	}
	if opts.ReplyTo != "" {
		m.SetHeader("Reply-To", opts.ReplyTo)
	}
	m.SetHeader("Subject", opts.Subject)

	if opts.BodyHTML != "" && opts.Body != "" {
		m.SetBody("text/plain", opts.Body)
		m.AddAlternative("text/html", opts.BodyHTML)
	} else if opts.BodyHTML != "" {
		m.SetBody("text/html", opts.BodyHTML)
	} else {
		m.SetBody("text/plain", opts.Body)
	}

	for _, path := range opts.Attachments {
		m.Attach(path)
	}

	var buf bytes.Buffer
	if _, err := m.WriteTo(&buf); err != nil {
		return nil, fmt.Errorf("compose mime: %w", err)
	}
	return buf.Bytes(), nil
}

func Sign(raw []byte, domain, selector, keyFile string) ([]byte, error) {
	signer, err := loadPrivateKey(keyFile)
	if err != nil {
		return nil, err
	}

	var signed bytes.Buffer
	if err := dkim.Sign(&signed, bytes.NewReader(raw), &dkim.SignOptions{
		Domain:   domain,
		Selector: selector,
		Signer:   signer,
	}); err != nil {
		return nil, fmt.Errorf("dkim sign: %w", err)
	}
	return signed.Bytes(), nil
}

func loadPrivateKey(path string) (crypto.Signer, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read key file: %w", err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("no PEM block found in %s", path)
	}

	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		key8, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse private key (tried PKCS1 and PKCS8): %w", err)
		}
		signer, ok := key8.(crypto.Signer)
		if !ok {
			return nil, fmt.Errorf("key does not implement crypto.Signer")
		}
		return signer, nil
	}
	return key, nil
}

func Deliver(from string, to []string, raw []byte, cfg SMTPDeliverConfig) (string, error) {
	addr := net.JoinHostPort(cfg.Host, fmt.Sprintf("%d", cfg.Port))

	if cfg.UseTLS {
		return deliverDirectTLS(addr, cfg.Host, cfg.User, cfg.Pass, from, to, raw)
	}
	return deliverStartTLS(addr, cfg.Host, cfg.User, cfg.Pass, cfg.StartTLS, from, to, raw)
}

func deliverDirectTLS(addr, host, user, pass, from string, to []string, raw []byte) (string, error) {
	tlsCfg := &tls.Config{ServerName: host}
	conn, err := tls.Dial("tcp", addr, tlsCfg)
	if err != nil {
		return "", fmt.Errorf("tls dial: %w", err)
	}
	defer conn.Close()

	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return "", fmt.Errorf("smtp client: %w", err)
	}
	defer client.Quit()

	return sendData(client, host, user, pass, from, to, raw)
}

func deliverStartTLS(addr, host, user, pass string, useStartTLS bool, from string, to []string, raw []byte) (string, error) {
	client, err := smtp.Dial(addr)
	if err != nil {
		return "", fmt.Errorf("smtp dial: %w", err)
	}
	defer client.Quit()

	if useStartTLS {
		tlsCfg := &tls.Config{ServerName: host}
		if err := client.StartTLS(tlsCfg); err != nil {
			return "", fmt.Errorf("starttls: %w", err)
		}
	}

	return sendData(client, host, user, pass, from, to, raw)
}

func sendData(client *smtp.Client, host, user, pass, from string, to []string, raw []byte) (string, error) {
	if pass != "" {
		auth := smtp.PlainAuth("", user, pass, host)
		if err := client.Auth(auth); err != nil {
			return "", fmt.Errorf("auth: %w", err)
		}
	}

	if err := client.Mail(from); err != nil {
		return "", fmt.Errorf("mail from: %w", err)
	}
	for _, rcpt := range to {
		if err := client.Rcpt(rcpt); err != nil {
			return "", fmt.Errorf("rcpt to %s: %w", rcpt, err)
		}
	}
	// client.Data() (net/smtp) discards the server's final response text on
	// Close(), returning only an error. To surface the actual response, the
	// DATA sequence is replicated manually via the exported client.Text
	// (textproto.Conn), mirroring net/smtp's own internal implementation.
	id, err := client.Text.Cmd("DATA")
	if err != nil {
		return "", fmt.Errorf("data: %w", err)
	}
	client.Text.StartResponse(id)
	_, _, err = client.Text.ReadResponse(354)
	client.Text.EndResponse(id)
	if err != nil {
		return "", fmt.Errorf("data: %w", err)
	}

	dw := client.Text.DotWriter()
	if _, err := dw.Write(raw); err != nil {
		return "", fmt.Errorf("write: %w", err)
	}
	if err := dw.Close(); err != nil {
		return "", fmt.Errorf("write: %w", err)
	}

	_, resp, err := client.Text.ReadResponse(250)
	if err != nil {
		return resp, err
	}
	return resp, nil
}
