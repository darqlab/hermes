package queue

import (
	"fmt"
	"log"
	"time"

	"github.com/darqlab/hermes/internal/config"
	"github.com/darqlab/hermes/internal/mail"
)

// AccountResolver returns the delivery settings for a queued job's account.
// An empty name means "whatever the default account is" — that is how jobs
// written by older Hermes versions (no "account" field in the queue file)
// keep working.
type AccountResolver func(account string) (*config.Account, error)

// StaticResolver adapts a single fixed SMTP config to an AccountResolver,
// for callers (and tests) that only ever deliver through one account.
func StaticResolver(cfg mail.SMTPDeliverConfig) AccountResolver {
	return func(string) (*config.Account, error) {
		return &config.Account{
			Name: "default",
			SMTP: config.SMTPConfig{
				Host:     cfg.Host,
				Port:     cfg.Port,
				User:     cfg.User,
				Pass:     cfg.Pass,
				UseTLS:   cfg.UseTLS,
				StartTLS: cfg.StartTLS,
			},
		}, nil
	}
}

type Worker struct {
	store   *Store
	resolve AccountResolver
	tick    time.Duration
	stopCh  chan struct{}
	doneCh  chan struct{}
}

func NewWorker(store *Store, resolve AccountResolver, tick time.Duration) *Worker {
	return &Worker{
		store:   store,
		resolve: resolve,
		tick:    tick,
		stopCh:  make(chan struct{}),
		doneCh:  make(chan struct{}),
	}
}

func (w *Worker) Start() {
	go w.loop()
}

func (w *Worker) Stop() {
	close(w.stopCh)
	<-w.doneCh
}

func (w *Worker) loop() {
	defer close(w.doneCh)

	ticker := time.NewTicker(w.tick)
	defer ticker.Stop()

	for {
		select {
		case <-w.stopCh:
			return
		case <-ticker.C:
			w.processOne()
		}
	}
}

func (w *Worker) ProcessOne() bool {
	return w.processOne()
}

func (w *Worker) processOne() bool {
	job, err := w.store.Dequeue()
	if err != nil {
		log.Printf("queue: dequeue error: %v", err)
		return false
	}
	if job == nil {
		return false
	}

	log.Printf("queue: delivering job %s (attempt %d/%d) to %v",
		job.ID[:8], job.RetryCount+1, w.store.retryMax, job.EnvelopeTo)

	mailCfg, err := w.deliverConfig(job.Account)
	if err != nil {
		log.Printf("queue: job %s account resolution failed: %v", job.ID[:8], err)
		if markErr := w.store.MarkFailed(job.ID, err.Error()); markErr != nil {
			log.Printf("queue: mark failed error: %v", markErr)
		}
		return false
	}

	resp, err := mail.Deliver(job.EnvelopeFrom, job.EnvelopeTo, job.RawMIME, mailCfg)
	if err != nil {
		log.Printf("queue: job %s failed: %v", job.ID[:8], err)
		if markErr := w.store.MarkFailed(job.ID, err.Error()); markErr != nil {
			log.Printf("queue: mark failed error: %v", markErr)
		}
		return false
	}

	if err := w.store.MarkDone(job.ID); err != nil {
		log.Printf("queue: mark done error: %v", err)
	}
	log.Printf("queue: job %s delivered — %s", job.ID[:8], resp)
	return true
}

func (w *Worker) deliverConfig(account string) (mail.SMTPDeliverConfig, error) {
	if w.resolve == nil {
		return mail.SMTPDeliverConfig{}, fmt.Errorf("no account resolver configured")
	}
	acct, err := w.resolve(account)
	if err != nil {
		return mail.SMTPDeliverConfig{}, err
	}
	return mail.SMTPDeliverConfig{
		Host:     acct.SMTP.Host,
		Port:     acct.SMTP.Port,
		User:     acct.SMTP.User,
		Pass:     acct.SMTP.Pass,
		UseTLS:   acct.SMTP.UseTLS,
		StartTLS: acct.SMTP.StartTLS,
	}, nil
}
