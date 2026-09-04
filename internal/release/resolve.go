package release

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

var (
	// ErrConflict is returned when both an internal javbeacon_release_id and
	// an external release_external_id are supplied and the resolved
	// release's own video_id does not match the supplied external id
	// (TODO Part 5).
	ErrConflict = errors.New("release metadata conflict")
	// ErrAmbiguous is returned when an external/catalog ID lookup matches
	// more than one release in JAVBeacon (TODO Part 4: "do not guess").
	ErrAmbiguous = errors.New("release lookup ambiguous")
)

// Request is the subset of jobs.Request needed to resolve release metadata.
// Defined locally, rather than importing internal/jobs, so internal/jobs can
// import this package without a cycle.
type Request struct {
	JAVBeaconReleaseID *int64
	ReleaseExternalID  string
	ManualTitle        string
	ManualStory        string
	FilePath           string
	AutoDetect         bool
}

// Resolution is what Resolve fills in on a Job.
type Resolution struct {
	Title       string
	Story       string
	TitleSource string // "manual" | "javbeacon" | ""
	StorySource string // "manual" | "javbeacon" | ""

	LookupMethod  string // explicit ID or one of the exact auto-detection rules
	LookupMatched bool
	// LookupError carries a non-fatal diagnostic (JAVBeacon unreachable, not
	// configured) for an explicit-ID lookup that could not be completed -
	// distinct from job creation actually failing (TODO Part 39: "manual
	// metadata should still be usable"). Empty when nothing went wrong.
	LookupError string

	Provider       string // JAVBeacon's Release.Source ("GIGA" | "JavLibrary" | "")
	MatchedVideoID string
	StashFilePath  string

	// StashSceneID/StashURL are populated only when the matched release is
	// locally matched in the operator's StashApp instance, letting the UI
	// show and link directly to it.
	StashSceneID string
	StashURL     string
}

// Resolve implements the deterministic release-lookup precedence:
// javbeacon_release_id > release_external_id > opt-in exact media matching >
// none. It never guesses: an internal ID that doesn't resolve
// (ErrNotFound), an internal/external ID mismatch (ErrConflict), or an
// external ID matching more than one release (ErrAmbiguous) are all hard
// errors - the caller should fail job creation rather than silently
// continuing with the wrong release or no release at all.
//
// A JAVBeacon instance that is simply unreachable for an *explicit* ID
// lookup is different: it is reported via Resolution.LookupError but does
// NOT fail Resolve, so a job with manual title/story (or none at all) can
// still be created rather than being blocked by an external service outage.
//
// When client is nil (no JAVBeacon configured), any requested lookup resolves
// the same way: LookupError set, no hard failure.
func Resolve(ctx context.Context, client *Client, req Request) (Resolution, error) {
	res := Resolution{
		Title:        strings.TrimSpace(req.ManualTitle),
		Story:        req.ManualStory,
		LookupMethod: "none",
	}
	if res.Title != "" {
		res.TitleSource = "manual"
	}
	if strings.TrimSpace(req.ManualStory) != "" {
		res.StorySource = "manual"
	}

	var matched *Release
	switch {
	case req.JAVBeaconReleaseID != nil:
		res.LookupMethod = "javbeacon_release_id"
		if client == nil {
			res.LookupError = "javbeacon_release_id was supplied but no JAVBeacon instance is configured"
			return res, nil
		}
		found, err := client.ByID(ctx, *req.JAVBeaconReleaseID)
		if err != nil {
			if errors.Is(err, ErrUnavailable) {
				res.LookupError = err.Error()
				return res, nil
			}
			// ErrNotFound (or anything else unexpected): do not silently
			// bind to a filename-derived different release - fail job
			// creation with a clear diagnostic (TODO Part 3).
			return res, fmt.Errorf("javbeacon_release_id %d: %w", *req.JAVBeaconReleaseID, err)
		}
		if external := strings.TrimSpace(req.ReleaseExternalID); external != "" && !strings.EqualFold(external, found.VideoID) {
			return res, fmt.Errorf("%w: javbeacon_release_id %d resolves to video_id %q, which does not match supplied release_external_id %q",
				ErrConflict, *req.JAVBeaconReleaseID, found.VideoID, external)
		}
		matched = &found
	case strings.TrimSpace(req.ReleaseExternalID) != "":
		res.LookupMethod = "external_release_id"
		if client == nil {
			res.LookupError = "release_external_id was supplied but no JAVBeacon instance is configured"
			return res, nil
		}
		found, err := client.ByVideoID(ctx, strings.TrimSpace(req.ReleaseExternalID))
		if err != nil {
			if errors.Is(err, ErrUnavailable) {
				res.LookupError = err.Error()
				return res, nil
			}
			return res, fmt.Errorf("release_external_id %q: %w", req.ReleaseExternalID, err)
		}
		switch len(found) {
		case 0:
			// No match: not an error, falls through to manual metadata.
		case 1:
			matched = &found[0]
		default:
			return res, fmt.Errorf("%w: release_external_id %q matched %d releases in JAVBeacon",
				ErrAmbiguous, req.ReleaseExternalID, len(found))
		}
	case req.AutoDetect:
		res.LookupMethod = "auto_detect"
		if client == nil {
			res.LookupError = "auto-detection was requested but no JAVBeacon instance is configured"
			return res, nil
		}
		found, err := DetectFile(ctx, client, req.FilePath)
		if err != nil {
			if errors.Is(err, ErrUnavailable) {
				res.LookupError = err.Error()
				return res, nil
			}
			return res, err
		}
		if found != nil {
			res.LookupMethod = found.Method
			matched = &found.Release
		}
	}

	if matched != nil {
		res.LookupMatched = true
		res.Provider = matched.Source
		res.MatchedVideoID = matched.VideoID
		res.StashFilePath = matched.StashFilePath
		if res.Title == "" && strings.TrimSpace(matched.Title) != "" {
			res.Title = matched.Title
			res.TitleSource = "javbeacon"
		}
		if res.Story == "" && strings.TrimSpace(matched.Story) != "" {
			res.Story = matched.Story
			res.StorySource = "javbeacon"
		}
		if client != nil {
			if sceneURL := client.StashSceneURL(ctx, *matched); sceneURL != "" {
				res.StashSceneID = matched.StashSceneID
				res.StashURL = sceneURL
			}
		}
	}
	return res, nil
}
