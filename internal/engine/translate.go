package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"javbeaconsubs/internal/subtitle"
)

type chatRequest struct {
	Model          string            `json:"model"`
	Temperature    float64           `json:"temperature"`
	Messages       []chatMessage     `json:"messages"`
	ResponseFormat map[string]string `json:"response_format,omitempty"`
}
type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
	Usage tokenUsage `json:"usage"`
}
type tokenUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}
type translationResult struct {
	Translations []struct {
		ID   int    `json:"id"`
		Text string `json:"text"`
	} `json:"translations"`
}

func (r *Runner) translate(ctx context.Context, source []subtitle.Segment, progress ProgressFunc) ([]subtitle.Segment, tokenUsage, error) {
	out := make([]subtitle.Segment, len(source))
	copy(out, source)
	var totalUsage tokenUsage
	batchSize := r.cfg.Translation.BatchSize
	for start := 0; start < len(source); start += batchSize {
		end := start + batchSize
		if end > len(source) {
			end = len(source)
		}
		items := make([]map[string]any, 0, end-start+4)
		contextStart := start - 2
		if contextStart < 0 {
			contextStart = 0
		}
		contextEnd := end + 2
		if contextEnd > len(source) {
			contextEnd = len(source)
		}
		for i := contextStart; i < contextEnd; i++ {
			items = append(items, map[string]any{"id": i, "translate": i >= start && i < end, "japanese": source[i].Text})
		}
		payload, _ := json.Marshal(items)
		system := `You are an expert Japanese-to-English subtitle translator. Translate natural spoken Japanese, preserving names, relationships, tone, sexual language, incomplete sentences, and short reactions. Use the surrounding non-translation rows only as context. Never merge, omit, repeat, censor, explain, or renumber rows. Produce concise natural English suitable for subtitles. Return strict JSON: {"translations":[{"id":number,"text":"English"}]}.`
		if r.cfg.Translation.Glossary != "" {
			system += "\nRequired glossary and style notes:\n" + r.cfg.Translation.Glossary
		}
		requestBody := chatRequest{Model: r.cfg.Translation.Model, Temperature: .1, Messages: []chatMessage{{Role: "system", Content: system}, {Role: "user", Content: string(payload)}}, ResponseFormat: map[string]string{"type": "json_object"}}
		var translated translationResult
		usage, err := r.chat(ctx, requestBody, &translated)
		if err != nil {
			return nil, totalUsage, fmt.Errorf("translate rows %d-%d: %w", start+1, end, err)
		}
		totalUsage.PromptTokens += usage.PromptTokens
		totalUsage.CompletionTokens += usage.CompletionTokens
		totalUsage.TotalTokens += usage.TotalTokens
		byID := make(map[int]string, len(translated.Translations))
		for _, item := range translated.Translations {
			byID[item.ID] = strings.TrimSpace(item.Text)
		}
		for i := start; i < end; i++ {
			text, ok := byID[i]
			if !ok || text == "" {
				return nil, totalUsage, fmt.Errorf("translation response omitted row %d", i)
			}
			out[i].Text = text
		}
		percent := 74 + int(float64(end)/float64(len(source))*20)
		progress("translation", percent, fmt.Sprintf("Translated %d of %d dialogue segments", end, len(source)))
	}
	r.log.Info("contextual translation usage", "input_tokens", totalUsage.PromptTokens, "output_tokens", totalUsage.CompletionTokens, "total_tokens", totalUsage.TotalTokens)
	return out, totalUsage, nil
}

func (r *Runner) chat(ctx context.Context, input chatRequest, output any) (tokenUsage, error) {
	body, err := json.Marshal(input)
	if err != nil {
		return tokenUsage{}, err
	}
	url := strings.TrimRight(r.cfg.Translation.BaseURL, "/")
	if !strings.HasSuffix(url, "/chat/completions") {
		url += "/chat/completions"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return tokenUsage{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if r.cfg.Translation.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+r.cfg.Translation.APIKey)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return tokenUsage{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return tokenUsage{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return tokenUsage{}, fmt.Errorf("translation API returned %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	var chat chatResponse
	if err := json.Unmarshal(data, &chat); err != nil {
		return tokenUsage{}, err
	}
	if len(chat.Choices) == 0 {
		return tokenUsage{}, fmt.Errorf("translation API returned no choices")
	}
	content := strings.TrimSpace(chat.Choices[0].Message.Content)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	if err := json.Unmarshal([]byte(strings.TrimSpace(content)), output); err != nil {
		return chat.Usage, fmt.Errorf("invalid translation JSON: %w", err)
	}
	return chat.Usage, nil
}
