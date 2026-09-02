package engine

import (
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"javbeaconsubs/internal/config"
	"javbeaconsubs/internal/subtitle"
)

func TestAtomicWriteCreatesGroupWritableSubtitle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "movie.en.srt")
	if err := atomicWrite(path, "subtitle"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0o664); got != want {
		t.Fatalf("subtitle mode = %o, want %o", got, want)
	}
}

func TestValidateSegmentsRejectsCollapsedLongTail(t *testing.T) {
	segments := []subtitle.Segment{
		{StartMS: 403_600, EndMS: 8_659_490, Text: "collapsed tail"},
	}
	if err := validateSegments(segments, 8_659_490, 60_000); err == nil {
		t.Fatal("accepted a segment spanning the remainder of a feature-length recording")
	}
}

func TestValidateSegmentsAcceptsOrderedSubtitleRows(t *testing.T) {
	segments := []subtitle.Segment{
		{StartMS: 1_000, EndMS: 2_500, Text: "one"},
		{StartMS: 3_000, EndMS: 5_000, Text: "two"},
	}
	if err := validateSegments(segments, 6_000, 60_000); err != nil {
		t.Fatal(err)
	}
}

func TestExistingArtifactOnlyReturnsRegularFiles(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "movie.en.srt")
	if got := existingArtifact(path); got != "" {
		t.Fatalf("missing artifact = %q", got)
	}
	if err := os.WriteFile(path, []byte("subtitle"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := existingArtifact(path); got != path {
		t.Fatalf("existing artifact = %q, want %q", got, path)
	}
	if got := existingArtifact(root); got != "" {
		t.Fatalf("directory artifact = %q", got)
	}
}

func TestWriteSRTKeepsPrimaryOutputWhenSidecarFails(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "movie.en.srt")
	if err := os.Mkdir(subtitle.SidecarPath(path), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &Runner{
		cfg: config.Config{Output: config.OutputConfig{MaxLineChars: 46, MaxLines: 2, WriteProvenance: true}},
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	result := Result{}
	if err := runner.writeSRT(&result, path, "en", "qwen", "contextual", []subtitle.Segment{{StartMS: 1000, EndMS: 2000, Text: "Hello."}}); err != nil {
		t.Fatal(err)
	}
	if existingArtifact(path) == "" || len(result.Warnings) != 1 || len(result.SubtitleProvenance) != 1 || result.SubtitleProvenance[0].Status != subtitle.ProvenanceValid {
		t.Fatalf("sidecar failure was not preserved as a nonfatal warning: %#v", result)
	}
}

func TestExistingProvenancePrefersSubtitleProjectMetadata(t *testing.T) {
	root := t.TempDir()
	srtPath := filepath.Join(root, "movie.en.srt")
	projectPath := filepath.Join(root, "movie.subtitles.json")
	content := []byte("1\n00:00:01,000 --> 00:00:02,000\nHello.\n\n")
	if err := os.WriteFile(srtPath, content, 0o664); err != nil {
		t.Fatal(err)
	}
	metadata := subtitle.NewProvenance("0.3.0", "en", "qwen", "contextual", srtPath, content)
	project, err := json.Marshal(map[string]any{"subtitle_provenance": []ProvenanceArtifact{{SubtitlePath: srtPath, Status: subtitle.ProvenanceValid, Metadata: metadata}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(projectPath, project, 0o664); err != nil {
		t.Fatal(err)
	}
	result := Result{}
	result.loadExistingProvenance(projectPath, srtPath)
	if len(result.SubtitleProvenance) != 1 || result.SubtitleProvenance[0].Status != subtitle.ProvenanceValid || result.SubtitleProvenance[0].SidecarPath != "" {
		t.Fatalf("project provenance was not used: %#v", result.SubtitleProvenance)
	}
}
