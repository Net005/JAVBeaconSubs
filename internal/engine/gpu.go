package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

func (r *Runner) prepareGPU(ctx context.Context) (bool, error) {
	if !r.cfg.Whisper.UseGPU || !r.cfg.Whisper.GPUPreflight {
		return r.cfg.Whisper.UseGPU, nil
	}
	if _, err := exec.LookPath("nvidia-smi"); err != nil {
		r.log.Warn("nvidia-smi is unavailable; skipping the CUDA preflight probe")
		return true, nil
	}
	if status, err := gpuStatus(ctx); err == nil {
		r.log.Info("GPU preflight passed", "memory", status)
		return true, nil
	} else if !r.cfg.Whisper.GPUAutoReset {
		if r.cfg.Whisper.GPUFallbackCPU {
			r.log.Warn("GPU preflight failed; using CPU for this file", "error", err)
			return false, nil
		}
		return false, fmt.Errorf("GPU preflight failed: %w; automatic reset is disabled", err)
	} else {
		r.log.Warn("GPU preflight failed after sleep/resume or a previous session; attempting reset", "error", err)
	}
	if err := r.resetGPU(ctx); err != nil {
		if r.cfg.Whisper.GPUFallbackCPU {
			r.log.Warn("GPU preflight recovery failed; using CPU for this file", "error", err)
			return false, nil
		}
		return false, fmt.Errorf("GPU preflight recovery: %w", err)
	}
	if status, err := gpuStatus(ctx); err != nil {
		if r.cfg.Whisper.GPUFallbackCPU {
			return false, nil
		}
		return false, fmt.Errorf("GPU remained unhealthy after reset: %w", err)
	} else {
		r.log.Info("GPU recovered", "memory", status)
	}
	return true, nil
}

func (r *Runner) resetGPU(parent context.Context) error {
	if _, err := exec.LookPath("nvidia-smi"); err != nil {
		return errors.New("nvidia-smi is unavailable")
	}
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "nvidia-smi", "--gpu-reset")
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(output.String())
		return fmt.Errorf("nvidia-smi --gpu-reset failed: %w: %s (close games, display/compute clients, and other GPU containers; some driver failures still require a host driver reload or reboot)", err, message)
	}
	time.Sleep(2 * time.Second)
	return nil
}

func (r *Runner) logGPUStatus(ctx context.Context, phase string) {
	if !r.cfg.Whisper.UseGPU {
		return
	}
	if status, err := gpuStatus(ctx); err == nil {
		r.log.Info("GPU context released by whisper worker", "phase", phase, "memory", status)
	} else {
		r.log.Warn("unable to verify GPU state", "phase", phase, "error", err)
	}
}

func gpuStatus(parent context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "nvidia-smi", "--query-gpu=index,memory.used,memory.total", "--format=csv,noheader,nounits")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("nvidia-smi probe: %w: %s", err, strings.TrimSpace(string(output)))
	}
	status := strings.TrimSpace(string(output))
	if status == "" {
		return "", errors.New("nvidia-smi returned no GPUs")
	}
	return status + " MiB (index, used, total)", nil
}

func isCUDAFailure(err error) bool {
	message := strings.ToLower(err.Error())
	for _, marker := range []string{"cuda", "cublas", "gpu backend", "ggml_cuda", "driver/library version mismatch", "failed to initialize"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}
