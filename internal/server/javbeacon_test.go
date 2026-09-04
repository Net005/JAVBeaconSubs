package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"javbeaconsubs/internal/auth"
	"javbeaconsubs/internal/config"
	"javbeaconsubs/internal/engine"
	"javbeaconsubs/internal/jobs"
	"javbeaconsubs/internal/store"
)

// newTestJAVBeaconServer stands in for a real JAVBeacon instance, serving
// canned GET /api/releases/{id} responses gated on an expected API key so
// tests can prove which client (and which key) a request actually used.
func newTestJAVBeaconServer(t *testing.T, expectedKey string, byID map[int64]map[string]any) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if expectedKey != "" && r.Header.Get("Authorization") != "Bearer "+expectedKey {
			w.WriteHeader(401)
			return
		}
		idStr := strings.TrimPrefix(r.URL.Path, "/api/releases/")
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			w.WriteHeader(400)
			return
		}
		found, ok := byID[id]
		if !ok {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(found)
	}))
	t.Cleanup(server.Close)
	return server
}

func newTestServerWithManager(t *testing.T, cfg config.Config) (*Server, *jobs.Manager, *store.SQLite) {
	t.Helper()
	root := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	database, err := store.Open(filepath.Join(root, "settings.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	runner := engine.New(cfg, logger)
	authManager, err := auth.New(database, "", "")
	if err != nil {
		t.Fatal(err)
	}
	post := jobs.NewPostProcessor(config.PostProcessingConfig{Mode: "none", TimeoutSec: 60}, logger)
	manager, err := jobs.New(cfg, runner, database, post, logger)
	if err != nil {
		t.Fatal(err)
	}
	return New(cfg, manager, runner, authManager, post, database, logger), manager, database
}

func TestJAVBeaconSettingsArePersistentAndKeyIsWriteOnly(t *testing.T) {
	cfg := config.Config{APIToken: "test-api-token"}
	srv, manager, database := newTestServerWithManager(t, cfg)
	handler := srv.Handler()

	body := `{"translation":{"mode":"direct","batch_size":24,"timeout_seconds":120},"post_processing":{"mode":"none","timeout_seconds":60},"javbeacon":{"base_url":"http://javbeacon.local:8080","api_key":"very-secret","timeout_seconds":15}}`
	request := httptest.NewRequest(http.MethodPut, "/api/v1/settings", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer test-api-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status %d: %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "very-secret") {
		t.Fatal("API response exposed javbeacon key")
	}
	if !strings.Contains(response.Body.String(), `"javbeacon":{"base_url":"http://javbeacon.local:8080","timeout_seconds":15,"api_key_set":true}`) {
		t.Fatalf("javbeacon settings not reported correctly: %s", response.Body.String())
	}
	saved, ok, err := database.LoadJAVBeacon()
	if err != nil || !ok || saved.APIKey != "very-secret" || saved.BaseURL != "http://javbeacon.local:8080" || saved.TimeoutSec != 15 {
		t.Fatalf("saved settings: %#v ok=%v err=%v", saved, ok, err)
	}
	if got := manager.JAVBeacon(); got.APIKey != "very-secret" || got.BaseURL != "http://javbeacon.local:8080" {
		t.Fatalf("manager did not pick up the updated javbeacon config: %#v", got)
	}

	// A settings PUT that omits "javbeacon" entirely must never wipe what
	// was just saved (the field is a pointer specifically to guarantee
	// this - see settingsRequest.JAVBeacon's doc comment).
	body2 := `{"translation":{"mode":"direct","batch_size":24,"timeout_seconds":120},"post_processing":{"mode":"none","timeout_seconds":60}}`
	request2 := httptest.NewRequest(http.MethodPut, "/api/v1/settings", strings.NewReader(body2))
	request2.Header.Set("Authorization", "Bearer test-api-token")
	response2 := httptest.NewRecorder()
	handler.ServeHTTP(response2, request2)
	if response2.Code != http.StatusOK {
		t.Fatalf("status %d: %s", response2.Code, response2.Body.String())
	}
	if !strings.Contains(response2.Body.String(), `"javbeacon":{"base_url":"http://javbeacon.local:8080","timeout_seconds":15,"api_key_set":true}`) {
		t.Fatalf("javbeacon settings were wiped by an update that omitted them: %s", response2.Body.String())
	}
}

func TestJAVBeaconTestEndpointReportsMatchedRelease(t *testing.T) {
	fake := newTestJAVBeaconServer(t, "test-key", map[int64]map[string]any{
		42: {"id": 42, "video_id": "ADN-803", "title": "Sample Title", "story": "Sample story.", "source": "javbeacon"},
	})
	cfg := config.Config{APIToken: "test-api-token"}
	srv, _, _ := newTestServerWithManager(t, cfg)
	handler := srv.Handler()

	body := `{"base_url":"` + fake.URL + `","api_key":"test-key","timeout_seconds":5,"release_id":42}`
	request := httptest.NewRequest(http.MethodPost, "/api/v1/javbeacon/test", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer test-api-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status %d: %s", response.Code, response.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["reachable"] != true {
		t.Fatalf("reachable = %v, want true: %s", got["reachable"], response.Body.String())
	}
	release, ok := got["release"].(map[string]any)
	if !ok || release["title"] != "Sample Title" || release["video_id"] != "ADN-803" {
		t.Fatalf("unexpected release payload: %s", response.Body.String())
	}
}

func TestJAVBeaconTestEndpointReportsNotFoundForUnknownRelease(t *testing.T) {
	fake := newTestJAVBeaconServer(t, "test-key", map[int64]map[string]any{})
	cfg := config.Config{APIToken: "test-api-token"}
	srv, _, _ := newTestServerWithManager(t, cfg)
	handler := srv.Handler()

	body := `{"base_url":"` + fake.URL + `","api_key":"test-key","timeout_seconds":5,"release_id":999}`
	request := httptest.NewRequest(http.MethodPost, "/api/v1/javbeacon/test", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer test-api-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status %d: %s", response.Code, response.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	// A clean 404 from a reachable server is "connected, but nothing at
	// that id" - not a connectivity failure.
	if got["reachable"] != true {
		t.Fatalf("reachable = %v, want true (a 404 proves the server was reached): %s", got["reachable"], response.Body.String())
	}
	if _, hasRelease := got["release"]; hasRelease {
		t.Fatalf("did not expect a release payload for an unknown id: %s", response.Body.String())
	}
	if errMsg, _ := got["error"].(string); errMsg == "" {
		t.Fatal("expected a not-found error message")
	}
}

func TestJAVBeaconTestEndpointReportsUnreachableOnAuthFailure(t *testing.T) {
	fake := newTestJAVBeaconServer(t, "correct-key", map[int64]map[string]any{
		1: {"id": 1, "video_id": "ADN-803"},
	})
	cfg := config.Config{APIToken: "test-api-token"}
	srv, _, _ := newTestServerWithManager(t, cfg)
	handler := srv.Handler()

	body := `{"base_url":"` + fake.URL + `","api_key":"wrong-key","timeout_seconds":5,"release_id":1}`
	request := httptest.NewRequest(http.MethodPost, "/api/v1/javbeacon/test", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer test-api-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status %d: %s", response.Code, response.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["reachable"] != false {
		t.Fatalf("reachable = %v, want false for a 401 response: %s", got["reachable"], response.Body.String())
	}
	if errMsg, _ := got["error"].(string); errMsg == "" {
		t.Fatal("expected an error message describing the failure")
	}
}

func TestJAVBeaconTestEndpointRequiresBaseURL(t *testing.T) {
	cfg := config.Config{APIToken: "test-api-token"}
	srv, _, _ := newTestServerWithManager(t, cfg)
	handler := srv.Handler()

	request := httptest.NewRequest(http.MethodPost, "/api/v1/javbeacon/test", strings.NewReader(`{}`))
	request.Header.Set("Authorization", "Bearer test-api-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 when no base_url is configured or supplied: %s", response.Code, response.Body.String())
	}
}

func TestJAVBeaconTestEndpointFallsBackToSavedAPIKeyWhenRequestKeyBlank(t *testing.T) {
	fake := newTestJAVBeaconServer(t, "saved-key", map[int64]map[string]any{
		1: {"id": 1, "video_id": "ADN-803", "title": "Saved Key Works"},
	})
	cfg := config.Config{APIToken: "test-api-token", JAVBeacon: config.JAVBeaconConfig{BaseURL: fake.URL, APIKey: "saved-key", TimeoutSec: 10}}
	srv, _, _ := newTestServerWithManager(t, cfg)
	handler := srv.Handler()

	// api_key omitted entirely: must fall back to the currently configured
	// (saved) key, not an empty one.
	body := `{"release_id":1}`
	request := httptest.NewRequest(http.MethodPost, "/api/v1/javbeacon/test", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer test-api-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status %d: %s", response.Code, response.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["reachable"] != true {
		t.Fatalf("reachable = %v, want true using the saved API key: %s", got["reachable"], response.Body.String())
	}
}
