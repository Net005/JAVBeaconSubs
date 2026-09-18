package release

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

// Match describes a JAVBeacon release match and the rule that found it.
// Ambiguous is true when Release was deterministically auto-picked among
// several releases that matched the same auto-detection rule, rather than
// being the sole result - callers must surface Ambiguous/AmbiguousMatches
// rather than treating the match as certain (TODO Part 4: "do not guess" -
// nothing here is picked without being recorded as a guess).
type Match struct {
	Release          Release
	Method           string
	Ambiguous        bool
	AmbiguousMatches int
}

// MatchVideoID performs one exact video_id lookup and refuses ambiguous data.
func MatchVideoID(ctx context.Context, client *Client, videoID, method string) (*Match, error) {
	matches, err := client.ByVideoID(ctx, strings.TrimSpace(videoID))
	if err != nil {
		return nil, err
	}
	return uniqueMatch(matches, method, "video_id", videoID)
}

// DetectFilename matches a filename (with or without an extension) against
// video_id, first as-is and then with ASCII hyphens removed.
func DetectFilename(ctx context.Context, client *Client, filename string) (*Match, error) {
	name := filepath.Base(strings.TrimSpace(filename))
	stem := strings.TrimSuffix(name, filepath.Ext(name))
	if stem == "" || stem == "." {
		return nil, nil
	}
	match, err := MatchVideoID(ctx, client, stem, "filename_video_id")
	if err != nil || match != nil {
		return match, err
	}
	compact := strings.ReplaceAll(stem, "-", "")
	if compact == stem || compact == "" {
		return nil, nil
	}
	return MatchVideoID(ctx, client, compact, "filename_video_id_compact")
}

// DetectFile applies the documented auto-detection order: exact full path,
// filename stem as video_id, then filename stem with hyphens removed.
// Auto-detection is opt-in, best-effort matching (unlike an explicit
// javbeacon_release_id/release_external_id lookup): an ambiguous result at
// any stage does not fail the whole chain. When stash_file_path itself is
// ambiguous, the filename-derived rules - an independent matching signal -
// are still tried first, since they may resolve uniquely. If nothing ever
// resolves to a single certain match, the most complete candidate from the
// stash_file_path match (the strongest, path-exact signal) is
// deterministically auto-picked and flagged as ambiguous, rather than
// leaving the file unmatched (TODO Part 4 in spirit: nothing is guessed
// silently, every pick is recorded on the returned Match).
func DetectFile(ctx context.Context, client *Client, filePath string) (*Match, error) {
	filePath = strings.TrimSpace(filePath)
	if filePath == "" {
		return nil, nil
	}
	matches, err := client.ByStashFilePath(ctx, filePath)
	if err != nil {
		return nil, err
	}
	switch len(matches) {
	case 0:
		return autoDetectFilename(ctx, client, filePath)
	case 1:
		return &Match{Release: matches[0], Method: "stash_file_path"}, nil
	}
	if filenameMatch, filenameErr := autoDetectFilename(ctx, client, filePath); filenameErr != nil {
		return nil, filenameErr
	} else if filenameMatch != nil && !filenameMatch.Ambiguous {
		return filenameMatch, nil
	}
	return &Match{Release: pickAmbiguousMatch(matches), Method: "stash_file_path", Ambiguous: true, AmbiguousMatches: len(matches)}, nil
}

func uniqueMatch(matches []Release, method, field, value string) (*Match, error) {
	switch len(matches) {
	case 0:
		return nil, nil
	case 1:
		return &Match{Release: matches[0], Method: method}, nil
	default:
		return nil, fmt.Errorf("%w: %s %q matched %d releases in JAVBeacon", ErrAmbiguous, field, value, len(matches))
	}
}

// releaseCompleteness scores a release by how much usable metadata it
// carries, used to break ties among ambiguous matches: a release with a
// title and story is more useful (and more likely to be the intended one)
// than a bare stub entry.
func releaseCompleteness(r Release) int {
	score := 0
	if strings.TrimSpace(r.Title) != "" {
		score++
	}
	if strings.TrimSpace(r.Story) != "" {
		score++
	}
	return score
}

// pickAmbiguousMatch deterministically chooses one release from a set of
// ambiguous candidates: the release with the most complete metadata,
// breaking ties by the lowest release ID so the pick is stable and
// reproducible regardless of API response ordering.
func pickAmbiguousMatch(matches []Release) Release {
	best := matches[0]
	bestScore := releaseCompleteness(best)
	for _, candidate := range matches[1:] {
		score := releaseCompleteness(candidate)
		if score > bestScore || (score == bestScore && candidate.ID < best.ID) {
			best, bestScore = candidate, score
		}
	}
	return best
}

// autoDetectByVideoID performs one video_id lookup for the auto-detection
// chain. Unlike an explicit single-ID lookup (MatchVideoID, used for a
// caller-supplied release_external_id or the manual JAVBeacon test/preview
// endpoint), auto-detection is opt-in, best-effort matching: an ambiguous
// result does not fail the lookup outright. It deterministically picks the
// most complete candidate and flags the match as ambiguous so it is never
// presented as certain.
func autoDetectByVideoID(ctx context.Context, client *Client, videoID, method string) (*Match, error) {
	matches, err := client.ByVideoID(ctx, strings.TrimSpace(videoID))
	if err != nil {
		return nil, err
	}
	switch len(matches) {
	case 0:
		return nil, nil
	case 1:
		return &Match{Release: matches[0], Method: method}, nil
	default:
		return &Match{Release: pickAmbiguousMatch(matches), Method: method, Ambiguous: true, AmbiguousMatches: len(matches)}, nil
	}
}

// autoDetectFilename mirrors DetectFilename for the auto-detection chain:
// see autoDetectByVideoID for why ambiguous filename-derived matches are
// flagged and auto-picked rather than failing the chain.
func autoDetectFilename(ctx context.Context, client *Client, filename string) (*Match, error) {
	name := filepath.Base(strings.TrimSpace(filename))
	stem := strings.TrimSuffix(name, filepath.Ext(name))
	if stem == "" || stem == "." {
		return nil, nil
	}
	match, err := autoDetectByVideoID(ctx, client, stem, "filename_video_id")
	if err != nil || match != nil {
		return match, err
	}
	compact := strings.ReplaceAll(stem, "-", "")
	if compact == stem || compact == "" {
		return nil, nil
	}
	return autoDetectByVideoID(ctx, client, compact, "filename_video_id_compact")
}
