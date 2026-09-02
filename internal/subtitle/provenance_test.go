package subtitle

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestProvenanceSidecarAndVerification(t *testing.T) {
	path := filepath.Join(t.TempDir(), "movie.en.srt")
	content := []byte("1\n00:00:01,000 --> 00:00:02,000\nHello.\n\n")
	if err := os.WriteFile(path, content, 0o664); err != nil {
		t.Fatal(err)
	}
	metadata := NewProvenance("0.3.0", "en", "qwen", "contextual-model", path, content)
	if err := WriteProvenanceSidecar(path, metadata); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(SidecarPath(path))
	if err != nil {
		t.Fatal(err)
	}
	var decoded Provenance
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Schema != ProvenanceSchema || decoded.Generator != ProvenanceGenerator || decoded.SubtitleFile != filepath.Base(path) || decoded.SubtitleSHA256 == "" {
		t.Fatalf("unexpected provenance: %#v", decoded)
	}
	status, _, err := VerifyProvenance(path)
	if err != nil || status != ProvenanceValid {
		t.Fatalf("verification = %q, %v", status, err)
	}
	if err := os.WriteFile(path, append(content, []byte("modified")...), 0o664); err != nil {
		t.Fatal(err)
	}
	status, _, err = VerifyProvenance(path)
	if err != nil || status != ProvenanceModified {
		t.Fatalf("modified verification = %q, %v", status, err)
	}
}

func TestProvenanceMissingInvalidAndLegacy(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "missing-sidecar.srt")
	if err := os.WriteFile(missing, []byte("1\n00:00:00,000 --> 00:00:01,000\nHi\n"), 0o664); err != nil {
		t.Fatal(err)
	}
	status, _, err := VerifyProvenance(missing)
	if err != nil || status != ProvenanceUnknown {
		t.Fatalf("missing sidecar = %q, %v", status, err)
	}
	if err := os.WriteFile(SidecarPath(missing), []byte("{broken"), 0o664); err != nil {
		t.Fatal(err)
	}
	status, _, err = VerifyProvenance(missing)
	if err != nil || status != ProvenanceInvalid {
		t.Fatalf("invalid sidecar = %q, %v", status, err)
	}
	legacy := filepath.Join(root, "legacy.srt")
	if err := os.WriteFile(legacy, []byte("; generated-by=javbeaconsubs-v1\n1\n00:00:00,000 --> 00:00:01,000\nHi\n"), 0o664); err != nil {
		t.Fatal(err)
	}
	status, _, err = VerifyProvenance(legacy)
	if err != nil || status != ProvenanceLegacy {
		t.Fatalf("legacy marker = %q, %v", status, err)
	}
}

func TestProvenanceSidecarFailureDoesNotRemoveSRT(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "movie.en.srt")
	content := []byte("1\n00:00:00,000 --> 00:00:01,000\nHi\n")
	if err := os.WriteFile(path, content, 0o664); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(SidecarPath(path), 0o755); err != nil {
		t.Fatal(err)
	}
	err := WriteProvenanceSidecar(path, NewProvenance("0.3.0", "en", "qwen", "", path, content))
	if err == nil {
		t.Fatal("expected sidecar publication failure")
	}
	if got, readErr := os.ReadFile(path); readErr != nil || string(got) != string(content) {
		t.Fatalf("primary SRT was lost after sidecar failure: %q, %v", got, readErr)
	}
}
