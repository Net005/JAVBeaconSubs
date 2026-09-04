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
	Batches          int `json:"-"`
	TranslatedRows   int `json:"-"`
	ContextRows      int `json:"-"`
	ReusedRows       int `json:"-"`
	// RowsWithJapaneseScript and RowsRepaired are set once, after
	// translate()'s main loop, by repairMixedScriptLeaks (TODO Part 23-25).
	RowsWithJapaneseScript int `json:"-"`
	RowsRepaired           int `json:"-"`
	// ProperNameVariants is set once, after translate()'s repair pass, by
	// DetectProperNameVariants (TODO Part 22). Diagnostic only.
	ProperNameVariants []ProperNameVariant `json:"-"`
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

type glossaryTermMapping struct {
	Source string
	Target string
}

type structuredGlossaryIndex struct {
	styles       []string
	termsByFirst map[rune][]glossaryTermMapping
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

// values returns a snapshot of every currently-stored translation (TODO
// Part 21 source 5: "repeated accepted translation forms"). Order is not
// meaningful; callers that need determinism should sort.
func (m *TranslationMemory) values() []string {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]string, 0, len(m.exact))
	for _, value := range m.exact {
		out = append(out, value)
	}
	return out
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

func newStructuredGlossaryIndex(structured *config.StructuredGlossary) structuredGlossaryIndex {
	index := structuredGlossaryIndex{termsByFirst: make(map[rune][]glossaryTermMapping)}
	if structured == nil {
		return index
	}
	for _, rule := range structured.Style {
		if rule = strings.TrimSpace(rule); rule != "" {
			index.styles = append(index.styles, rule)
		}
	}
	for source, target := range structured.Terms {
		source, target = strings.TrimSpace(source), strings.TrimSpace(target)
		if source == "" || target == "" {
			continue
		}
		for _, first := range source {
			index.termsByFirst[first] = append(index.termsByFirst[first], glossaryTermMapping{Source: source, Target: target})
			break
		}
	}
	return index
}

func glossaryInstructions(legacy string, structured *config.StructuredGlossary, rows []translationWindowRow) (string, int) {
	return glossaryInstructionsIndexed(legacy, newStructuredGlossaryIndex(structured), rows)
}

func glossaryInstructionsIndexed(legacy string, index structuredGlossaryIndex, rows []translationWindowRow) (string, int) {
	var sections []string
	entries := 0
	if legacy = strings.TrimSpace(legacy); legacy != "" {
		sections = append(sections, "Required glossary and style notes:\n"+legacy)
		entries++
	}
	var styles []string
	for _, rule := range index.styles {
		styles = append(styles, "- "+rule)
		entries++
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
	relevant := make(map[string]string)
	checked := make(map[string]bool)
	for _, first := range dialogueText {
		for _, term := range index.termsByFirst[first] {
			if checked[term.Source] {
				continue
			}
			checked[term.Source] = true
			if strings.Contains(dialogueText, term.Source) {
				relevant[term.Source] = term.Target
			}
		}
	}
	keys := make([]string, 0, len(relevant))
	for source := range relevant {
		keys = append(keys, source)
	}
	sort.Strings(keys)
	terms := make([]string, 0, len(keys))
	for _, source := range keys {
		terms = append(terms, "- "+source+" => "+relevant[source])
		entries++
	}
	if len(terms) > 0 {
		sections = append(sections, "Relevant term mappings:\n"+strings.Join(terms, "\n"))
	}
	return strings.Join(sections, "\n"), entries
}

// glossaryTargetsUsedIn returns the deduplicated English target terms from
// index whose Japanese source appears anywhere in text. This is the same
// first-rune-indexed matching glossaryInstructionsIndexed already does,
// but returns the matched targets themselves rather than a formatted
// prompt string (TODO Part 21 sources 1+4: title/series overrides are
// already merged into index by applyCatalogs before translate() builds
// it, so a single scan over the glossary index covers both sources).
func glossaryTargetsUsedIn(index structuredGlossaryIndex, text string) []string {
	checked := make(map[string]bool)
	var targets []string
	for _, first := range text {
		for _, term := range index.termsByFirst[first] {
			if checked[term.Source] {
				continue
			}
			checked[term.Source] = true
			if strings.Contains(text, term.Source) {
				if target := strings.TrimSpace(term.Target); target != "" {
					targets = append(targets, target)
				}
			}
		}
	}
	return targets
}

// collectJobLocalTerms gathers the bounded, high-confidence set of English
// terms established for this file so far: glossary/title-series-override
// terms that matched its Japanese dialogue, plus every accepted
// translation-memory entry (TODO Part 21). Deduplicated case-
// insensitively; first occurrence wins. Deliberately NOT split into
// semantic categories (character/scenario/organization/series) - doing so
// reliably would require the kind of free-text name extraction from prose
// this project treats as a fabrication risk; the sources used here are
// already fully structured data with no extraction step.
func collectJobLocalTerms(source []subtitle.Segment, index structuredGlossaryIndex, memory *TranslationMemory) []string {
	seen := make(map[string]bool)
	var terms []string
	add := func(term string) {
		term = strings.TrimSpace(term)
		if term == "" {
			return
		}
		key := strings.ToLower(term)
		if seen[key] {
			return
		}
		seen[key] = true
		terms = append(terms, term)
	}
	var allText strings.Builder
	for _, seg := range source {
		allText.WriteString(seg.Text)
		allText.WriteByte('\n')
	}
	for _, target := range glossaryTargetsUsedIn(index, allText.String()) {
		add(target)
	}
	for _, value := range memory.values() {
		add(value)
	}
	return terms
}

// capTerms truncates terms to at most n entries, keeping prompt context
// compact (TODO Part 15/21: "do not dump huge metadata blobs... keep
// prompt context compact").
func capTerms(terms []string, n int) []string {
	if len(terms) <= n {
		return terms
	}
	return terms[:n]
}

const translationSystemPrompt = `Translate spoken Japanese into concise, natural English subtitles. Preserve names, relationships, tone, slang, explicit/sexual language, incomplete speech, and reactions without censorship. Input rows are [id,t,text]: t=1 must be translated; t=0 is context only. Return every t=1 id exactly once; never merge, omit, repeat, explain, or renumber. Output strict JSON only: {"translations":[{"id":number,"text":"English"}]}.`

// releaseContextInstructions builds the release title/story background
// context block for the translation system prompt. It returns "" when both
// are empty. The guardrail and authority sentences are always present
// whenever either field is non-empty, so the model can never mistake this
// for dialogue to translate or a source of truth over the actual spoken
// Japanese.
func releaseContextInstructions(title, story string) string {
	title, story = strings.TrimSpace(title), strings.TrimSpace(story)
	if title == "" && story == "" {
		return ""
	}
	var lines []string
	if title != "" {
		lines = append(lines, "Release title: "+title)
	}
	if story != "" {
		lines = append(lines, "Release story: "+story)
	}
	lines = append(lines, "The release title and story above are background context only. Do not copy, summarize, quote, or invent dialogue from them. Translate only the supplied Japanese subtitle text. Use this context only to resolve proper names, terminology, roles, relationships, factions, and scenario ambiguity. If the spoken Japanese conflicts with this context, the spoken Japanese is authoritative.")
	return strings.Join(lines, "\n")
}

// containsJapaneseScript reports whether text contains hiragana, katakana,
// or kanji. Used to detect Japanese script that leaked into an English
// translation (TODO Part 23). Deliberately narrower than
// profile.containsRecognitionCharacters, which also matches digits and
// uppercase Latin - those are normal in English proper nouns and must not
// false-positive here.
func containsJapaneseScript(text string) bool {
	for _, r := range text {
		if unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana) {
			return true
		}
	}
	return false
}

const mixedScriptRepairSystemPrompt = `The following rows were already translated from Japanese to English, but the English output incorrectly contains leftover Japanese script (hiragana, katakana, or kanji) instead of a natural English translation. Re-translate only the flagged rows (t=1) into natural English. Context rows (t=0) are for reference only, do not translate them. Preserve the original meaning exactly; do not invent, omit, or merge content. Return English text only, unless a specific proper noun genuinely requires keeping part of the original script by convention. Return every t=1 id exactly once. Output strict JSON only: {"translations":[{"id":number,"text":"English"}]}.`

// repairMixedScriptLeaks scans out for rows still containing Japanese
// script after translation and attempts one selective re-translation pass
// for just those rows (TODO Part 23-25). It never retranslates rows that
// are already clean, never touches source or timing, and on any failure
// (HTTP error, missing id, or a repair that still contains script) leaves
// the original translated text in out unchanged - the repair pass is
// best-effort QA, not part of the required translation path.
func (r *Runner) repairMixedScriptLeaks(ctx context.Context, source []subtitle.Segment, out []subtitle.Segment, glossaryIndex structuredGlossaryIndex, releaseContext string, jobLocalTerms []string) (detected, repaired int, usage tokenUsage, err error) {
	if !r.cfg.Translation.MixedScriptQA || len(source) == 0 {
		return 0, 0, tokenUsage{}, nil
	}
	flagged := make(map[int]bool)
	for i := range out {
		if containsJapaneseScript(out[i].Text) {
			flagged[i] = true
		}
	}
	detected = len(flagged)
	if detected == 0 {
		return 0, 0, tokenUsage{}, nil
	}
	batchSize := r.cfg.Translation.BatchSize
	for start := 0; start < len(source); start += batchSize {
		end := start + batchSize
		if end > len(source) {
			end = len(source)
		}
		chunkIDs := make(map[int]bool)
		for i := start; i < end; i++ {
			if flagged[i] {
				chunkIDs[i] = true
			}
		}
		if len(chunkIDs) == 0 {
			continue
		}
		window := buildTranslationWindow(source, start, end, r.cfg.Translation.ContextGapMS, chunkIDs)
		window = fitWindowToLegacyPayloadBudget(window, source, start, end)
		payload, marshalErr := compactTranslationPayload(window)
		if marshalErr != nil {
			return detected, repaired, usage, marshalErr
		}
		glossary, _ := glossaryInstructionsIndexed(r.cfg.Translation.Glossary, glossaryIndex, window.Rows)
		system := mixedScriptRepairSystemPrompt
		if glossary != "" {
			system += "\n" + glossary
		}
		if releaseContext != "" {
			system += "\n" + releaseContext
		}
		if len(jobLocalTerms) > 0 {
			system += "\nKnown proper names/terms already established for this file: " + strings.Join(capTerms(jobLocalTerms, 40), ", ")
		}
		requestBody := chatRequest{Model: r.cfg.Translation.Model, Messages: []chatMessage{{Role: "system", Content: system}, {Role: "user", Content: string(payload)}}, ResponseFormat: map[string]string{"type": "json_object"}}
		var repairedResult translationResult
		callUsage, callErr := r.chat(ctx, requestBody, &repairedResult)
		if callErr != nil {
			r.log.Warn("mixed-script repair batch failed", "rows", start+1, "end", end, "error", callErr)
			continue
		}
		usage.PromptTokens += callUsage.PromptTokens
		usage.CompletionTokens += callUsage.CompletionTokens
		usage.TotalTokens += callUsage.TotalTokens
		byID := make(map[int]string, len(repairedResult.Translations))
		for _, item := range repairedResult.Translations {
			byID[item.ID] = strings.TrimSpace(item.Text)
		}
		for id := range chunkIDs {
			text, ok := byID[id]
			if !ok || text == "" || containsJapaneseScript(text) {
				continue
			}
			out[id].Text = text
			repaired++
		}
	}
	return detected, repaired, usage, nil
}

func (r *Runner) translate(ctx context.Context, source []subtitle.Segment, progress ProgressFunc, memory *TranslationMemory) ([]subtitle.Segment, tokenUsage, error) {
	// Defense in depth: punctuation-only ASR artifacts must never consume paid
	// translation tokens even if a future caller bypasses the ASR cleanup path.
	source = subtitle.Clean(source)
	out := make([]subtitle.Segment, len(source))
	copy(out, source)
	var totalUsage tokenUsage
	batchSize := r.cfg.Translation.BatchSize
	glossaryIndex := newStructuredGlossaryIndex(r.cfg.Translation.StructuredGlossary)
	releaseContext := releaseContextInstructions(r.activeReleaseTitle, r.activeReleaseStory)
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
		glossary, batchGlossaryEntries := glossaryInstructionsIndexed(r.cfg.Translation.Glossary, glossaryIndex, window.Rows)
		system := translationSystemPrompt
		if glossary != "" {
			system += "\n" + glossary
		}
		if releaseContext != "" {
			system += "\n" + releaseContext
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
		totalUsage.Batches++
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
	totalUsage.TranslatedRows = translatedRows
	totalUsage.ContextRows = contextRows
	totalUsage.ReusedRows = reusedRows
	jobLocalTerms := collectJobLocalTerms(source, glossaryIndex, memory)
	detected, repairedCount, repairUsage, repairErr := r.repairMixedScriptLeaks(ctx, source, out, glossaryIndex, releaseContext, jobLocalTerms)
	if repairErr != nil {
		r.log.Warn("mixed-script repair pass failed", "error", repairErr)
	} else {
		totalUsage.PromptTokens += repairUsage.PromptTokens
		totalUsage.CompletionTokens += repairUsage.CompletionTokens
		totalUsage.TotalTokens += repairUsage.TotalTokens
		totalUsage.RowsWithJapaneseScript = detected
		totalUsage.RowsRepaired = repairedCount
	}
	if r.cfg.Translation.ProperNameVariantQA {
		totalUsage.ProperNameVariants = DetectProperNameVariants(out, jobLocalTerms)
	}
	r.log.Info("contextual translation usage", "input_tokens", totalUsage.PromptTokens, "output_tokens", totalUsage.CompletionTokens, "total_tokens", totalUsage.TotalTokens, "translated_rows", translatedRows, "context_rows", contextRows, "reused_rows", reusedRows, "glossary_entries", glossaryEntries, "rows_with_japanese_script", detected, "rows_repaired", repairedCount)
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
