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

	"javbeaconsubs/internal/config"
	"javbeaconsubs/internal/engine"
	profiles "javbeaconsubs/internal/profile"
	"javbeaconsubs/internal/release"
)

var videoExtensions = map[string]bool{".mp4": true, ".mkv": true, ".avi": true, ".mov": true, ".wmv": true, ".flv": true, ".webm": true, ".ts": true, ".m4v": true, ".mp3": true, ".wav": true, ".m4a": true, ".flac": true}

// Release metadata size limits (TODO Part 36): generous enough for any
// real title/synopsis, small enough to reject an absurd payload outright.
const (
	maxReleaseTitleBytes = 2 * 1024
	maxReleaseStoryBytes = 32 * 1024
)

type Request struct {
	Inputs       []string `json:"inputs"`
	Recursive    bool     `json:"recursive"`
	Overwrite    bool     `json:"overwrite"`
	KeepJapanese *bool    `json:"keep_japanese,omitempty"`
	WriteASS     *bool    `json:"write_ass,omitempty"`
	ASRMode      string   `json:"asr_mode,omitempty"`
	ASRProfile   string   `json:"asr_profile,omitempty"`
	Profile      string   `json:"profile,omitempty"`
	DebugMode    bool     `json:"debug_mode,omitempty"`
	ExternalID   string   `json:"external_id,omitempty"`
	CallbackURL  string   `json:"callback_url,omitempty"`

	// Release metadata (all optional; see internal/release for lookup
	// precedence). ReleaseExternalID falls back to ExternalID when empty so
	// existing clients/jobs keep working unchanged.
	ReleaseExternalID  string `json:"release_external_id,omitempty"`
	JAVBeaconReleaseID *int64 `json:"javbeacon_release_id,omitempty"`
	ReleaseTitle       string `json:"release_title,omitempty"`
	ReleaseStory       string `json:"release_story,omitempty"`

	// FileReleaseOverrides sets per-file release metadata for a batch job
	// (TODO Part 33), keyed by the same path used in Inputs for that file.
	// Only meaningful for multi-file batch submission via the JSON "paths"
	// API; a file with no entry here uses the job-level release fields
	// above. Not supported for folder-expanded inputs, since the caller
	// cannot know the expanded paths in advance.
	FileReleaseOverrides map[string]ReleaseOverrideRequest `json:"file_release_overrides,omitempty"`
}

// ReleaseOverrideRequest is one file's release metadata override inside a
// batch job (TODO Part 33). Same fields and semantics as the job-level
// equivalents on Request.
type ReleaseOverrideRequest struct {
	ReleaseExternalID  string `json:"release_external_id,omitempty"`
	JAVBeaconReleaseID *int64 `json:"javbeacon_release_id,omitempty"`
	ReleaseTitle       string `json:"release_title,omitempty"`
	ReleaseStory       string `json:"release_story,omitempty"`
}

type Job struct {
	ID                   string                         `json:"id"`
	ExternalID           string                         `json:"external_id,omitempty"`
	ReleaseExternalID    string                         `json:"release_external_id,omitempty"`
	JAVBeaconReleaseID   *int64                         `json:"javbeacon_release_id,omitempty"`
	ReleaseTitle         string                         `json:"release_title,omitempty"`
	ReleaseStory         string                         `json:"release_story,omitempty"`
	ReleaseTitleSource   string                         `json:"release_title_source,omitempty"`
	ReleaseStorySource   string                         `json:"release_story_source,omitempty"`
	ReleaseLookupMethod  string                         `json:"release_lookup_method,omitempty"`
	ReleaseLookupMatched bool                           `json:"release_lookup_matched,omitempty"`
	ReleaseLookupError   string                         `json:"release_lookup_error,omitempty"`
	ReleaseProvider      string                         `json:"release_provider,omitempty"`
	ReleaseStashSceneID  string                         `json:"release_stash_scene_id,omitempty"`
	ReleaseStashURL      string                         `json:"release_stash_url,omitempty"`
	Status               string                         `json:"status"`
	Phase                string                         `json:"phase,omitempty"`
	Progress             int                            `json:"progress"`
	Message              string                         `json:"message,omitempty"`
	CurrentPath          string                         `json:"current_path,omitempty"`
	CurrentFile          string                         `json:"current_file,omitempty"`
	CurrentFileNumber    int                            `json:"current_file_number,omitempty"`
	ETASeconds           int64                          `json:"eta_seconds,omitempty"`
	EstimatedFinishAt    *time.Time                     `json:"estimated_finish_at,omitempty"`
	Inputs               []string                       `json:"inputs"`
	Files                []string                       `json:"files"`
	Results              []engine.Result                `json:"results,omitempty"`
	Error                string                         `json:"error,omitempty"`
	CallbackURL          string                         `json:"-"`
	Overwrite            bool                           `json:"-"`
	KeepJapanese         bool                           `json:"keep_japanese"`
	WriteASS             bool                           `json:"write_ass"`
	ASRMode              string                         `json:"asr_mode"`
	ASRProfile           string                         `json:"asr_profile"`
	Profile              string                         `json:"profile"`
	ProfileSource        string                         `json:"profile_source,omitempty"`
	ASRModeSource        string                         `json:"asr_mode_source,omitempty"`
	FileSettings         map[string]profiles.Resolution `json:"file_settings,omitempty"`
	FileReleaseMetadata  map[string]FileReleaseMetadata `json:"file_release_metadata,omitempty"`
	DebugMode            bool                           `json:"debug_mode,omitempty"`
	CreatedAt            time.Time                      `json:"created_at"`
	StartedAt            *time.Time                     `json:"started_at,omitempty"`
	FinishedAt           *time.Time                     `json:"finished_at,omitempty"`
	PostProcessingStatus string                         `json:"post_processing_status,omitempty"`
	PostProcessingError  string                         `json:"post_processing_error,omitempty"`
	cancel               context.CancelFunc
}

// FileReleaseMetadata is one file's resolved release metadata inside a
// batch job (TODO Part 33) - present only for files with an explicit
// override in Request.FileReleaseOverrides; absence means "use the
// job-level release fields," mirroring FileSettings' fallback shape.
type FileReleaseMetadata struct {
	ReleaseExternalID    string `json:"release_external_id,omitempty"`
	JAVBeaconReleaseID   *int64 `json:"javbeacon_release_id,omitempty"`
	ReleaseTitle         string `json:"release_title,omitempty"`
	ReleaseStory         string `json:"release_story,omitempty"`
	ReleaseTitleSource   string `json:"release_title_source,omitempty"`
	ReleaseStorySource   string `json:"release_story_source,omitempty"`
	ReleaseLookupMethod  string `json:"release_lookup_method,omitempty"`
	ReleaseLookupMatched bool   `json:"release_lookup_matched,omitempty"`
	ReleaseLookupError   string `json:"release_lookup_error,omitempty"`
	ReleaseProvider      string `json:"release_provider,omitempty"`
	ReleaseStashSceneID  string `json:"release_stash_scene_id,omitempty"`
	ReleaseStashURL      string `json:"release_stash_url,omitempty"`
}

type Manager struct {
	cfg           config.Config
	runner        *engine.Runner
	log           *slog.Logger
	mu            sync.RWMutex
	jobs          map[string]*Job
	queue         chan string
	subscribers   map[chan []byte]struct{}
	persistence   Persistence
	post          *PostProcessor
	releaseClient *release.Client
}

type Persistence interface {
	Save(*Job) error
	Load() ([]*Job, error)
}

func New(cfg config.Config, runner *engine.Runner, persistence Persistence, post *PostProcessor, log *slog.Logger) (*Manager, error) {
	releaseTimeout := time.Duration(cfg.JAVBeacon.TimeoutSec) * time.Second
	releaseClient := release.NewClient(cfg.JAVBeacon.BaseURL, cfg.JAVBeacon.APIKey, releaseTimeout)
	m := &Manager{cfg: cfg, runner: runner, persistence: persistence, post: post, log: log, jobs: make(map[string]*Job), queue: make(chan string, 256), subscribers: make(map[chan []byte]struct{}), releaseClient: releaseClient}
	stored, err := persistence.Load()
	if err != nil {
		return nil, err
	}
	for _, job := range stored {
		job.ASRProfile = config.NormalizeProfile(job.ASRProfile)
		if job.ASRProfile == "" {
			job.ASRProfile = cfg.Profiles.DefaultProfile
		}
		job.Profile = job.ASRProfile
		if job.ProfileSource == "" {
			job.ProfileSource = "legacy"
		}
		if job.ASRModeSource == "" {
			job.ASRModeSource = "legacy"
		}
		if job.ReleaseExternalID == "" {
			job.ReleaseExternalID = job.ExternalID
		}
		if job.ReleaseTitleSource == "" && job.ReleaseTitle != "" {
			job.ReleaseTitleSource = "legacy"
		}
		if job.ReleaseStorySource == "" && job.ReleaseStory != "" {
			job.ReleaseStorySource = "legacy"
		}
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

// resolveFileRelease validates release metadata size limits and resolves
// it against JAVBeacon/manual overrides (TODO Part 33/36). Shared by the
// job-level default (Create()) and every per-file override, so the
// validate-then-resolve logic exists exactly once.
func (m *Manager) resolveFileRelease(javbeaconID *int64, externalID, title, story string) (release.Resolution, error) {
	title = strings.TrimSpace(title)
	if len(title) > maxReleaseTitleBytes {
		return release.Resolution{}, fmt.Errorf("release_title exceeds %d bytes", maxReleaseTitleBytes)
	}
	story = strings.Trim(story, " \t\r\n")
	if len(story) > maxReleaseStoryBytes {
		return release.Resolution{}, fmt.Errorf("release_story exceeds %d bytes", maxReleaseStoryBytes)
	}
	m.mu.RLock()
	client := m.releaseClient
	m.mu.RUnlock()
	return release.Resolve(context.Background(), client, release.Request{
		JAVBeaconReleaseID: javbeaconID,
		ReleaseExternalID:  externalID,
		ManualTitle:        title,
		ManualStory:        story,
	})
}

// JAVBeacon returns the current JAVBeacon lookup client configuration.
func (m *Manager) JAVBeacon() config.JAVBeaconConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg.JAVBeacon
}

// UpdateJAVBeacon replaces the JAVBeacon lookup client configuration and
// rebuilds the underlying HTTP client (base URL, API key, and timeout can
// all change), mirroring engine.Runner.UpdateTranslation's client-rebuild
// pattern. release.NewClient returns nil for an empty BaseURL, so clearing
// it here correctly disables lookups for every job created afterward.
func (m *Manager) UpdateJAVBeacon(value config.JAVBeaconConfig) {
	client := release.NewClient(value.BaseURL, value.APIKey, time.Duration(value.TimeoutSec)*time.Second)
	m.mu.Lock()
	m.cfg.JAVBeacon = value
	m.releaseClient = client
	m.mu.Unlock()
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
	req.ASRMode = config.NormalizeASRMode(req.ASRMode)
	if req.ASRMode != "" && req.ASRMode != "fast" && req.ASRMode != "balanced" && req.ASRMode != "high_accuracy" {
		return nil, fmt.Errorf("asr_mode must be fast, balanced, or high_accuracy")
	}
	if strings.TrimSpace(req.Profile) != "" {
		req.ASRProfile = req.Profile
	}
	req.ASRProfile = config.NormalizeProfile(req.ASRProfile)
	if req.ASRProfile != "" && req.ASRProfile != "standard" && req.ASRProfile != "jav" && req.ASRProfile != "giga" {
		return nil, fmt.Errorf("profile must be standard, jav, or giga")
	}
	releaseExternalID := strings.TrimSpace(req.ReleaseExternalID)
	if releaseExternalID == "" {
		releaseExternalID = strings.TrimSpace(req.ExternalID)
	}
	resolution, err := m.resolveFileRelease(req.JAVBeaconReleaseID, releaseExternalID, req.ReleaseTitle, req.ReleaseStory)
	if err != nil {
		return nil, err
	}
	fileReleaseMetadata := make(map[string]FileReleaseMetadata, len(req.FileReleaseOverrides))
	if len(req.FileReleaseOverrides) > 0 {
		discovered := make(map[string]bool, len(files))
		for _, f := range files {
			discovered[f] = true
		}
		for rawKey, override := range req.FileReleaseOverrides {
			key, absErr := filepath.Abs(rawKey)
			if absErr == nil {
				key = filepath.Clean(key)
			} else {
				key = rawKey
			}
			if !discovered[key] {
				return nil, fmt.Errorf("file_release_overrides references a file not in this job: %s", rawKey)
			}
			fileExternalID := strings.TrimSpace(override.ReleaseExternalID)
			fileResolution, resolveErr := m.resolveFileRelease(override.JAVBeaconReleaseID, fileExternalID, override.ReleaseTitle, override.ReleaseStory)
			if resolveErr != nil {
				return nil, fmt.Errorf("file_release_overrides[%s]: %w", rawKey, resolveErr)
			}
			fileReleaseMetadata[key] = FileReleaseMetadata{
				ReleaseExternalID: fileExternalID, JAVBeaconReleaseID: override.JAVBeaconReleaseID,
				ReleaseTitle: fileResolution.Title, ReleaseStory: fileResolution.Story,
				ReleaseTitleSource: fileResolution.TitleSource, ReleaseStorySource: fileResolution.StorySource,
				ReleaseLookupMethod: fileResolution.LookupMethod, ReleaseLookupMatched: fileResolution.LookupMatched, ReleaseLookupError: fileResolution.LookupError,
				ReleaseProvider: fileResolution.Provider, ReleaseStashSceneID: fileResolution.StashSceneID, ReleaseStashURL: fileResolution.StashURL,
			}
		}
	}
	fileSettings := make(map[string]profiles.Resolution, len(files))
	profileSettings := m.Profiles()
	for _, file := range files {
		fileSettings[file] = profiles.Resolve(profileSettings, file, req.ASRProfile, req.ASRMode)
	}
	firstResolution := fileSettings[files[0]]
	keepJapanese := m.cfg.Output.KeepJapanese
	if req.KeepJapanese != nil {
		keepJapanese = *req.KeepJapanese
	}
	writeASS := m.cfg.Output.WriteASS
	if req.WriteASS != nil {
		writeASS = *req.WriteASS
	}
	job := &Job{
		ID: newID(), ExternalID: req.ExternalID, Status: "queued", Progress: 0, Message: "Waiting for subtitle worker",
		Inputs: req.Inputs, Files: files, CallbackURL: req.CallbackURL, Overwrite: req.Overwrite, KeepJapanese: keepJapanese, WriteASS: writeASS,
		ASRMode: firstResolution.ASRMode, ASRProfile: firstResolution.Profile, Profile: firstResolution.Profile,
		ProfileSource: firstResolution.ProfileSource, ASRModeSource: firstResolution.ASRModeSource, FileSettings: fileSettings,
		DebugMode: req.DebugMode, CreatedAt: time.Now().UTC(),
		ReleaseExternalID: releaseExternalID, JAVBeaconReleaseID: req.JAVBeaconReleaseID,
		ReleaseTitle: resolution.Title, ReleaseStory: resolution.Story,
		ReleaseTitleSource: resolution.TitleSource, ReleaseStorySource: resolution.StorySource,
		ReleaseLookupMethod: resolution.LookupMethod, ReleaseLookupMatched: resolution.LookupMatched, ReleaseLookupError: resolution.LookupError,
		ReleaseProvider: resolution.Provider, ReleaseStashSceneID: resolution.StashSceneID, ReleaseStashURL: resolution.StashURL,
		FileReleaseMetadata: fileReleaseMetadata,
	}
	m.mu.Lock()
	m.jobs[job.ID] = job
	m.mu.Unlock()
	m.publish(job)
	m.queue <- job.ID
	return clone(job), nil
}

func (m *Manager) Get(id string) (*Job, bool) {
	m.mu.RLock()
	job, ok := m.jobs[id]
	if ok {
		result := clone(job)
		m.mu.RUnlock()
		return result, true
	}
	m.mu.RUnlock()
	if store, supportsGet := m.persistence.(interface {
		GetJob(string) (*Job, bool, error)
	}); supportsGet {
		stored, found, err := store.GetJob(id)
		return stored, found && err == nil
	}
	return nil, false
}

func (m *Manager) Profiles() config.ProfilesConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg.Profiles
}

func (m *Manager) UpdateProfiles(value config.ProfilesConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cfg.Profiles = value
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

type Page struct {
	Jobs     []*Job `json:"jobs"`
	Page     int    `json:"page"`
	PageSize int    `json:"page_size"`
	Total    int    `json:"total"`
	Pages    int    `json:"pages"`
}

type pagePersistence interface {
	ListPage(page, pageSize int, filter string) ([]*Job, int, error)
}

func (m *Manager) ListPage(page, pageSize int, filter string) (Page, error) {
	if page < 1 {
		page = 1
	}
	if pageSize != 25 && pageSize != 50 && pageSize != 100 {
		pageSize = 50
	}
	if store, ok := m.persistence.(pagePersistence); ok {
		items, total, err := store.ListPage(page, pageSize, filter)
		if err != nil {
			return Page{}, err
		}
		pages := (total + pageSize - 1) / pageSize
		return Page{Jobs: items, Page: page, PageSize: pageSize, Total: total, Pages: pages}, nil
	}
	items := m.List()
	return Page{Jobs: items, Page: 1, PageSize: len(items), Total: len(items), Pages: 1}, nil
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
	translationMemory := engine.NewTranslationMemory()
	profileSettings := m.Profiles()
	for index, file := range job.Files {
		resolution := job.FileSettings[file]
		if resolution.Profile == "" {
			resolution = profiles.Resolve(profileSettings, file, job.ASRProfile, job.ASRMode)
		}
		m.mu.Lock()
		current := m.jobs[id]
		current.CurrentPath = file
		current.CurrentFile = filepath.Base(file)
		current.CurrentFileNumber = index + 1
		current.ASRProfile = resolution.Profile
		current.Profile = resolution.Profile
		current.ASRMode = resolution.ASRMode
		current.ProfileSource = resolution.ProfileSource
		current.ASRModeSource = resolution.ASRModeSource
		startSnapshot := clone(current)
		m.mu.Unlock()
		m.publish(startSnapshot)
		progress := func(phase string, pct int, message string) {
			m.mu.Lock()
			current := m.jobs[id]
			if current.Status != "cancelled" {
				current.Phase = phase
				current.Progress = (index*100 + pct) / len(job.Files)
				current.Message = message
				if current.StartedAt != nil && current.Progress > 0 && current.Progress < 100 {
					elapsed := time.Since(*current.StartedAt)
					remaining := time.Duration(float64(elapsed) * float64(100-current.Progress) / float64(current.Progress))
					finish := time.Now().UTC().Add(remaining)
					current.ETASeconds = int64(remaining.Round(time.Second) / time.Second)
					current.EstimatedFinishAt = &finish
				}
				snapshot := clone(current)
				m.mu.Unlock()
				m.publish(snapshot)
			} else {
				m.mu.Unlock()
			}
		}
		titleKey, releaseTitle, releaseStory := job.ReleaseExternalID, job.ReleaseTitle, job.ReleaseStory
		if override, ok := job.FileReleaseMetadata[file]; ok {
			titleKey, releaseTitle, releaseStory = override.ReleaseExternalID, override.ReleaseTitle, override.ReleaseStory
		}
		result, err := m.runner.ProcessWithOptions(ctx, file, job.Overwrite, job.KeepJapanese, engine.ProcessOptions{ASRMode: resolution.ASRMode, ASRProfile: resolution.Profile, DebugMode: job.DebugMode, Title: titleKey, ReleaseTitle: releaseTitle, ReleaseStory: releaseStory, WriteASS: &job.WriteASS}, progress, translationMemory)
		if err != nil {
			m.finish(id, "failed", err.Error())
			return
		}
		m.mu.Lock()
		m.jobs[id].Results = append(m.jobs[id].Results, result)
		m.mu.Unlock()
	}
	status := "complete"
	if allResultsSkipped(jobResultSnapshot(m, id)) {
		status = "skipped"
	}
	m.finish(id, status, "")
}

func jobResultSnapshot(m *Manager, id string) []engine.Result {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]engine.Result(nil), m.jobs[id].Results...)
}

func allResultsSkipped(results []engine.Result) bool {
	if len(results) == 0 {
		return false
	}
	for _, result := range results {
		if !result.Skipped {
			return false
		}
	}
	return true
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
	} else if status == "complete" || status == "skipped" {
		job.Message = completionMessage(job.Results)
	}
	now := time.Now().UTC()
	job.FinishedAt = &now
	job.ETASeconds = 0
	job.EstimatedFinishAt = nil
	job.cancel = nil
	snapshot := clone(job)
	m.mu.Unlock()
	m.publish(snapshot)
	if snapshot.CallbackURL != "" {
		go callback(snapshot)
	}
	if status == "complete" && m.post != nil && m.post.Enabled() {
		go m.runPostProcessing(id)
	}
}

func completionMessage(results []engine.Result) string {
	skipped := 0
	for _, result := range results {
		if result.Skipped {
			skipped++
		}
	}
	if skipped == 0 {
		return "All subtitles generated"
	}
	if skipped == len(results) {
		return fmt.Sprintf("Skipped %d existing file(s); enable Replace existing subtitles to rerun recognition or change accuracy", skipped)
	}
	return fmt.Sprintf("Generated %d file(s); skipped %d existing file(s)", len(results)-skipped, skipped)
}

func (m *Manager) runPostProcessing(id string) {
	m.mu.Lock()
	job := m.jobs[id]
	job.PostProcessingStatus = "running"
	job.PostProcessingError = ""
	snapshot := clone(job)
	m.mu.Unlock()
	m.publish(snapshot)
	err := m.post.Run(context.Background(), snapshot)
	m.mu.Lock()
	job = m.jobs[id]
	if err != nil {
		job.PostProcessingStatus = "failed"
		job.PostProcessingError = err.Error()
	} else {
		job.PostProcessingStatus = "complete"
	}
	snapshot = clone(job)
	m.mu.Unlock()
	m.publish(snapshot)
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
