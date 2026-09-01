package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	Listen         string               `json:"listen"`
	APIToken       string               `json:"api_token"`
	WebUsername    string               `json:"web_username"`
	WebPassword    string               `json:"web_password"`
	DatabasePath   string               `json:"database_path"`
	UploadDir      string               `json:"upload_dir"`
	MaxUploadGB    int64                `json:"max_upload_gb"`
	Whisper        WhisperConfig        `json:"whisper"`
	Translation    TranslationConfig    `json:"translation"`
	PostProcessing PostProcessingConfig `json:"post_processing"`
	Output         OutputConfig         `json:"output"`
	Workers        int                  `json:"workers"`
}

type WhisperConfig struct {
	Binary         string  `json:"binary"`
	Model          string  `json:"model"`
	VADModel       string  `json:"vad_model"`
	Language       string  `json:"language"`
	Threads        int     `json:"threads"`
	UseGPU         bool    `json:"use_gpu"`
	BeamSize       int     `json:"beam_size"`
	VAD            bool    `json:"vad"`
	VADThreshold   float64 `json:"vad_threshold"`
	MinSpeechMS    int     `json:"vad_min_speech_ms"`
	MinSilenceMS   int     `json:"vad_min_silence_ms"`
	SpeechPadMS    int     `json:"vad_speech_pad_ms"`
	Prompt         string  `json:"prompt"`
	GPUPreflight   bool    `json:"gpu_preflight"`
	GPUAutoReset   bool    `json:"gpu_auto_reset"`
	GPUFallbackCPU bool    `json:"gpu_fallback_cpu"`
}

type TranslationConfig struct {
	Mode               string              `json:"mode"`
	BaseURL            string              `json:"base_url"`
	APIKey             string              `json:"api_key"`
	Model              string              `json:"model"`
	BatchSize          int                 `json:"batch_size"`
	TimeoutSec         int                 `json:"timeout_seconds"`
	ContextGapMS       int64               `json:"context_gap_ms"`
	TranslationMemory  bool                `json:"translation_memory"`
	Glossary           string              `json:"glossary"`
	StructuredGlossary *StructuredGlossary `json:"structured_glossary,omitempty"`
}

type StructuredGlossary struct {
	Style []string          `json:"style,omitempty"`
	Terms map[string]string `json:"terms,omitempty"`
}

type PostProcessingConfig struct {
	Mode               string `json:"mode"`
	ShellScript        string `json:"shell_script"`
	WebhookURL         string `json:"webhook_url"`
	WebhookBearerToken string `json:"webhook_bearer_token"`
	TimeoutSec         int    `json:"timeout_seconds"`
}

type OutputConfig struct {
	EnglishSuffix  string `json:"english_suffix"`
	JapaneseSuffix string `json:"japanese_suffix"`
	KeepJapanese   bool   `json:"keep_japanese"`
	Overwrite      bool   `json:"overwrite"`
	MaxLineChars   int    `json:"max_line_chars"`
	MaxLines       int    `json:"max_lines"`
}

func defaults() Config {
	return Config{
		Listen: "127.0.0.1:8097", DatabasePath: "./data/javbeaconsubs.db",
		UploadDir: "./data/uploads", MaxUploadGB: 30,
		Whisper: WhisperConfig{
			Binary: "whisper-cli", Language: "ja", Threads: 8, UseGPU: true,
			BeamSize: 5, VAD: true, VADThreshold: .42, MinSpeechMS: 100,
			MinSilenceMS: 250, SpeechPadMS: 320, GPUPreflight: true,
			GPUFallbackCPU: true,
			Prompt:         "日本語の会話です。固有名詞、呼び名、短い返事、息遣いも正確に文字起こししてください。",
		},
		Translation:    TranslationConfig{Mode: "direct", BatchSize: 24, TimeoutSec: 120, ContextGapMS: 8000},
		PostProcessing: PostProcessingConfig{Mode: "none", TimeoutSec: 60},
		Output:         OutputConfig{EnglishSuffix: ".en.srt", JapaneseSuffix: ".ja.srt", KeepJapanese: true, MaxLineChars: 42, MaxLines: 2},
		Workers:        1,
	}
}

func Load(path string) (Config, error) {
	cfg := defaults()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, fmt.Errorf("%s does not exist; copy config.example.json to config.json", path)
		}
		return cfg, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("decode %s: %w", path, err)
	}
	if value := os.Getenv("JAVBEACONSUBS_API_TOKEN"); value != "" {
		cfg.APIToken = value
	}
	if value := os.Getenv("JAVBEACONSUBS_WEB_USERNAME"); value != "" {
		cfg.WebUsername = value
	}
	if value := os.Getenv("JAVBEACONSUBS_WEB_PASSWORD"); value != "" {
		cfg.WebPassword = value
	}
	if value := os.Getenv("JAVBEACONSUBS_LISTEN"); value != "" {
		cfg.Listen = value
	}
	if value := os.Getenv("JAVBEACONSUBS_DATABASE_PATH"); value != "" {
		cfg.DatabasePath = value
	}
	if value := os.Getenv("JAVBEACONSUBS_UPLOAD_DIR"); value != "" {
		cfg.UploadDir = value
	}
	if value := os.Getenv("JAVBEACONSUBS_WHISPER_BINARY"); value != "" {
		cfg.Whisper.Binary = value
	}
	if value := os.Getenv("JAVBEACONSUBS_WHISPER_MODEL"); value != "" {
		cfg.Whisper.Model = value
	}
	if value := os.Getenv("JAVBEACONSUBS_VAD_MODEL"); value != "" {
		cfg.Whisper.VADModel = value
	}
	if value := os.Getenv("JAVBEACONSUBS_USE_GPU"); value != "" {
		cfg.Whisper.UseGPU = envBool(value)
	}
	if value := os.Getenv("JAVBEACONSUBS_GPU_PREFLIGHT"); value != "" {
		cfg.Whisper.GPUPreflight = envBool(value)
	}
	if value := os.Getenv("JAVBEACONSUBS_GPU_AUTO_RESET"); value != "" {
		cfg.Whisper.GPUAutoReset = envBool(value)
	}
	if value := os.Getenv("JAVBEACONSUBS_GPU_FALLBACK_CPU"); value != "" {
		cfg.Whisper.GPUFallbackCPU = envBool(value)
	}
	if value := os.Getenv("JAVBEACONSUBS_TRANSLATION_API_KEY"); value != "" {
		cfg.Translation.APIKey = value
	}
	if cfg.Workers < 1 {
		cfg.Workers = 1
	}
	if cfg.MaxUploadGB < 1 {
		cfg.MaxUploadGB = 30
	}
	if cfg.Translation.BatchSize < 1 {
		cfg.Translation.BatchSize = 24
	}
	if cfg.Translation.TimeoutSec < 1 {
		cfg.Translation.TimeoutSec = 120
	}
	if cfg.Output.MaxLineChars < 20 {
		cfg.Output.MaxLineChars = 42
	}
	if cfg.Output.MaxLines < 1 {
		cfg.Output.MaxLines = 2
	}
	if cfg.Whisper.Language == "" {
		cfg.Whisper.Language = "ja"
	}
	if err := NormalizeTranslation(&cfg.Translation); err != nil {
		return cfg, err
	}
	if err := NormalizePostProcessing(&cfg.PostProcessing); err != nil {
		return cfg, err
	}
	for field, value := range map[string]*string{"database_path": &cfg.DatabasePath, "upload_dir": &cfg.UploadDir} {
		absolute, err := filepath.Abs(*value)
		if err != nil {
			return cfg, fmt.Errorf("%s %q: %w", field, *value, err)
		}
		*value = filepath.Clean(absolute)
	}
	return cfg, nil
}

func NormalizePostProcessing(value *PostProcessingConfig) error {
	value.Mode = strings.ToLower(strings.TrimSpace(value.Mode))
	value.ShellScript = strings.TrimSpace(value.ShellScript)
	value.WebhookURL = strings.TrimSpace(value.WebhookURL)
	if value.TimeoutSec < 1 {
		value.TimeoutSec = 60
	}
	if value.TimeoutSec > 3600 {
		return errors.New("post-processing timeout cannot exceed 3600 seconds")
	}
	switch value.Mode {
	case "none":
		return nil
	case "shell":
		if value.ShellScript == "" {
			return errors.New("shell post-processing requires a script path")
		}
	case "webhook":
		if value.WebhookURL == "" || (!strings.HasPrefix(value.WebhookURL, "http://") && !strings.HasPrefix(value.WebhookURL, "https://")) {
			return errors.New("webhook post-processing requires an http:// or https:// URL")
		}
	default:
		return errors.New("post-processing mode must be none, shell, or webhook")
	}
	return nil
}

func NormalizeTranslation(value *TranslationConfig) error {
	value.Mode = strings.ToLower(strings.TrimSpace(value.Mode))
	value.BaseURL = strings.TrimSpace(value.BaseURL)
	value.Model = strings.TrimSpace(value.Model)
	if value.BatchSize < 1 {
		value.BatchSize = 24
	}
	if value.TimeoutSec < 1 {
		value.TimeoutSec = 120
	}
	if value.ContextGapMS < 1 {
		value.ContextGapMS = 8000
	}
	if value.ContextGapMS > 3_600_000 {
		return errors.New("translation.context_gap_ms cannot exceed 3600000")
	}
	if value.StructuredGlossary != nil {
		styles := make([]string, 0, len(value.StructuredGlossary.Style))
		seenStyles := make(map[string]bool, len(value.StructuredGlossary.Style))
		for _, style := range value.StructuredGlossary.Style {
			style = strings.TrimSpace(style)
			if style != "" && !seenStyles[style] {
				styles = append(styles, style)
				seenStyles[style] = true
			}
		}
		value.StructuredGlossary.Style = styles
		cleanTerms := make(map[string]string, len(value.StructuredGlossary.Terms))
		for source, target := range value.StructuredGlossary.Terms {
			source, target = strings.TrimSpace(source), strings.TrimSpace(target)
			if source != "" && target != "" {
				if existing, exists := cleanTerms[source]; exists && existing != target {
					return fmt.Errorf("structured glossary has conflicting translations for %q", source)
				}
				cleanTerms[source] = target
			}
		}
		value.StructuredGlossary.Terms = cleanTerms
	}
	if value.Mode != "direct" && value.Mode != "contextual" && value.Mode != "none" {
		return fmt.Errorf("translation.mode must be direct, contextual, or none")
	}
	if value.Mode == "contextual" && (value.BaseURL == "" || value.Model == "") {
		return errors.New("contextual translation requires an API URL and model")
	}
	return nil
}

func envBool(value string) bool {
	return strings.EqualFold(value, "true") || value == "1" || strings.EqualFold(value, "yes") || strings.EqualFold(value, "on")
}
