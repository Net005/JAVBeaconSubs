package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	Listen                    string               `json:"listen"`
	APIToken                  string               `json:"api_token"`
	WebUsername               string               `json:"web_username"`
	WebPassword               string               `json:"web_password"`
	DatabasePath              string               `json:"database_path"`
	UploadDir                 string               `json:"upload_dir"`
	MaxUploadGB               int64                `json:"max_upload_gb"`
	Whisper                   WhisperConfig        `json:"whisper"`
	Translation               TranslationConfig    `json:"translation"`
	PostProcessing            PostProcessingConfig `json:"post_processing"`
	Output                    OutputConfig         `json:"output"`
	RecognitionVocabularyPath string               `json:"recognition_vocabulary_path"`
	TranslationGlossaryPath   string               `json:"translation_glossary_path"`
	Profiles                  ProfilesConfig       `json:"profiles"`
	Workers                   int                  `json:"workers"`
}

type ProfilesConfig struct {
	DefaultProfile string        `json:"default_profile"`
	DefaultASRMode string        `json:"default_asr_mode"`
	PathMappings   []PathMapping `json:"path_mappings"`
}

type PathMapping struct {
	ID       string `json:"id,omitempty"`
	Path     string `json:"path"`
	Profile  string `json:"profile,omitempty"`
	ASRMode  string `json:"asr_mode,omitempty"`
	Enabled  bool   `json:"enabled"`
	Priority int    `json:"priority,omitempty"`
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
	WhisperDevice     string  `json:"whisper_device"`
	WhisperCPUTimeout int     `json:"whisper_cpu_timeout_seconds"`
	WhisperStatusPath string  `json:"whisper_runtime_status_path"`
	WhisperCUDAMinMB  int     `json:"whisper_cuda_safe_minimum_mb"`
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
	BestOf            int     `json:"best_of"`
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
	Mode                 string              `json:"mode"`
	BaseURL              string              `json:"base_url"`
	APIKey               string              `json:"api_key"`
	Model                string              `json:"model"`
	BatchSize            int                 `json:"batch_size"`
	TimeoutSec           int                 `json:"timeout_seconds"`
	ContextGapMS         int64               `json:"context_gap_ms"`
	TranslationMemory    bool                `json:"translation_memory"`
	InputCostPerMillion  float64             `json:"input_cost_per_million,omitempty"`
	OutputCostPerMillion float64             `json:"output_cost_per_million,omitempty"`
	Glossary             string              `json:"glossary"`
	StructuredGlossary   *StructuredGlossary `json:"structured_glossary,omitempty"`
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
	EnglishSuffix      string  `json:"english_suffix"`
	JapaneseSuffix     string  `json:"japanese_suffix"`
	EnglishASS         string  `json:"english_ass_suffix"`
	JapaneseASS        string  `json:"japanese_ass_suffix"`
	ProjectJSON        string  `json:"project_json_suffix"`
	KeepJapanese       bool    `json:"keep_japanese"`
	Overwrite          bool    `json:"overwrite"`
	NormalizeSubtitles bool    `json:"subtitle_normalization_enabled"`
	TargetLineChars    int     `json:"subtitle_target_chars_per_line"`
	MaxLineChars       int     `json:"max_line_chars"`
	MaxLines           int     `json:"max_lines"`
	TargetCueChars     int     `json:"subtitle_target_chars_per_cue"`
	MaxCueDurationMS   int64   `json:"subtitle_max_duration_ms"`
	MinCueDurationMS   int64   `json:"subtitle_min_duration_ms"`
	TargetCPS          float64 `json:"subtitle_target_cps"`
	WriteProvenance    bool    `json:"subtitle_write_provenance_sidecar"`
}

func defaults() Config {
	return Config{
		Listen: "127.0.0.1:8097", DatabasePath: "./data/javbeaconsubs.db",
		UploadDir: "./data/uploads", MaxUploadGB: 30,
		RecognitionVocabularyPath: "./vocabulary/javbeaconsubs_japanese_recognition_vocabulary_v1.json",
		TranslationGlossaryPath:   "./javbeaconsubs_translation_glossary_v2.json",
		Profiles:                  ProfilesConfig{DefaultProfile: "jav", DefaultASRMode: "balanced"},
		Whisper: WhisperConfig{
			Backend: "qwen", Mode: "balanced", Profile: "jav", Binary: "whisper-cli", Language: "ja", Threads: 12, UseGPU: true,
			QwenPython: "python3", QwenScript: "./asr/qwen_pipeline.py", QwenModel: "Qwen/Qwen3-ASR-1.7B", AlignerModel: "Qwen/Qwen3-ForcedAligner-0.6B",
			QwenRevision: "7278e1e70fe206f11671096ffdd38061171dd6e5", AlignerRevision: "c7cbfc2048c462b0d63a45797104fc9db3ad62b7",
			ASRBatchSize: 4, ReazonEnabled: false, WhisperEnabled: true,
			ReazonPython: "python3", ReazonScript: "./asr/reazon_worker.py", ReazonBatchScript: "./asr/reazon_batch_worker.py", ReazonModel: "reazon-research/reazonspeech-nemo-v2",
			ChunkSeconds: 45, OverlapSeconds: 2, MaxSegmentSec: 30, FallbackWhisper: true,
			BeamSize: 5, BestOf: 5, VAD: true, VADThreshold: .42, MinSpeechMS: 100,
			MinSilenceMS: 500, SpeechPadMS: 320, VADPreRollMS: 350, VADPostRollMS: 600, VADEnergyFactor: 1.45, GPUPreflight: true,
			GPUFallbackCPU: true, WhisperDevice: "auto", WhisperCPUTimeout: 7200,
			WhisperCUDAMinMB:  4096,
			WhisperStatusPath: "./data/whisper-runtime.json",
			Prompt:            "",
		},
		Translation:    TranslationConfig{Mode: "direct", BatchSize: 24, TimeoutSec: 120, ContextGapMS: 8000},
		PostProcessing: PostProcessingConfig{Mode: "none", TimeoutSec: 60},
		Output: OutputConfig{
			EnglishSuffix: ".en.srt", JapaneseSuffix: ".ja.srt", EnglishASS: ".en.ass", JapaneseASS: ".ja.ass", ProjectJSON: ".subtitles.json",
			KeepJapanese: false, NormalizeSubtitles: true, TargetLineChars: 40, MaxLineChars: 46, MaxLines: 2,
			TargetCueChars: 80, MaxCueDurationMS: 6000, MinCueDurationMS: 1000, TargetCPS: 17, WriteProvenance: true,
		},
		Workers: 1,
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
	if value := os.Getenv("JAVBEACONSUBS_RECOGNITION_VOCABULARY"); value != "" {
		cfg.RecognitionVocabularyPath = value
	}
	if value := os.Getenv("JAVBEACONSUBS_TRANSLATION_GLOSSARY"); value != "" {
		cfg.TranslationGlossaryPath = value
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
	if value := os.Getenv("JAVBEACONSUBS_WHISPER_DEVICE"); value != "" {
		cfg.Whisper.WhisperDevice = value
	}
	if value := os.Getenv("JAVBEACONSUBS_WHISPER_CPU_TIMEOUT_SECONDS"); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil {
			cfg.Whisper.WhisperCPUTimeout = parsed
		}
	}
	if value := os.Getenv("JAVBEACONSUBS_WHISPER_RUNTIME_STATUS_PATH"); value != "" {
		cfg.Whisper.WhisperStatusPath = value
	}
	if value := os.Getenv("JAVBEACONSUBS_WHISPER_THREADS"); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil {
			cfg.Whisper.Threads = parsed
		}
	}
	if value := os.Getenv("JAVBEACONSUBS_WHISPER_BEAM_SIZE"); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil {
			cfg.Whisper.BeamSize = parsed
		}
	}
	if value := os.Getenv("JAVBEACONSUBS_WHISPER_BEST_OF"); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil {
			cfg.Whisper.BestOf = parsed
		}
	}
	if value := os.Getenv("JAVBEACONSUBS_WHISPER_CUDA_SAFE_MINIMUM_MB"); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil {
			cfg.Whisper.WhisperCUDAMinMB = parsed
		}
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
	cfg.Whisper.Profile = NormalizeProfile(cfg.Whisper.Profile)
	if cfg.Whisper.Profile == "" {
		cfg.Whisper.Profile = "jav"
	}
	if cfg.Whisper.Profile != "standard" && cfg.Whisper.Profile != "jav" && cfg.Whisper.Profile != "giga" {
		return cfg, errors.New("whisper.profile must be standard, jav, or giga")
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
	cfg.Whisper.WhisperDevice = strings.ToLower(strings.TrimSpace(cfg.Whisper.WhisperDevice))
	if cfg.Whisper.WhisperDevice == "" {
		cfg.Whisper.WhisperDevice = "auto"
	}
	if cfg.Whisper.WhisperDevice != "auto" && cfg.Whisper.WhisperDevice != "cuda" && cfg.Whisper.WhisperDevice != "cpu" {
		return cfg, errors.New("whisper.whisper_device must be auto, cuda, or cpu")
	}
	if cfg.Whisper.WhisperCPUTimeout < 300 {
		cfg.Whisper.WhisperCPUTimeout = 7200
	}
	if cfg.Whisper.Threads < 1 {
		cfg.Whisper.Threads = 12
	}
	if cfg.Whisper.BeamSize < 1 {
		cfg.Whisper.BeamSize = 5
	}
	if cfg.Whisper.BestOf < 1 {
		cfg.Whisper.BestOf = 5
	}
	if cfg.Whisper.WhisperCUDAMinMB < 0 {
		cfg.Whisper.WhisperCUDAMinMB = 0
	}
	if cfg.Output.TargetLineChars < 1 {
		cfg.Output.TargetLineChars = 40
	}
	if cfg.Output.MaxLineChars < cfg.Output.TargetLineChars {
		cfg.Output.MaxLineChars = 46
	}
	if cfg.Output.MaxLines < 1 || cfg.Output.MaxLines > 2 {
		cfg.Output.MaxLines = 2
	}
	if cfg.Output.TargetCueChars < 1 {
		cfg.Output.TargetCueChars = 80
	}
	if cfg.Output.MaxCueDurationMS < 1 {
		cfg.Output.MaxCueDurationMS = 6000
	}
	if cfg.Output.MinCueDurationMS < 1 {
		cfg.Output.MinCueDurationMS = 1000
	}
	if cfg.Output.TargetCPS <= 0 {
		cfg.Output.TargetCPS = 17
	}
	if cfg.Profiles.DefaultProfile == "" {
		cfg.Profiles.DefaultProfile = cfg.Whisper.Profile
	}
	if cfg.Profiles.DefaultASRMode == "" {
		cfg.Profiles.DefaultASRMode = cfg.Whisper.Mode
	}
	if err := NormalizeProfiles(&cfg.Profiles); err != nil {
		return cfg, err
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

func NormalizeASRMode(value string) string { return strings.ToLower(strings.TrimSpace(value)) }

func NormalizeProfiles(value *ProfilesConfig) error {
	value.DefaultProfile = NormalizeProfile(value.DefaultProfile)
	if value.DefaultProfile == "" {
		value.DefaultProfile = "jav"
	}
	if value.DefaultProfile != "standard" && value.DefaultProfile != "jav" && value.DefaultProfile != "giga" {
		return errors.New("default profile must be standard, jav, or giga")
	}
	value.DefaultASRMode = NormalizeASRMode(value.DefaultASRMode)
	if value.DefaultASRMode == "" {
		value.DefaultASRMode = "balanced"
	}
	if value.DefaultASRMode != "fast" && value.DefaultASRMode != "balanced" && value.DefaultASRMode != "high_accuracy" {
		return errors.New("default recognition accuracy must be fast, balanced, or high_accuracy")
	}
	for index := range value.PathMappings {
		mapping := &value.PathMappings[index]
		mapping.Path = strings.TrimSpace(mapping.Path)
		mapping.Profile = NormalizeProfile(mapping.Profile)
		mapping.ASRMode = NormalizeASRMode(mapping.ASRMode)
		if mapping.Path == "" {
			return fmt.Errorf("path mapping %d requires a path", index+1)
		}
		if mapping.Profile != "" && mapping.Profile != "standard" && mapping.Profile != "jav" && mapping.Profile != "giga" {
			return fmt.Errorf("path mapping %d has invalid profile", index+1)
		}
		if mapping.ASRMode != "" && mapping.ASRMode != "fast" && mapping.ASRMode != "balanced" && mapping.ASRMode != "high_accuracy" {
			return fmt.Errorf("path mapping %d has invalid asr_mode", index+1)
		}
		if mapping.Profile == "" && mapping.ASRMode == "" {
			return fmt.Errorf("path mapping %d must set profile or asr_mode", index+1)
		}
	}
	return nil
}

// NormalizeProfile accepts historical API/config aliases but always returns a
// canonical value suitable for persistence and responses.
func NormalizeProfile(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "tokusatsu" || value == "akiba" {
		return "giga"
	}
	return value
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
	if value.InputCostPerMillion < 0 || value.OutputCostPerMillion < 0 {
		return errors.New("translation token costs cannot be negative")
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
