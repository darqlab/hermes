package read

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

const (
	idleBackoffBase = time.Second
	idleBackoffCap  = 30 * time.Second
	defaultPoll     = 30 * time.Second
)

type WatchConfig struct {
	Host         string
	Port         int
	User         string
	Pass         string
	Mailbox      string
	PollInterval time.Duration
	NoIDLE       bool
}

func hasCapIDLE(client *imapclient.Client) bool {
	caps := client.Caps()
	return caps.Has(imap.CapIMAP4rev2) || caps.Has(imap.Cap("IDLE"))
}

func Watch(ctx context.Context, cfg WatchConfig, onMessage func(Message) error) error {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = defaultPoll
	}

	for attempt := 0; ; attempt++ {
		if err := watchOnce(ctx, cfg, onMessage); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			log.Printf("watch disconnected: %v", err)
		}

		backoff := idleBackoffBase
		for i := 0; i < attempt; i++ {
			backoff *= 2
			if backoff > idleBackoffCap {
				backoff = idleBackoffCap
			}
		}

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
	}
}

func highestUID(client *imapclient.Client) (imap.UID, error) {
	data, err := client.UIDSearch(&imap.SearchCriteria{}, nil).Wait()
	if err != nil {
		return 0, fmt.Errorf("uid search: %w", err)
	}
	uids := data.AllUIDs()
	if len(uids) == 0 {
		return 0, nil
	}
	return uids[len(uids)-1], nil
}

func fetchAndProcess(cl *imapclient.Client, uids []imap.UID, onMessage func(Message) error) error {
	if len(uids) == 0 {
		return nil
	}
	uidSet := imap.UIDSetNum(uids...)
	msgs, err := cl.Fetch(uidSet, &imap.FetchOptions{
		UID:          true,
		Flags:        true,
		Envelope:     true,
		InternalDate: true,
		BodySection: []*imap.FetchItemBodySection{{
			Peek: true,
		}},
	}).Collect()
	if err != nil {
		return fmt.Errorf("fetch new messages: %w", err)
	}

	for _, buf := range msgs {
		raw := RawMessage{
			UID:          buf.UID,
			SeqNum:       buf.SeqNum,
			Flags:        buf.Flags,
			Envelope:     buf.Envelope,
			InternalDate: buf.InternalDate,
		}
		for _, bs := range buf.BodySection {
			raw.BodyData = bs.Bytes
			break
		}

		msg, err := ParseMessage(raw)
		if err != nil {
			log.Printf("parse message uid %v: %v", buf.UID, err)
			continue
		}

		if err := onMessage(*msg); err != nil {
			return err
		}
	}
	return nil
}

func watchOnce(ctx context.Context, cfg WatchConfig, onMessage func(Message) error) error {
	cl, err := DialTLS(cfg.Host, cfg.Port)
	if err != nil {
		return err
	}
	defer cl.Close()

	if err := cl.Login(cfg.User, cfg.Pass); err != nil {
		return err
	}

	if _, err := cl.Select(cfg.Mailbox); err != nil {
		return err
	}

	lastUID, err := highestUID(cl.client)
	if err != nil {
		return err
	}

	useIDLE := !cfg.NoIDLE && hasCapIDLE(cl.client)
	if !useIDLE {
		if cfg.NoIDLE {
			log.Printf("idle disabled, polling every %s", cfg.PollInterval)
		} else {
			log.Printf("server does not support IDLE, falling back to poll (%s)", cfg.PollInterval)
		}
	}

	for {
		if useIDLE {
			idleCmd, err := cl.client.Idle()
			if err != nil {
				log.Printf("idle error: %v, switching to poll", err)
				useIDLE = false
				continue
			}

			idleDone := make(chan error, 1)
			go func() {
				idleDone <- idleCmd.Wait()
			}()

			select {
			case <-ctx.Done():
				idleCmd.Close()
				return nil
			case err := <-idleDone:
				if err != nil {
					log.Printf("idle wait error: %v", err)
				}
			}

			_ = idleCmd.Close()
		} else {
			timer := time.NewTimer(cfg.PollInterval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil
			case <-timer.C:
			}
		}

		var uidSet imap.UIDSet
		uidSet.AddRange(lastUID+1, 0)
		data, err := cl.client.UIDSearch(&imap.SearchCriteria{UID: []imap.UIDSet{uidSet}}, nil).Wait()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("uid search: %w", err)
		}

		newUIDs := data.AllUIDs()
		if len(newUIDs) == 0 {
			continue
		}

		log.Printf("found %d new message(s)", len(newUIDs))

		if err := fetchAndProcess(cl.client, newUIDs, onMessage); err != nil {
			return err
		}
		lastUID = newUIDs[len(newUIDs)-1]
	}
}
