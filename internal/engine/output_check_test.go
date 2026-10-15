package engine

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestOutputDirectoryPreflightPreservesExistingOutput(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "example.mp4")
	output := filepath.Join(dir, "example.en.srt")
	if err := os.WriteFile(input, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(output, []byte("synthetic existing output"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := checkOutputDirectory(input); err != nil {
		t.Fatal(err)
	}
	files, _ := os.ReadDir(dir)
	data, _ := os.ReadFile(output)
	if len(files) != 2 || string(data) != "synthetic existing output" {
		t.Fatal("probe changed output or left temporary files")
	}
	if err := checkOutputDirectory(filepath.Join(dir, "missing.mp4")); err == nil {
		t.Fatal("offline or missing input accepted")
	}
}

func TestOutputDirectoryPreflightRejectsReadOnlyDirectory(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	dir := t.TempDir()
	input := filepath.Join(dir, "example.mp4")
	if err := os.WriteFile(input, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0755)
	if err := checkOutputDirectory(input); err == nil || !strings.Contains(err.Error(), "not writable") {
		t.Fatalf("expected early permissions failure, got %v", err)
	}
}
