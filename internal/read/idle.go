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

func watchOnce(ctx context.Context, cfg WatchConfig, onMessage func(Message) error) error {
	client, err := DialTLS(cfg.Host, cfg.Port)
	if err != nil {
		return err
	}
	defer client.Close()

	if err := client.Login(cfg.User, cfg.Pass); err != nil {
		return err
	}

	selectData, err := client.Select(cfg.Mailbox)
	if err != nil {
		return err
	}
	lastSeen := selectData.NumMessages

	useIDLE := hasCapIDLE(client.client)
	if !useIDLE {
		log.Printf("server does not support IDLE, falling back to poll (%s)", cfg.PollInterval)
	}

	for {
		if useIDLE {
			idleCmd, err := client.client.Idle()
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

		status, err := client.client.Status(cfg.Mailbox, &imap.StatusOptions{NumMessages: true}).Wait()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("status: %w", err)
		}

		current := uint32(0)
		if status.NumMessages != nil {
			current = *status.NumMessages
		}
		if current > lastSeen {
			newCount := current - lastSeen
			log.Printf("found %d new message(s)", newCount)

			start := lastSeen + 1
			uids := make([]imap.UID, 0, newCount)
			for i := uint32(0); i < newCount; i++ {
				uids = append(uids, imap.UID(start+i))
			}

			uidSet := imap.UIDSetNum(uids...)
			msgs, err := client.client.Fetch(uidSet, &imap.FetchOptions{
				UID:          true,
				Flags:        true,
				Envelope:     true,
				InternalDate: true,
				BodySection: []*imap.FetchItemBodySection{{
					Peek: true,
				}},
			}).Collect()
			if err != nil {
				log.Printf("fetch new messages failed: %v", err)
				lastSeen = current
				continue
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
		}
		lastSeen = current
	}
}
