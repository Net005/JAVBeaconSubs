package release

import (
	"context"
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
// filename stem as video_id, then filename stem with hyphens removed.
func DetectFile(ctx context.Context, client *Client, filePath string) (*Match, error) {
	filePath = strings.TrimSpace(filePath)
	if filePath == "" {
		return nil, nil
	}
	matches, err := client.ByStashFilePath(ctx, filePath)
	if err != nil {
		return nil, err
	}
	match, err := uniqueMatch(matches, "stash_file_path", "stash_file_path", filePath)
	if err != nil || match != nil {
		return match, err
	}
	return DetectFilename(ctx, client, filePath)
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
