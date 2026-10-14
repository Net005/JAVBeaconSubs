package jobs

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"javbeaconsubs/internal/config"
	"javbeaconsubs/internal/release"
)

func TestFilteredPreparationOnlyResolvesPossibleCandidates(t *testing.T) {
	root := t.TempDir()
	for n := 0; n < 1300; n++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("OTHER-%d.mp4", n)), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	candidate := filepath.Join(root, "DEMO-1.mp4")
	os.WriteFile(candidate, nil, 0600)
	exactCalls, catalogCalls := 0, 0
	pathConflict := false
	row := release.Release{ID: 1, VideoID: "DEMO-1", Watchlist: true, ReleaseDate: "2026-02-01"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/releases/count" {
			catalogCalls++
			fmt.Fprint(w, `{"total":1}`)
			return
		}
		if r.URL.Query().Get("watchlist") == "true" {
			catalogCalls++
			json.NewEncoder(w).Encode([]release.Release{row})
			return
		}
		exactCalls++
		if pathConflict && r.URL.Query().Get("stash_file_path") == candidate {
			json.NewEncoder(w).Encode([]release.Release{{ID: 2, VideoID: "OTHER", Watchlist: false, ReleaseDate: "2026-02-01"}})
			return
		}
		if r.URL.Query().Get("video_id") == "DEMO-1" {
			json.NewEncoder(w).Encode([]release.Release{row})
		} else {
			fmt.Fprint(w, `[]`)
		}
	}))
	defer server.Close()
	m := &Manager{cfg: config.Config{}, log: slog.New(slog.NewTextHandler(io.Discard, nil)), jobs: map[string]*Job{}, queue: make(chan string, 1), subscribers: map[chan []byte]struct{}{}, persistence: testPersistence{}}
	m.UpdateJAVBeacon(config.JAVBeaconConfig{BaseURL: server.URL, TimeoutSec: 5})
	job, err := m.Create(Request{Inputs: []string{root}, OnlyWatchlist: true, OnlyNewReleases: true, ReleaseStartDate: "2026-01-01"})
	if err != nil {
		t.Fatal(err)
	}
	if len(job.Files) != 1 || job.Files[0] != candidate || len(job.ScanResults) != 1301 {
		t.Fatal("filtered preparation lost candidate or scan audit")
	}
	if catalogCalls != 3 || exactCalls != 2 {
		t.Fatalf("requests: catalog=%d exact=%d", catalogCalls, exactCalls)
	}
	for _, result := range job.ScanResults {
		if result.Path != candidate && (result.Included || result.LookupMethod != "eligibility_prefilter") {
			t.Fatal("incorrect skipped decision")
		}
	}
	pathConflict = true
	m.jobs = map[string]*Job{}
	job, err = m.Create(Request{Inputs: []string{root}, OnlyWatchlist: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(job.Files) != 0 {
		t.Fatal("filtered candidate overrode stronger excluded path match")
	}
}

func TestFilteredScanPrefilterReason(t *testing.T) {
	results := buildScanResults([]string{"/media/example.mp4"}, release.Resolution{}, map[string]FileReleaseMetadata{"/media/example.mp4": {ReleaseLookupMethod: "eligibility_prefilter"}}, true, "2026-01-01", nil, true)
	if len(results) != 1 || results[0].Included || results[0].LookupMethod != "eligibility_prefilter" {
		t.Fatal("filtered audit decision lost")
	}
}

type checkpointPersistence struct{ saves int }

func (p *checkpointPersistence) Save(*Job) error       { p.saves++; return nil }
func (p *checkpointPersistence) Load() ([]*Job, error) { return nil, nil }
func TestPreparationCheckpointsBoundWritesAndRetainEveryDecision(t *testing.T) {
	p := &checkpointPersistence{}
	m := &Manager{jobs: map[string]*Job{"scan": {ID: "scan", Status: "queued"}}, persistence: p, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	for n := 0; n < 1300; n++ {
		m.preparationProgress("scan", "matching_releases", 20, "Matching", "/media/example.mp4", n+1, 1300)
		m.preparationScanResult("scan", ScanResult{Path: fmt.Sprintf("/media/example-%d.mp4", n)}, nil)
	}
	if p.saves > 3 {
		t.Fatalf("per-file persistence still occurs: %d saves", p.saves)
	}
	if len(m.jobs["scan"].ScanResults) != 1300 || Summary(m.jobs["scan"]).DiscoveredCount != 1300 {
		t.Fatal("in-memory scan state missing")
	}
	m.preparationProgress("scan", "preparing", 48, "Complete", "", 1300, 1300)
	if p.saves < 3 {
		t.Fatal("phase transition not checkpointed")
	}
}
