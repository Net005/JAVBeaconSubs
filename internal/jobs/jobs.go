package jobs

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"beaconsubs/internal/config"
	"beaconsubs/internal/engine"
)

var videoExtensions = map[string]bool{".mp4": true, ".mkv": true, ".avi": true, ".mov": true, ".wmv": true, ".flv": true, ".webm": true, ".ts": true, ".m4v": true, ".mp3": true, ".wav": true, ".m4a": true, ".flac": true}

type Request struct {
	Inputs      []string `json:"inputs"`
	Recursive   bool     `json:"recursive"`
	Overwrite   bool     `json:"overwrite"`
	ExternalID  string   `json:"external_id,omitempty"`
	CallbackURL string   `json:"callback_url,omitempty"`
}

type Job struct {
	ID          string          `json:"id"`
	ExternalID  string          `json:"external_id,omitempty"`
	Status      string          `json:"status"`
	Phase       string          `json:"phase,omitempty"`
	Progress    int             `json:"progress"`
	Message     string          `json:"message,omitempty"`
	Inputs      []string        `json:"inputs"`
	Files       []string        `json:"files"`
	Results     []engine.Result `json:"results,omitempty"`
	Error       string          `json:"error,omitempty"`
	CallbackURL string          `json:"-"`
	Overwrite   bool            `json:"-"`
	CreatedAt   time.Time       `json:"created_at"`
	StartedAt   *time.Time      `json:"started_at,omitempty"`
	FinishedAt  *time.Time      `json:"finished_at,omitempty"`
	cancel      context.CancelFunc
}

type Manager struct {
	cfg         config.Config
	runner      *engine.Runner
	log         *slog.Logger
	mu          sync.RWMutex
	jobs        map[string]*Job
	queue       chan string
	subscribers map[chan []byte]struct{}
	persistence Persistence
}

type Persistence interface {
	Save(*Job) error
	Load() ([]*Job, error)
}

func New(cfg config.Config, runner *engine.Runner, persistence Persistence, log *slog.Logger) (*Manager, error) {
	m := &Manager{cfg: cfg, runner: runner, persistence: persistence, log: log, jobs: make(map[string]*Job), queue: make(chan string, 256), subscribers: make(map[chan []byte]struct{})}
	stored, err := persistence.Load()
	if err != nil {
		return nil, err
	}
	for _, job := range stored {
		if job.Status == "queued" || job.Status == "running" {
			job.Status = "failed"
			job.Error = "Service restarted before this job completed"
			job.Message = job.Error
			now := time.Now().UTC()
			job.FinishedAt = &now
			_ = persistence.Save(job)
		}
		m.jobs[job.ID] = job
	}
	return m, nil
}

func (m *Manager) Run(ctx context.Context) {
	for i := 0; i < m.cfg.Workers; i++ {
		go m.worker(ctx)
	}
	<-ctx.Done()
}

func (m *Manager) Create(req Request) (*Job, error) {
	if len(req.Inputs) == 0 {
		return nil, fmt.Errorf("inputs must contain at least one file or folder")
	}
	files, err := m.discover(req.Inputs, req.Recursive)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no supported media files found")
	}
	job := &Job{ID: newID(), ExternalID: req.ExternalID, Status: "queued", Progress: 0, Message: "Waiting for subtitle worker", Inputs: req.Inputs, Files: files, CallbackURL: req.CallbackURL, Overwrite: req.Overwrite, CreatedAt: time.Now().UTC()}
	m.mu.Lock()
	m.jobs[job.ID] = job
	m.mu.Unlock()
	m.publish(job)
	m.queue <- job.ID
	return clone(job), nil
}

func (m *Manager) Get(id string) (*Job, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	job, ok := m.jobs[id]
	if !ok {
		return nil, false
	}
	return clone(job), true
}

func (m *Manager) List() []*Job {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Job, 0, len(m.jobs))
	for _, j := range m.jobs {
		out = append(out, clone(j))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if len(out) > 100 {
		out = out[:100]
	}
	return out
}

func (m *Manager) Cancel(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	job, ok := m.jobs[id]
	if !ok {
		return false
	}
	if job.Status == "complete" || job.Status == "failed" || job.Status == "cancelled" {
		return true
	}
	job.Status = "cancelled"
	job.Message = "Cancellation requested"
	if job.cancel != nil {
		job.cancel()
	}
	go m.publish(clone(job))
	return true
}

func (m *Manager) Subscribe() (<-chan []byte, func()) {
	ch := make(chan []byte, 32)
	m.mu.Lock()
	m.subscribers[ch] = struct{}{}
	m.mu.Unlock()
	return ch, func() { m.mu.Lock(); delete(m.subscribers, ch); close(ch); m.mu.Unlock() }
}

func (m *Manager) worker(root context.Context) {
	for {
		select {
		case <-root.Done():
			return
		case id := <-m.queue:
			m.mu.Lock()
			job := m.jobs[id]
			if job == nil || job.Status == "cancelled" {
				m.mu.Unlock()
				continue
			}
			ctx, cancel := context.WithCancel(root)
			job.cancel = cancel
			now := time.Now().UTC()
			job.StartedAt = &now
			job.Status = "running"
			job.Message = "Starting"
			snapshot := clone(job)
			m.mu.Unlock()
			m.publish(snapshot)
			m.process(ctx, id)
			cancel()
		}
	}
}

func (m *Manager) process(ctx context.Context, id string) {
	m.mu.RLock()
	job := clone(m.jobs[id])
	m.mu.RUnlock()
	for index, file := range job.Files {
		progress := func(phase string, pct int, message string) {
			m.mu.Lock()
			current := m.jobs[id]
			if current.Status != "cancelled" {
				current.Phase = phase
				current.Progress = (index*100 + pct) / len(job.Files)
				current.Message = message
				snapshot := clone(current)
				m.mu.Unlock()
				m.publish(snapshot)
			} else {
				m.mu.Unlock()
			}
		}
		result, err := m.runner.Process(ctx, file, job.Overwrite, progress)
		if err != nil {
			m.finish(id, "failed", err.Error())
			return
		}
		m.mu.Lock()
		m.jobs[id].Results = append(m.jobs[id].Results, result)
		m.mu.Unlock()
	}
	m.finish(id, "complete", "")
}

func (m *Manager) finish(id, status, errorMessage string) {
	m.mu.Lock()
	job := m.jobs[id]
	if job.Status == "cancelled" {
		status = "cancelled"
	}
	job.Status = status
	job.Progress = 100
	job.Error = errorMessage
	if errorMessage != "" {
		job.Message = errorMessage
	} else if status == "complete" {
		job.Message = "All subtitles generated"
	}
	now := time.Now().UTC()
	job.FinishedAt = &now
	job.cancel = nil
	snapshot := clone(job)
	m.mu.Unlock()
	m.publish(snapshot)
	if snapshot.CallbackURL != "" {
		go callback(snapshot)
	}
}

func (m *Manager) publish(job *Job) {
	if err := m.persistence.Save(job); err != nil {
		m.log.Error("persist job", "job", job.ID, "error", err)
	}
	data, _ := json.Marshal(job)
	m.mu.RLock()
	defer m.mu.RUnlock()
	for ch := range m.subscribers {
		select {
		case ch <- data:
		default:
		}
	}
}

func (m *Manager) discover(inputs []string, recursive bool) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, input := range inputs {
		abs, err := filepath.Abs(input)
		if err != nil {
			return nil, err
		}
		abs = filepath.Clean(abs)
		if !m.allowed(abs) {
			return nil, fmt.Errorf("path is outside allowed_roots: %s", abs)
		}
		info, err := os.Stat(abs)
		if err != nil {
			return nil, fmt.Errorf("input %s: %w", abs, err)
		}
		if !info.IsDir() {
			if videoExtensions[strings.ToLower(filepath.Ext(abs))] && !seen[abs] {
				seen[abs] = true
				out = append(out, abs)
			}
			continue
		}
		walkErr := filepath.WalkDir(abs, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && path != abs && !recursive {
				return filepath.SkipDir
			}
			if !d.IsDir() && videoExtensions[strings.ToLower(filepath.Ext(path))] && !seen[path] {
				seen[path] = true
				out = append(out, path)
			}
			return nil
		})
		if walkErr != nil {
			return nil, walkErr
		}
	}
	sort.Strings(out)
	return out, nil
}

func (m *Manager) allowed(path string) bool {
	if m.cfg.UploadDir != "" {
		rel, err := filepath.Rel(m.cfg.UploadDir, path)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	if len(m.cfg.AllowedRoots) == 0 {
		return true
	}
	for _, root := range m.cfg.AllowedRoots {
		rel, err := filepath.Rel(root, path)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func clone(j *Job) *Job {
	data, _ := json.Marshal(j)
	var out Job
	_ = json.Unmarshal(data, &out)
	out.CallbackURL = j.CallbackURL
	out.Overwrite = j.Overwrite
	return &out
}
func newID() string { b := make([]byte, 8); _, _ = rand.Read(b); return "sub_" + hex.EncodeToString(b) }
func callback(job *Job) {
	data, _ := json.Marshal(job)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, job.CallbackURL, bytes.NewReader(data))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err == nil {
		_ = resp.Body.Close()
	}
}
