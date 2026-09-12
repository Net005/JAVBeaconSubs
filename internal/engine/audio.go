package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
)

const normalizedAudioFilter = "highpass=f=70,lowpass=f=7600,loudnorm=I=-19:TP=-2:LRA=11"

type commandRunner func(context.Context, string, ...string) error

var errInvalidExtractedAudio = errors.New("FFmpeg did not create usable normalized audio")

// extractAudio retries decoding failures caused by damaged media once with
// corrupt-packet tolerance, regenerated timestamps, and audio resynchronization.
func extractAudio(ctx context.Context, input, output string, execute commandRunner) (bool, error) {
	strictErr := execute(ctx, "ffmpeg",
		"-hide_banner", "-loglevel", "error", "-y", "-i", input,
		"-map", "0:a:0", "-vn", "-ac", "1", "-ar", "16000",
		"-af", normalizedAudioFilter, "-c:a", "pcm_s16le", output,
	)
	if strictErr == nil {
		strictErr = validateExtractedAudio(output)
	}
	if strictErr == nil {
		return false, nil
	}
	if !isRecoverableAudioError(strictErr) {
		return false, strictErr
	}

	_ = os.Remove(output)
	recoveryErr := execute(ctx, "ffmpeg",
		"-hide_banner", "-loglevel", "warning", "-y",
		"-fflags", "+discardcorrupt+genpts", "-err_detect", "ignore_err",
		"-analyzeduration", "200M", "-probesize", "200M", "-i", input,
		"-map", "0:a:0", "-vn", "-max_error_rate", "1.0",
		"-ac", "1", "-ar", "16000",
		"-af", "aresample=async=1:first_pts=0,"+normalizedAudioFilter,
		"-c:a", "pcm_s16le", output,
	)
	if recoveryErr != nil {
		return true, fmt.Errorf("strict decode failed (%s); damaged-audio recovery failed: %w", compactAudioError(strictErr), recoveryErr)
	}
	if err := validateExtractedAudio(output); err != nil {
		return true, fmt.Errorf("strict decode failed (%s); damaged-audio recovery produced no usable audio: %w", compactAudioError(strictErr), err)
	}
	return true, nil
}

func validateExtractedAudio(path string) error {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 44 {
		return errInvalidExtractedAudio
	}
	return nil
}

func isRecoverableAudioError(err error) bool {
	if errors.Is(err, errInvalidExtractedAudio) {
		return true
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{"error while decoding", "invalid data found when processing input", "channel element", "invalid packet", "corrupt", "header missing"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func compactAudioError(err error) string {
	message := strings.Join(strings.Fields(err.Error()), " ")
	if len(message) > 240 {
		return message[:240] + "…"
	}
	return message
}
