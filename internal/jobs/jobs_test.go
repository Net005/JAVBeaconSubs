package jobs

import (
	"os"
	"path/filepath"
	"testing"

	"javbeaconsubs/internal/config"
)

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
