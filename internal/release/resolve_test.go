package release

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeJAVBeacon stands in for a real JAVBeacon instance, serving canned
// /api/releases... responses so these tests never touch the network.
func fakeJAVBeacon(t *testing.T, byID map[int64]Release, byVideoID map[string][]Release, settings map[string]string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/releases/{id}", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			w.WriteHeader(401)
			return
		}
		idNum, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			w.WriteHeader(400)
			return
		}
		release, ok := byID[idNum]
		if !ok {
			w.WriteHeader(404)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "release not found"})
			return
		}
		_ = json.NewEncoder(w).Encode(release)
	})
	mux.HandleFunc("GET /api/releases", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			w.WriteHeader(401)
			return
		}
		videoID := strings.ToLower(r.URL.Query().Get("video_id"))
		stashPath := strings.ToLower(r.URL.Query().Get("stash_file_path"))
		var matches []Release
		if stashPath != "" {
			seen := map[int64]bool{}
			for _, item := range byID {
				if strings.EqualFold(item.StashFilePath, stashPath) && !seen[item.ID] {
					matches = append(matches, item)
					seen[item.ID] = true
				}
			}
			for _, releases := range byVideoID {
				for _, item := range releases {
					if strings.EqualFold(item.StashFilePath, stashPath) && !seen[item.ID] {
						matches = append(matches, item)
						seen[item.ID] = true
					}
				}
			}
		} else {
			for key, releases := range byVideoID {
				if strings.EqualFold(key, videoID) {
					matches = releases
					break
				}
			}
		}
		_ = json.NewEncoder(w).Encode(matches)
	})
	mux.HandleFunc("GET /api/settings", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(settings)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func testClient(t *testing.T, server *httptest.Server) *Client {
	t.Helper()
	c := NewClient(server.URL, "test-key", 2*time.Second)
	if c == nil {
		t.Fatal("NewClient returned nil for a non-empty base URL")
	}
	return c
}

func TestTrimVideoIDPrefix(t *testing.T) {
	tests := []struct {
		title, videoID, want string
	}{
		{"DSOD-03 For One Week", "DSOD-03", "For One Week"},
		{"dsod-03: For One Week", "DSOD-03", "For One Week"},
		{"DSOD-03 — For One Week", "DSOD-03", "For One Week"},
		{"DSOD-030 Is Different", "DSOD-03", "DSOD-030 Is Different"},
		{"DSOD-03", "DSOD-03", "DSOD-03"},
	}
	for _, tc := range tests {
		if got := trimVideoIDPrefix(tc.title, tc.videoID); got != tc.want {
			t.Errorf("trimVideoIDPrefix(%q, %q) = %q, want %q", tc.title, tc.videoID, got, tc.want)
		}
	}
}

func TestResolveByJAVBeaconReleaseIDSucceeds(t *testing.T) {
	server := fakeJAVBeacon(t,
		map[int64]Release{12345: {ID: 12345, VideoID: "ADN-803", Title: "Example Title", Story: "Example story.", Source: "GIGA"}},
		nil, nil,
	)
	client := testClient(t, server)
	id := int64(12345)
	res, err := Resolve(context.Background(), client, Request{JAVBeaconReleaseID: &id})
	if err != nil {
		t.Fatal(err)
	}
	if !res.LookupMatched || res.LookupMethod != "javbeacon_release_id" {
		t.Fatalf("resolution = %#v", res)
	}
	if res.Title != "Example Title" || res.TitleSource != "javbeacon" || res.Story != "Example story." || res.StorySource != "javbeacon" || res.Provider != "GIGA" {
		t.Fatalf("resolution = %#v", res)
	}
}

func TestResolveByReleaseExternalIDSucceedsCaseInsensitive(t *testing.T) {
	server := fakeJAVBeacon(t, nil,
		map[string][]Release{"adn-803": {{ID: 1, VideoID: "ADN-803", Title: "T", Source: "JavLibrary"}}},
		nil,
	)
	client := testClient(t, server)
	res, err := Resolve(context.Background(), client, Request{ReleaseExternalID: "adn-803"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.LookupMatched || res.LookupMethod != "external_release_id" || res.Provider != "JavLibrary" {
		t.Fatalf("resolution = %#v", res)
	}
}

func TestResolveInvalidInternalIDReturnsNotFound(t *testing.T) {
	server := fakeJAVBeacon(t, map[int64]Release{}, nil, nil)
	client := testClient(t, server)
	id := int64(999)
	_, err := Resolve(context.Background(), client, Request{JAVBeaconReleaseID: &id})
	if err == nil || !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestResolveInternalAndExternalMismatchReturnsConflict(t *testing.T) {
	server := fakeJAVBeacon(t,
		map[int64]Release{1: {ID: 1, VideoID: "ADN-803", Title: "T"}},
		nil, nil,
	)
	client := testClient(t, server)
	id := int64(1)
	_, err := Resolve(context.Background(), client, Request{JAVBeaconReleaseID: &id, ReleaseExternalID: "SPSF-50"})
	if err == nil || !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
}

func TestResolveExactIDWinsOverConflictingExternal(t *testing.T) {
	// Matching video_id (case-insensitive) between the two supplied IDs
	// must succeed, not conflict.
	server := fakeJAVBeacon(t,
		map[int64]Release{1: {ID: 1, VideoID: "ADN-803", Title: "T"}},
		nil, nil,
	)
	client := testClient(t, server)
	id := int64(1)
	res, err := Resolve(context.Background(), client, Request{JAVBeaconReleaseID: &id, ReleaseExternalID: "adn-803"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.LookupMatched || res.MatchedVideoID != "ADN-803" {
		t.Fatalf("resolution = %#v", res)
	}
}

func TestResolveAmbiguousExternalMatch(t *testing.T) {
	server := fakeJAVBeacon(t, nil,
		map[string][]Release{"dup-1": {{ID: 1, VideoID: "DUP-1"}, {ID: 2, VideoID: "DUP-1"}}},
		nil,
	)
	client := testClient(t, server)
	_, err := Resolve(context.Background(), client, Request{ReleaseExternalID: "DUP-1"})
	if err == nil || !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("expected ErrAmbiguous, got %v", err)
	}
}

func TestResolveDoesNotAutoDetectUnlessRequested(t *testing.T) {
	res, err := Resolve(context.Background(), nil, Request{ManualTitle: "Manual Title"})
	if err != nil {
		t.Fatal(err)
	}
	if res.LookupMethod != "none" || res.LookupMatched {
		t.Fatalf("resolution = %#v", res)
	}
	if res.Title != "Manual Title" || res.TitleSource != "manual" {
		t.Fatalf("manual title should survive with no ID supplied: %#v", res)
	}
}

func TestResolveAutoDetectsExactStashPathFirstCaseInsensitive(t *testing.T) {
	item := Release{ID: 1, VideoID: "OTHER-1", StashFilePath: "/P2P/HTTP/ADN-816.mp4", Title: "Path match"}
	server := fakeJAVBeacon(t, map[int64]Release{1: item}, map[string][]Release{"ADN-816": {{ID: 2, VideoID: "ADN-816", Title: "Filename match"}}}, nil)
	res, err := Resolve(context.Background(), testClient(t, server), Request{AutoDetect: true, FilePath: "/p2p/http/adn-816.MP4"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.LookupMatched || res.LookupMethod != "stash_file_path" || res.Title != "Path match" || res.StashFilePath != item.StashFilePath {
		t.Fatalf("resolution = %#v", res)
	}
}

func TestResolveAutoDetectsFilenameVideoIDCaseInsensitive(t *testing.T) {
	server := fakeJAVBeacon(t, nil, map[string][]Release{"SSIS-001": {{ID: 1, VideoID: "SSIS-001", Title: "Found"}}}, nil)
	res, err := Resolve(context.Background(), testClient(t, server), Request{AutoDetect: true, FilePath: "/movies/ssis-001.mkv"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.LookupMatched || res.LookupMethod != "filename_video_id" || res.MatchedVideoID != "SSIS-001" {
		t.Fatalf("resolution = %#v", res)
	}
}

func TestResolveAutoDetectsFilenameWithoutHyphens(t *testing.T) {
	server := fakeJAVBeacon(t, nil, map[string][]Release{"ADN816": {{ID: 1, VideoID: "ADN816"}}}, nil)
	res, err := Resolve(context.Background(), testClient(t, server), Request{AutoDetect: true, FilePath: "/movies/ADN-816.mp4"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.LookupMatched || res.LookupMethod != "filename_video_id_compact" {
		t.Fatalf("resolution = %#v", res)
	}
}

func TestResolveExplicitIDPrecedesAutoDetection(t *testing.T) {
	server := fakeJAVBeacon(t,
		map[int64]Release{7: {ID: 7, VideoID: "EXPLICIT-7", Title: "Explicit"}},
		map[string][]Release{"ADN-816": {{ID: 8, VideoID: "ADN-816", Title: "Automatic"}}}, nil)
	id := int64(7)
	res, err := Resolve(context.Background(), testClient(t, server), Request{JAVBeaconReleaseID: &id, AutoDetect: true, FilePath: "/movies/ADN-816.mp4"})
	if err != nil {
		t.Fatal(err)
	}
	if res.LookupMethod != "javbeacon_release_id" || res.Title != "Explicit" {
		t.Fatalf("resolution = %#v", res)
	}
}

func TestResolveJAVBeaconUnavailableDoesNotBreakManualMetadata(t *testing.T) {
	// No client configured at all (nil) - an explicit external ID lookup
	// must not fail job creation, and manual title/story must still apply.
	res, err := Resolve(context.Background(), nil, Request{ReleaseExternalID: "ADN-803", ManualTitle: "Manual Title", ManualStory: "Manual story."})
	if err != nil {
		t.Fatalf("unavailable JAVBeacon must not fail Resolve: %v", err)
	}
	if res.LookupError == "" {
		t.Fatal("expected a non-fatal LookupError to be recorded")
	}
	if res.Title != "Manual Title" || res.TitleSource != "manual" || res.Story != "Manual story." || res.StorySource != "manual" {
		t.Fatalf("manual metadata was not preserved: %#v", res)
	}
	if res.LookupMatched {
		t.Fatalf("unavailable lookup must not report matched: %#v", res)
	}
}

func TestResolveJAVBeaconUnreachableServerIsNonFatal(t *testing.T) {
	// A configured but unreachable client (connection refused) must behave
	// the same as "not configured" for an explicit external-ID lookup.
	client := NewClient("http://127.0.0.1:1", "test-key", 200*time.Millisecond)
	res, err := Resolve(context.Background(), client, Request{ReleaseExternalID: "ADN-803"})
	if err != nil {
		t.Fatalf("unreachable JAVBeacon must not fail Resolve for an external-id lookup: %v", err)
	}
	if res.LookupError == "" {
		t.Fatal("expected a non-fatal LookupError to be recorded")
	}
}

func TestResolveManualMetadataWinsOverJAVBeaconMatch(t *testing.T) {
	server := fakeJAVBeacon(t,
		map[int64]Release{1: {ID: 1, VideoID: "ADN-803", Title: "JAVBeacon Title", Story: "JAVBeacon story."}},
		nil, nil,
	)
	client := testClient(t, server)
	id := int64(1)
	res, err := Resolve(context.Background(), client, Request{JAVBeaconReleaseID: &id, ManualTitle: "My Title", ManualStory: "My story."})
	if err != nil {
		t.Fatal(err)
	}
	if res.Title != "My Title" || res.TitleSource != "manual" || res.Story != "My story." || res.StorySource != "manual" {
		t.Fatalf("manual metadata should win over a JAVBeacon match: %#v", res)
	}
	// The match itself is still recorded (provider, matched video id).
	if !res.LookupMatched || res.MatchedVideoID != "ADN-803" {
		t.Fatalf("resolution = %#v", res)
	}
}

func TestResolveStashLinkOnlyWhenLocallyMatched(t *testing.T) {
	server := fakeJAVBeacon(t,
		map[int64]Release{
			1: {ID: 1, VideoID: "LOCAL-1", Local: true, StashSceneID: "42"},
			2: {ID: 2, VideoID: "REMOTE-1", Local: false, StashSceneID: ""},
		},
		nil,
		map[string]string{"stash_base_url": "http://stash.local:9999/"},
	)
	client := testClient(t, server)

	localID := int64(1)
	local, err := Resolve(context.Background(), client, Request{JAVBeaconReleaseID: &localID})
	if err != nil {
		t.Fatal(err)
	}
	if local.StashSceneID != "42" || local.StashURL != "http://stash.local:9999/scenes/42" {
		t.Fatalf("expected a Stash link for a locally matched release: %#v", local)
	}

	remoteID := int64(2)
	remote, err := Resolve(context.Background(), client, Request{JAVBeaconReleaseID: &remoteID})
	if err != nil {
		t.Fatal(err)
	}
	if remote.StashURL != "" || remote.StashSceneID != "" {
		t.Fatalf("expected no Stash link for a non-local release: %#v", remote)
	}
}
