package jobs

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"javbeaconsubs/internal/config"
	"javbeaconsubs/internal/engine"
	"javbeaconsubs/internal/release"
)

type testPersistence struct{}

func (testPersistence) Save(*Job) error       { return nil }
func (testPersistence) Load() ([]*Job, error) { return nil, nil }

func TestCompletionMessageReportsSkippedExistingFiles(t *testing.T) {
	results := []engine.Result{{Skipped: true}, {Skipped: true}}
	if !allResultsSkipped(results) {
		t.Fatal("all-skipped results were not recognized")
	}
	got := completionMessage(results, 0)
	if got != "Skipped 2 existing file(s); enable Replace existing subtitles to rerun recognition or change accuracy" {
		t.Fatalf("completion message = %q", got)
	}
	if got := completionMessage([]engine.Result{{}, {Skipped: true}}, 0); got != "Generated 1 file(s); skipped 1 existing file(s)" {
		t.Fatalf("mixed completion message = %q", got)
	}
	if allResultsSkipped([]engine.Result{{}, {Skipped: true}}) || allResultsSkipped(nil) {
		t.Fatal("mixed or empty results were marked all-skipped")
	}
}

func TestCompletionMessageReportsFailedFilesWithoutDiscardingSuccesses(t *testing.T) {
	if got := completionMessage([]engine.Result{{}}, 2); got != "Generated 1 file(s); skipped 2 failed file(s)" {
		t.Fatalf("partial completion message = %q", got)
	}
}

func TestJobOrderingPinsRunningThenQueuedInProcessingOrder(t *testing.T) {
	base := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	items := []*Job{
		{ID: "completed-new", Status: "complete", CreatedAt: base.Add(4 * time.Minute)},
		{ID: "queued-new", Status: "queued", CreatedAt: base.Add(3 * time.Minute)},
		{ID: "running", Status: "running", CreatedAt: base.Add(time.Minute)},
		{ID: "queued-old", Status: "queued", CreatedAt: base.Add(2 * time.Minute)},
	}
	sort.SliceStable(items, func(i, j int) bool { return jobComesBefore(items[i], items[j]) })
	got := []string{items[0].ID, items[1].ID, items[2].ID, items[3].ID}
	want := []string{"running", "queued-old", "queued-new", "completed-new"}
	if !slices.Equal(got, want) {
		t.Fatalf("job order = %v, want %v", got, want)
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

func TestNewReleaseFilterExplainsEveryDecision(t *testing.T) {
	files := []string{"/media/NEW-1.mp4", "/media/OLD-1.mp4", "/media/UNKNOWN.mp4", "/media/NODATE.mp4"}
	metadata := map[string]FileReleaseMetadata{
		files[0]: {ReleaseLookupMatched: true, ReleaseDate: "2026-09-01", ReleaseExternalID: "NEW-1", ReleaseTitle: "New", ReleaseProvider: "JavLibrary"},
		files[1]: {ReleaseLookupMatched: true, ReleaseDate: "2026-08-31", ReleaseExternalID: "OLD-1"},
		files[3]: {ReleaseLookupMatched: true, ReleaseExternalID: "NODATE"},
	}
	results := buildScanResults(files, release.Resolution{}, metadata, true, "2026-09-01")
	if len(results) != 4 || !results[0].Included || results[1].Included || results[2].Included || results[3].Included {
		t.Fatalf("unexpected release decisions: %#v", results)
	}
	if results[0].ReleaseDate != "2026-09-01" || !strings.Contains(results[1].Reason, "predates") || !strings.Contains(results[2].Reason, "no JAVBeacon") || !strings.Contains(results[3].Reason, "missing") {
		t.Fatalf("release decisions are not auditable: %#v", results)
	}
}

func TestQueueReturnsBeforePreparationFailure(t *testing.T) {
	m := &Manager{cfg: config.Config{}, log: slog.New(slog.NewTextHandler(io.Discard, nil)), jobs: map[string]*Job{}, queue: make(chan string, 1), subscribers: map[chan []byte]struct{}{}, persistence: testPersistence{}}
	job, err := m.Queue(Request{Inputs: []string{filepath.Join(t.TempDir(), "missing.mp4")}})
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != "queued" || job.Phase != "queued" || job.Progress != 1 {
		t.Fatalf("submission was not immediately queued: %#v", job)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		current, _ := m.Get(job.ID)
		if current.Status == "failed" {
			if !strings.Contains(current.Error, "missing.mp4") {
				t.Fatalf("preparation error lost its path: %#v", current)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("asynchronous preparation failure was not published")
}

func TestNewReleaseFilterRequiresValidStartDate(t *testing.T) {
	for _, value := range []string{"", "09/01/2026", "2026-13-01"} {
		if _, err := (&Manager{}).Queue(Request{Inputs: []string{"/media"}, OnlyNewReleases: true, ReleaseStartDate: value}); err == nil {
			t.Fatalf("accepted invalid release_start_date %q", value)
		}
	}
}

func TestRelativeReleaseWindowResolvesFromCurrentDate(t *testing.T) {
	now := time.Date(2026, 9, 12, 18, 30, 0, 0, time.FixedZone("CEST", 2*60*60))
	req := resolveRelativeReleaseWindow(Request{ReleaseWithinDays: 30}, now)
	if !req.OnlyNewReleases || !req.AutoDetectRelease || req.ReleaseStartDate != "2026-08-13" {
		t.Fatalf("relative release window = %#v", req)
	}
	if err := validateRequest(Request{Inputs: []string{"/media"}, ReleaseWithinDays: 30}); err != nil {
		t.Fatalf("relative API filter rejected: %v", err)
	}
	for _, days := range []int{-1, 36501} {
		if err := validateRequest(Request{Inputs: []string{"/media"}, ReleaseWithinDays: days}); err == nil {
			t.Fatalf("accepted release_within_days=%d", days)
		}
	}
}

func TestReserveFilesExcludesPathsAlreadyQueuedOrRunning(t *testing.T) {
	m := &Manager{jobs: map[string]*Job{
		"first":  {ID: "first", Status: "running", Files: []string{"/media/one.mp4"}},
		"second": {ID: "second", Status: "queued"},
	}}
	results := []ScanResult{
		{Path: "/media/one.mp4", Included: true, Reason: "discovered"},
		{Path: "/media/two.mp4", Included: true, Reason: "discovered"},
	}
	accepted := m.reserveFiles("second", []string{"/media/one.mp4", "/media/two.mp4"}, results)
	if len(accepted) != 1 || accepted[0] != "/media/two.mp4" {
		t.Fatalf("accepted files = %#v", accepted)
	}
	if results[0].Included || results[0].DuplicateJobID != "first" {
		t.Fatalf("duplicate scan result = %#v", results[0])
	}
	if got := m.jobs["second"].Files; len(got) != 1 || got[0] != "/media/two.mp4" {
		t.Fatalf("reservation was not stored atomically: %#v", got)
	}
}

func TestReserveFilesIgnoresFinishedJobs(t *testing.T) {
	m := &Manager{jobs: map[string]*Job{
		"done": {ID: "done", Status: "complete", Files: []string{"/media/one.mp4"}},
		"next": {ID: "next", Status: "queued"},
	}}
	results := []ScanResult{{Path: "/media/one.mp4", Included: true}}
	accepted := m.reserveFiles("next", []string{"/media/one.mp4"}, results)
	if len(accepted) != 1 || !results[0].Included {
		t.Fatalf("finished job incorrectly blocked file: %#v %#v", accepted, results)
	}
}

func TestPreparationScanResultPublishesReleaseMetadataImmediately(t *testing.T) {
	m := &Manager{
		log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		jobs:        map[string]*Job{"queued": {ID: "queued", Status: "queued"}},
		subscribers: map[chan []byte]struct{}{},
		persistence: testPersistence{},
	}
	metadata := FileReleaseMetadata{ReleaseTitle: "Example title", ReleaseStory: "Example story", ReleaseLookupMatched: true}
	m.preparationScanResult("queued", ScanResult{Path: "/media/one.mp4", Filename: "one.mp4", ReleaseMatched: true}, &metadata)
	job := m.jobs["queued"]
	if got := job.FileReleaseMetadata["/media/one.mp4"]; got.ReleaseTitle != "Example title" || got.ReleaseStory != "Example story" {
		t.Fatalf("live release metadata = %#v", got)
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

func TestReleaseContextForFileUsesJobAndPerFileLookupProvenance(t *testing.T) {
	jobID := int64(50)
	overrideID := int64(611)
	job := &Job{
		ReleaseExternalID: "SPSF-50", JAVBeaconReleaseID: &jobID,
		ReleaseTitleSource: "javbeacon", ReleaseStorySource: "javbeacon",
		ReleaseLookupMethod: "external_release_id", ReleaseLookupMatched: true,
		FileReleaseMetadata: map[string]FileReleaseMetadata{
			"/media/START-611.mp4": {
				ReleaseExternalID: "START-611", JAVBeaconReleaseID: &overrideID,
				ReleaseTitleSource: "manual", ReleaseLookupMethod: "javbeacon_release_id", ReleaseLookupMatched: true,
			},
		},
	}
	base := releaseContextForFile(job, "/media/SPSF-50.mp4")
	if base.ReleaseExternalID != "SPSF-50" || base.LookupMethod != "external_release_id" || base.StorySource != "javbeacon" {
		t.Fatalf("job-level release context = %#v", base)
	}
	override := releaseContextForFile(job, "/media/START-611.mp4")
	if override.ReleaseExternalID != "START-611" || override.JAVBeaconReleaseID == nil || *override.JAVBeaconReleaseID != overrideID || override.LookupMethod != "javbeacon_release_id" || override.StorySource != "" {
		t.Fatalf("per-file release context = %#v", override)
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

func TestCreateAutoDetectsReleaseMetadataPerFileInFolderJob(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "ssis-001.mp4")
	second := filepath.Join(root, "ADN-816.mkv")
	for _, path := range []string{first, second} {
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch strings.ToLower(r.URL.Query().Get("video_id")) {
		case "ssis-001":
			_, _ = io.WriteString(w, `[{"id":1,"video_id":"SSIS-001","title":"First"}]`)
		case "adn816":
			_, _ = io.WriteString(w, `[{"id":2,"video_id":"ADN816","title":"Second"}]`)
		default:
			_, _ = io.WriteString(w, `[]`)
		}
	}))
	defer server.Close()

	m := &Manager{cfg: config.Config{}, log: slog.New(slog.NewTextHandler(io.Discard, nil)), jobs: map[string]*Job{}, queue: make(chan string, 1), subscribers: map[chan []byte]struct{}{}, persistence: testPersistence{}}
	m.UpdateJAVBeacon(config.JAVBeaconConfig{BaseURL: server.URL, TimeoutSec: 5})
	job, err := m.Create(Request{Inputs: []string{root}, Recursive: true, AutoDetectRelease: true})
	if err != nil {
		t.Fatal(err)
	}
	if !job.AutoDetectRelease || len(job.FileReleaseMetadata) != 2 {
		t.Fatalf("job auto-detection metadata = %#v", job.FileReleaseMetadata)
	}
	if got := job.FileReleaseMetadata[first]; !got.ReleaseLookupMatched || got.ReleaseLookupMethod != "filename_video_id" || got.ReleaseExternalID != "SSIS-001" {
		t.Fatalf("first metadata = %#v", got)
	}
	if got := job.FileReleaseMetadata[second]; !got.ReleaseLookupMatched || got.ReleaseLookupMethod != "filename_video_id_compact" || got.ReleaseExternalID != "ADN816" {
		t.Fatalf("second metadata = %#v", got)
	}
}

// TestCreateBatchJobContinuesWhenOneFileHasAmbiguousAutoDetect exercises the
// GHKR-75.wmv real-world report: a multi-file auto-detect batch used to
// abort job creation entirely ("auto-detect release for %s: %w") the moment
// any single file's stash_file_path matched more than one JAVBeacon
// release. That per-file failure is now non-fatal (release.Resolve degrades
// an ambiguous auto-detect to a recorded LookupError), so the batch job
// must still be created, with the other files' metadata resolved normally
// and the ambiguous file flagged rather than blocking everyone else.
func TestCreateBatchJobContinuesWhenOneFileHasAmbiguousAutoDetect(t *testing.T) {
	root := t.TempDir()
	clean := filepath.Join(root, "ssis-001.mp4")
	ambiguous := filepath.Join(root, "GHKR-75.wmv")
	for _, path := range []string{clean, ambiguous} {
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if stashPath := r.URL.Query().Get("stash_file_path"); strings.EqualFold(stashPath, ambiguous) {
			_, _ = io.WriteString(w, `[{"id":1,"video_id":"GHKR-75","title":"First"},{"id":2,"video_id":"GHKR-75","title":"Second"}]`)
			return
		}
		switch strings.ToLower(r.URL.Query().Get("video_id")) {
		case "ssis-001":
			_, _ = io.WriteString(w, `[{"id":3,"video_id":"SSIS-001","title":"Clean match"}]`)
		default:
			_, _ = io.WriteString(w, `[]`)
		}
	}))
	defer server.Close()

	m := &Manager{cfg: config.Config{}, log: slog.New(slog.NewTextHandler(io.Discard, nil)), jobs: map[string]*Job{}, queue: make(chan string, 1), subscribers: map[chan []byte]struct{}{}, persistence: testPersistence{}}
	m.UpdateJAVBeacon(config.JAVBeaconConfig{BaseURL: server.URL, TimeoutSec: 5})
	job, err := m.Create(Request{Inputs: []string{root}, Recursive: true, AutoDetectRelease: true})
	if err != nil {
		t.Fatalf("ambiguous auto-detect on one file must not abort batch job creation, got %v", err)
	}
	if len(job.FileReleaseMetadata) != 2 {
		t.Fatalf("expected metadata for both files, got %#v", job.FileReleaseMetadata)
	}
	if got := job.FileReleaseMetadata[clean]; !got.ReleaseLookupMatched || got.ReleaseExternalID != "SSIS-001" {
		t.Fatalf("clean file metadata = %#v", got)
	}
	if got := job.FileReleaseMetadata[ambiguous]; got.ReleaseLookupMatched || got.ReleaseLookupError == "" || !strings.Contains(got.ReleaseLookupError, "ambiguous") {
		t.Fatalf("ambiguous file metadata = %#v", got)
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

	if _, err := m.resolveFileRelease(nil, "ADN-803", "", "", "", false); err != nil {
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

	resolution, err := m.resolveFileRelease(nil, "ADN-803", "", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if resolution.LookupError == "" {
		t.Fatal("expected a lookup error explaining no JAVBeacon instance is configured")
	}
}
