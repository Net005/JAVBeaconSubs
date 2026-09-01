package server

import (
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"javbeaconsubs/internal/auth"
	"javbeaconsubs/internal/config"
	"javbeaconsubs/internal/engine"
	"javbeaconsubs/internal/jobs"
)

//go:embed web/*
var assets embed.FS

type Server struct {
	cfg      config.Config
	jobs     *jobs.Manager
	runner   *engine.Runner
	auth     *auth.Manager
	post     *jobs.PostProcessor
	settings SettingsStore
	log      *slog.Logger
}

type SettingsStore interface {
	SaveTranslation(config.TranslationConfig) error
	SavePostProcessing(config.PostProcessingConfig) error
}

func New(cfg config.Config, manager *jobs.Manager, runner *engine.Runner, authManager *auth.Manager, post *jobs.PostProcessor, settings SettingsStore, log *slog.Logger) *Server {
	return &Server{cfg: cfg, jobs: manager, runner: runner, auth: authManager, post: post, settings: settings, log: log}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.index)
	mux.HandleFunc("GET /api/v1/session", s.sessionStatus)
	mux.HandleFunc("POST /api/v1/session", s.login)
	mux.HandleFunc("POST /api/v1/session/setup", s.setupAccount)
	mux.HandleFunc("DELETE /api/v1/session", s.logout)
	mux.HandleFunc("GET /api/v1/health", s.health)
	mux.HandleFunc("GET /api/v1/settings", s.getSettings)
	mux.HandleFunc("PUT /api/v1/settings", s.updateSettings)
	mux.HandleFunc("GET /api/v1/jobs", s.listJobs)
	mux.HandleFunc("POST /api/v1/jobs", s.createJob)
	mux.HandleFunc("POST /api/v1/jobs/upload", s.uploadJob)
	mux.HandleFunc("GET /api/v1/jobs/{id}", s.getJob)
	mux.HandleFunc("GET /api/v1/jobs/{id}/outputs/{index}/{language}", s.downloadOutput)
	mux.HandleFunc("DELETE /api/v1/jobs/{id}", s.cancelJob)
	mux.HandleFunc("GET /api/v1/events", s.events)
	return s.middleware(mux)
}

type translationSettingsResponse struct {
	Mode               string                     `json:"mode"`
	BaseURL            string                     `json:"base_url"`
	Model              string                     `json:"model"`
	BatchSize          int                        `json:"batch_size"`
	TimeoutSec         int                        `json:"timeout_seconds"`
	ContextGapMS       int64                      `json:"context_gap_ms"`
	TranslationMemory  bool                       `json:"translation_memory"`
	Glossary           string                     `json:"glossary"`
	StructuredGlossary *config.StructuredGlossary `json:"structured_glossary,omitempty"`
	APIKeySet          bool                       `json:"api_key_set"`
}

type translationSettingsRequest struct {
	Mode               string                     `json:"mode"`
	BaseURL            string                     `json:"base_url"`
	APIKey             string                     `json:"api_key"`
	ClearAPIKey        bool                       `json:"clear_api_key"`
	Model              string                     `json:"model"`
	BatchSize          int                        `json:"batch_size"`
	TimeoutSec         int                        `json:"timeout_seconds"`
	ContextGapMS       int64                      `json:"context_gap_ms"`
	TranslationMemory  bool                       `json:"translation_memory"`
	Glossary           string                     `json:"glossary"`
	StructuredGlossary *config.StructuredGlossary `json:"structured_glossary"`
}

type postProcessingSettingsResponse struct {
	Mode           string `json:"mode"`
	ShellScript    string `json:"shell_script"`
	WebhookURL     string `json:"webhook_url"`
	TimeoutSec     int    `json:"timeout_seconds"`
	BearerTokenSet bool   `json:"bearer_token_set"`
}

type postProcessingSettingsRequest struct {
	Mode               string `json:"mode"`
	ShellScript        string `json:"shell_script"`
	WebhookURL         string `json:"webhook_url"`
	WebhookBearerToken string `json:"webhook_bearer_token"`
	ClearBearerToken   bool   `json:"clear_bearer_token"`
	TimeoutSec         int    `json:"timeout_seconds"`
}

type settingsRequest struct {
	Translation    translationSettingsRequest    `json:"translation"`
	PostProcessing postProcessingSettingsRequest `json:"post_processing"`
}

func settingsResponse(value config.TranslationConfig) translationSettingsResponse {
	return translationSettingsResponse{Mode: value.Mode, BaseURL: value.BaseURL, Model: value.Model, BatchSize: value.BatchSize, TimeoutSec: value.TimeoutSec, ContextGapMS: value.ContextGapMS, TranslationMemory: value.TranslationMemory, Glossary: value.Glossary, StructuredGlossary: value.StructuredGlossary, APIKeySet: value.APIKey != ""}
}

func (s *Server) getSettings(w http.ResponseWriter, _ *http.Request) {
	post := s.post.Config()
	writeJSON(w, 200, map[string]any{"translation": settingsResponse(s.runner.Translation()), "post_processing": postProcessingSettingsResponse{Mode: post.Mode, ShellScript: post.ShellScript, WebhookURL: post.WebhookURL, TimeoutSec: post.TimeoutSec, BearerTokenSet: post.WebhookBearerToken != ""}})
}

func (s *Server) updateSettings(w http.ResponseWriter, r *http.Request) {
	var request settingsRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid settings: " + err.Error()})
		return
	}
	current := s.runner.Translation()
	t := request.Translation
	value := config.TranslationConfig{Mode: t.Mode, BaseURL: t.BaseURL, Model: t.Model, BatchSize: t.BatchSize, TimeoutSec: t.TimeoutSec, ContextGapMS: t.ContextGapMS, TranslationMemory: t.TranslationMemory, Glossary: t.Glossary, StructuredGlossary: t.StructuredGlossary, APIKey: current.APIKey}
	if t.ClearAPIKey {
		value.APIKey = ""
	} else if strings.TrimSpace(t.APIKey) != "" {
		value.APIKey = strings.TrimSpace(t.APIKey)
	}
	if err := config.NormalizeTranslation(&value); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	currentPost := s.post.Config()
	p := request.PostProcessing
	postValue := config.PostProcessingConfig{Mode: p.Mode, ShellScript: p.ShellScript, WebhookURL: p.WebhookURL, TimeoutSec: p.TimeoutSec, WebhookBearerToken: currentPost.WebhookBearerToken}
	if p.ClearBearerToken {
		postValue.WebhookBearerToken = ""
	} else if strings.TrimSpace(p.WebhookBearerToken) != "" {
		postValue.WebhookBearerToken = strings.TrimSpace(p.WebhookBearerToken)
	}
	if err := config.NormalizePostProcessing(&postValue); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	if s.settings == nil {
		writeJSON(w, 500, map[string]string{"error": "settings storage is unavailable"})
		return
	}
	if err := s.settings.SaveTranslation(value); err != nil {
		s.log.Error("save translation settings", "error", err)
		writeJSON(w, 500, map[string]string{"error": "could not save settings"})
		return
	}
	if err := s.settings.SavePostProcessing(postValue); err != nil {
		s.log.Error("save post-processing settings", "error", err)
		writeJSON(w, 500, map[string]string{"error": "could not save post-processing settings"})
		return
	}
	s.runner.UpdateTranslation(value)
	s.post.Update(postValue)
	writeJSON(w, 200, map[string]any{"translation": settingsResponse(value), "post_processing": postProcessingSettingsResponse{Mode: postValue.Mode, ShellScript: postValue.ShellScript, WebhookURL: postValue.WebhookURL, TimeoutSec: postValue.TimeoutSec, BearerTokenSet: postValue.WebhookBearerToken != ""}})
}

func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if strings.HasPrefix(r.URL.Path, "/api/") && !strings.HasPrefix(r.URL.Path, "/api/v1/session") {
			if !s.validSession(r) && !s.validAPIToken(r) {
				writeJSON(w, 401, map[string]string{"error": "unauthorized"})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

const sessionCookieName = "javbeaconsubs_session"

func (s *Server) validSession(r *http.Request) bool {
	if s.auth == nil {
		return false
	}
	cookie, err := r.Cookie(sessionCookieName)
	return err == nil && s.auth.Valid(cookie.Value)
}

func (s *Server) validAPIToken(r *http.Request) bool {
	if s.cfg.APIToken == "" {
		return false
	}
	provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if provided == "" {
		provided = r.Header.Get("X-API-Key")
	}
	if provided == "" {
		provided = r.URL.Query().Get("token")
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(s.cfg.APIToken)) == 1
}

func (s *Server) sessionStatus(w http.ResponseWriter, r *http.Request) {
	username, configured := s.auth.Status()
	writeJSON(w, 200, map[string]any{"authenticated": s.validSession(r), "configured": configured, "username": username})
}

type credentialsRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Server) setupAccount(w http.ResponseWriter, r *http.Request) {
	var input credentialsRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&input); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid credentials"})
		return
	}
	if err := s.auth.Setup(input.Username, input.Password); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	s.issueSession(w, r, input)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var input credentialsRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&input); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid credentials"})
		return
	}
	s.issueSession(w, r, input)
}

func (s *Server) issueSession(w http.ResponseWriter, r *http.Request, input credentialsRequest) {
	token, err := s.auth.Authenticate(input.Username, input.Password)
	if err != nil {
		writeJSON(w, 401, map[string]string{"error": err.Error()})
		return
	}
	secure := r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: token, Path: "/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode, MaxAge: 7 * 24 * 60 * 60})
	writeJSON(w, 200, map[string]any{"authenticated": true, "username": input.Username})
}

func (s *Server) logout(w http.ResponseWriter, _ *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: "", Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	writeJSON(w, 200, map[string]bool{"authenticated": false})
}
func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	data, err := assets.ReadFile("web/index.html")
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}
func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	status := s.runner.Check()
	status["storage"] = "sqlite"
	status["max_upload_gb"] = s.cfg.MaxUploadGB
	writeJSON(w, 200, status)
}
func (s *Server) listJobs(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{"jobs": s.jobs.List()})
}
func (s *Server) getJob(w http.ResponseWriter, r *http.Request) {
	job, ok := s.jobs.Get(r.PathValue("id"))
	if !ok {
		writeJSON(w, 404, map[string]string{"error": "job not found"})
		return
	}
	writeJSON(w, 200, job)
}
func (s *Server) createJob(w http.ResponseWriter, r *http.Request) {
	var req jobs.Request
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid JSON: " + err.Error()})
		return
	}
	job, err := s.jobs.Create(req)
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	w.Header().Set("Location", "/api/v1/jobs/"+job.ID)
	writeJSON(w, 202, job)
}

func (s *Server) uploadJob(w http.ResponseWriter, r *http.Request) {
	limit := s.cfg.MaxUploadGB << 30
	r.Body = http.MaxBytesReader(w, r.Body, limit+(1<<20))
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": fmt.Sprintf("upload exceeds the %d GB limit or is invalid: %v", s.cfg.MaxUploadGB, err)})
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": "a media file is required"})
		return
	}
	defer file.Close()
	extension := strings.ToLower(filepath.Ext(header.Filename))
	if !supportedUploadExtension(extension) {
		writeJSON(w, 400, map[string]string{"error": "unsupported media extension: " + extension})
		return
	}
	uploadDir := filepath.Join(s.cfg.UploadDir, "upload-"+randomSuffix())
	if err := os.MkdirAll(uploadDir, 0o750); err != nil {
		writeJSON(w, 500, map[string]string{"error": "create upload directory: " + err.Error()})
		return
	}
	removeOnError := true
	defer func() {
		if removeOnError {
			_ = os.RemoveAll(uploadDir)
		}
	}()
	name := safeFilename(header.Filename)
	destination := filepath.Join(uploadDir, name)
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "store upload: " + err.Error()})
		return
	}
	_, copyErr := io.Copy(out, file)
	closeErr := out.Close()
	if copyErr != nil || closeErr != nil {
		if copyErr == nil {
			copyErr = closeErr
		}
		writeJSON(w, 500, map[string]string{"error": "store upload: " + copyErr.Error()})
		return
	}
	job, err := s.jobs.Create(jobs.Request{
		Inputs:      []string{destination},
		Overwrite:   r.FormValue("overwrite") == "true",
		ExternalID:  strings.TrimSpace(r.FormValue("external_id")),
		CallbackURL: strings.TrimSpace(r.FormValue("callback_url")),
	})
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	removeOnError = false
	w.Header().Set("Location", "/api/v1/jobs/"+job.ID)
	writeJSON(w, 202, job)
}

func (s *Server) downloadOutput(w http.ResponseWriter, r *http.Request) {
	job, ok := s.jobs.Get(r.PathValue("id"))
	if !ok {
		writeJSON(w, 404, map[string]string{"error": "job not found"})
		return
	}
	index, err := strconv.Atoi(r.PathValue("index"))
	if err != nil || index < 0 || index >= len(job.Results) {
		writeJSON(w, 404, map[string]string{"error": "output not found"})
		return
	}
	var path string
	switch r.PathValue("language") {
	case "en":
		path = job.Results[index].EnglishSRT
	case "ja":
		path = job.Results[index].JapaneseSRT
	default:
		writeJSON(w, 404, map[string]string{"error": "language output not found"})
		return
	}
	if path == "" {
		writeJSON(w, 404, map[string]string{"error": "language output not found"})
		return
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filepath.Base(path)}))
	http.ServeFile(w, r, path)
}
func (s *Server) cancelJob(w http.ResponseWriter, r *http.Request) {
	if !s.jobs.Cancel(r.PathValue("id")) {
		writeJSON(w, 404, map[string]string{"error": "job not found"})
		return
	}
	writeJSON(w, 202, map[string]string{"status": "cancelling"})
}
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", 500)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	ch, unsubscribe := s.jobs.Subscribe()
	defer unsubscribe()
	heartbeat := time.NewTicker(20 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case data := <-ch:
			fmt.Fprintf(w, "event: job\ndata: %s\n\n", data)
			flusher.Flush()
		case <-heartbeat.C:
			fmt.Fprint(w, ": heartbeat\n\n")
			flusher.Flush()
		}
	}
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func supportedUploadExtension(extension string) bool {
	switch extension {
	case ".mp4", ".mkv", ".avi", ".mov", ".wmv", ".flv", ".webm", ".ts", ".m4v", ".mp3", ".wav", ".m4a", ".flac":
		return true
	default:
		return false
	}
}

func safeFilename(name string) string {
	base := filepath.Base(strings.TrimSpace(name))
	base = strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`/\\:*?"<>|`, r) {
			return '_'
		}
		return r
	}, base)
	if base == "" || base == "." {
		return "upload.bin"
	}
	return base
}

func randomSuffix() string {
	value := make([]byte, 8)
	_, _ = rand.Read(value)
	return hex.EncodeToString(value)
}
