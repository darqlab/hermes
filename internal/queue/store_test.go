package queue

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
)

func newTestStore(t *testing.T, retryMax int, backoffBase, backoffCap time.Duration) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "queue.json")
	s, err := NewStore(path, retryMax, backoffBase, backoffCap)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	return s, path
}

func newTestJob(status JobStatus) *Job {
	return &Job{
		ID:           uuid.NewString(),
		EnvelopeFrom: "from@example.com",
		EnvelopeTo:   []string{"to@example.com"},
		RawMIME:      []byte("Subject: test\r\n\r\nbody"),
		Status:       status,
		CreatedAt:    time.Now().UTC(),
	}
}

func TestNewStore_CreatesFileIfMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "queue.json")

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("precondition failed: file already exists at %s", path)
	}

	_, err := NewStore(path, 10, time.Second, 5*time.Minute)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected queue file to be created at %s, stat error: %v", path, err)
	}
}

func TestNewStore_DoesNotOverwriteExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "queue.json")
	s, err := NewStore(path, 10, time.Second, 5*time.Minute)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	job := newTestJob(StatusPending)
	if err := s.Enqueue(job); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}

	// Reopen the same path.
	s2, err := NewStore(path, 10, time.Second, 5*time.Minute)
	if err != nil {
		t.Fatalf("NewStore() (reopen) error = %v", err)
	}
	jobs, err := s2.List("")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("List() returned %d jobs, want 1 (existing data should be preserved)", len(jobs))
	}
}

func TestEnqueueAndList_RoundTrip(t *testing.T) {
	s, _ := newTestStore(t, 10, time.Second, 5*time.Minute)

	job := newTestJob(StatusPending)
	if err := s.Enqueue(job); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}

	jobs, err := s.List("")
	if err != nil {
		t.Fatalf("List(\"\") error = %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("List() returned %d jobs, want 1", len(jobs))
	}
	got := jobs[0]
	if got.ID != job.ID {
		t.Errorf("ID = %q, want %q", got.ID, job.ID)
	}
	if got.EnvelopeFrom != job.EnvelopeFrom {
		t.Errorf("EnvelopeFrom = %q, want %q", got.EnvelopeFrom, job.EnvelopeFrom)
	}
	if len(got.EnvelopeTo) != 1 || got.EnvelopeTo[0] != "to@example.com" {
		t.Errorf("EnvelopeTo = %v, want [to@example.com]", got.EnvelopeTo)
	}
	if string(got.RawMIME) != string(job.RawMIME) {
		t.Errorf("RawMIME = %q, want %q", got.RawMIME, job.RawMIME)
	}
	if got.Status != StatusPending {
		t.Errorf("Status = %q, want %q", got.Status, StatusPending)
	}
}

func TestDequeue_OnlyPendingAndDue(t *testing.T) {
	s, _ := newTestStore(t, 10, time.Second, 5*time.Minute)

	now := time.Now().UTC()

	notYetDue := newTestJob(StatusPending)
	notYetDue.NextRetryAt = now.Add(1 * time.Hour)

	doneJob := newTestJob(StatusDone)

	failedJob := newTestJob(StatusFailed)
	failedJob.NextRetryAt = now.Add(-1 * time.Minute)

	deadJob := newTestJob(StatusDead)

	duePending := newTestJob(StatusPending)
	duePending.NextRetryAt = now.Add(-1 * time.Minute) // in the past: due

	for _, j := range []*Job{notYetDue, doneJob, failedJob, deadJob, duePending} {
		if err := s.Enqueue(j); err != nil {
			t.Fatalf("Enqueue() error = %v", err)
		}
	}

	got, err := s.Dequeue()
	if err != nil {
		t.Fatalf("Dequeue() error = %v", err)
	}
	if got == nil {
		t.Fatal("Dequeue() returned nil, want the due pending job")
	}
	if got.ID != duePending.ID {
		t.Errorf("Dequeue() returned job %s, want %s (the only pending+due job)", got.ID, duePending.ID)
	}
}

func TestDequeue_ZeroValueNextRetryAtIsDue(t *testing.T) {
	s, _ := newTestStore(t, 10, time.Second, 5*time.Minute)

	job := newTestJob(StatusPending) // NextRetryAt left at zero value
	if err := s.Enqueue(job); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}

	got, err := s.Dequeue()
	if err != nil {
		t.Fatalf("Dequeue() error = %v", err)
	}
	if got == nil || got.ID != job.ID {
		t.Fatalf("Dequeue() = %v, want job with zero-value NextRetryAt to be considered due", got)
	}
}

func TestDequeue_EmptyQueueReturnsNil(t *testing.T) {
	s, _ := newTestStore(t, 10, time.Second, 5*time.Minute)

	got, err := s.Dequeue()
	if err != nil {
		t.Fatalf("Dequeue() error = %v", err)
	}
	if got != nil {
		t.Errorf("Dequeue() = %v, want nil for empty queue", got)
	}
}

func TestMarkDone(t *testing.T) {
	s, _ := newTestStore(t, 10, time.Second, 5*time.Minute)
	job := newTestJob(StatusPending)
	if err := s.Enqueue(job); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}

	if err := s.MarkDone(job.ID); err != nil {
		t.Fatalf("MarkDone() error = %v", err)
	}

	jobs, err := s.List(StatusDone)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(jobs) != 1 || jobs[0].ID != job.ID {
		t.Errorf("List(StatusDone) = %v, want job %s marked done", jobs, job.ID)
	}
}

func TestMarkFailed_RetryProgressionAndBackoff(t *testing.T) {
	backoffBase := 1 * time.Second
	backoffCap := 10 * time.Second
	retryMax := 10 // high enough that we don't hit dead status during this test

	s, _ := newTestStore(t, retryMax, backoffBase, backoffCap)
	job := newTestJob(StatusPending)
	if err := s.Enqueue(job); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}

	// backoffFn (internal/queue/store.go) computes d := backoffBase, then
	// doubles it `retry` times — i.e. backoffBase * 2^RetryCount, not
	// backoffBase * 2^(RetryCount-1). So after the Nth failure (RetryCount==N)
	// the delta is backoffBase*2^N, capped at backoffCap (10s here).
	wantDeltas := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 10 * time.Second}
	const tolerance = 300 * time.Millisecond

	for i, wantDelta := range wantDeltas {
		before := time.Now().UTC()
		if err := s.MarkFailed(job.ID, "boom"); err != nil {
			t.Fatalf("MarkFailed() error = %v", err)
		}
		jobs, err := s.List("")
		if err != nil {
			t.Fatalf("List() error = %v", err)
		}
		var got *Job
		for _, j := range jobs {
			if j.ID == job.ID {
				got = j
			}
		}
		if got == nil {
			t.Fatalf("job %s not found after MarkFailed", job.ID)
		}
		if got.RetryCount != i+1 {
			t.Errorf("iteration %d: RetryCount = %d, want %d", i, got.RetryCount, i+1)
		}
		if got.Status != StatusFailed {
			t.Errorf("iteration %d: Status = %q, want %q", i, got.Status, StatusFailed)
		}
		if got.LastError != "boom" {
			t.Errorf("iteration %d: LastError = %q, want boom", i, got.LastError)
		}
		delta := got.NextRetryAt.Sub(before)
		if delta < wantDelta-tolerance || delta > wantDelta+tolerance {
			t.Errorf("iteration %d: NextRetryAt delta = %v, want ~%v", i, delta, wantDelta)
		}
	}
}

func TestMarkFailed_BackoffCappedAndDeadAtRetryMax(t *testing.T) {
	backoffBase := 1 * time.Second
	backoffCap := 3 * time.Second
	retryMax := 3

	s, _ := newTestStore(t, retryMax, backoffBase, backoffCap)
	job := newTestJob(StatusPending)
	if err := s.Enqueue(job); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}

	// 1st failure: retryCount=1, delta ~2s (base*2^1), below cap.
	if err := s.MarkFailed(job.ID, "e1"); err != nil {
		t.Fatalf("MarkFailed() error = %v", err)
	}
	jobs, _ := s.List("")
	got := findJob(jobs, job.ID)
	if got.Status != StatusFailed {
		t.Fatalf("after 1st failure: Status = %q, want failed", got.Status)
	}

	// 2nd failure: retryCount=2, delta should be capped at backoffCap (3s).
	before := time.Now().UTC()
	if err := s.MarkFailed(job.ID, "e2"); err != nil {
		t.Fatalf("MarkFailed() error = %v", err)
	}
	jobs, _ = s.List("")
	got = findJob(jobs, job.ID)
	if got.Status != StatusFailed {
		t.Fatalf("after 2nd failure: Status = %q, want failed", got.Status)
	}
	delta := got.NextRetryAt.Sub(before)
	if delta > backoffCap+300*time.Millisecond {
		t.Errorf("after 2nd failure: delta = %v, want capped at ~%v", delta, backoffCap)
	}

	// 3rd failure: retryCount=3 >= retryMax(3) -> status dead.
	if err := s.MarkFailed(job.ID, "e3"); err != nil {
		t.Fatalf("MarkFailed() error = %v", err)
	}
	jobs, _ = s.List("")
	got = findJob(jobs, job.ID)
	if got.Status != StatusDead {
		t.Errorf("after 3rd failure: Status = %q, want dead (RetryCount %d >= retryMax %d)", got.Status, got.RetryCount, retryMax)
	}
}

func findJob(jobs []*Job, id string) *Job {
	for _, j := range jobs {
		if j.ID == id {
			return j
		}
	}
	return nil
}

func TestRetryNow(t *testing.T) {
	s, _ := newTestStore(t, 3, time.Second, 5*time.Minute)
	job := newTestJob(StatusFailed)
	job.RetryCount = 2
	job.NextRetryAt = time.Now().UTC().Add(time.Hour)
	job.LastError = "boom"
	if err := s.Enqueue(job); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}

	if err := s.RetryNow(job.ID); err != nil {
		t.Fatalf("RetryNow() error = %v", err)
	}

	jobs, _ := s.List("")
	got := findJob(jobs, job.ID)
	if got.Status != StatusPending {
		t.Errorf("Status = %q, want pending", got.Status)
	}
	if got.RetryCount != 0 {
		t.Errorf("RetryCount = %d, want 0", got.RetryCount)
	}
	if !got.NextRetryAt.IsZero() {
		t.Errorf("NextRetryAt = %v, want zero value", got.NextRetryAt)
	}
	if got.LastError != "" {
		t.Errorf("LastError = %q, want empty", got.LastError)
	}
}

func TestRetryAll(t *testing.T) {
	s, _ := newTestStore(t, 3, time.Second, 5*time.Minute)

	failedJob := newTestJob(StatusFailed)
	failedJob.RetryCount = 1
	failedJob.NextRetryAt = time.Now().UTC().Add(time.Hour)

	deadJob := newTestJob(StatusDead)
	deadJob.RetryCount = 3

	doneJob := newTestJob(StatusDone)

	pendingJob := newTestJob(StatusPending)

	for _, j := range []*Job{failedJob, deadJob, doneJob, pendingJob} {
		if err := s.Enqueue(j); err != nil {
			t.Fatalf("Enqueue() error = %v", err)
		}
	}

	if err := s.RetryAll(); err != nil {
		t.Fatalf("RetryAll() error = %v", err)
	}

	jobs, _ := s.List("")
	gotFailed := findJob(jobs, failedJob.ID)
	gotDead := findJob(jobs, deadJob.ID)
	gotDone := findJob(jobs, doneJob.ID)
	gotPending := findJob(jobs, pendingJob.ID)

	if gotFailed.Status != StatusPending || gotFailed.RetryCount != 0 {
		t.Errorf("failedJob after RetryAll = %+v, want reset to pending/0", gotFailed)
	}
	if gotDead.Status != StatusPending || gotDead.RetryCount != 0 {
		t.Errorf("deadJob after RetryAll = %+v, want reset to pending/0", gotDead)
	}
	if gotDone.Status != StatusDone {
		t.Errorf("doneJob after RetryAll: Status = %q, want unchanged done", gotDone.Status)
	}
	if gotPending.Status != StatusPending {
		t.Errorf("pendingJob after RetryAll: Status = %q, want unchanged pending", gotPending.Status)
	}
}

func TestList_FiltersByStatus(t *testing.T) {
	s, _ := newTestStore(t, 10, time.Second, 5*time.Minute)

	pending1 := newTestJob(StatusPending)
	pending2 := newTestJob(StatusPending)
	done1 := newTestJob(StatusDone)
	dead1 := newTestJob(StatusDead)

	for _, j := range []*Job{pending1, pending2, done1, dead1} {
		if err := s.Enqueue(j); err != nil {
			t.Fatalf("Enqueue() error = %v", err)
		}
	}

	pendingJobs, err := s.List(StatusPending)
	if err != nil {
		t.Fatalf("List(StatusPending) error = %v", err)
	}
	if len(pendingJobs) != 2 {
		t.Errorf("List(StatusPending) returned %d jobs, want 2", len(pendingJobs))
	}

	doneJobs, err := s.List(StatusDone)
	if err != nil {
		t.Fatalf("List(StatusDone) error = %v", err)
	}
	if len(doneJobs) != 1 {
		t.Errorf("List(StatusDone) returned %d jobs, want 1", len(doneJobs))
	}

	allJobs, err := s.List("")
	if err != nil {
		t.Fatalf("List(\"\") error = %v", err)
	}
	if len(allJobs) != 4 {
		t.Errorf("List(\"\") returned %d jobs, want 4", len(allJobs))
	}
}

func TestPurge_RemovesOnlyDoneAndDead(t *testing.T) {
	s, _ := newTestStore(t, 10, time.Second, 5*time.Minute)

	pending := newTestJob(StatusPending)
	failed := newTestJob(StatusFailed)
	done := newTestJob(StatusDone)
	dead := newTestJob(StatusDead)

	for _, j := range []*Job{pending, failed, done, dead} {
		if err := s.Enqueue(j); err != nil {
			t.Fatalf("Enqueue() error = %v", err)
		}
	}

	if err := s.Purge(); err != nil {
		t.Fatalf("Purge() error = %v", err)
	}

	remaining, err := s.List("")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(remaining) != 2 {
		t.Fatalf("List() after Purge returned %d jobs, want 2", len(remaining))
	}
	ids := map[string]bool{}
	for _, j := range remaining {
		ids[j.ID] = true
	}
	if !ids[pending.ID] || !ids[failed.ID] {
		t.Errorf("Purge() removed a job it shouldn't have; remaining = %v", remaining)
	}
	if ids[done.ID] || ids[dead.ID] {
		t.Errorf("Purge() left a done/dead job behind; remaining = %v", remaining)
	}
}

func TestPendingCount(t *testing.T) {
	s, _ := newTestStore(t, 10, time.Second, 5*time.Minute)

	jobs := []*Job{
		newTestJob(StatusPending),
		newTestJob(StatusPending),
		newTestJob(StatusPending),
		newTestJob(StatusDone),
		newTestJob(StatusFailed),
		newTestJob(StatusDead),
	}
	for _, j := range jobs {
		if err := s.Enqueue(j); err != nil {
			t.Fatalf("Enqueue() error = %v", err)
		}
	}

	count, err := s.PendingCount()
	if err != nil {
		t.Fatalf("PendingCount() error = %v", err)
	}
	if count != 3 {
		t.Errorf("PendingCount() = %d, want 3", count)
	}
}

func TestUpdateJob_NonexistentIDReturnsError(t *testing.T) {
	s, _ := newTestStore(t, 10, time.Second, 5*time.Minute)

	err := s.updateJob("does-not-exist", func(j *Job) {})
	if err == nil {
		t.Fatal("updateJob() error = nil, want error for nonexistent ID")
	}
}

func TestMarkDone_NonexistentIDReturnsError(t *testing.T) {
	s, _ := newTestStore(t, 10, time.Second, 5*time.Minute)

	err := s.MarkDone("does-not-exist")
	if err == nil {
		t.Fatal("MarkDone() error = nil, want error for nonexistent ID")
	}
}
