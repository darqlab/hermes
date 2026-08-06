package read

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	netmail "net/mail"
	"strings"
	"time"

	goimap "github.com/emersion/go-imap/v2"
)

type AttachmentMeta struct {
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	SizeBytes   int    `json:"size_bytes"`
}

type Message struct {
	UID          uint32           `json:"uid"`
	SeqNum       uint32           `json:"seq_num"`
	From         string           `json:"from"`
	To           []string         `json:"to"`
	Cc           []string         `json:"cc,omitempty"`
	Subject      string           `json:"subject"`
	Date         time.Time        `json:"date"`
	Flags        []string         `json:"flags"`
	BodyText     string           `json:"body_text,omitempty"`
	BodyHTML     string           `json:"body_html,omitempty"`
	Attachments  []AttachmentMeta `json:"attachments,omitempty"`
}

type ParseError struct {
	UID     uint32
	Message string
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("parse message uid %v: %s", e.UID, e.Message)
}

func ParseMessage(raw RawMessage) (*Message, error) {
	msg := &Message{
		UID:    uint32(raw.UID),
		SeqNum: raw.SeqNum,
	}

	if raw.Envelope != nil {
		env := raw.Envelope
		msg.Subject = env.Subject
		msg.Date = env.Date
		msg.From = formatAddress(env.From)
		msg.To = addressListToStrings(env.To)
		msg.Cc = addressListToStrings(env.Cc)
	}

	for _, flag := range raw.Flags {
		msg.Flags = append(msg.Flags, string(flag))
	}

	if len(raw.BodyData) == 0 {
		return msg, nil
	}

	parsed, err := netmail.ReadMessage(bytes.NewReader(raw.BodyData))
	if err != nil {
		return nil, &ParseError{UID: msg.UID, Message: fmt.Sprintf("read mime: %v", err)}
	}

	mediaType, params, err := mime.ParseMediaType(parsed.Header.Get("Content-Type"))
	if err != nil {
		mediaType = "text/plain"
		params = nil
	}

	if strings.HasPrefix(mediaType, "multipart/") {
		err = parseMultipart(msg, parsed.Body, params["boundary"])
	} else {
		body, err := decodeBody(parsed.Body, parsed.Header.Get("Content-Transfer-Encoding"))
		if err != nil {
			return nil, &ParseError{UID: msg.UID, Message: fmt.Sprintf("decode body: %v", err)}
		}
		switch mediaType {
		case "text/html":
			msg.BodyHTML = string(body)
		case "text/plain":
			msg.BodyText = string(body)
		default:
			msg.BodyText = string(body)
		}
	}

	return msg, nil
}

func parseMultipart(msg *Message, body io.Reader, boundary string) error {
	if boundary == "" {
		return fmt.Errorf("missing boundary")
	}
	r := multipart.NewReader(body, boundary)

	for {
		part, err := r.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		contentType := part.Header.Get("Content-Type")
		mediaType, _, err := mime.ParseMediaType(contentType)
		if err != nil {
			mediaType = "text/plain"
		}

		body, err := decodeBody(part, part.Header.Get("Content-Transfer-Encoding"))
		if err != nil {
			continue
		}

		if strings.HasPrefix(mediaType, "multipart/") {
			_, params, err := mime.ParseMediaType(contentType)
			if err != nil {
				continue
			}
			if err := parseMultipart(msg, bytes.NewReader(body), params["boundary"]); err != nil {
				continue
			}
			continue
		}

		disposition := part.Header.Get("Content-Disposition")
		if strings.HasPrefix(disposition, "attachment") {
			_, dispParams, _ := mime.ParseMediaType(disposition)
			filename := dispParams["filename"]
			if filename == "" {
				filename = "unnamed"
			}
			msg.Attachments = append(msg.Attachments, AttachmentMeta{
				Filename:    filename,
				ContentType: mediaType,
				SizeBytes:   len(body),
			})
			continue
		}

		switch mediaType {
		case "text/html":
			if msg.BodyHTML == "" {
				msg.BodyHTML = string(body)
			}
		case "text/plain":
			if msg.BodyText == "" {
				msg.BodyText = string(body)
			}
		}
	}
	return nil
}

func decodeBody(r io.Reader, encoding string) ([]byte, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}

	encoding = strings.ToLower(encoding)
	switch {
	case encoding == "quoted-printable":
		decoded, err := io.ReadAll(quotedprintable.NewReader(bytes.NewReader(raw)))
		if err != nil {
			return nil, err
		}
		return decoded, nil
	case encoding == "base64":
		decoded, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, bytes.NewReader(raw)))
		if err != nil {
			return nil, err
		}
		return decoded, nil
	default:
		return raw, nil
	}
}

func formatAddress(addrs []goimap.Address) string {
	if len(addrs) == 0 {
		return ""
	}
	a := addrs[0]
	if a.Name != "" {
		return fmt.Sprintf("%s <%s>", a.Name, a.Addr())
	}
	return a.Addr()
}

func addressListToStrings(addrs []goimap.Address) []string {
	var out []string
	for _, a := range addrs {
		addr := a.Addr()
		if addr == "" {
			continue
		}
		if a.Name != "" {
			out = append(out, fmt.Sprintf("%s <%s>", a.Name, addr))
		} else {
			out = append(out, addr)
		}
	}
	return out
}
