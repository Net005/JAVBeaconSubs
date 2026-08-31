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
	"strings"
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
	cfg := config.Config{UploadDir: filepath.Join(root, "uploads"), MaxUploadGB: 1}
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
	New(cfg, manager, runner, database, logger).Handler().ServeHTTP(response, req)
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

func TestTranslationSettingsArePersistentAndKeyIsWriteOnly(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{Translation: config.TranslationConfig{Mode: "direct", BatchSize: 24, TimeoutSec: 120}}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	database, err := store.Open(filepath.Join(root, "settings.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	runner := engine.New(cfg, logger)
	handler := New(cfg, nil, runner, database, logger).Handler()
	body := `{"mode":"contextual","base_url":"http://llm/v1","api_key":"very-secret","model":"qwen","batch_size":12,"timeout_seconds":60,"glossary":"Mio=Mio"}`
	request := httptest.NewRequest(http.MethodPut, "/api/v1/settings", strings.NewReader(body))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status %d: %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "very-secret") {
		t.Fatal("API response exposed translation key")
	}
	if !strings.Contains(response.Body.String(), `"api_key_set":true`) {
		t.Fatal("API did not report a saved key")
	}
	saved, ok, err := database.LoadTranslation()
	if err != nil || !ok || saved.APIKey != "very-secret" || saved.Mode != "contextual" {
		t.Fatalf("saved settings: %#v ok=%v err=%v", saved, ok, err)
	}
}
