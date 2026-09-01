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

	"javbeaconsubs/internal/auth"
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
	cfg := config.Config{UploadDir: filepath.Join(root, "uploads"), MaxUploadGB: 1, APIToken: "test-api-token"}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	database, err := store.Open(filepath.Join(root, "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	runner := engine.New(cfg, logger)
	post := jobs.NewPostProcessor(config.PostProcessingConfig{Mode: "none", TimeoutSec: 60}, logger)
	manager, err := jobs.New(cfg, runner, database, post, logger)
	if err != nil {
		t.Fatal(err)
	}
	authManager, err := auth.New(database, "", "")
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
	req.Header.Set("Authorization", "Bearer test-api-token")
	response := httptest.NewRecorder()
	New(cfg, manager, runner, authManager, post, database, logger).Handler().ServeHTTP(response, req)
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
	cfg := config.Config{APIToken: "test-api-token", Translation: config.TranslationConfig{Mode: "direct", BatchSize: 24, TimeoutSec: 120}}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	database, err := store.Open(filepath.Join(root, "settings.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	runner := engine.New(cfg, logger)
	authManager, err := auth.New(database, "", "")
	if err != nil {
		t.Fatal(err)
	}
	post := jobs.NewPostProcessor(config.PostProcessingConfig{Mode: "none", TimeoutSec: 60}, logger)
	handler := New(cfg, nil, runner, authManager, post, database, logger).Handler()
	body := `{"translation":{"mode":"contextual","base_url":"http://llm/v1","api_key":"very-secret","model":"qwen","batch_size":12,"timeout_seconds":60,"context_gap_ms":5000,"translation_memory":true,"glossary":"Mio=Mio","structured_glossary":{"style":["Keep honorifics"],"terms":{"先生":"teacher"}}},"post_processing":{"mode":"none","timeout_seconds":60}}`
	request := httptest.NewRequest(http.MethodPut, "/api/v1/settings", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer test-api-token")
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
	if err != nil || !ok || saved.APIKey != "very-secret" || saved.Mode != "contextual" || saved.ContextGapMS != 5000 || !saved.TranslationMemory || saved.StructuredGlossary.Terms["先生"] != "teacher" {
		t.Fatalf("saved settings: %#v ok=%v err=%v", saved, ok, err)
	}
}

func TestWebLoginCreatesSessionThatAuthorizesSettings(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{
		APIToken:       "external-api-token",
		Translation:    config.TranslationConfig{Mode: "direct", BatchSize: 24, TimeoutSec: 120},
		PostProcessing: config.PostProcessingConfig{Mode: "none", TimeoutSec: 60},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	database, err := store.Open(filepath.Join(root, "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	runner := engine.New(cfg, logger)
	authManager, err := auth.New(database, "", "")
	if err != nil {
		t.Fatal(err)
	}
	post := jobs.NewPostProcessor(cfg.PostProcessing, logger)
	handler := New(cfg, nil, runner, authManager, post, database, logger).Handler()

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/v1/settings", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.Code)
	}

	setup := httptest.NewRecorder()
	handler.ServeHTTP(setup, httptest.NewRequest(http.MethodPost, "/api/v1/session/setup", strings.NewReader(`{"username":"subtitle-admin","password":"long-test-password"}`)))
	if setup.Code != http.StatusOK || len(setup.Result().Cookies()) != 1 {
		t.Fatalf("setup status %d cookies %d: %s", setup.Code, len(setup.Result().Cookies()), setup.Body.String())
	}

	authorized := httptest.NewRequest(http.MethodGet, "/api/v1/settings", nil)
	authorized.AddCookie(setup.Result().Cookies()[0])
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, authorized)
	if response.Code != http.StatusOK {
		t.Fatalf("session status %d: %s", response.Code, response.Body.String())
	}

	external := httptest.NewRequest(http.MethodGet, "/api/v1/settings", nil)
	external.Header.Set("Authorization", "Bearer external-api-token")
	externalResponse := httptest.NewRecorder()
	handler.ServeHTTP(externalResponse, external)
	if externalResponse.Code != http.StatusOK {
		t.Fatalf("API token status %d: %s", externalResponse.Code, externalResponse.Body.String())
	}
}
