package engine

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"javbeaconsubs/internal/config"
	"javbeaconsubs/internal/subtitle"
)

const marker = "; generated-by=javbeaconsubs-v1"

type ProgressFunc func(phase string, percent int, message string)

type ProcessOptions struct {
	ASRMode    string
	ASRProfile string
	DebugMode  bool
}

type Result struct {
	Input                   string `json:"input"`
	EnglishSRT              string `json:"english_srt,omitempty"`
	JapaneseSRT             string `json:"japanese_srt,omitempty"`
	EnglishASS              string `json:"english_ass,omitempty"`
	JapaneseASS             string `json:"japanese_ass,omitempty"`
	ProjectJSON             string `json:"project_json,omitempty"`
	Segments                int    `json:"segments"`
	Skipped                 bool   `json:"skipped,omitempty"`
	TranslationInputTokens  int    `json:"translation_input_tokens,omitempty"`
	TranslationOutputTokens int    `json:"translation_output_tokens,omitempty"`
	TranslationTotalTokens  int    `json:"translation_total_tokens,omitempty"`
}

type Runner struct {
	mu     sync.RWMutex
	cfg    config.Config
	log    *slog.Logger
	client *http.Client
}

func New(cfg config.Config, log *slog.Logger) *Runner {
	return &Runner{cfg: cfg, log: log, client: &http.Client{Timeout: time.Duration(cfg.Translation.TimeoutSec) * time.Second}}
}

func (r *Runner) Translation() config.TranslationConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.cfg.Translation
}

func (r *Runner) UpdateTranslation(value config.TranslationConfig) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cfg.Translation = value
	r.client = &http.Client{Timeout: time.Duration(value.TimeoutSec) * time.Second}
}

func (r *Runner) Check() map[string]any {
	r.mu.RLock()
	defer r.mu.RUnlock()
	whisperPath, whisperErr := exec.LookPath(r.cfg.Whisper.Binary)
	ffmpegPath, ffmpegErr := exec.LookPath("ffmpeg")
	_, modelErr := os.Stat(r.cfg.Whisper.Model)
	result := map[string]any{"ready": whisperErr == nil && ffmpegErr == nil && modelErr == nil, "translation_mode": r.cfg.Translation.Mode}
	result["asr_backend"] = r.cfg.Whisper.Backend
	if r.cfg.Whisper.Backend == "qwen" {
		pythonPath, pythonErr := exec.LookPath(r.cfg.Whisper.QwenPython)
		_, scriptErr := os.Stat(r.cfg.Whisper.QwenScript)
		if pythonErr != nil || scriptErr != nil {
			result["ready"] = false
		}
		result["asr_mode"] = r.cfg.Whisper.Mode
		result["qwen_model"] = r.cfg.Whisper.QwenModel
		result["aligner_model"] = r.cfg.Whisper.AlignerModel
		if pythonErr == nil {
			result["qwen_python"] = pythonPath
		} else {
			result["qwen_python_error"] = pythonErr.Error()
		}
		if scriptErr != nil {
			result["qwen_script_error"] = scriptErr.Error()
		}
	}
	if r.cfg.Whisper.Backend == "reazon" {
		pythonPath, pythonErr := exec.LookPath(r.cfg.Whisper.ReazonPython)
		_, scriptErr := os.Stat(r.cfg.Whisper.ReazonScript)
		if pythonErr != nil || scriptErr != nil {
			result["ready"] = false
		}
		if pythonErr == nil {
			result["reazon_python"] = pythonPath
		} else {
			result["reazon_python_error"] = pythonErr.Error()
		}
		if scriptErr == nil {
			result["reazon_model"] = r.cfg.Whisper.ReazonModel
		} else {
			result["reazon_script_error"] = scriptErr.Error()
		}
	}
	if whisperErr == nil {
		result["whisper"] = whisperPath
	} else {
		result["whisper_error"] = whisperErr.Error()
	}
	if ffmpegErr == nil {
		result["ffmpeg"] = ffmpegPath
	} else {
		result["ffmpeg_error"] = ffmpegErr.Error()
	}
	if modelErr != nil {
		result["model_error"] = modelErr.Error()
	} else {
		result["model"] = r.cfg.Whisper.Model
	}
	if r.cfg.Whisper.VAD {
		if _, err := os.Stat(r.cfg.Whisper.VADModel); err != nil {
			result["vad_model_error"] = err.Error()
			result["ready"] = false
		}
	}
	return result
}

func (r *Runner) Process(ctx context.Context, input string, overwrite bool, progress ProgressFunc) (Result, error) {
	r.mu.RLock()
	keepJapanese := r.cfg.Output.KeepJapanese
	r.mu.RUnlock()
	return r.ProcessWithMemory(ctx, input, overwrite, keepJapanese, progress, NewTranslationMemory())
}

func (r *Runner) ProcessWithMemory(ctx context.Context, input string, overwrite, keepJapanese bool, progress ProgressFunc, memory *TranslationMemory) (Result, error) {
	return r.ProcessWithOptions(ctx, input, overwrite, keepJapanese, ProcessOptions{}, progress, memory)
}

func (r *Runner) ProcessWithOptions(ctx context.Context, input string, overwrite, keepJapanese bool, options ProcessOptions, progress ProgressFunc, memory *TranslationMemory) (Result, error) {
	r.mu.RLock()
	worker := &Runner{cfg: r.cfg, log: r.log, client: r.client}
	r.mu.RUnlock()
	if options.ASRMode != "" {
		worker.cfg.Whisper.Mode = options.ASRMode
	}
	if options.ASRProfile != "" {
		worker.cfg.Whisper.Profile = options.ASRProfile
	}
	if options.DebugMode {
		worker.cfg.Whisper.DebugMode = true
	}
	return worker.process(ctx, input, overwrite, keepJapanese, progress, memory)
}

func (r *Runner) process(ctx context.Context, input string, overwrite, keepJapanese bool, progress ProgressFunc, memory *TranslationMemory) (Result, error) {
	result := Result{Input: input}
	base := strings.TrimSuffix(input, filepath.Ext(input))
	englishPath := base + r.cfg.Output.EnglishSuffix
	japanesePath := base + r.cfg.Output.JapaneseSuffix
	englishASSPath := base + r.cfg.Output.EnglishASS
	japaneseASSPath := base + r.cfg.Output.JapaneseASS
	projectPath := base + r.cfg.Output.ProjectJSON
	if !overwrite && !r.cfg.Output.Overwrite {
		if _, err := os.Stat(englishPath); err == nil {
			result.EnglishSRT = englishPath
			result.Skipped = true
			return result, nil
		}
	}

	tmpDir, err := os.MkdirTemp("", "javbeaconsubs-*")
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(tmpDir)
	wav := filepath.Join(tmpDir, "audio.wav")
	progress("audio", 5, "Extracting and normalizing speech audio")
	if err := run(ctx, "ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-i", input, "-vn", "-ac", "1", "-ar", "16000", "-af", "highpass=f=70,lowpass=f=7600,loudnorm=I=-19:TP=-2:LRA=11", "-c:a", "pcm_s16le", wav); err != nil {
		return result, fmt.Errorf("extract audio: %w", err)
	}

	progress("transcription", 15, "Transcribing Japanese speech")
	useGPU, err := r.prepareGPU(ctx)
	if err != nil {
		return result, err
	}
	transcriptPrefix := filepath.Join(tmpDir, "transcript")
	segments, err := r.transcribe(ctx, wav, transcriptPrefix, false, useGPU, func(pct int) {
		progress("transcription", 15+pct*55/100, fmt.Sprintf("Transcribing Japanese speech — %d%%", pct))
	})
	if err != nil {
		return result, err
	}
	segments = subtitle.Clean(segments)
	if len(segments) == 0 {
		return result, errors.New("no speech segments were recognized")
	}

	if keepJapanese {
		progress("subtitle", 72, "Writing Japanese transcript")
		if err := atomicWrite(japanesePath, subtitle.RenderSRT(segments, 30, r.cfg.Output.MaxLines, marker)); err != nil {
			return result, err
		}
		if err := atomicWrite(japaneseASSPath, subtitle.RenderASS(segments, 30, r.cfg.Output.MaxLines, filepath.Base(input)+" Japanese")); err != nil {
			return result, err
		}
		result.JapaneseSRT = japanesePath
		result.JapaneseASS = japaneseASSPath
	}

	english := segments
	switch r.cfg.Translation.Mode {
	case "direct":
		progress("translation", 74, "Running legacy local speech translation")
		english, err = r.transcribeWhisper(ctx, wav, filepath.Join(tmpDir, "translation-direct"), true, useGPU, func(pct int) {
			progress("translation", 74+pct*18/100, fmt.Sprintf("Local translation — %d%%", pct))
		})
		if err != nil {
			return result, err
		}
	case "contextual":
		progress("translation", 74, "Translating with surrounding dialogue context")
		var usage tokenUsage
		english, usage, err = r.translate(ctx, segments, progress, memory)
		if err != nil {
			return result, err
		}
		result.TranslationInputTokens = usage.PromptTokens
		result.TranslationOutputTokens = usage.CompletionTokens
		result.TranslationTotalTokens = usage.TotalTokens
	case "none":
		result.JapaneseSRT = japanesePath
		if !keepJapanese {
			if err := atomicWrite(japanesePath, subtitle.RenderSRT(segments, 30, r.cfg.Output.MaxLines, marker)); err != nil {
				return result, err
			}
			if err := atomicWrite(japaneseASSPath, subtitle.RenderASS(segments, 30, r.cfg.Output.MaxLines, filepath.Base(input)+" Japanese")); err != nil {
				return result, err
			}
		}
		result.JapaneseASS = japaneseASSPath
		project := map[string]any{
			"pipeline_version": "qwen-first-v1", "input": input, "language": "ja", "japanese": segments,
			"models": map[string]string{"asr_primary": modelIdentity(r.cfg.Whisper.QwenModel, r.cfg.Whisper.QwenRevision), "asr_secondary": r.cfg.Whisper.ReazonModel, "asr_tertiary": r.cfg.Whisper.Model, "aligner": modelIdentity(r.cfg.Whisper.AlignerModel, r.cfg.Whisper.AlignerRevision)},
		}
		if raw, readErr := os.ReadFile(transcriptPrefix + ".qwen.json"); readErr == nil {
			var diagnostics any
			if json.Unmarshal(raw, &diagnostics) == nil {
				project["diagnostics"] = diagnostics
			}
		}
		projectJSON, marshalErr := json.MarshalIndent(project, "", "  ")
		if marshalErr != nil {
			return result, marshalErr
		}
		if err := atomicWrite(projectPath, string(projectJSON)+"\n"); err != nil {
			return result, err
		}
		result.ProjectJSON = projectPath
		result.Segments = len(segments)
		return result, nil
	}

	progress("subtitle", 95, "Writing English subtitles")
	if err := atomicWrite(englishPath, subtitle.RenderSRT(subtitle.Clean(english), r.cfg.Output.MaxLineChars, r.cfg.Output.MaxLines, marker)); err != nil {
		return result, err
	}
	if err := atomicWrite(englishASSPath, subtitle.RenderASS(subtitle.Clean(english), r.cfg.Output.MaxLineChars, r.cfg.Output.MaxLines, filepath.Base(input)+" English")); err != nil {
		return result, err
	}
	project := map[string]any{
		"pipeline_version": "qwen-first-v1", "input": input, "language": "ja",
		"japanese": segments, "english": subtitle.Clean(english),
		"models": map[string]string{"asr_primary": modelIdentity(r.cfg.Whisper.QwenModel, r.cfg.Whisper.QwenRevision), "asr_secondary": r.cfg.Whisper.ReazonModel, "asr_tertiary": r.cfg.Whisper.Model, "aligner": modelIdentity(r.cfg.Whisper.AlignerModel, r.cfg.Whisper.AlignerRevision), "translator": r.cfg.Translation.Model},
	}
	if raw, readErr := os.ReadFile(transcriptPrefix + ".qwen.json"); readErr == nil {
		var diagnostics any
		if json.Unmarshal(raw, &diagnostics) == nil {
			project["diagnostics"] = diagnostics
		}
	}
	projectJSON, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		return result, err
	}
	if err := atomicWrite(projectPath, string(projectJSON)+"\n"); err != nil {
		return result, err
	}
	result.EnglishSRT = englishPath
	result.EnglishASS = englishASSPath
	result.ProjectJSON = projectPath
	result.Segments = len(english)
	progress("complete", 100, "Subtitle generation complete")
	return result, nil
}

func (r *Runner) transcribe(ctx context.Context, wav, prefix string, translate, useGPU bool, report func(int)) ([]subtitle.Segment, error) {
	if r.cfg.Whisper.Backend == "qwen" && !translate {
		segments, err := r.transcribeQwen(ctx, wav, prefix, useGPU, report)
		if err == nil {
			return segments, nil
		}
		if r.cfg.Whisper.ReazonEnabled {
			r.log.Warn("Qwen pipeline failed; trying ReazonSpeech whole-file fallback", "error", err)
			segments, reazonErr := r.transcribeReazon(ctx, wav, prefix+"-fallback", useGPU, report)
			if reazonErr == nil {
				return segments, nil
			}
			r.log.Warn("ReazonSpeech whole-file fallback failed", "error", reazonErr)
		}
		if !r.cfg.Whisper.FallbackWhisper {
			return nil, err
		}
		r.log.Warn("Qwen pipeline failed; using whole-file compatibility fallback", "error", err)
	}
	if r.cfg.Whisper.Backend == "reazon" && !translate {
		segments, err := r.transcribeReazon(ctx, wav, prefix, useGPU, report)
		if err == nil {
			return segments, nil
		}
		if !r.cfg.Whisper.FallbackWhisper {
			return nil, err
		}
		r.log.Warn("ReazonSpeech failed; falling back to validated Whisper transcription", "error", err)
	}
	return r.transcribeWhisper(ctx, wav, prefix, translate, useGPU, report)
}

func (r *Runner) transcribeQwen(ctx context.Context, wav, prefix string, useGPU bool, report func(int)) ([]subtitle.Segment, error) {
	c := r.cfg.Whisper
	output := prefix + ".qwen.json"
	device := "cpu"
	if useGPU {
		device = "cuda"
	}
	args := []string{
		c.QwenScript, "--input", wav, "--output", output, "--device", device,
		"--mode", c.Mode, "--profile", c.Profile, "--qwen-model", c.QwenModel,
		"--qwen-revision", c.QwenRevision,
		"--context", c.Prompt,
		"--aligner-model", c.AlignerModel, "--aligner-revision", c.AlignerRevision, "--reazon-model", c.ReazonModel,
		"--reazon-python", c.ReazonPython, "--reazon-script", c.ReazonBatchScript,
		"--whisper-binary", c.Binary, "--whisper-model", c.Model,
		"--batch-size", strconv.Itoa(c.ASRBatchSize),
		"--vad-threshold", fmt.Sprintf("%.3f", c.VADEnergyFactor),
		"--vad-min-speech-ms", strconv.Itoa(c.MinSpeechMS),
		"--vad-merge-silence-ms", strconv.Itoa(c.MinSilenceMS),
		"--vad-pre-roll-ms", strconv.Itoa(c.VADPreRollMS),
		"--vad-post-roll-ms", strconv.Itoa(c.VADPostRollMS),
		"--max-segment-seconds", strconv.Itoa(c.MaxSegmentSec),
	}
	if !c.ReazonEnabled {
		args = append(args, "--disable-reazon")
	}
	if !c.WhisperEnabled || !c.FallbackWhisper {
		args = append(args, "--disable-whisper")
	}
	if c.DebugMode {
		args = append(args, "--debug")
	}
	err := runWithProgress(ctx, report, c.QwenPython, args...)
	if err != nil && useGPU && c.GPUFallbackCPU {
		r.log.Warn("Qwen CUDA pipeline failed; retrying this file in CPU mode", "error", err)
		_ = os.Remove(output)
		for index := range args {
			if args[index] == "cuda" && index > 0 && args[index-1] == "--device" {
				args[index] = "cpu"
			}
		}
		report(0)
		err = runWithProgress(ctx, report, c.QwenPython, args...)
	}
	if err != nil {
		return nil, fmt.Errorf("Qwen Japanese ASR pipeline: %w", err)
	}
	r.logGPUStatus(ctx, "after Qwen Japanese ASR pipeline")
	data, err := os.ReadFile(output)
	if err != nil {
		return nil, fmt.Errorf("read Qwen pipeline JSON: %w", err)
	}
	var doc qwenDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("decode Qwen pipeline JSON: %w", err)
	}
	if doc.Language != "ja" {
		return nil, fmt.Errorf("Qwen pipeline returned unexpected language %q", doc.Language)
	}
	if doc.DurationMS <= 0 || doc.ProcessedMS < doc.DurationMS-1000 {
		return nil, fmt.Errorf("Qwen pipeline coverage incomplete: processed %dms of %dms", doc.ProcessedMS, doc.DurationMS)
	}
	segments := make([]subtitle.Segment, 0, len(doc.Segments))
	for _, item := range doc.Segments {
		segments = append(segments, subtitle.Segment{StartMS: item.StartMS, EndMS: item.EndMS, Text: item.Text})
	}
	if err := validateSegments(segments, doc.DurationMS, int64(c.MaxSegmentSec)*1000); err != nil {
		return nil, fmt.Errorf("validate Qwen pipeline output: %w", err)
	}
	return segments, nil
}

type qwenDocument struct {
	DurationMS  int64  `json:"duration_ms"`
	ProcessedMS int64  `json:"processed_ms"`
	Language    string `json:"language"`
	Segments    []struct {
		StartMS int64  `json:"start_ms"`
		EndMS   int64  `json:"end_ms"`
		Text    string `json:"text"`
	} `json:"segments"`
}

func (r *Runner) transcribeReazon(ctx context.Context, wav, prefix string, useGPU bool, report func(int)) ([]subtitle.Segment, error) {
	c := r.cfg.Whisper
	output := prefix + ".reazon.json"
	device := "cpu"
	if useGPU {
		device = "cuda"
	}
	args := reazonArgs(c, wav, output, device)
	err := runWithProgress(ctx, report, c.ReazonPython, args...)
	if err != nil && useGPU && c.GPUFallbackCPU {
		r.log.Warn("ReazonSpeech CUDA inference failed; retrying this file on CPU", "error", err)
		_ = os.Remove(output)
		args = reazonArgs(c, wav, output, "cpu")
		report(0)
		err = runWithProgress(ctx, report, c.ReazonPython, args...)
	}
	if err != nil {
		return nil, fmt.Errorf("ReazonSpeech inference: %w", err)
	}
	r.logGPUStatus(ctx, "after ReazonSpeech inference")
	data, err := os.ReadFile(output)
	if err != nil {
		return nil, fmt.Errorf("read ReazonSpeech JSON: %w", err)
	}
	var doc reazonDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("decode ReazonSpeech JSON: %w", err)
	}
	if doc.DurationMS <= 0 || doc.ProcessedMS < doc.DurationMS-1000 {
		return nil, fmt.Errorf("ReazonSpeech coverage incomplete: processed %dms of %dms", doc.ProcessedMS, doc.DurationMS)
	}
	segments := make([]subtitle.Segment, 0, len(doc.Segments))
	for _, item := range doc.Segments {
		segments = append(segments, subtitle.Segment{StartMS: item.StartMS, EndMS: item.EndMS, Text: item.Text})
	}
	if err := validateSegments(segments, doc.DurationMS, int64(c.MaxSegmentSec)*1000); err != nil {
		return nil, fmt.Errorf("validate ReazonSpeech output: %w", err)
	}
	return segments, nil
}

func reazonArgs(c config.WhisperConfig, wav, output, device string) []string {
	return []string{c.ReazonScript, "--input", wav, "--output", output, "--model", c.ReazonModel, "--device", device, "--chunk-seconds", strconv.Itoa(c.ChunkSeconds), "--overlap-seconds", strconv.Itoa(c.OverlapSeconds), "--max-segment-seconds", strconv.Itoa(c.MaxSegmentSec)}
}

type reazonDocument struct {
	DurationMS  int64 `json:"duration_ms"`
	ProcessedMS int64 `json:"processed_ms"`
	Segments    []struct {
		StartMS int64  `json:"start_ms"`
		EndMS   int64  `json:"end_ms"`
		Text    string `json:"text"`
	} `json:"segments"`
}

func (r *Runner) transcribeWhisper(ctx context.Context, wav, prefix string, translate, useGPU bool, report func(int)) ([]subtitle.Segment, error) {
	c := r.cfg.Whisper
	args := []string{"-m", c.Model, "-f", wav, "-l", c.Language, "-ojf", "-of", prefix, "-t", strconv.Itoa(c.Threads), "-bs", strconv.Itoa(c.BeamSize), "-bo", strconv.Itoa(c.BeamSize), "-sow", "-ml", "42", "-sns", "-pp"}
	if c.Prompt != "" {
		args = append(args, "--prompt", c.Prompt)
	}
	if translate {
		args = append(args, "-tr")
	}
	if !useGPU {
		args = append(args, "-ng")
	}
	if c.VAD {
		args = append(args, "--vad", "-vm", c.VADModel, "-vt", fmt.Sprintf("%.2f", c.VADThreshold), "-vspd", strconv.Itoa(c.MinSpeechMS), "-vsd", strconv.Itoa(c.MinSilenceMS), "-vp", strconv.Itoa(c.SpeechPadMS))
	}
	if err := runWithProgress(ctx, report, c.Binary, args...); err != nil {
		originalErr := err
		if useGPU && c.GPUAutoReset && isCUDAFailure(err) {
			r.log.Warn("CUDA inference failed; attempting a guarded GPU reset", "error", err)
			if resetErr := r.resetGPU(ctx); resetErr == nil {
				r.log.Info("GPU reset succeeded; retrying whisper inference once")
				if retryErr := runWithProgress(ctx, report, c.Binary, args...); retryErr == nil {
					originalErr = nil
				} else {
					originalErr = retryErr
				}
			} else {
				r.log.Warn("GPU reset was unavailable; continuing with configured fallback", "error", resetErr)
			}
		}
		if originalErr != nil && useGPU && c.GPUFallbackCPU && isCUDAFailure(originalErr) {
			r.log.Warn("CUDA inference failed; retrying this file on CPU", "error", originalErr)
			report(0)
			cpuArgs := append(append([]string{}, args...), "-ng")
			if cpuErr := runWithProgress(ctx, report, c.Binary, cpuArgs...); cpuErr != nil {
				return nil, fmt.Errorf("whisper CUDA inference failed (%v); CPU fallback also failed: %w", originalErr, cpuErr)
			}
		} else if originalErr != nil {
			return nil, fmt.Errorf("whisper inference: %w", originalErr)
		}
	}
	r.logGPUStatus(ctx, "after inference")
	data, err := os.ReadFile(prefix + ".json")
	if err != nil {
		return nil, fmt.Errorf("read whisper JSON: %w", err)
	}
	var doc whisperDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("decode whisper JSON: %w", err)
	}
	out := make([]subtitle.Segment, 0, len(doc.Transcription))
	for _, item := range doc.Transcription {
		start, end := item.Offsets.From, item.Offsets.To
		if end == 0 {
			start, _ = parseClock(item.Timestamps.From)
			end, _ = parseClock(item.Timestamps.To)
		}
		out = append(out, subtitle.Segment{StartMS: start, EndMS: end, Text: item.Text})
	}
	if err := validateSegments(out, 0, int64(c.MaxSegmentSec)*1000); err != nil {
		return nil, fmt.Errorf("validate whisper output: %w", err)
	}
	return out, nil
}

func validateSegments(segments []subtitle.Segment, durationMS, maxSegmentMS int64) error {
	var previousStart int64 = -1
	for index, segment := range segments {
		if segment.StartMS < 0 || segment.EndMS <= segment.StartMS {
			return fmt.Errorf("segment %d has invalid timestamps %d-%dms", index+1, segment.StartMS, segment.EndMS)
		}
		if previousStart > segment.StartMS {
			return fmt.Errorf("segment %d starts before the previous segment", index+1)
		}
		if maxSegmentMS > 0 && segment.EndMS-segment.StartMS > maxSegmentMS {
			return fmt.Errorf("segment %d spans %dms, exceeding the %dms safety limit", index+1, segment.EndMS-segment.StartMS, maxSegmentMS)
		}
		if durationMS > 0 && segment.EndMS > durationMS+1000 {
			return fmt.Errorf("segment %d ends beyond the %dms recording", index+1, durationMS)
		}
		previousStart = segment.StartMS
	}
	return nil
}

type whisperDocument struct {
	Transcription []struct {
		Timestamps struct {
			From string `json:"from"`
			To   string `json:"to"`
		} `json:"timestamps"`
		Offsets struct {
			From int64 `json:"from"`
			To   int64 `json:"to"`
		} `json:"offsets"`
		Text string `json:"text"`
	} `json:"transcription"`
}

func parseClock(value string) (int64, error) {
	value = strings.ReplaceAll(value, ",", ".")
	var h, m int64
	var sec float64
	if _, err := fmt.Sscanf(value, "%d:%d:%f", &h, &m, &sec); err != nil {
		return 0, err
	}
	return h*3_600_000 + m*60_000 + int64(sec*1000), nil
}

func run(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if len(message) > 2000 {
			message = message[len(message)-2000:]
		}
		return fmt.Errorf("%s: %w: %s", name, err, message)
	}
	return nil
}

var whisperProgress = regexp.MustCompile(`(?i)progress\s*=\s*([0-9]{1,3})%`)

func runWithProgress(ctx context.Context, report func(int), name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	var captured bytes.Buffer
	scanner := bufio.NewScanner(stderr)
	scanner.Split(splitLinesAndCarriageReturns)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	last := -1
	for scanner.Scan() {
		line := scanner.Text()
		if captured.Len()+len(line)+1 > 8000 {
			content := captured.String()
			captured.Reset()
			if len(content) > 4000 {
				captured.WriteString(content[len(content)-4000:])
			}
		}
		captured.WriteString(line)
		captured.WriteByte('\n')
		match := whisperProgress.FindStringSubmatch(line)
		if len(match) == 2 {
			pct, _ := strconv.Atoi(match[1])
			if pct > 100 {
				pct = 100
			}
			if pct != last {
				last = pct
				report(pct)
			}
		}
	}
	waitErr := cmd.Wait()
	if waitErr != nil {
		message := strings.TrimSpace(captured.String())
		if len(message) > 2000 {
			message = message[len(message)-2000:]
		}
		return fmt.Errorf("%s: %w: %s", name, waitErr, message)
	}
	return scanner.Err()
}

func splitLinesAndCarriageReturns(data []byte, atEOF bool) (advance int, token []byte, err error) {
	for i, value := range data {
		if value == '\n' || value == '\r' {
			return i + 1, data[:i], nil
		}
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

func atomicWrite(path, content string) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".javbeaconsubs-*.tmp")
	if err != nil {
		return fmt.Errorf("create subtitle beside %s: %w", path, err)
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err = io.WriteString(f, content); err == nil {
		err = f.Sync()
	}
	if err == nil {
		err = f.Chmod(0o664)
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return nil
}

func modelIdentity(name, revision string) string {
	if revision == "" {
		return name
	}
	return name + "@" + revision
}
