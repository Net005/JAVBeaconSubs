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

	"beaconsubs/internal/config"
	"beaconsubs/internal/engine"
	"beaconsubs/internal/jobs"
)

//go:embed web/*
var assets embed.FS

type Server struct {
	cfg    config.Config
	jobs   *jobs.Manager
	runner *engine.Runner
	log    *slog.Logger
}

func New(cfg config.Config, jobs *jobs.Manager, runner *engine.Runner, log *slog.Logger) *Server {
	return &Server{cfg: cfg, jobs: jobs, runner: runner, log: log}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.index)
	mux.HandleFunc("GET /api/v1/health", s.health)
	mux.HandleFunc("GET /api/v1/jobs", s.listJobs)
	mux.HandleFunc("POST /api/v1/jobs", s.createJob)
	mux.HandleFunc("POST /api/v1/jobs/upload", s.uploadJob)
	mux.HandleFunc("GET /api/v1/jobs/{id}", s.getJob)
	mux.HandleFunc("GET /api/v1/jobs/{id}/outputs/{index}/{language}", s.downloadOutput)
	mux.HandleFunc("DELETE /api/v1/jobs/{id}", s.cancelJob)
	mux.HandleFunc("GET /api/v1/events", s.events)
	return s.middleware(mux)
}

func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if strings.HasPrefix(r.URL.Path, "/api/") && s.cfg.APIToken != "" {
			provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if provided == "" {
				provided = r.Header.Get("X-API-Key")
			}
			if provided == "" {
				provided = r.URL.Query().Get("token")
			}
			if subtle.ConstantTimeCompare([]byte(provided), []byte(s.cfg.APIToken)) != 1 {
				writeJSON(w, 401, map[string]string{"error": "unauthorized"})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
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
