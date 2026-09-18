package release

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// Match describes an exact JAVBeacon release match and the rule that found it.
type Match struct {
	Release Release
	Method  string
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
// filename stem as video_id, then filename stem with hyphens removed. An
// ambiguous stash_file_path match (2+ releases share the same path in
// JAVBeacon) does not stop here: the filename-derived rules are an
// independent matching signal and may still resolve uniquely, so they are
// tried before giving up. This never picks one of the ambiguous
// stash_file_path matches itself (TODO Part 4: "do not guess") - it only
// tries a different rule; if that also fails to find a unique match, the
// original stash_file_path ambiguity is what gets reported.
func DetectFile(ctx context.Context, client *Client, filePath string) (*Match, error) {
	filePath = strings.TrimSpace(filePath)
	if filePath == "" {
		return nil, nil
	}
	matches, err := client.ByStashFilePath(ctx, filePath)
	if err != nil {
		return nil, err
	}
	match, matchErr := uniqueMatch(matches, "stash_file_path", "stash_file_path", filePath)
	if match != nil {
		return match, nil
	}
	if matchErr != nil && !errors.Is(matchErr, ErrAmbiguous) {
		return nil, matchErr
	}
	filenameMatch, filenameErr := DetectFilename(ctx, client, filePath)
	if filenameMatch != nil || filenameErr != nil {
		return filenameMatch, filenameErr
	}
	return nil, matchErr
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
