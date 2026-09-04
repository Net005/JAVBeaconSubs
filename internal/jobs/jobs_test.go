package jobs

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"javbeaconsubs/internal/config"
	"javbeaconsubs/internal/engine"
)

type testPersistence struct{}

func (testPersistence) Save(*Job) error       { return nil }
func (testPersistence) Load() ([]*Job, error) { return nil, nil }

func TestCompletionMessageReportsSkippedExistingFiles(t *testing.T) {
	results := []engine.Result{{Skipped: true}, {Skipped: true}}
	if !allResultsSkipped(results) {
		t.Fatal("all-skipped results were not recognized")
	}
	got := completionMessage(results)
	if got != "Skipped 2 existing file(s); enable Replace existing subtitles to rerun recognition or change accuracy" {
		t.Fatalf("completion message = %q", got)
	}
	if got := completionMessage([]engine.Result{{}, {Skipped: true}}); got != "Generated 1 file(s); skipped 1 existing file(s)" {
		t.Fatalf("mixed completion message = %q", got)
	}
	if allResultsSkipped([]engine.Result{{}, {Skipped: true}}) || allResultsSkipped(nil) {
		t.Fatal("mixed or empty results were marked all-skipped")
	}
}

func TestDiscoverSpecificFileAndFolder(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(root, "one.mkv"), filepath.Join(root, "ignore.txt"), filepath.Join(nested, "two.mp4")} {
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.Config{}
	m := &Manager{cfg: cfg}

	flat, err := m.discover([]string{root}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(flat) != 1 || filepath.Base(flat[0]) != "one.mkv" {
		t.Fatalf("flat discovery: %#v", flat)
	}
	recursive, err := m.discover([]string{root}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(recursive) != 2 {
		t.Fatalf("recursive discovery: %#v", recursive)
	}
	specific, err := m.discover([]string{filepath.Join(nested, "two.mp4")}, false)
	if err != nil || len(specific) != 1 {
		t.Fatalf("specific discovery: %#v, %v", specific, err)
	}
}

func TestDiscoverAllowsAnyReadablePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "movie.mkv")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	m := &Manager{cfg: config.Config{}}
	files, err := m.discover([]string{path}, false)
	if err != nil || len(files) != 1 || files[0] != path {
		t.Fatalf("discover any readable path: %#v, %v", files, err)
	}
}

func TestCreateResolvesWriteASSDefaultAndOverride(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "one.mp4")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Output: config.OutputConfig{WriteASS: true}}
	m := &Manager{cfg: cfg, log: slog.New(slog.NewTextHandler(io.Discard, nil)), jobs: map[string]*Job{}, queue: make(chan string, 2), subscribers: map[chan []byte]struct{}{}, persistence: testPersistence{}}

	byDefault, err := m.Create(Request{Inputs: []string{path}})
	if err != nil {
		t.Fatal(err)
	}
	if !byDefault.WriteASS {
		t.Fatalf("job did not inherit output.write_ass default: %#v", byDefault)
	}

	disabled := false
	overridden, err := m.Create(Request{Inputs: []string{path}, WriteASS: &disabled})
	if err != nil {
		t.Fatal(err)
	}
	if overridden.WriteASS {
		t.Fatalf("per-job write_ass override was ignored: %#v", overridden)
	}
}

func TestCreateResolvesMixedRecursiveFilesIndependently(t *testing.T) {
	root := t.TempDir()
	javDir, gigaDir := filepath.Join(root, "JAV"), filepath.Join(root, "GIGA")
	if err := os.MkdirAll(javDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(gigaDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(javDir, "one.mp4"), filepath.Join(gigaDir, "two.mp4")} {
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.Config{Profiles: config.ProfilesConfig{DefaultProfile: "standard", DefaultASRMode: "fast", PathMappings: []config.PathMapping{
		{ID: "jav", Path: javDir, Profile: "jav", Enabled: true},
		{ID: "giga", Path: gigaDir, Profile: "giga", ASRMode: "balanced", Enabled: true},
	}}}
	m := &Manager{cfg: cfg, log: slog.New(slog.NewTextHandler(io.Discard, nil)), jobs: map[string]*Job{}, queue: make(chan string, 1), subscribers: map[chan []byte]struct{}{}, persistence: testPersistence{}}
	job, err := m.Create(Request{Inputs: []string{root}, Recursive: true})
	if err != nil {
		t.Fatal(err)
	}
	if job.FileSettings[filepath.Join(javDir, "one.mp4")].Profile != "jav" || job.FileSettings[filepath.Join(gigaDir, "two.mp4")].ASRMode != "balanced" {
		t.Fatalf("file settings = %#v", job.FileSettings)
	}
}

func TestCreateReleaseExternalIDFallsBackToExternalIDWithNoJAVBeaconConfigured(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "one.mp4")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	m := &Manager{cfg: config.Config{}, log: slog.New(slog.NewTextHandler(io.Discard, nil)), jobs: map[string]*Job{}, queue: make(chan string, 1), subscribers: map[chan []byte]struct{}{}, persistence: testPersistence{}}

	job, err := m.Create(Request{Inputs: []string{path}, ExternalID: "movie-123"})
	if err != nil {
		t.Fatal(err)
	}
	if job.ExternalID != "movie-123" {
		t.Fatalf("external_id = %q", job.ExternalID)
	}
	if job.ReleaseExternalID != "movie-123" {
		t.Fatalf("release_external_id did not fall back to external_id: %q", job.ReleaseExternalID)
	}
	// No JAVBeacon configured (releaseClient is nil): the ExternalID
	// fallback still counts as an external-id lookup attempt, so it must be
	// reported as unmatched with a non-fatal lookup error - not silently
	// treated as a match, and not blocking job creation either.
	if job.ReleaseLookupMethod != "external_release_id" || job.ReleaseLookupMatched {
		t.Fatalf("resolution should not report a match with no client configured: method=%q matched=%v", job.ReleaseLookupMethod, job.ReleaseLookupMatched)
	}
	if job.ReleaseLookupError == "" {
		t.Fatal("expected a non-fatal release_lookup_error when no JAVBeacon client is configured")
	}
}

func TestCreateReleaseExternalIDOverridesExternalIDWhenBothSupplied(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "one.mp4")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	m := &Manager{cfg: config.Config{}, log: slog.New(slog.NewTextHandler(io.Discard, nil)), jobs: map[string]*Job{}, queue: make(chan string, 1), subscribers: map[chan []byte]struct{}{}, persistence: testPersistence{}}

	job, err := m.Create(Request{Inputs: []string{path}, ExternalID: "legacy-name", ReleaseExternalID: "ADN-803"})
	if err != nil {
		t.Fatal(err)
	}
	if job.ExternalID != "legacy-name" {
		t.Fatalf("external_id should be preserved unchanged: %q", job.ExternalID)
	}
	if job.ReleaseExternalID != "ADN-803" {
		t.Fatalf("release_external_id should take precedence when explicitly supplied: %q", job.ReleaseExternalID)
	}
}

func TestCreateManualReleaseTitleAndStoryPersistAsManual(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "one.mp4")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	m := &Manager{cfg: config.Config{}, log: slog.New(slog.NewTextHandler(io.Discard, nil)), jobs: map[string]*Job{}, queue: make(chan string, 1), subscribers: map[chan []byte]struct{}{}, persistence: testPersistence{}}

	job, err := m.Create(Request{Inputs: []string{path}, ReleaseTitle: "  My Title  ", ReleaseStory: "My story.\nSecond line."})
	if err != nil {
		t.Fatal(err)
	}
	if job.ReleaseTitle != "My Title" || job.ReleaseTitleSource != "manual" {
		t.Fatalf("release title = %q source = %q", job.ReleaseTitle, job.ReleaseTitleSource)
	}
	if job.ReleaseStory != "My story.\nSecond line." || job.ReleaseStorySource != "manual" {
		t.Fatalf("release story = %q source = %q", job.ReleaseStory, job.ReleaseStorySource)
	}
}

func TestCreateRejectsOversizedReleaseTitleAndStory(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "one.mp4")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	m := &Manager{cfg: config.Config{}, log: slog.New(slog.NewTextHandler(io.Discard, nil)), jobs: map[string]*Job{}, queue: make(chan string, 1), subscribers: map[chan []byte]struct{}{}, persistence: testPersistence{}}

	if _, err := m.Create(Request{Inputs: []string{path}, ReleaseTitle: strings.Repeat("x", maxReleaseTitleBytes+1)}); err == nil {
		t.Fatal("expected an error for an oversized release_title")
	}
	if _, err := m.Create(Request{Inputs: []string{path}, ReleaseStory: strings.Repeat("x", maxReleaseStoryBytes+1)}); err == nil {
		t.Fatal("expected an error for an oversized release_story")
	}
	// A title/story right at the limit must still be accepted.
	if _, err := m.Create(Request{Inputs: []string{path}, ReleaseTitle: strings.Repeat("x", maxReleaseTitleBytes)}); err != nil {
		t.Fatalf("release_title at the exact limit should be accepted: %v", err)
	}
}

// TestCreateReleaseFieldsAllPopulatedForProcessOptionsWiring exercises
// release_external_id, release_title, and release_story together on one
// job - the exact three fields Manager.process()'s
// engine.ProcessOptions{Title: job.ReleaseExternalID, ReleaseTitle:
// job.ReleaseTitle, ReleaseStory: job.ReleaseStory} call site reads
// verbatim (Stage 2, TODO Part 20). Manager.process() itself isn't
// unit-tested here since m.runner is a concrete *engine.Runner with no
// seam to intercept the call, but the call site is a direct one-line
// field read, so asserting the job carries the correct values is the
// meaningful regression guard.
func TestCreateReleaseFieldsAllPopulatedForProcessOptionsWiring(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "one.mp4")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	m := &Manager{cfg: config.Config{}, log: slog.New(slog.NewTextHandler(io.Discard, nil)), jobs: map[string]*Job{}, queue: make(chan string, 1), subscribers: map[chan []byte]struct{}{}, persistence: testPersistence{}}

	job, err := m.Create(Request{
		Inputs:            []string{path},
		ReleaseExternalID: "ADN-803",
		ReleaseTitle:      "  My Title  ",
		ReleaseStory:      "My story.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if job.ReleaseExternalID != "ADN-803" {
		t.Fatalf("release_external_id = %q, want ADN-803 (this becomes ProcessOptions.Title)", job.ReleaseExternalID)
	}
	if job.ReleaseTitle != "My Title" {
		t.Fatalf("release_title = %q, want trimmed \"My Title\" (this becomes ProcessOptions.ReleaseTitle)", job.ReleaseTitle)
	}
	if job.ReleaseStory != "My story." {
		t.Fatalf("release_story = %q, want \"My story.\" (this becomes ProcessOptions.ReleaseStory)", job.ReleaseStory)
	}
}

// presetPersistence returns a fixed set of stored jobs from Load(), for
// exercising jobs.New()'s legacy-record backfill.
type presetPersistence struct{ jobs []*Job }

func (presetPersistence) Save(*Job) error         { return nil }
func (p presetPersistence) Load() ([]*Job, error) { return p.jobs, nil }

func TestNewBackfillsLegacyReleaseFieldsOnOldRecords(t *testing.T) {
	stored := &Job{
		ID: "sub_legacy", ExternalID: "legacy-ref", Status: "complete",
		ReleaseTitle: "Old Title", ReleaseStory: "Old story.",
		// ReleaseExternalID, ReleaseTitleSource, ReleaseStorySource left
		// unset, as a pre-Stage-1 persisted record would have them.
	}
	cfg := config.Config{Profiles: config.ProfilesConfig{DefaultProfile: "jav"}}
	m, err := New(cfg, nil, presetPersistence{jobs: []*Job{stored}}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	got, ok := m.Get("sub_legacy")
	if !ok {
		t.Fatal("legacy job not loaded")
	}
	if got.ReleaseExternalID != "legacy-ref" {
		t.Fatalf("release_external_id should backfill from external_id: %q", got.ReleaseExternalID)
	}
	if got.ReleaseTitleSource != "legacy" || got.ReleaseStorySource != "legacy" {
		t.Fatalf("legacy title/story should be marked as such: title_source=%q story_source=%q", got.ReleaseTitleSource, got.ReleaseStorySource)
	}
}

func TestNewLeavesReleaseSourceEmptyWhenJobNeverHadMetadata(t *testing.T) {
	stored := &Job{ID: "sub_plain", Status: "complete"}
	cfg := config.Config{Profiles: config.ProfilesConfig{DefaultProfile: "jav"}}
	m, err := New(cfg, nil, presetPersistence{jobs: []*Job{stored}}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	got, ok := m.Get("sub_plain")
	if !ok {
		t.Fatal("job not loaded")
	}
	if got.ReleaseTitleSource != "" || got.ReleaseStorySource != "" {
		t.Fatalf("a job that never had release metadata should not gain a source marker: title_source=%q story_source=%q", got.ReleaseTitleSource, got.ReleaseStorySource)
	}
}

// TestCreateFileReleaseOverridesResolvedPerFile exercises a two-file batch
// with an override for only one of them (TODO Part 33): only the
// overridden file gets a FileReleaseMetadata entry, the other must have
// none (fallback to job-level fields happens in Manager.process()).
func TestCreateFileReleaseOverridesResolvedPerFile(t *testing.T) {
	root := t.TempDir()
	pathA := filepath.Join(root, "one.mp4")
	pathB := filepath.Join(root, "two.mp4")
	for _, p := range []string{pathA, pathB} {
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m := &Manager{cfg: config.Config{}, log: slog.New(slog.NewTextHandler(io.Discard, nil)), jobs: map[string]*Job{}, queue: make(chan string, 1), subscribers: map[chan []byte]struct{}{}, persistence: testPersistence{}}

	job, err := m.Create(Request{
		Inputs: []string{pathA, pathB},
		FileReleaseOverrides: map[string]ReleaseOverrideRequest{
			pathA: {ReleaseTitle: "  Override Title  ", ReleaseStory: "Override story."},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	absA, _ := filepath.Abs(pathA)
	absA = filepath.Clean(absA)
	absB, _ := filepath.Abs(pathB)
	absB = filepath.Clean(absB)

	meta, ok := job.FileReleaseMetadata[absA]
	if !ok {
		t.Fatalf("expected FileReleaseMetadata entry for %s, got %#v", absA, job.FileReleaseMetadata)
	}
	if meta.ReleaseTitle != "Override Title" || meta.ReleaseTitleSource != "manual" {
		t.Fatalf("override metadata = %#v", meta)
	}
	if meta.ReleaseStory != "Override story." {
		t.Fatalf("override story = %q", meta.ReleaseStory)
	}
	if _, ok := job.FileReleaseMetadata[absB]; ok {
		t.Fatalf("non-overridden file must have no FileReleaseMetadata entry: %#v", job.FileReleaseMetadata)
	}
}

func TestCreateFileReleaseOverridesRejectsUnknownFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "one.mp4")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	m := &Manager{cfg: config.Config{}, log: slog.New(slog.NewTextHandler(io.Discard, nil)), jobs: map[string]*Job{}, queue: make(chan string, 1), subscribers: map[chan []byte]struct{}{}, persistence: testPersistence{}}

	_, err := m.Create(Request{
		Inputs: []string{path},
		FileReleaseOverrides: map[string]ReleaseOverrideRequest{
			filepath.Join(root, "missing.mp4"): {ReleaseTitle: "Whatever"},
		},
	})
	if err == nil {
		t.Fatal("expected an error when file_release_overrides references a file not in this job")
	}
}

// TestCreateFileReleaseOverridesNormalizesRelativePaths uses an
// unnormalized (but still resolvable) override key - containing a literal
// "/./" segment rather than the exact clean path discover() produces - to
// confirm Create() applies the same filepath.Abs+filepath.Clean
// normalization discover() already applies before matching, so a caller
// echoing back whatever raw form it used in Inputs still resolves.
func TestCreateFileReleaseOverridesNormalizesRelativePaths(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "one.mp4")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	m := &Manager{cfg: config.Config{}, log: slog.New(slog.NewTextHandler(io.Discard, nil)), jobs: map[string]*Job{}, queue: make(chan string, 1), subscribers: map[chan []byte]struct{}{}, persistence: testPersistence{}}

	messyKey := filepath.Dir(path) + string(filepath.Separator) + "." + string(filepath.Separator) + filepath.Base(path)
	if messyKey == path {
		t.Fatal("test setup error: messyKey must differ from the clean path")
	}
	job, err := m.Create(Request{
		Inputs: []string{path},
		FileReleaseOverrides: map[string]ReleaseOverrideRequest{
			messyKey: {ReleaseTitle: "Normalized Title"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	cleanPath, _ := filepath.Abs(path)
	cleanPath = filepath.Clean(cleanPath)
	meta, ok := job.FileReleaseMetadata[cleanPath]
	if !ok || meta.ReleaseTitle != "Normalized Title" {
		t.Fatalf("override should match after path normalization: metadata=%#v", job.FileReleaseMetadata)
	}
}

func TestCreateFileReleaseOverridesRespectsSizeLimits(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "one.mp4")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	m := &Manager{cfg: config.Config{}, log: slog.New(slog.NewTextHandler(io.Discard, nil)), jobs: map[string]*Job{}, queue: make(chan string, 1), subscribers: map[chan []byte]struct{}{}, persistence: testPersistence{}}

	_, err := m.Create(Request{
		Inputs: []string{path},
		FileReleaseOverrides: map[string]ReleaseOverrideRequest{
			path: {ReleaseTitle: strings.Repeat("x", maxReleaseTitleBytes+1)},
		},
	})
	if err == nil {
		t.Fatal("expected an error for an oversized per-file release_title override")
	}
}

// TestUpdateJAVBeaconSwapsReleaseClientUsedByResolve exercises
// Manager.JAVBeacon()/UpdateJAVBeacon(): after swapping in a new
// base_url/api_key, resolveFileRelease (which Create() calls for both the
// job-level default and every per-file override) must use the newly
// configured client - not a nil or stale one held from construction. The
// fake server records the Authorization header it received; seeing the
// new key proves the swap actually took effect under the Manager's mutex.
func TestUpdateJAVBeaconSwapsReleaseClientUsedByResolve(t *testing.T) {
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(404)
	}))
	defer server.Close()

	m := &Manager{cfg: config.Config{}, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if got := m.JAVBeacon(); got.BaseURL != "" || got.APIKey != "" {
		t.Fatalf("expected zero-value javbeacon config initially, got %#v", got)
	}

	m.UpdateJAVBeacon(config.JAVBeaconConfig{BaseURL: server.URL, APIKey: "new-key", TimeoutSec: 5})
	if got := m.JAVBeacon(); got.BaseURL != server.URL || got.APIKey != "new-key" || got.TimeoutSec != 5 {
		t.Fatalf("JAVBeacon() did not reflect the update: %#v", got)
	}

	if _, err := m.resolveFileRelease(nil, "ADN-803", "", ""); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer new-key" {
		t.Fatalf("resolveFileRelease used a stale or nil release client: got Authorization=%q, want %q", gotAuth, "Bearer new-key")
	}
}

// TestUpdateJAVBeaconCanDisableLookupsByClearingBaseURL confirms that
// swapping in an empty base_url (e.g. a user clearing the field) actually
// disables lookups going forward - release.NewClient returns nil for an
// empty base_url, and resolveFileRelease must handle that nil client
// gracefully (via release.Resolve's own client==nil handling) rather than
// panicking.
func TestUpdateJAVBeaconCanDisableLookupsByClearingBaseURL(t *testing.T) {
	m := &Manager{cfg: config.Config{}, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	m.UpdateJAVBeacon(config.JAVBeaconConfig{BaseURL: "http://example.invalid", APIKey: "key", TimeoutSec: 5})
	m.UpdateJAVBeacon(config.JAVBeaconConfig{BaseURL: "", TimeoutSec: 5})

	resolution, err := m.resolveFileRelease(nil, "ADN-803", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if resolution.LookupError == "" {
		t.Fatal("expected a lookup error explaining no JAVBeacon instance is configured")
	}
}
