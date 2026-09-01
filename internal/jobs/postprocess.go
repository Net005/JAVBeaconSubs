package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"javbeaconsubs/internal/config"
)

type PostProcessor struct {
	mu  sync.RWMutex
	cfg config.PostProcessingConfig
	log *slog.Logger
}

func NewPostProcessor(cfg config.PostProcessingConfig, log *slog.Logger) *PostProcessor {
	return &PostProcessor{cfg: cfg, log: log}
}

func (p *PostProcessor) Config() config.PostProcessingConfig {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.cfg
}

func (p *PostProcessor) Update(cfg config.PostProcessingConfig) {
	p.mu.Lock()
	p.cfg = cfg
	p.mu.Unlock()
}

func (p *PostProcessor) Enabled() bool { return p.Config().Mode != "none" }

func (p *PostProcessor) Run(parent context.Context, job *Job) error {
	cfg := p.Config()
	if cfg.Mode == "none" {
		return nil
	}
	payload, err := json.Marshal(job)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, time.Duration(cfg.TimeoutSec)*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	switch cfg.Mode {
	case "shell":
		cmd = exec.CommandContext(ctx, "/bin/bash", "--noprofile", "--norc", cfg.ShellScript)
		cmd.Env = append(os.Environ(),
			"JAVBEACONSUBS_JOB_ID="+job.ID,
			"JAVBEACONSUBS_EXTERNAL_ID="+job.ExternalID,
			"JAVBEACONSUBS_STATUS="+job.Status,
			"JAVBEACONSUBS_FILES="+strconv.Itoa(len(job.Files)),
		)
	case "webhook":
		args := []string{"--fail-with-body", "--silent", "--show-error", "--max-time", strconv.Itoa(cfg.TimeoutSec), "--request", "POST", "--header", "Content-Type: application/json"}
		if cfg.WebhookBearerToken != "" {
			args = append(args, "--header", "Authorization: Bearer "+cfg.WebhookBearerToken)
		}
		args = append(args, "--data-binary", "@-", cfg.WebhookURL)
		cmd = exec.CommandContext(ctx, "/usr/bin/curl", args...)
	default:
		return fmt.Errorf("unsupported post-processing mode %q", cfg.Mode)
	}
	cmd.Stdin = bytes.NewReader(payload)
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(output.String())
		if len(message) > 2000 {
			message = message[len(message)-2000:]
		}
		return fmt.Errorf("%s post-processing: %w: %s", cfg.Mode, err, message)
	}
	p.log.Info("post-processing completed", "job", job.ID, "mode", cfg.Mode)
	return nil
}
