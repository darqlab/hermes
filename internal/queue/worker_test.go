package queue

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/darqlab/hermes/internal/config"
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
	w := NewWorker(store, StaticResolver(mail.SMTPDeliverConfig{}), time.Second)

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
	w := NewWorker(store, StaticResolver(mailCfg), time.Second)

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
	w := NewWorker(store, StaticResolver(mailCfg), time.Second)

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

// A job carries the account it was composed for, and the worker resolves
// delivery settings per job at send time rather than being locked to one
// SMTP config at construction.
func TestProcessOne_ResolvesDeliveryConfigPerJobAccount(t *testing.T) {
	srv := startFakeSMTP(t, false)
	host, port := srv.hostPort(t)

	store := newWorkerStore(t)
	job := &Job{
		ID:           uuid.NewString(),
		EnvelopeFrom: "from@example.com",
		EnvelopeTo:   []string{"to@example.com"},
		RawMIME:      []byte("Subject: test\r\n\r\nbody\r\n"),
		Status:       StatusPending,
		Account:      "work",
		CreatedAt:    time.Now().UTC(),
	}
	if err := store.Enqueue(job); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}

	var asked string
	w := NewWorker(store, func(account string) (*config.Account, error) {
		asked = account
		if account != "work" {
			return nil, fmt.Errorf("unexpected account %q", account)
		}
		return &config.Account{
			Name: "work",
			SMTP: config.SMTPConfig{Host: host, Port: port},
		}, nil
	}, time.Second)

	if got := w.ProcessOne(); got != true {
		t.Fatalf("ProcessOne() = %v, want true", got)
	}
	if asked != "work" {
		t.Errorf("resolver called with %q, want the job's account \"work\"", asked)
	}
}

func TestProcessOne_ResolverErrorMarksJobFailed(t *testing.T) {
	store := newWorkerStore(t)
	job := &Job{
		ID:           uuid.NewString(),
		EnvelopeFrom: "from@example.com",
		EnvelopeTo:   []string{"to@example.com"},
		RawMIME:      []byte("Subject: test\r\n\r\nbody\r\n"),
		Status:       StatusPending,
		Account:      "gone",
		CreatedAt:    time.Now().UTC(),
	}
	if err := store.Enqueue(job); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}

	w := NewWorker(store, func(string) (*config.Account, error) {
		return nil, fmt.Errorf("account \"gone\" not found")
	}, time.Second)

	if got := w.ProcessOne(); got != false {
		t.Errorf("ProcessOne() = %v, want false when the account cannot be resolved", got)
	}
	failed, err := store.List(StatusFailed)
	if err != nil {
		t.Fatalf("List(StatusFailed) error = %v", err)
	}
	if len(failed) != 1 || failed[0].LastError == "" {
		t.Errorf("List(StatusFailed) = %v, want the job marked failed with the resolver error", failed)
	}
}

// BACKWARD COMPATIBILITY: queue files written before multi-account support
// have no "account" field. Such jobs must still load and deliver, with an
// empty account name meaning "the default account".
func TestQueueFile_LoadsJobWithNoAccountField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "queue.json")
	legacy := `[
  {
    "id": "3fa1c2d4-5678-4abc-9def-0123456789ab",
    "envelope_from": "from@example.com",
    "envelope_to": ["to@example.com"],
    "raw_mime": "U3ViamVjdDogdGVzdA0KDQpib2R5DQo=",
    "retry_count": 0,
    "next_retry_at": "0001-01-01T00:00:00Z",
    "status": "pending",
    "created_at": "2026-08-01T00:00:00Z"
  }
]`
	if err := os.WriteFile(path, []byte(legacy), 0600); err != nil {
		t.Fatalf("write legacy queue file: %v", err)
	}

	store, err := NewStore(path, 10, time.Second, 5*time.Minute)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	jobs, err := store.List("")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("List() returned %d jobs, want 1", len(jobs))
	}
	if jobs[0].Account != "" {
		t.Errorf("Account = %q, want empty (meaning the default account)", jobs[0].Account)
	}

	srv := startFakeSMTP(t, false)
	host, port := srv.hostPort(t)
	var asked string
	w := NewWorker(store, func(account string) (*config.Account, error) {
		asked = account
		return &config.Account{Name: "default", SMTP: config.SMTPConfig{Host: host, Port: port}}, nil
	}, time.Second)

	if got := w.ProcessOne(); got != true {
		t.Fatalf("ProcessOne() = %v, want true for a legacy account-less job", got)
	}
	if asked != "" {
		t.Errorf("resolver called with %q, want \"\" for a legacy account-less job", asked)
	}
}

// Account round-trips through the JSON queue file, and is omitted when empty.
func TestJob_AccountRoundTripsAndOmitsWhenEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "queue.json")
	store, err := NewStore(path, 10, time.Second, 5*time.Minute)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	if err := store.Enqueue(&Job{ID: uuid.NewString(), Status: StatusPending, Account: "work"}); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	if err := store.Enqueue(&Job{ID: uuid.NewString(), Status: StatusPending}); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read queue file: %v", err)
	}
	if !strings.Contains(string(data), `"account": "work"`) {
		t.Errorf("queue file = %s, want it to persist the account field", data)
	}
	if strings.Count(string(data), `"account"`) != 1 {
		t.Errorf("queue file = %s, want the empty account omitted", data)
	}

	jobs, err := store.List("")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(jobs) != 2 || jobs[0].Account != "work" || jobs[1].Account != "" {
		t.Errorf("List() = %+v, want accounts [work, \"\"]", jobs)
	}
}
