package queue

import (
	"bufio"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/darqlab/hermes/internal/mail"
	"github.com/google/uuid"
)

// minimal fake SMTP server, local to this test file (kept separate from the
// one in internal/mail's tests since unexported test helpers aren't shared
// across packages). Supports EHLO/HELO, MAIL FROM, RCPT TO, DATA + ".", QUIT.
// No AUTH support — callers must use an empty Pass so mail.sendData skips auth.
type fakeSMTP struct {
	ln       net.Listener
	failRcpt bool
}

func startFakeSMTP(t *testing.T, failRcpt bool) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &fakeSMTP{ln: ln, failRcpt: failRcpt}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		s.handle(conn)
	}()
	t.Cleanup(func() {
		ln.Close()
		wg.Wait()
	})
	return s
}

func (s *fakeSMTP) handle(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	w := bufio.NewWriter(conn)
	writeLine := func(format string, args ...interface{}) {
		fmt.Fprintf(w, format+"\r\n", args...)
		w.Flush()
	}
	writeLine("220 fake.smtp ESMTP ready")

	inData := false
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")

		if inData {
			if line == "." {
				inData = false
				writeLine("250 OK: message accepted")
			}
			continue
		}

		upper := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(upper, "EHLO"), strings.HasPrefix(upper, "HELO"):
			writeLine("250 fake.smtp greets you")
		case strings.HasPrefix(upper, "MAIL FROM"):
			writeLine("250 OK")
		case strings.HasPrefix(upper, "RCPT TO"):
			if s.failRcpt {
				writeLine("550 no such user")
			} else {
				writeLine("250 OK")
			}
		case strings.HasPrefix(upper, "DATA"):
			inData = true
			writeLine("354 End data with <CR><LF>.<CR><LF>")
		case strings.HasPrefix(upper, "QUIT"):
			writeLine("221 Bye")
			return
		default:
			writeLine("500 unrecognized command")
		}
	}
}

func (s *fakeSMTP) hostPort(t *testing.T) (string, int) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(s.ln.Addr().String())
	if err != nil {
		t.Fatalf("split host/port: %v", err)
	}
	var port int
	fmt.Sscanf(portStr, "%d", &port)
	return host, port
}

func newWorkerStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "queue.json")
	s, err := NewStore(path, 10, time.Second, 5*time.Minute)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	return s
}

func TestProcessOne_EmptyQueueReturnsFalse(t *testing.T) {
	store := newWorkerStore(t)
	w := NewWorker(store, mail.SMTPDeliverConfig{}, time.Second)

	got := w.ProcessOne()
	if got != false {
		t.Errorf("ProcessOne() = %v, want false for empty queue", got)
	}
}

func TestProcessOne_SuccessEndToEnd(t *testing.T) {
	srv := startFakeSMTP(t, false)
	host, port := srv.hostPort(t)

	store := newWorkerStore(t)
	job := &Job{
		ID:           uuid.NewString(),
		EnvelopeFrom: "from@example.com",
		EnvelopeTo:   []string{"to@example.com"},
		RawMIME:      []byte("Subject: test\r\n\r\nbody\r\n"),
		Status:       StatusPending,
		CreatedAt:    time.Now().UTC(),
	}
	if err := store.Enqueue(job); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}

	mailCfg := mail.SMTPDeliverConfig{
		Host:     host,
		Port:     port,
		Pass:     "", // no auth: fake server doesn't support AUTH
		UseTLS:   false,
		StartTLS: false,
	}
	w := NewWorker(store, mailCfg, time.Second)

	got := w.ProcessOne()
	if got != true {
		t.Fatalf("ProcessOne() = %v, want true on successful delivery", got)
	}

	doneJobs, err := store.List(StatusDone)
	if err != nil {
		t.Fatalf("List(StatusDone) error = %v", err)
	}
	if len(doneJobs) != 1 || doneJobs[0].ID != job.ID {
		t.Errorf("List(StatusDone) = %v, want job %s marked done", doneJobs, job.ID)
	}
}

func TestProcessOne_DeliveryFailureMarksJobFailed(t *testing.T) {
	srv := startFakeSMTP(t, true) // rejects RCPT TO
	host, port := srv.hostPort(t)

	store := newWorkerStore(t)
	job := &Job{
		ID:           uuid.NewString(),
		EnvelopeFrom: "from@example.com",
		EnvelopeTo:   []string{"to@example.com"},
		RawMIME:      []byte("Subject: test\r\n\r\nbody\r\n"),
		Status:       StatusPending,
		CreatedAt:    time.Now().UTC(),
	}
	if err := store.Enqueue(job); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}

	mailCfg := mail.SMTPDeliverConfig{
		Host:     host,
		Port:     port,
		Pass:     "",
		UseTLS:   false,
		StartTLS: false,
	}
	w := NewWorker(store, mailCfg, time.Second)

	got := w.ProcessOne()
	if got != false {
		t.Errorf("ProcessOne() = %v, want false on delivery failure", got)
	}

	failedJobs, err := store.List(StatusFailed)
	if err != nil {
		t.Fatalf("List(StatusFailed) error = %v", err)
	}
	if len(failedJobs) != 1 || failedJobs[0].ID != job.ID {
		t.Errorf("List(StatusFailed) = %v, want job %s marked failed", failedJobs, job.ID)
	}
	if failedJobs[0].LastError == "" {
		t.Errorf("LastError = %q, want non-empty error message recorded", failedJobs[0].LastError)
	}
}
