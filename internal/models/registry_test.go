package models

import (
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
