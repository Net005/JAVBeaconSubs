package engine

import (
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"javbeaconsubs/internal/config"
	"javbeaconsubs/internal/subtitle"
)

func TestNewerGPTModel(t *testing.T) {
	for _, test := range []struct {
		current, previous string
		want              bool
	}{
		{"gpt-6", "gpt-5.6-luna", true},
		{"gpt-6.1-sol", "gpt-6-astra", true},
		{"gpt-5.6-sol", "gpt-5.6-luna", false},
		{"gpt-5.6-luna", "gpt-6", false},
		{"claude-4", "gpt-5.6-luna", false},
		{"gpt-6", "whisper.cpp:ggml-large-v3.bin", false},
	} {
		if got := newerGPTModel(test.current, test.previous); got != test.want {
			t.Errorf("newerGPTModel(%q, %q) = %t, want %t", test.current, test.previous, got, test.want)
		}
	}
}

func TestExistingModelReplacement(t *testing.T) {
	dir := t.TempDir()
	english := filepath.Join(dir, "movie.en.srt")
	project := filepath.Join(dir, "movie.subtitles.json")
	content := []byte("1\n00:00:00,000 --> 00:00:01,000\nHello\n")
	if err := os.WriteFile(english, content, 0600); err != nil {
		t.Fatal(err)
	}
	worker := &Runner{cfg: config.Config{Translation: config.TranslationConfig{Mode: "contextual", Model: "gpt-6"}}, log: slog.New(slog.NewTextHandler(io.Discard, nil)), overwriteIfModelNewer: true}
	if replace, _ := worker.existingModelReplacement(english, project); replace {
		t.Fatal("missing provenance replaced without opt-in")
	}
	worker.overwriteUnverifiedProvenance = true
	if replace, _ := worker.existingModelReplacement(english, project); !replace {
		t.Fatal("missing provenance was not replaced with opt-in")
	}
	provenance := subtitle.NewProvenance("test", "en", "qwen", "gpt-5.6-luna", english, content)
	projectData, err := json.Marshal(map[string]any{"subtitle_provenance": []ProvenanceArtifact{{SubtitlePath: english, Metadata: provenance}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(project, projectData, 0600); err != nil {
		t.Fatal(err)
	}
	worker.overwriteUnverifiedProvenance = false
	if replace, _ := worker.existingModelReplacement(english, project); !replace {
		t.Fatal("verified GPT-5.6 subtitles were not replaced by GPT-6")
	}
	worker.cfg.Translation.Model = "gpt-5.6-sol"
	if replace, _ := worker.existingModelReplacement(english, project); replace {
		t.Fatal("same numeric version must not replace")
	}
	worker.cfg.Translation.Model = "gpt-6"
	if err := os.WriteFile(english, []byte("user-edited"), 0600); err != nil {
		t.Fatal(err)
	}
	if replace, _ := worker.existingModelReplacement(english, project); replace {
		t.Fatal("edited subtitles replaced without opt-in")
	}
	worker.overwriteUnverifiedProvenance = true
	if replace, _ := worker.existingModelReplacement(english, project); !replace {
		t.Fatal("edited subtitles not replaced with opt-in")
	}
}
