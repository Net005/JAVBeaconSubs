package models

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"javbeaconsubs/internal/config"
)

type Model struct {
	ID             string     `json:"id"`
	Role           string     `json:"role"`
	DisplayName    string     `json:"display_name"`
	Provider       string     `json:"provider"`
	Repository     string     `json:"repository"`
	ActiveRevision string     `json:"active_revision,omitempty"`
	Path           string     `json:"path,omitempty"`
	UpdatePolicy   string     `json:"update_policy"`
	Status         string     `json:"status"`
	Installed      bool       `json:"installed"`
	AvailableSHA   string     `json:"available_revision,omitempty"`
	LastChecked    *time.Time `json:"last_checked,omitempty"`
	Error          string     `json:"error,omitempty"`
	MinimumVRAMGB  int        `json:"minimum_vram_gb,omitempty"`
}

type Registry struct {
	mu     sync.RWMutex
	models map[string]*Model
	client *http.Client
}

func New(cfg config.Config) *Registry {
	values := []*Model{
		{ID: "qwen-asr", Role: "Primary ASR", DisplayName: "Qwen3-ASR-1.7B", Provider: "huggingface", Repository: cfg.Whisper.QwenModel, ActiveRevision: cfg.Whisper.QwenRevision, UpdatePolicy: "notify_only", Status: "active", Installed: cachedHuggingFace(cfg.Whisper.QwenModel), MinimumVRAMGB: 6},
		{ID: "qwen-aligner", Role: "Alignment", DisplayName: "Qwen3 Forced Aligner 0.6B", Provider: "huggingface", Repository: cfg.Whisper.AlignerModel, ActiveRevision: cfg.Whisper.AlignerRevision, UpdatePolicy: "notify_only", Status: "active", Installed: cachedHuggingFace(cfg.Whisper.AlignerModel), MinimumVRAMGB: 4},
		{ID: "whisper", Role: "Balanced fallback ASR", DisplayName: "Whisper Large v3", Provider: "local", Repository: "ggml-org/whisper.cpp", Path: cfg.Whisper.Model, UpdatePolicy: "pinned", Status: "active", Installed: regularFile(cfg.Whisper.Model), MinimumVRAMGB: 5},
		{ID: "reazon", Role: "Experimental / inactive", DisplayName: "ReazonSpeech NeMo v2", Provider: "huggingface", Repository: cfg.Whisper.ReazonModel, UpdatePolicy: "notify_only", Status: "inactive", Installed: cachedHuggingFace(cfg.Whisper.ReazonModel), MinimumVRAMGB: 6},
	}
	registry := &Registry{models: map[string]*Model{}, client: &http.Client{Timeout: 15 * time.Second}}
	for _, value := range values {
		registry.models[value.ID] = value
	}
	return registry
}

func (r *Registry) List() []Model {
	r.mu.RLock()
	defer r.mu.RUnlock()
	order := []string{"qwen-asr", "qwen-aligner", "whisper", "reazon"}
	result := make([]Model, 0, len(order))
	for _, id := range order {
		result = append(result, *r.models[id])
	}
	return result
}

func (r *Registry) Check(ctx context.Context, id string) (Model, error) {
	r.mu.RLock()
	value, ok := r.models[id]
	if !ok {
		r.mu.RUnlock()
		return Model{}, fmt.Errorf("unknown model %q", id)
	}
	copy := *value
	r.mu.RUnlock()
	now := time.Now().UTC()
	copy.LastChecked = &now
	if copy.Provider != "huggingface" {
		copy.Status = "pinned"
	} else {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://huggingface.co/api/models/"+escapeRepository(copy.Repository), nil)
		if err == nil {
			response, requestErr := r.client.Do(request)
			if requestErr != nil {
				err = requestErr
			} else {
				defer response.Body.Close()
				if response.StatusCode != http.StatusOK {
					err = fmt.Errorf("provider returned %s", response.Status)
				} else {
					var payload struct {
						SHA string `json:"sha"`
					}
					err = json.NewDecoder(response.Body).Decode(&payload)
					copy.AvailableSHA = payload.SHA
					if copy.ActiveRevision != "" && (strings.HasPrefix(payload.SHA, copy.ActiveRevision) || strings.HasPrefix(copy.ActiveRevision, payload.SHA)) {
						copy.Status = "up_to_date"
					} else {
						copy.Status = "update_available"
					}
				}
			}
		}
		if err != nil {
			copy.Status, copy.Error = "offline", err.Error()
		}
	}
	r.mu.Lock()
	*r.models[id] = copy
	r.mu.Unlock()
	return copy, nil
}

func cachedHuggingFace(repository string) bool {
	root := os.Getenv("HF_HOME")
	if root == "" {
		root = "/models/huggingface"
	}
	directory := "models--" + strings.ReplaceAll(repository, "/", "--")
	info, err := os.Stat(root + "/hub/" + directory)
	return err == nil && info.IsDir()
}

func regularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func escapeRepository(repository string) string {
	parts := strings.Split(repository, "/")
	for index := range parts {
		parts[index] = url.PathEscape(parts[index])
	}
	return strings.Join(parts, "/")
}
