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
	Listen       string            `json:"listen"`
	APIToken     string            `json:"api_token"`
	DatabasePath string            `json:"database_path"`
	UploadDir    string            `json:"upload_dir"`
	MaxUploadGB  int64             `json:"max_upload_gb"`
	AllowedRoots []string          `json:"allowed_roots"`
	Whisper      WhisperConfig     `json:"whisper"`
	Translation  TranslationConfig `json:"translation"`
	Output       OutputConfig      `json:"output"`
	Workers      int               `json:"workers"`
}

type WhisperConfig struct {
	Binary       string  `json:"binary"`
	Model        string  `json:"model"`
	VADModel     string  `json:"vad_model"`
	Language     string  `json:"language"`
	Threads      int     `json:"threads"`
	UseGPU       bool    `json:"use_gpu"`
	BeamSize     int     `json:"beam_size"`
	VAD          bool    `json:"vad"`
	VADThreshold float64 `json:"vad_threshold"`
	MinSpeechMS  int     `json:"vad_min_speech_ms"`
	MinSilenceMS int     `json:"vad_min_silence_ms"`
	SpeechPadMS  int     `json:"vad_speech_pad_ms"`
	Prompt       string  `json:"prompt"`
	GPUPreflight bool    `json:"gpu_preflight"`
	GPUAutoReset bool    `json:"gpu_auto_reset"`
}

type TranslationConfig struct {
	Mode       string `json:"mode"`
	BaseURL    string `json:"base_url"`
	APIKey     string `json:"api_key"`
	Model      string `json:"model"`
	BatchSize  int    `json:"batch_size"`
	TimeoutSec int    `json:"timeout_seconds"`
	Glossary   string `json:"glossary"`
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
			Prompt: "日本語の会話です。固有名詞、呼び名、短い返事、息遣いも正確に文字起こししてください。",
		},
		Translation: TranslationConfig{Mode: "direct", BatchSize: 24, TimeoutSec: 120},
		Output:      OutputConfig{EnglishSuffix: ".en.srt", JapaneseSuffix: ".ja.srt", KeepJapanese: true, MaxLineChars: 42, MaxLines: 2},
		Workers:     1,
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
	cfg.Translation.Mode = strings.ToLower(strings.TrimSpace(cfg.Translation.Mode))
	if cfg.Translation.Mode == "contextual" && (cfg.Translation.BaseURL == "" || cfg.Translation.Model == "") {
		return cfg, errors.New("translation.mode=contextual requires translation.base_url and translation.model")
	}
	for i, root := range cfg.AllowedRoots {
		absolute, err := filepath.Abs(root)
		if err != nil {
			return cfg, fmt.Errorf("allowed root %q: %w", root, err)
		}
		cfg.AllowedRoots[i] = filepath.Clean(absolute)
	}
	for field, value := range map[string]*string{"database_path": &cfg.DatabasePath, "upload_dir": &cfg.UploadDir} {
		absolute, err := filepath.Abs(*value)
		if err != nil {
			return cfg, fmt.Errorf("%s %q: %w", field, *value, err)
		}
		*value = filepath.Clean(absolute)
	}
	if cfg.Translation.Mode != "direct" && cfg.Translation.Mode != "contextual" && cfg.Translation.Mode != "none" {
		return cfg, fmt.Errorf("translation.mode must be direct, contextual, or none")
	}
	return cfg, nil
}

func envBool(value string) bool {
	return strings.EqualFold(value, "true") || value == "1" || strings.EqualFold(value, "yes") || strings.EqualFold(value, "on")
}
