package engine

import (
	"os"
	"path/filepath"
	"testing"

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
