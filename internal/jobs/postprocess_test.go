package jobs

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"javbeaconsubs/internal/config"
)

func TestShellPostProcessingReceivesJobJSON(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "complete.sh")
	output := filepath.Join(dir, "job.json")
	if err := os.WriteFile(script, []byte("set -euo pipefail\ncat > \"$TEST_POST_OUTPUT\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_POST_OUTPUT", output)
	processor := NewPostProcessor(config.PostProcessingConfig{Mode: "shell", ShellScript: script, TimeoutSec: 5}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := processor.Run(context.Background(), &Job{ID: "job-42", Status: "complete", Files: []string{"/mnt/data/movie.mkv"}}); err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), `"id":"job-42"`) {
		t.Fatalf("script did not receive job JSON: %s", payload)
	}
}
