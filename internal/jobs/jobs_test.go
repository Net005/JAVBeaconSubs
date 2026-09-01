package jobs

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"javbeaconsubs/internal/config"
)

type testPersistence struct{}

func (testPersistence) Save(*Job) error       { return nil }
func (testPersistence) Load() ([]*Job, error) { return nil, nil }

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
