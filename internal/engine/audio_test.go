package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestExtractAudioRetriesDamagedAAC(t *testing.T) {
	output := filepath.Join(t.TempDir(), "audio.wav")
	var calls [][]string
	execute := func(_ context.Context, name string, args ...string) error {
		calls = append(calls, append([]string{name}, args...))
		if len(calls) == 1 {
			return errors.New("channel element 0.0 is not allocated; Error while decoding stream")
		}
		return os.WriteFile(output, make([]byte, 128), 0o600)
	}
	recovered, err := extractAudio(context.Background(), "damaged.mp4", output, execute)
	if err != nil || !recovered || len(calls) != 2 {
		t.Fatalf("recovered=%v calls=%d err=%v", recovered, len(calls), err)
	}
	if !slices.Contains(calls[1], "+discardcorrupt+genpts") || !slices.Contains(calls[1], "1.0") || !strings.Contains(strings.Join(calls[1], " "), "aresample=async=1") {
		t.Fatalf("recovery arguments do not enable damaged-stream handling: %q", calls[1])
	}
}

func TestExtractAudioDoesNotRetryUnrelatedFailure(t *testing.T) {
	calls := 0
	_, err := extractAudio(context.Background(), "missing.mp4", filepath.Join(t.TempDir(), "audio.wav"), func(context.Context, string, ...string) error {
		calls++
		return errors.New("No such file or directory")
	})
	if err == nil || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestValidateExtractedAudioRejectsEmptyWAV(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audio.wav")
	if err := os.WriteFile(path, make([]byte, 44), 0o600); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(validateExtractedAudio(path), errInvalidExtractedAudio) {
		t.Fatal("header-only WAV was accepted")
	}
}
