package server

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"javbeaconsubs/internal/config"
	"javbeaconsubs/internal/engine"
	"javbeaconsubs/internal/jobs"
	"javbeaconsubs/internal/store"
)

func TestSafeFilename(t *testing.T) {
	for input, want := range map[string]string{
		"../../movie.mkv": "movie.mkv",
		"bad:name?.mp4":   "bad_name_.mp4",
		"voice.wav":       "voice.wav",
	} {
		if got := safeFilename(input); got != want {
			t.Errorf("safeFilename(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestSupportedUploadExtension(t *testing.T) {
	if !supportedUploadExtension(".mkv") || !supportedUploadExtension(".flac") {
		t.Fatal("expected media extensions to be supported")
	}
	if supportedUploadExtension(".srt") || supportedUploadExtension(".exe") {
		t.Fatal("unexpected extension accepted")
	}
}

func TestSingleFileUploadCreatesPersistedJob(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{UploadDir: filepath.Join(root, "uploads"), MaxUploadGB: 1, AllowedRoots: []string{filepath.Join(root, "uploads")}}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	database, err := store.Open(filepath.Join(root, "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	runner := engine.New(cfg, logger)
	manager, err := jobs.New(cfg, runner, database, logger)
	if err != nil {
		t.Fatal(err)
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	file, err := writer.CreateFormFile("file", "sample.mkv")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.Write([]byte("fake media"))
	_ = writer.WriteField("external_id", "movie-42")
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/jobs/upload", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	New(cfg, manager, runner, logger).Handler().ServeHTTP(response, req)
	if response.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", response.Code, response.Body.String())
	}
	var job jobs.Job
	if err := json.Unmarshal(response.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	if job.ExternalID != "movie-42" || len(job.Files) != 1 {
		t.Fatalf("unexpected job: %#v", job)
	}
	if _, err := os.Stat(job.Files[0]); err != nil {
		t.Fatalf("uploaded file was not stored: %v", err)
	}
	stored, err := database.Load()
	if err != nil || len(stored) != 1 {
		t.Fatalf("persisted jobs: %d, %v", len(stored), err)
	}
}
