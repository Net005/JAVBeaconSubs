package models

import (
	"os"
	"path/filepath"
	"testing"

	"javbeaconsubs/internal/config"
)

func TestBalancedModelRolesKeepReazonInactive(t *testing.T) {
	cfg := config.Config{Whisper: config.WhisperConfig{
		QwenModel: "Qwen/Qwen3-ASR-1.7B", AlignerModel: "Qwen/Qwen3-ForcedAligner-0.6B",
		ReazonModel: "reazon-research/reazonspeech-nemo-v2", Model: "/missing/ggml-large-v3.bin",
	}}
	items := New(cfg).List()
	roles := map[string]Model{}
	for _, item := range items {
		roles[item.ID] = item
	}
	if roles["whisper"].Role != "Balanced fallback ASR" || roles["whisper"].Status != "active" {
		t.Fatalf("Whisper role/status = %q/%q", roles["whisper"].Role, roles["whisper"].Status)
	}
	if roles["reazon"].Role != "Experimental / inactive" || roles["reazon"].Status != "inactive" {
		t.Fatalf("Reazon role/status = %q/%q", roles["reazon"].Role, roles["reazon"].Status)
	}
}

func TestWhisperMetadataIncludesModelAndLastRuntimeState(t *testing.T) {
	directory := t.TempDir()
	modelPath := filepath.Join(directory, "ggml-large-v3-q5_0.bin")
	statusPath := filepath.Join(directory, "runtime.json")
	if err := os.WriteFile(modelPath, []byte("model"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statusPath, []byte(`{"last_load_result":"cpu_fallback_success","last_cuda_failure":"whisper_cuda_oom","execution_device":"cpu","cpu_fallback_available":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Whisper: config.WhisperConfig{
		Model: modelPath, WhisperDevice: "auto", WhisperStatusPath: statusPath, GPUFallbackCPU: true,
	}}
	items := New(cfg).List()
	var whisper Model
	for _, item := range items {
		if item.ID == "whisper" {
			whisper = item
		}
	}
	if whisper.ModelType != "large-v3" || whisper.Quantization != "q5_0" || whisper.FileSizeBytes != 5 {
		t.Fatalf("unexpected model metadata: %#v", whisper)
	}
	if whisper.LastLoadResult != "cpu_fallback_success" || whisper.LastCUDAFailure != "whisper_cuda_oom" || !whisper.CPUFallbackAvailable {
		t.Fatalf("unexpected runtime metadata: %#v", whisper)
	}
}
