package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"javbeaconsubs/internal/config"
	"javbeaconsubs/internal/subtitle"
)

type chatRequest struct {
	Model          string            `json:"model"`
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

type translationWindowRow struct {
	ID        int
	Translate bool
	Text      string
}

type translationWindow struct {
	Rows            []translationWindowRow
	ExternalContext int
}

type legacyTranslationRow struct {
	ID        int    `json:"id"`
	Translate bool   `json:"translate"`
	Japanese  string `json:"japanese"`
}

// TranslationMemory is scoped to one submitted job and shared by every media
// file in it. It stores only exact, non-ambiguous source text.
type TranslationMemory struct {
	mu    sync.RWMutex
	exact map[string]string
}

func NewTranslationMemory() *TranslationMemory {
	return &TranslationMemory{exact: make(map[string]string)}
}

func (m *TranslationMemory) lookup(source string) (string, bool) {
	key, ok := translationMemoryKey(source)
	if m == nil || !ok {
		return "", false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	value, found := m.exact[key]
	return value, found
}

func (m *TranslationMemory) store(source, translated string) {
	key, ok := translationMemoryKey(source)
	if m == nil || !ok || strings.TrimSpace(translated) == "" {
		return
	}
	m.mu.Lock()
	m.exact[key] = translated
	m.mu.Unlock()
}

func translationMemoryKey(value string) (string, bool) {
	value = strings.Join(strings.Fields(value), " ")
	if value == "" {
		return "", false
	}
	var meaningful strings.Builder
	for _, r := range value {
		if !unicode.IsSpace(r) && !unicode.IsPunct(r) && !unicode.IsSymbol(r) {
			meaningful.WriteRune(r)
		}
	}
	return value, utf8.RuneCountInString(meaningful.String()) >= 4
}

func buildTranslationWindow(source []subtitle.Segment, start, end int, gapMS int64, translateIDs map[int]bool) translationWindow {
	if start < 0 {
		start = 0
	}
	if end > len(source) {
		end = len(source)
	}
	if end < start {
		end = start
	}
	contextStart := start
	for contextStart > 0 && start-contextStart < 3 {
		previous := source[contextStart-1]
		next := source[contextStart]
		if next.StartMS-previous.EndMS > gapMS {
			break
		}
		contextStart--
	}
	contextEnd := end
	if contextEnd < len(source) && source[contextEnd].StartMS-source[contextEnd-1].EndMS <= gapMS {
		contextEnd++
	}
	rows := make([]translationWindowRow, 0, contextEnd-contextStart)
	for i := contextStart; i < contextEnd; i++ {
		rows = append(rows, translationWindowRow{ID: i, Translate: translateIDs[i], Text: source[i].Text})
	}
	return translationWindow{Rows: rows, ExternalContext: (start - contextStart) + (contextEnd - end)}
}

func compactTranslationPayload(window translationWindow) ([]byte, error) {
	rows := make([][3]any, 0, len(window.Rows))
	for _, row := range window.Rows {
		translate := 0
		if row.Translate {
			translate = 1
		}
		rows = append(rows, [3]any{row.ID, translate, row.Text})
	}
	return json.Marshal(rows)
}

func fitWindowToLegacyPayloadBudget(window translationWindow, source []subtitle.Segment, start, end int) translationWindow {
	legacyStart := start - 2
	if legacyStart < 0 {
		legacyStart = 0
	}
	legacyEnd := end + 2
	if legacyEnd > len(source) {
		legacyEnd = len(source)
	}
	legacy := make([]legacyTranslationRow, 0, legacyEnd-legacyStart)
	for i := legacyStart; i < legacyEnd; i++ {
		legacy = append(legacy, legacyTranslationRow{ID: i, Translate: i >= start && i < end, Japanese: source[i].Text})
	}
	legacyPayload, _ := json.Marshal(legacy)
	for {
		compact, _ := compactTranslationPayload(window)
		if len(compact) <= len(legacyPayload) {
			return window
		}
		remove := -1
		for i, row := range window.Rows {
			if row.ID < start {
				remove = i
				break
			}
		}
		if remove < 0 {
			for i := len(window.Rows) - 1; i >= 0; i-- {
				if window.Rows[i].ID >= end {
					remove = i
					break
				}
			}
		}
		if remove < 0 {
			return window
		}
		window.Rows = append(window.Rows[:remove], window.Rows[remove+1:]...)
		window.ExternalContext--
	}
}

func glossaryInstructions(legacy string, structured *config.StructuredGlossary, rows []translationWindowRow) (string, int) {
	var sections []string
	entries := 0
	if legacy = strings.TrimSpace(legacy); legacy != "" {
		sections = append(sections, "Required glossary and style notes:\n"+legacy)
		entries++
	}
	if structured == nil {
		return strings.Join(sections, "\n"), entries
	}
	var styles []string
	for _, rule := range structured.Style {
		if rule = strings.TrimSpace(rule); rule != "" {
			styles = append(styles, "- "+rule)
			entries++
		}
	}
	if len(styles) > 0 {
		sections = append(sections, "Global style rules:\n"+strings.Join(styles, "\n"))
	}
	var dialogue strings.Builder
	for _, row := range rows {
		dialogue.WriteString(row.Text)
		dialogue.WriteByte('\n')
	}
	dialogueText := dialogue.String()
	keys := make([]string, 0, len(structured.Terms))
	for source := range structured.Terms {
		keys = append(keys, source)
	}
	sort.Strings(keys)
	var terms []string
	for _, source := range keys {
		if source != "" && strings.Contains(dialogueText, source) {
			terms = append(terms, "- "+source+" => "+structured.Terms[source])
			entries++
		}
	}
	if len(terms) > 0 {
		sections = append(sections, "Relevant term mappings:\n"+strings.Join(terms, "\n"))
	}
	return strings.Join(sections, "\n"), entries
}

const translationSystemPrompt = `Translate spoken Japanese into concise, natural English subtitles. Preserve names, relationships, tone, slang, explicit/sexual language, incomplete speech, and reactions without censorship. Input rows are [id,t,text]: t=1 must be translated; t=0 is context only. Return every t=1 id exactly once; never merge, omit, repeat, explain, or renumber. Output strict JSON only: {"translations":[{"id":number,"text":"English"}]}.`

func (r *Runner) translate(ctx context.Context, source []subtitle.Segment, progress ProgressFunc, memory *TranslationMemory) ([]subtitle.Segment, tokenUsage, error) {
	out := make([]subtitle.Segment, len(source))
	copy(out, source)
	var totalUsage tokenUsage
	batchSize := r.cfg.Translation.BatchSize
	translatedRows, contextRows, reusedRows, glossaryEntries := 0, 0, 0, 0
	for start := 0; start < len(source); start += batchSize {
		end := start + batchSize
		if end > len(source) {
			end = len(source)
		}
		translateIDs := make(map[int]bool, end-start)
		batchHits := 0
		for i := start; i < end; i++ {
			if r.cfg.Translation.TranslationMemory {
				if translated, ok := memory.lookup(source[i].Text); ok {
					out[i].Text = translated
					batchHits++
					continue
				}
			}
			translateIDs[i] = true
		}
		reusedRows += batchHits
		if len(translateIDs) == 0 {
			r.log.Info("contextual translation batch", "translated_rows", 0, "context_rows", 0, "reused_rows", batchHits, "glossary_entries", 0)
			percent := 74 + int(float64(end)/float64(len(source))*20)
			progress("translation", percent, fmt.Sprintf("Translated %d of %d dialogue segments", end, len(source)))
			continue
		}
		window := buildTranslationWindow(source, start, end, r.cfg.Translation.ContextGapMS, translateIDs)
		window = fitWindowToLegacyPayloadBudget(window, source, start, end)
		payload, err := compactTranslationPayload(window)
		if err != nil {
			return nil, totalUsage, err
		}
		glossary, batchGlossaryEntries := glossaryInstructions(r.cfg.Translation.Glossary, r.cfg.Translation.StructuredGlossary, window.Rows)
		system := translationSystemPrompt
		if glossary != "" {
			system += "\n" + glossary
		}
		requestBody := chatRequest{Model: r.cfg.Translation.Model, Messages: []chatMessage{{Role: "system", Content: system}, {Role: "user", Content: string(payload)}}, ResponseFormat: map[string]string{"type": "json_object"}}
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
			if !translateIDs[i] {
				continue
			}
			text, ok := byID[i]
			if !ok || text == "" {
				return nil, totalUsage, fmt.Errorf("translation response omitted row %d", i)
			}
			out[i].Text = text
			if r.cfg.Translation.TranslationMemory {
				memory.store(source[i].Text, text)
			}
		}
		translatedRows += len(translateIDs)
		contextRows += window.ExternalContext
		glossaryEntries += batchGlossaryEntries
		r.log.Info("contextual translation batch", "translated_rows", len(translateIDs), "context_rows", window.ExternalContext, "reused_rows", batchHits, "glossary_entries", batchGlossaryEntries)
		percent := 74 + int(float64(end)/float64(len(source))*20)
		progress("translation", percent, fmt.Sprintf("Translated %d of %d dialogue segments", end, len(source)))
	}
	r.log.Info("contextual translation usage", "input_tokens", totalUsage.PromptTokens, "output_tokens", totalUsage.CompletionTokens, "total_tokens", totalUsage.TotalTokens, "translated_rows", translatedRows, "context_rows", contextRows, "reused_rows", reusedRows, "glossary_entries", glossaryEntries)
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
