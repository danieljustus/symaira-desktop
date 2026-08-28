package selfhost

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const jobSchemaVersion = 1

var ErrNoPendingJob = errors.New("no pending job")

type Job struct {
	ID            string     `json:"id"`
	SchemaVersion int        `json:"schema_version"`
	Status        string     `json:"status"`
	SourcePath    string     `json:"source_path"`
	OriginalName  string     `json:"original_name"`
	ContentType   string     `json:"content_type,omitempty"`
	Capability    string     `json:"capability"`
	WorkerID      string     `json:"worker_id,omitempty"`
	Engine        string     `json:"engine,omitempty"`
	Model         string     `json:"model,omitempty"`
	NotePath      string     `json:"note_path,omitempty"`
	Error         string     `json:"error,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	LeaseUntil    *time.Time `json:"lease_until,omitempty"`
}

type JobStore struct {
	dir string
	mu  sync.Mutex
}

func NewJobStore(vaultRoot string) (*JobStore, error) {
	dir := filepath.Join(vaultRoot, ".symdesk", "server", "jobs")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create job store: %w", err)
	}
	return &JobStore{dir: dir}, nil
}

func NewJobID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (s *JobStore) Create(sourcePath, originalName, contentType string) (*Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, err := NewJobID()
	if err != nil {
		return nil, fmt.Errorf("create job id: %w", err)
	}
	now := time.Now().UTC()
	job := &Job{
		ID: id, SchemaVersion: jobSchemaVersion, Status: "pending",
		SourcePath: sourcePath, OriginalName: originalName, ContentType: contentType,
		Capability: "ocr", CreatedAt: now, UpdatedAt: now,
	}
	if err := s.writeLocked(job); err != nil {
		return nil, err
	}
	return job, nil
}

func (s *JobStore) List() ([]Job, error) {
	jobs, _, err := s.ListPage(0, 0)
	return jobs, err
}

// ListPage returns a newest-first page and the total number of jobs. A limit
// of zero returns all jobs after offset, preserving List's legacy behavior.
func (s *JobStore) ListPage(limit, offset int) ([]Job, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	jobs, err := s.readAllLocked()
	if err != nil {
		return nil, 0, err
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].CreatedAt.After(jobs[j].CreatedAt) })
	total := len(jobs)
	if offset < 0 {
		offset = 0
	}
	if offset >= total {
		return []Job{}, total, nil
	}
	end := total
	if limit > 0 && offset+limit < end {
		end = offset + limit
	}
	return jobs[offset:end], total, nil
}

func (s *JobStore) Get(id string) (*Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readLocked(id)
}

func (s *JobStore) Lease(workerID string, capabilities []string, duration time.Duration) (*Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	capable := false
	for _, capability := range capabilities {
		if capability == "ocr" {
			capable = true
			break
		}
	}
	if !capable {
		return nil, ErrNoPendingJob
	}

	jobs, err := s.readAllLocked()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].CreatedAt.Before(jobs[j].CreatedAt) })
	for i := range jobs {
		job := &jobs[i]
		if job.Status == "processing" && job.LeaseUntil != nil && job.LeaseUntil.Before(now) {
			job.Status = "pending"
			job.WorkerID = ""
			job.LeaseUntil = nil
			job.Error = ""
		}
		if job.Status != "pending" || job.Capability != "ocr" {
			continue
		}
		leaseUntil := now.Add(duration)
		job.Status = "processing"
		job.WorkerID = workerID
		job.LeaseUntil = &leaseUntil
		job.UpdatedAt = now
		if err := s.writeLocked(job); err != nil {
			return nil, err
		}
		clone := *job
		return &clone, nil
	}
	return nil, ErrNoPendingJob
}

func (s *JobStore) Complete(id, workerID, engine, model, notePath string) (*Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, err := s.readLocked(id)
	if err != nil {
		return nil, err
	}
	if job.Status != "processing" || job.WorkerID != workerID {
		return nil, fmt.Errorf("job is not leased by worker %q", workerID)
	}
	job.Status = "completed"
	job.Engine = engine
	job.Model = model
	job.NotePath = notePath
	job.LeaseUntil = nil
	job.Error = ""
	job.UpdatedAt = time.Now().UTC()
	if err := s.writeLocked(job); err != nil {
		return nil, err
	}
	return job, nil
}

func (s *JobStore) Fail(id, workerID, message string, retry bool) (*Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, err := s.readLocked(id)
	if err != nil {
		return nil, err
	}
	if job.Status != "processing" || job.WorkerID != workerID {
		return nil, fmt.Errorf("job is not leased by worker %q", workerID)
	}
	job.Status = "failed"
	if retry {
		job.Status = "pending"
	}
	job.Error = strings.TrimSpace(message)
	job.LeaseUntil = nil
	job.UpdatedAt = time.Now().UTC()
	if retry {
		job.WorkerID = ""
	}
	if err := s.writeLocked(job); err != nil {
		return nil, err
	}
	return job, nil
}

func (s *JobStore) Retry(id string) (*Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, err := s.readLocked(id)
	if err != nil {
		return nil, err
	}
	if job.Status != "failed" {
		return nil, fmt.Errorf("only failed jobs can be retried")
	}
	job.Status = "pending"
	job.WorkerID = ""
	job.LeaseUntil = nil
	job.Error = ""
	job.UpdatedAt = time.Now().UTC()
	if err := s.writeLocked(job); err != nil {
		return nil, err
	}
	return job, nil
}

func (s *JobStore) readAllLocked() ([]Job, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("read job store: %w", err)
	}
	jobs := make([]Job, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		job, err := s.readLocked(strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, *job)
	}
	return jobs, nil
}

func (s *JobStore) readLocked(id string) (*Job, error) {
	if !validJobID(id) {
		return nil, fmt.Errorf("invalid job id")
	}
	file, err := os.OpenInRoot(s.dir, id+".json")
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 1<<20 {
		return nil, fmt.Errorf("job file exceeds 1 MiB")
	}
	var job Job
	if err := json.Unmarshal(data, &job); err != nil {
		return nil, fmt.Errorf("decode job %s: %w", id, err)
	}
	return &job, nil
}

func (s *JobStore) writeLocked(job *Job) error {
	if !validJobID(job.ID) {
		return fmt.Errorf("invalid job id")
	}
	data, err := json.MarshalIndent(job, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(s.dir, job.ID+".json")
	tmp, err := os.CreateTemp(s.dir, ".job-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func validJobID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}
