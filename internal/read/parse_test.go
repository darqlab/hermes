package read

import (
	"bufio"
	"encoding/base64"
	"io"
	"mime/quotedprintable"
	"net/textproto"
	"strings"
	"testing"
	"time"

	goimap "github.com/emersion/go-imap/v2"
)

func makeMIMEBody(contentType, encoding, body string) []byte {
	var encoded string
	switch strings.ToLower(encoding) {
	case "base64":
		encoded = base64.StdEncoding.EncodeToString([]byte(body))
	case "quoted-printable":
		var buf strings.Builder
		w := quotedprintable.NewWriter(&buf)
		w.Write([]byte(body))
		w.Close()
		encoded = buf.String()
	default:
		encoded = body
	}

	ct := contentType
	if ct == "" {
		ct = "text/plain"
	}

	return []byte("Content-Type: " + ct + "\r\n" +
		"Content-Transfer-Encoding: " + encoding + "\r\n" +
		"\r\n" +
		encoded)
}

func TestParseMessage_PlainText(t *testing.T) {
	raw := RawMessage{
		UID:    42,
		SeqNum: 7,
		Envelope: &goimap.Envelope{
			Date:    time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC),
			Subject: "Hello",
			From:    []goimap.Address{{Name: "Sender", Mailbox: "sender", Host: "example.com"}},
			To:      []goimap.Address{{Name: "Recipient", Mailbox: "rcpt", Host: "example.com"}},
		},
		Flags:    []goimap.Flag{goimap.FlagSeen},
		BodyData: makeMIMEBody("text/plain", "7bit", "hello world"),
	}

	msg, err := ParseMessage(raw)
	if err != nil {
		t.Fatalf("ParseMessage() error = %v", err)
	}
	if msg.BodyText != "hello world" {
		t.Errorf("BodyText = %q, want %q", msg.BodyText, "hello world")
	}
	if msg.From != "Sender <sender@example.com>" {
		t.Errorf("From = %q", msg.From)
	}
	if len(msg.To) != 1 || msg.To[0] != "Recipient <rcpt@example.com>" {
		t.Errorf("To = %v", msg.To)
	}
	if msg.Subject != "Hello" {
		t.Errorf("Subject = %q", msg.Subject)
	}
	if len(msg.Flags) != 1 || msg.Flags[0] != string(goimap.FlagSeen) {
		t.Errorf("Flags = %v", msg.Flags)
	}
}

func TestParseMessage_HTMLOnly(t *testing.T) {
	raw := RawMessage{
		UID:      1,
		Envelope: &goimap.Envelope{},
		BodyData: makeMIMEBody("text/html", "7bit", "<p>hello</p>"),
	}

	msg, err := ParseMessage(raw)
	if err != nil {
		t.Fatalf("ParseMessage() error = %v", err)
	}
	if msg.BodyHTML != "<p>hello</p>" {
		t.Errorf("BodyHTML = %q", msg.BodyHTML)
	}
	if msg.BodyText != "" {
		t.Errorf("BodyText = %q, want empty", msg.BodyText)
	}
}

func TestParseMessage_MultipartAlternative(t *testing.T) {
	body := "Content-Type: multipart/alternative; boundary=abc123\r\n" +
		"\r\n" +
		"--abc123\r\n" +
		"Content-Type: text/plain\r\n" +
		"\r\n" +
		"plain body\r\n" +
		"--abc123\r\n" +
		"Content-Type: text/html\r\n" +
		"\r\n" +
		"<p>html body</p>\r\n" +
		"--abc123--\r\n"

	raw := RawMessage{
		UID:      1,
		Envelope: &goimap.Envelope{},
		BodyData: []byte(body),
	}

	msg, err := ParseMessage(raw)
	if err != nil {
		t.Fatalf("ParseMessage() error = %v", err)
	}
	if msg.BodyText != "plain body" {
		t.Errorf("BodyText = %q, want %q", msg.BodyText, "plain body")
	}
	if msg.BodyHTML != "<p>html body</p>" {
		t.Errorf("BodyHTML = %q, want %q", msg.BodyHTML, "<p>html body</p>")
	}
}

func TestParseMessage_MultipartMixedWithAttachment(t *testing.T) {
	body := "Content-Type: multipart/mixed; boundary=xyz789\r\n" +
		"\r\n" +
		"--xyz789\r\n" +
		"Content-Type: text/plain\r\n" +
		"\r\n" +
		"body text\r\n" +
		"--xyz789\r\n" +
		"Content-Type: application/pdf\r\n" +
		"Content-Disposition: attachment; filename=report.pdf\r\n" +
		"\r\n" +
		"fake-pdf-bytes\r\n" +
		"--xyz789--\r\n"

	raw := RawMessage{
		UID:      1,
		Envelope: &goimap.Envelope{},
		BodyData: []byte(body),
	}

	msg, err := ParseMessage(raw)
	if err != nil {
		t.Fatalf("ParseMessage() error = %v", err)
	}
	if msg.BodyText != "body text" {
		t.Errorf("BodyText = %q", msg.BodyText)
	}
	if len(msg.Attachments) != 1 {
		t.Fatalf("Attachments len = %d, want 1", len(msg.Attachments))
	}
	a := msg.Attachments[0]
	if a.Filename != "report.pdf" {
		t.Errorf("Attachment.Filename = %q", a.Filename)
	}
	if a.ContentType != "application/pdf" {
		t.Errorf("Attachment.ContentType = %q", a.ContentType)
	}
}

func TestParseMessage_QuotedPrintable(t *testing.T) {
	raw := RawMessage{
		UID:      1,
		Envelope: &goimap.Envelope{},
		BodyData: makeMIMEBody("text/plain", "quoted-printable", "café résumé"),
	}

	msg, err := ParseMessage(raw)
	if err != nil {
		t.Fatalf("ParseMessage() error = %v", err)
	}
	if msg.BodyText != "café résumé" {
		t.Errorf("BodyText = %q, want %q", msg.BodyText, "café résumé")
	}
}

func TestParseMessage_Base64(t *testing.T) {
	raw := RawMessage{
		UID:      1,
		Envelope: &goimap.Envelope{},
		BodyData: makeMIMEBody("text/plain", "base64", "base64 decoded text"),
	}

	msg, err := ParseMessage(raw)
	if err != nil {
		t.Fatalf("ParseMessage() error = %v", err)
	}
	if msg.BodyText != "base64 decoded text" {
		t.Errorf("BodyText = %q, want %q", msg.BodyText, "base64 decoded text")
	}
}

func TestParseMessage_EmptyBody(t *testing.T) {
	raw := RawMessage{
		UID:      1,
		Envelope: &goimap.Envelope{},
	}

	msg, err := ParseMessage(raw)
	if err != nil {
		t.Fatalf("ParseMessage() error = %v", err)
	}
	if msg.BodyText != "" || msg.BodyHTML != "" {
		t.Errorf("expected empty body, got text=%q html=%q", msg.BodyText, msg.BodyHTML)
	}
}

func TestParseMessage_MalformedMultipart(t *testing.T) {
	body := "Content-Type: multipart/alternative; boundary=bad\r\n" +
		"\r\n" +
		"--bad\r\n" +
		"Content-Type: text/plain\r\n" +
		"\r\n" +
		"valid part\r\n" +
		"--bad\r\n" +
		"Content-Type: text/plain\r\n" +
		"Content-Transfer-Encoding: base64\r\n" +
		"\r\n" +
		"!!!not-valid-base64!!!\r\n" +
		"--bad--\r\n"

	raw := RawMessage{
		UID:      1,
		Envelope: &goimap.Envelope{},
		BodyData: []byte(body),
	}

	msg, err := ParseMessage(raw)
	if err != nil {
		t.Fatalf("ParseMessage() error = %v", err)
	}
	if msg.BodyText != "valid part" {
		t.Errorf("BodyText = %q, want %q (malformed part should be skipped)", msg.BodyText, "valid part")
	}
}

func TestParseMessage_NoEnvelope(t *testing.T) {
	raw := RawMessage{
		UID:      1,
		BodyData: makeMIMEBody("text/plain", "7bit", "hello"),
	}

	msg, err := ParseMessage(raw)
	if err != nil {
		t.Fatalf("ParseMessage() error = %v", err)
	}
	if msg.From != "" {
		t.Errorf("From = %q, want empty", msg.From)
	}
}

func TestFormatAddress_Plain(t *testing.T) {
	addrs := []goimap.Address{{Mailbox: "user", Host: "example.com"}}
	if got := formatAddress(addrs); got != "user@example.com" {
		t.Errorf("formatAddress = %q, want %q", got, "user@example.com")
	}
}

func TestFormatAddress_WithName(t *testing.T) {
	addrs := []goimap.Address{{Name: "John Doe", Mailbox: "john", Host: "example.com"}}
	if got := formatAddress(addrs); got != "John Doe <john@example.com>" {
		t.Errorf("formatAddress = %q", got)
	}
}

func TestFormatAddress_Empty(t *testing.T) {
	if got := formatAddress(nil); got != "" {
		t.Errorf("formatAddress(nil) = %q", got)
	}
	if got := formatAddress([]goimap.Address{}); got != "" {
		t.Errorf("formatAddress([]) = %q", got)
	}
}

func TestMIMEBoundaryHelper(t *testing.T) {
	data := makeMIMEBody("text/plain", "7bit", "test")
	r := textproto.NewReader(bufio.NewReader(strings.NewReader(string(data))))
	msg, err := r.ReadMIMEHeader()
	if err != nil {
		t.Fatalf("makeMIMEBody produced invalid MIME: %v", err)
	}
	if msg.Get("Content-Type") != "text/plain" {
		t.Errorf("Content-Type = %q", msg.Get("Content-Type"))
	}
}

func TestQuotedPrintableDecoding(t *testing.T) {
	var buf strings.Builder
	w := quotedprintable.NewWriter(&buf)
	w.Write([]byte("héllo"))
	w.Close()

	r := quotedprintable.NewReader(strings.NewReader(buf.String()))
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if string(out) != "héllo" {
		t.Errorf("round-trip failed: got %q", string(out))
	}
}

func TestBase64Decoding(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte("test data"))
	decoded := make([]byte, len(encoded))
	n, err := base64.StdEncoding.Decode(decoded, []byte(encoded))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if string(decoded[:n]) != "test data" {
		t.Errorf("round-trip failed: got %q", string(decoded[:n]))
	}
}

func TestCCParsing(t *testing.T) {
	raw := RawMessage{
		UID: 1,
		Envelope: &goimap.Envelope{
			Cc: []goimap.Address{
				{Mailbox: "cc1", Host: "example.com"},
				{Name: "CC2", Mailbox: "cc2", Host: "example.com"},
			},
		},
		BodyData: makeMIMEBody("text/plain", "7bit", "test"),
	}

	msg, err := ParseMessage(raw)
	if err != nil {
		t.Fatalf("ParseMessage() error = %v", err)
	}
	if len(msg.Cc) != 2 {
		t.Fatalf("Cc len = %d, want 2", len(msg.Cc))
	}
	if msg.Cc[0] != "cc1@example.com" {
		t.Errorf("Cc[0] = %q", msg.Cc[0])
	}
	if msg.Cc[1] != "CC2 <cc2@example.com>" {
		t.Errorf("Cc[1] = %q", msg.Cc[1])
	}
}
