package engine

import (
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
	"strconv"
	"strings"
	"time"

	"beaconsubs/internal/config"
	"beaconsubs/internal/subtitle"
)

const marker = "; generated-by=beaconsubs-v1"

type ProgressFunc func(phase string, percent int, message string)

type Result struct {
	Input                   string `json:"input"`
	EnglishSRT              string `json:"english_srt,omitempty"`
	JapaneseSRT             string `json:"japanese_srt,omitempty"`
	Segments                int    `json:"segments"`
	Skipped                 bool   `json:"skipped,omitempty"`
	TranslationInputTokens  int    `json:"translation_input_tokens,omitempty"`
	TranslationOutputTokens int    `json:"translation_output_tokens,omitempty"`
	TranslationTotalTokens  int    `json:"translation_total_tokens,omitempty"`
}

type Runner struct {
	cfg    config.Config
	log    *slog.Logger
	client *http.Client
}

func New(cfg config.Config, log *slog.Logger) *Runner {
	return &Runner{cfg: cfg, log: log, client: &http.Client{Timeout: time.Duration(cfg.Translation.TimeoutSec) * time.Second}}
}

func (r *Runner) Check() map[string]any {
	whisperPath, whisperErr := exec.LookPath(r.cfg.Whisper.Binary)
	ffmpegPath, ffmpegErr := exec.LookPath("ffmpeg")
	_, modelErr := os.Stat(r.cfg.Whisper.Model)
	result := map[string]any{"ready": whisperErr == nil && ffmpegErr == nil && modelErr == nil, "translation_mode": r.cfg.Translation.Mode}
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
	result := Result{Input: input}
	base := strings.TrimSuffix(input, filepath.Ext(input))
	englishPath := base + r.cfg.Output.EnglishSuffix
	japanesePath := base + r.cfg.Output.JapaneseSuffix
	if !overwrite && !r.cfg.Output.Overwrite {
		if _, err := os.Stat(englishPath); err == nil {
			result.EnglishSRT = englishPath
			result.Skipped = true
			return result, nil
		}
	}

	tmpDir, err := os.MkdirTemp("", "beaconsubs-*")
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(tmpDir)
	wav := filepath.Join(tmpDir, "audio.wav")
	progress("audio", 5, "Extracting and normalizing speech audio")
	if err := run(ctx, "ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-i", input, "-vn", "-ac", "1", "-ar", "16000", "-af", "highpass=f=70,lowpass=f=7600,loudnorm=I=-19:TP=-2:LRA=11", "-c:a", "pcm_s16le", wav); err != nil {
		return result, fmt.Errorf("extract audio: %w", err)
	}

	direct := r.cfg.Translation.Mode == "direct"
	progress("transcription", 15, "Transcribing Japanese speech")
	if err := r.prepareGPU(ctx); err != nil {
		return result, err
	}
	segments, err := r.transcribe(ctx, wav, filepath.Join(tmpDir, "transcript"), direct)
	if err != nil {
		return result, err
	}
	segments = subtitle.Clean(segments)
	if len(segments) == 0 {
		return result, errors.New("no speech segments were recognized")
	}

	if r.cfg.Output.KeepJapanese && !direct {
		progress("subtitle", 72, "Writing Japanese transcript")
		if err := atomicWrite(japanesePath, subtitle.RenderSRT(segments, 30, r.cfg.Output.MaxLines, marker)); err != nil {
			return result, err
		}
		result.JapaneseSRT = japanesePath
	}

	english := segments
	switch r.cfg.Translation.Mode {
	case "contextual":
		progress("translation", 74, "Translating with surrounding dialogue context")
		var usage tokenUsage
		english, usage, err = r.translate(ctx, segments, progress)
		if err != nil {
			return result, err
		}
		result.TranslationInputTokens = usage.PromptTokens
		result.TranslationOutputTokens = usage.CompletionTokens
		result.TranslationTotalTokens = usage.TotalTokens
	case "none":
		result.JapaneseSRT = japanesePath
		if !r.cfg.Output.KeepJapanese {
			if err := atomicWrite(japanesePath, subtitle.RenderSRT(segments, 30, r.cfg.Output.MaxLines, marker)); err != nil {
				return result, err
			}
		}
		result.Segments = len(segments)
		return result, nil
	}

	progress("subtitle", 95, "Writing English subtitles")
	if err := atomicWrite(englishPath, subtitle.RenderSRT(subtitle.Clean(english), r.cfg.Output.MaxLineChars, r.cfg.Output.MaxLines, marker)); err != nil {
		return result, err
	}
	result.EnglishSRT = englishPath
	result.Segments = len(english)
	progress("complete", 100, "Subtitle generation complete")
	return result, nil
}

func (r *Runner) transcribe(ctx context.Context, wav, prefix string, translate bool) ([]subtitle.Segment, error) {
	c := r.cfg.Whisper
	args := []string{"-m", c.Model, "-f", wav, "-l", c.Language, "-ojf", "-of", prefix, "-t", strconv.Itoa(c.Threads), "-bs", strconv.Itoa(c.BeamSize), "-bo", strconv.Itoa(c.BeamSize), "-sow", "-ml", "42", "-sns", "-np"}
	if c.Prompt != "" {
		args = append(args, "--prompt", c.Prompt)
	}
	if translate {
		args = append(args, "-tr")
	}
	if !c.UseGPU {
		args = append(args, "-ng")
	}
	if c.VAD {
		args = append(args, "--vad", "-vm", c.VADModel, "-vt", fmt.Sprintf("%.2f", c.VADThreshold), "-vspd", strconv.Itoa(c.MinSpeechMS), "-vsd", strconv.Itoa(c.MinSilenceMS), "-vp", strconv.Itoa(c.SpeechPadMS))
	}
	if err := run(ctx, c.Binary, args...); err != nil {
		if c.UseGPU && c.GPUAutoReset && isCUDAFailure(err) {
			r.log.Warn("CUDA inference failed; attempting a guarded GPU reset", "error", err)
			if resetErr := r.resetGPU(ctx); resetErr != nil {
				return nil, fmt.Errorf("whisper inference failed and GPU recovery failed: %w (original inference error: %v)", resetErr, err)
			}
			r.log.Info("GPU reset succeeded; retrying whisper inference once")
			if retryErr := run(ctx, c.Binary, args...); retryErr != nil {
				return nil, fmt.Errorf("whisper inference failed after GPU recovery: %w", retryErr)
			}
		} else {
			return nil, fmt.Errorf("whisper inference: %w", err)
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
	return out, nil
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

func atomicWrite(path, content string) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".beaconsubs-*.tmp")
	if err != nil {
		return fmt.Errorf("create subtitle beside %s: %w", path, err)
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err = io.WriteString(f, content); err == nil {
		err = f.Sync()
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
