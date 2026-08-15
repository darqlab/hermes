package queue

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

type JobStatus string

const (
	StatusPending JobStatus = "pending"
	StatusDone    JobStatus = "done"
	StatusFailed  JobStatus = "failed"
	StatusDead    JobStatus = "dead"
)

type Job struct {
	ID           string    `json:"id"`
	EnvelopeFrom string    `json:"envelope_from"`
	EnvelopeTo   []string  `json:"envelope_to"`
	RawMIME      []byte    `json:"raw_mime"`
	RetryCount   int       `json:"retry_count"`
	NextRetryAt  time.Time `json:"next_retry_at"`
	Status       JobStatus `json:"status"`
	Account      string    `json:"account,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	LastError    string    `json:"last_error,omitempty"`
}

type Store struct {
	mu        sync.Mutex
	path      string
	retryMax  int
	backoffFn func(int) time.Duration
}

func NewStore(dbPath string, retryMax int, backoffBase, backoffCap time.Duration) (*Store, error) {
	s := &Store{
		path:     dbPath,
		retryMax: retryMax,
		backoffFn: func(retry int) time.Duration {
			d := backoffBase
			for i := 1; i < retry; i++ {
				d *= 2
				if d > backoffCap {
					return backoffCap
				}
			}
			return d
		},
	}

	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		if err := s.writeJobs(nil); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *Store) Close() error { return nil }

func (s *Store) Enqueue(job *Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	jobs, err := s.readJobs()
	if err != nil {
		return err
	}
	jobs = append(jobs, job)
	return s.writeJobs(jobs)
}

func (s *Store) Dequeue() (*Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	jobs, err := s.readJobs()
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	for i, j := range jobs {
		if j.Status == StatusPending && !j.NextRetryAt.After(now) {
			return jobs[i], nil
		}
	}
	return nil, nil
}

func (s *Store) MarkDone(id string) error {
	return s.updateJob(id, func(j *Job) {
		j.Status = StatusDone
	})
}

func (s *Store) MarkFailed(id string, errMsg string) error {
	return s.updateJob(id, func(j *Job) {
		j.RetryCount++
		if j.RetryCount >= s.retryMax {
			j.Status = StatusDead
		} else {
			j.Status = StatusFailed
			j.NextRetryAt = time.Now().UTC().Add(s.backoffFn(j.RetryCount))
		}
		j.LastError = errMsg
	})
}

func (s *Store) RetryNow(id string) error {
	return s.updateJob(id, func(j *Job) {
		j.Status = StatusPending
		j.RetryCount = 0
		j.NextRetryAt = time.Time{}
		j.LastError = ""
	})
}

func (s *Store) RetryAll() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	jobs, err := s.readJobs()
	if err != nil {
		return err
	}
	for i := range jobs {
		if jobs[i].Status == StatusFailed || jobs[i].Status == StatusDead {
			jobs[i].Status = StatusPending
			jobs[i].RetryCount = 0
			jobs[i].NextRetryAt = time.Time{}
			jobs[i].LastError = ""
		}
	}
	return s.writeJobs(jobs)
}

func (s *Store) List(status JobStatus) ([]*Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	jobs, err := s.readJobs()
	if err != nil {
		return nil, err
	}

	if status == "" {
		return jobs, nil
	}
	var filtered []*Job
	for _, j := range jobs {
		if j.Status == status {
			filtered = append(filtered, j)
		}
	}
	return filtered, nil
}

func (s *Store) Purge() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	jobs, err := s.readJobs()
	if err != nil {
		return err
	}
	var kept []*Job
	for _, j := range jobs {
		if j.Status != StatusDone && j.Status != StatusDead {
			kept = append(kept, j)
		}
	}
	return s.writeJobs(kept)
}

func (s *Store) PendingCount() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	jobs, err := s.readJobs()
	if err != nil {
		return 0, err
	}
	count := 0
	for _, j := range jobs {
		if j.Status == StatusPending {
			count++
		}
	}
	return count, nil
}

func (s *Store) updateJob(id string, fn func(*Job)) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	jobs, err := s.readJobs()
	if err != nil {
		return err
	}
	found := false
	for _, j := range jobs {
		if j.ID == id {
			fn(j)
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("job %s not found", id)
	}
	return s.writeJobs(jobs)
}

func (s *Store) readJobs() ([]*Job, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}
	var jobs []*Job
	if err := json.Unmarshal(data, &jobs); err != nil {
		return nil, fmt.Errorf("parse queue file: %w", err)
	}
	return jobs, nil
}

func (s *Store) writeJobs(jobs []*Job) error {
	if jobs == nil {
		jobs = []*Job{}
	}
	data, err := json.MarshalIndent(jobs, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal queue: %w", err)
	}
	data = append(data, '\n')
	return os.WriteFile(s.path, data, 0600)
}
