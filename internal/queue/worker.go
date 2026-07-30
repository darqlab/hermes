package queue

import (
	"log"
	"time"

	"github.com/darqlab/hermes/internal/mail"
)

type Worker struct {
	store    *Store
	mailCfg  mail.SMTPDeliverConfig
	tick     time.Duration
	stopCh   chan struct{}
	doneCh   chan struct{}
}

func NewWorker(store *Store, mailCfg mail.SMTPDeliverConfig, tick time.Duration) *Worker {
	return &Worker{
		store:   store,
		mailCfg: mailCfg,
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

	resp, err := mail.Deliver(job.EnvelopeFrom, job.EnvelopeTo, job.RawMIME, w.mailCfg)
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
