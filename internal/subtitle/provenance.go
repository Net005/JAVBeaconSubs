package subtitle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	ProvenanceSchema    = "javbeaconsubs.subtitle-provenance.v1"
	ProvenanceGenerator = "JAVBeaconSubs"
)

type ProvenanceStatus string

const (
	ProvenanceUnknown  ProvenanceStatus = "unknown"
	ProvenanceValid    ProvenanceStatus = "valid"
	ProvenanceModified ProvenanceStatus = "modified"
	ProvenanceInvalid  ProvenanceStatus = "invalid"
	ProvenanceLegacy   ProvenanceStatus = "legacy"
)

type Provenance struct {
	Schema               string `json:"schema"`
	Generator            string `json:"generator"`
	GeneratorVersion     string `json:"generator_version"`
	CreatedAt            string `json:"created_at"`
	Language             string `json:"language"`
	TranscriptionBackend string `json:"transcription_backend,omitempty"`
	TranslationBackend   string `json:"translation_backend,omitempty"`
	SubtitleFormat       string `json:"subtitle_format"`
	SubtitleFile         string `json:"subtitle_file"`
	SubtitleSHA256       string `json:"subtitle_sha256"`
}

func NewProvenance(version, language, transcriptionBackend, translationBackend, path string, content []byte) Provenance {
	hash := sha256.Sum256(content)
	return Provenance{
		Schema: ProvenanceSchema, Generator: ProvenanceGenerator, GeneratorVersion: version,
		CreatedAt: time.Now().UTC().Format(time.RFC3339), Language: language,
		TranscriptionBackend: transcriptionBackend, TranslationBackend: translationBackend,
		SubtitleFormat: "srt", SubtitleFile: filepath.Base(path), SubtitleSHA256: hex.EncodeToString(hash[:]),
	}
}

func SidecarPath(subtitlePath string) string { return subtitlePath + ".json" }

func WriteProvenanceSidecar(subtitlePath string, value Provenance) error {
	if strings.TrimSpace(subtitlePath) == "" {
		return fmt.Errorf("subtitle provenance path is empty")
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode subtitle provenance: %w", err)
	}
	data = append(data, '\n')
	return atomicWriteBytes(SidecarPath(subtitlePath), data)
}

func VerifyProvenance(subtitlePath string) (ProvenanceStatus, *Provenance, error) {
	sidecar, err := os.ReadFile(SidecarPath(subtitlePath))
	if os.IsNotExist(err) {
		file, readErr := os.Open(subtitlePath)
		if readErr != nil {
			return ProvenanceUnknown, nil, readErr
		}
		defer file.Close()
		first := make([]byte, len("; generated-by=javbeaconsubs-v1"))
		read, _ := io.ReadFull(file, first)
		if strings.EqualFold(string(first[:read]), "; generated-by=javbeaconsubs-v1") {
			return ProvenanceLegacy, nil, nil
		}
		return ProvenanceUnknown, nil, nil
	}
	if err != nil {
		return ProvenanceInvalid, nil, err
	}
	var value Provenance
	if err := json.Unmarshal(sidecar, &value); err != nil {
		return ProvenanceInvalid, nil, nil
	}
	status, verifyErr := VerifyProvenanceRecord(subtitlePath, value)
	return status, &value, verifyErr
}

// VerifyProvenanceRecord verifies metadata already stored in the canonical
// .subtitles.json project or job database without requiring a sidecar.
func VerifyProvenanceRecord(subtitlePath string, value Provenance) (ProvenanceStatus, error) {
	if value.Schema != ProvenanceSchema || value.Generator != ProvenanceGenerator || value.SubtitleFormat != "srt" {
		return ProvenanceInvalid, nil
	}
	content, err := os.ReadFile(subtitlePath)
	if err != nil {
		return ProvenanceInvalid, err
	}
	hash := sha256.Sum256(content)
	if !strings.EqualFold(value.SubtitleSHA256, hex.EncodeToString(hash[:])) {
		return ProvenanceModified, nil
	}
	return ProvenanceValid, nil
}

func atomicWriteBytes(path string, content []byte) error {
	directory := filepath.Dir(path)
	file, err := os.CreateTemp(directory, ".javbeaconsubs-provenance-*.tmp")
	if err != nil {
		return fmt.Errorf("create provenance beside %s: %w", path, err)
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	if _, err = file.Write(content); err == nil {
		err = file.Sync()
	}
	if err == nil {
		err = file.Chmod(0o664)
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("publish provenance %s: %w", path, err)
	}
	return nil
}
