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
	Backend           string  `json:"backend"`
	Mode              string  `json:"mode"`
	Profile           string  `json:"profile"`
	QwenPython        string  `json:"qwen_python"`
	QwenScript        string  `json:"qwen_script"`
	QwenModel         string  `json:"qwen_model"`
	QwenRevision      string  `json:"qwen_revision"`
	AlignerModel      string  `json:"aligner_model"`
	AlignerRevision   string  `json:"aligner_revision"`
	ASRBatchSize      int     `json:"asr_batch_size"`
	ReazonEnabled     bool    `json:"reazon_enabled"`
	WhisperEnabled    bool    `json:"whisper_enabled"`
	DebugMode         bool    `json:"debug_mode"`
	Binary            string  `json:"binary"`
	Model             string  `json:"model"`
	ReazonPython      string  `json:"reazon_python"`
	ReazonScript      string  `json:"reazon_script"`
	ReazonBatchScript string  `json:"reazon_batch_script"`
	ReazonModel       string  `json:"reazon_model"`
	ChunkSeconds      int     `json:"chunk_seconds"`
	OverlapSeconds    int     `json:"chunk_overlap_seconds"`
	MaxSegmentSec     int     `json:"max_segment_seconds"`
	FallbackWhisper   bool    `json:"fallback_whisper"`
	VADModel          string  `json:"vad_model"`
	Language          string  `json:"language"`
	Threads           int     `json:"threads"`
	UseGPU            bool    `json:"use_gpu"`
	BeamSize          int     `json:"beam_size"`
	VAD               bool    `json:"vad"`
	VADThreshold      float64 `json:"vad_threshold"`
	MinSpeechMS       int     `json:"vad_min_speech_ms"`
	MinSilenceMS      int     `json:"vad_min_silence_ms"`
	SpeechPadMS       int     `json:"vad_speech_pad_ms"`
	VADPreRollMS      int     `json:"vad_pre_roll_ms"`
	VADPostRollMS     int     `json:"vad_post_roll_ms"`
	VADEnergyFactor   float64 `json:"vad_energy_factor"`
	Prompt            string  `json:"prompt"`
	GPUPreflight      bool    `json:"gpu_preflight"`
	GPUAutoReset      bool    `json:"gpu_auto_reset"`
	GPUFallbackCPU    bool    `json:"gpu_fallback_cpu"`
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
	EnglishASS     string `json:"english_ass_suffix"`
	JapaneseASS    string `json:"japanese_ass_suffix"`
	ProjectJSON    string `json:"project_json_suffix"`
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
			Backend: "qwen", Mode: "balanced", Profile: "jav", Binary: "whisper-cli", Language: "ja", Threads: 8, UseGPU: true,
			QwenPython: "python3", QwenScript: "./asr/qwen_pipeline.py", QwenModel: "Qwen/Qwen3-ASR-1.7B", AlignerModel: "Qwen/Qwen3-ForcedAligner-0.6B",
			QwenRevision: "7278e1e70fe206f11671096ffdd38061171dd6e5", AlignerRevision: "c7cbfc2048c462b0d63a45797104fc9db3ad62b7",
			ASRBatchSize: 4, ReazonEnabled: true, WhisperEnabled: true,
			ReazonPython: "python3", ReazonScript: "./asr/reazon_worker.py", ReazonBatchScript: "./asr/reazon_batch_worker.py", ReazonModel: "reazon-research/reazonspeech-nemo-v2",
			ChunkSeconds: 45, OverlapSeconds: 2, MaxSegmentSec: 30, FallbackWhisper: true,
			BeamSize: 5, VAD: true, VADThreshold: .42, MinSpeechMS: 100,
			MinSilenceMS: 500, SpeechPadMS: 320, VADPreRollMS: 350, VADPostRollMS: 600, VADEnergyFactor: 1.45, GPUPreflight: true,
			GPUFallbackCPU: true,
			Prompt:         "",
		},
		Translation:    TranslationConfig{Mode: "direct", BatchSize: 24, TimeoutSec: 120, ContextGapMS: 8000},
		PostProcessing: PostProcessingConfig{Mode: "none", TimeoutSec: 60},
		Output:         OutputConfig{EnglishSuffix: ".en.srt", JapaneseSuffix: ".ja.srt", EnglishASS: ".en.ass", JapaneseASS: ".ja.ass", ProjectJSON: ".subtitles.json", KeepJapanese: false, MaxLineChars: 42, MaxLines: 2},
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
	if value := os.Getenv("JAVBEACONSUBS_ASR_BACKEND"); value != "" {
		cfg.Whisper.Backend = value
	}
	if value := os.Getenv("JAVBEACONSUBS_ASR_MODE"); value != "" {
		cfg.Whisper.Mode = value
	}
	if value := os.Getenv("JAVBEACONSUBS_ASR_PROFILE"); value != "" {
		cfg.Whisper.Profile = value
	}
	if value := os.Getenv("JAVBEACONSUBS_QWEN_PYTHON"); value != "" {
		cfg.Whisper.QwenPython = value
	}
	if value := os.Getenv("JAVBEACONSUBS_QWEN_SCRIPT"); value != "" {
		cfg.Whisper.QwenScript = value
	}
	if value := os.Getenv("JAVBEACONSUBS_QWEN_MODEL"); value != "" {
		cfg.Whisper.QwenModel = value
	}
	if value := os.Getenv("JAVBEACONSUBS_QWEN_REVISION"); value != "" {
		cfg.Whisper.QwenRevision = value
	}
	if value := os.Getenv("JAVBEACONSUBS_ALIGNER_MODEL"); value != "" {
		cfg.Whisper.AlignerModel = value
	}
	if value := os.Getenv("JAVBEACONSUBS_ALIGNER_REVISION"); value != "" {
		cfg.Whisper.AlignerRevision = value
	}
	if value := os.Getenv("JAVBEACONSUBS_REAZON_ENABLED"); value != "" {
		cfg.Whisper.ReazonEnabled = envBool(value)
	}
	if value := os.Getenv("JAVBEACONSUBS_WHISPER_ENABLED"); value != "" {
		cfg.Whisper.WhisperEnabled = envBool(value)
	}
	if value := os.Getenv("JAVBEACONSUBS_ASR_DEBUG"); value != "" {
		cfg.Whisper.DebugMode = envBool(value)
	}
	if value := os.Getenv("JAVBEACONSUBS_REAZON_SCRIPT"); value != "" {
		cfg.Whisper.ReazonScript = value
	}
	if value := os.Getenv("JAVBEACONSUBS_REAZON_PYTHON"); value != "" {
		cfg.Whisper.ReazonPython = value
	}
	if value := os.Getenv("JAVBEACONSUBS_REAZON_BATCH_SCRIPT"); value != "" {
		cfg.Whisper.ReazonBatchScript = value
	}
	if value := os.Getenv("JAVBEACONSUBS_REAZON_MODEL"); value != "" {
		cfg.Whisper.ReazonModel = value
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
	if cfg.Output.EnglishASS == "" {
		cfg.Output.EnglishASS = ".en.ass"
	}
	if cfg.Output.JapaneseASS == "" {
		cfg.Output.JapaneseASS = ".ja.ass"
	}
	if cfg.Output.ProjectJSON == "" {
		cfg.Output.ProjectJSON = ".subtitles.json"
	}
	if cfg.Whisper.Language == "" {
		cfg.Whisper.Language = "ja"
	}
	cfg.Whisper.Backend = strings.ToLower(strings.TrimSpace(cfg.Whisper.Backend))
	if cfg.Whisper.Backend == "" {
		cfg.Whisper.Backend = "qwen"
	}
	if cfg.Whisper.Backend != "qwen" && cfg.Whisper.Backend != "reazon" && cfg.Whisper.Backend != "whisper" {
		return cfg, errors.New("whisper.backend must be qwen, reazon, or whisper")
	}
	cfg.Whisper.Mode = strings.ToLower(strings.TrimSpace(cfg.Whisper.Mode))
	if cfg.Whisper.Mode == "" {
		cfg.Whisper.Mode = "balanced"
	}
	if cfg.Whisper.Mode != "fast" && cfg.Whisper.Mode != "balanced" && cfg.Whisper.Mode != "high_accuracy" {
		return cfg, errors.New("whisper.mode must be fast, balanced, or high_accuracy")
	}
	cfg.Whisper.Profile = strings.ToLower(strings.TrimSpace(cfg.Whisper.Profile))
	if cfg.Whisper.Profile == "" {
		cfg.Whisper.Profile = "jav"
	}
	if cfg.Whisper.Profile != "standard" && cfg.Whisper.Profile != "jav" && cfg.Whisper.Profile != "tokusatsu" && cfg.Whisper.Profile != "akiba" {
		return cfg, errors.New("whisper.profile must be standard, jav, tokusatsu, or akiba")
	}
	if cfg.Whisper.ASRBatchSize < 1 {
		cfg.Whisper.ASRBatchSize = 4
	}
	if cfg.Whisper.VADPreRollMS < 0 || cfg.Whisper.VADPostRollMS < 0 {
		return cfg, errors.New("VAD pre/post roll must be non-negative")
	}
	if cfg.Whisper.VADEnergyFactor < 1 {
		cfg.Whisper.VADEnergyFactor = 1.45
	}
	if cfg.Whisper.ChunkSeconds < 10 {
		cfg.Whisper.ChunkSeconds = 45
	}
	if cfg.Whisper.OverlapSeconds < 0 || cfg.Whisper.OverlapSeconds*2 >= cfg.Whisper.ChunkSeconds {
		return cfg, errors.New("whisper.chunk_overlap_seconds must be non-negative and less than half the chunk duration")
	}
	if cfg.Whisper.MaxSegmentSec < 1 {
		cfg.Whisper.MaxSegmentSec = 60
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
