package read

import (
	"fmt"
	"net"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

type Client struct {
	client *imapclient.Client
}

func DialTLS(host string, port int) (*Client, error) {
	addr := net.JoinHostPort(host, fmt.Sprintf("%d", port))
	c, err := imapclient.DialTLS(addr, nil)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	return &Client{client: c}, nil
}

func (c *Client) Login(username, password string) error {
	if err := c.client.Login(username, password).Wait(); err != nil {
		return fmt.Errorf("authenticate: %w", err)
	}
	return nil
}

type MailboxInfo struct {
	Name string
}

func (c *Client) ListMailboxes() ([]MailboxInfo, error) {
	data, err := c.client.List("", "%", nil).Collect()
	if err != nil {
		return nil, fmt.Errorf("list mailboxes: %w", err)
	}
	var out []MailboxInfo
	for _, d := range data {
		out = append(out, MailboxInfo{Name: d.Mailbox})
	}
	return out, nil
}

func (c *Client) Select(mailbox string) (*imap.SelectData, error) {
	data, err := c.client.Select(mailbox, nil).Wait()
	if err != nil {
		return nil, fmt.Errorf("select mailbox %q: %w", mailbox, err)
	}
	return data, nil
}

func (c *Client) Search(criteria *imap.SearchCriteria) (*imap.SearchData, error) {
	data, err := c.client.UIDSearch(criteria, nil).Wait()
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	return data, nil
}

type RawMessage struct {
	UID          imap.UID
	SeqNum       uint32
	Flags        []imap.Flag
	Envelope     *imap.Envelope
	InternalDate time.Time
	BodyData     []byte
}

func (c *Client) FetchMessages(uids []imap.UID) ([]RawMessage, error) {
	if len(uids) == 0 {
		return nil, nil
	}

	uidSet := imap.UIDSetNum(uids...)
	msgs, err := c.client.Fetch(uidSet, &imap.FetchOptions{
		UID:          true,
		Flags:        true,
		Envelope:     true,
		InternalDate: true,
		BodySection: []*imap.FetchItemBodySection{{
			Peek: true,
		}},
	}).Collect()
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}

	var out []RawMessage
	for _, buf := range msgs {
		rm := RawMessage{
			UID:          buf.UID,
			SeqNum:       buf.SeqNum,
			Flags:        buf.Flags,
			Envelope:     buf.Envelope,
			InternalDate: buf.InternalDate,
		}
		for _, bs := range buf.BodySection {
			rm.BodyData = bs.Bytes
			break
		}
		out = append(out, rm)
	}
	return out, nil
}

func (c *Client) Close() error {
	return c.client.Close()
}

func (c *Client) Logout() error {
	return c.client.Logout().Wait()
}
