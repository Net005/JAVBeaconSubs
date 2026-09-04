package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"javbeaconsubs/internal/config"
	"javbeaconsubs/internal/subtitle"
)

// fakeTranslationCall records one request the fake translation server
// received, decoded into its system prompt and the set of row ids the
// request asked to have translated (t=1).
type fakeTranslationCall struct {
	System string
	IDs    []int
}

// newFakeTranslationServer starts a fake OpenAI-chat-compatible server for
// exercising (r *Runner).translate/repairMixedScriptLeaks end-to-end.
// respond is invoked once per request and returns the id->text map to send
// back as the translation result; every call is recorded and returned via
// the *[]fakeTranslationCall pointer so tests can assert on call count,
// order, and exactly which rows were requested.
func newFakeTranslationServer(t *testing.T, respond func(call fakeTranslationCall) map[int]string) (*httptest.Server, *[]fakeTranslationCall) {
	t.Helper()
	calls := &[]fakeTranslationCall{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		var req chatRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		var system, userPayload string
		for _, m := range req.Messages {
			switch m.Role {
			case "system":
				system = m.Content
			case "user":
				userPayload = m.Content
			}
		}
		var rows [][3]any
		if err := json.Unmarshal([]byte(userPayload), &rows); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		var ids []int
		for _, row := range rows {
			id := int(row[0].(float64))
			if translate, ok := row[1].(float64); ok && translate == 1 {
				ids = append(ids, id)
			}
		}
		call := fakeTranslationCall{System: system, IDs: ids}
		*calls = append(*calls, call)
		texts := respond(call)
		translations := make([]map[string]any, 0, len(texts))
		for id, text := range texts {
			translations = append(translations, map[string]any{"id": id, "text": text})
		}
		content, err := json.Marshal(map[string]any{"translations": translations})
		if err != nil {
			t.Fatalf("marshal fake translation content: %v", err)
		}
		respBody, err := json.Marshal(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": string(content)}}},
			"usage":   map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
		})
		if err != nil {
			t.Fatalf("marshal fake response: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write(respBody); err != nil {
			t.Fatalf("write fake response: %v", err)
		}
	}))
	t.Cleanup(server.Close)
	return server, calls
}

// isRepairCall reports whether a captured request was the mixed-script
// repair pass rather than the main translation batch, by checking for text
// unique to mixedScriptRepairSystemPrompt.
func isRepairCall(call fakeTranslationCall) bool {
	return strings.Contains(call.System, "leftover Japanese script")
}

func continuousSegments(count int) []subtitle.Segment {
	segments := make([]subtitle.Segment, count)
	for i := range segments {
		segments[i] = subtitle.Segment{StartMS: int64(i * 1200), EndMS: int64(i*1200 + 1000), Text: "行"}
	}
	return segments
}

func translatedIDs(start, end int) map[int]bool {
	ids := make(map[int]bool, end-start)
	for i := start; i < end; i++ {
		ids[i] = true
	}
	return ids
}

func windowIDs(window translationWindow) []int {
	ids := make([]int, len(window.Rows))
	for i, row := range window.Rows {
		ids[i] = row.ID
	}
	return ids
}

func TestTranslationWindowTranscriptBoundaries(t *testing.T) {
	segments := continuousSegments(10)
	beginning := buildTranslationWindow(segments, 0, 2, 8000, translatedIDs(0, 2))
	if got := windowIDs(beginning); !equalInts(got, []int{0, 1, 2}) || beginning.ExternalContext != 1 {
		t.Fatalf("beginning window = %v, external=%d", got, beginning.ExternalContext)
	}
	end := buildTranslationWindow(segments, 8, 10, 8000, translatedIDs(8, 10))
	if got := windowIDs(end); !equalInts(got, []int{5, 6, 7, 8, 9}) || end.ExternalContext != 3 {
		t.Fatalf("end window = %v, external=%d", got, end.ExternalContext)
	}
}

func TestTranslationWindowContinuousConversationPrefersThreeBeforeOneAfter(t *testing.T) {
	segments := continuousSegments(10)
	window := buildTranslationWindow(segments, 4, 6, 8000, translatedIDs(4, 6))
	if got := windowIDs(window); !equalInts(got, []int{1, 2, 3, 4, 5, 6}) {
		t.Fatalf("window IDs = %v", got)
	}
	if window.ExternalContext != 4 {
		t.Fatalf("external context = %d, want 4", window.ExternalContext)
	}
}

func TestTranslationWindowDoesNotCrossLargeGap(t *testing.T) {
	segments := continuousSegments(8)
	segments[4].StartMS = segments[3].EndMS + 9000
	segments[4].EndMS = segments[4].StartMS + 1000
	for i := 5; i < len(segments); i++ {
		segments[i].StartMS = segments[i-1].EndMS + 200
		segments[i].EndMS = segments[i].StartMS + 1000
	}
	window := buildTranslationWindow(segments, 4, 6, 8000, translatedIDs(4, 6))
	if got := windowIDs(window); !equalInts(got, []int{4, 5, 6}) || window.ExternalContext != 1 {
		t.Fatalf("gap window = %v, external=%d", got, window.ExternalContext)
	}
}

func TestTranslationWindowsNeverExceedFourExternalRows(t *testing.T) {
	segments := continuousSegments(20)
	for start := range segments {
		for size := 1; size <= 5 && start+size <= len(segments); size++ {
			window := buildTranslationWindow(segments, start, start+size, 8000, translatedIDs(start, start+size))
			if window.ExternalContext > 4 {
				t.Fatalf("start=%d size=%d external=%d", start, size, window.ExternalContext)
			}
		}
	}
}

func TestCompactWindowDoesNotExceedLegacyPayloadBudget(t *testing.T) {
	segments := continuousSegments(10)
	segments[1].Text = strings.Repeat("とても長い文", 100)
	window := buildTranslationWindow(segments, 4, 6, 8000, translatedIDs(4, 6))
	window = fitWindowToLegacyPayloadBudget(window, segments, 4, 6)
	compact, err := compactTranslationPayload(window)
	if err != nil {
		t.Fatal(err)
	}
	legacy := make([]legacyTranslationRow, 0, 6)
	for i := 2; i < 8; i++ {
		legacy = append(legacy, legacyTranslationRow{ID: i, Translate: i >= 4 && i < 6, Japanese: segments[i].Text})
	}
	legacyPayload, _ := json.Marshal(legacy)
	if len(compact) > len(legacyPayload) {
		t.Fatalf("compact payload bytes %d exceed legacy %d", len(compact), len(legacyPayload))
	}
	if window.ExternalContext > 4 {
		t.Fatalf("external context = %d", window.ExternalContext)
	}
}

func TestCompactTranslationPayload(t *testing.T) {
	payload, err := compactTranslationPayload(translationWindow{Rows: []translationWindowRow{{ID: 1, Text: "前"}, {ID: 2, Translate: true, Text: "本体"}}})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(payload), `[[1,0,"前"],[2,1,"本体"]]`; got != want {
		t.Fatalf("payload = %s, want %s", got, want)
	}
	if strings.Contains(string(payload), "translate") || strings.Contains(string(payload), "japanese") {
		t.Fatal("payload contains legacy repeated field names")
	}
}

func TestStructuredGlossaryIncludesOnlyRelevantTerms(t *testing.T) {
	structured := &config.StructuredGlossary{
		Style: []string{"Keep honorifics"},
		Terms: map[string]string{"お姉ちゃん": "big sis", "先生": "teacher"},
	}
	instructions, count := glossaryInstructions("Mio stays Mio", structured, []translationWindowRow{{Text: "お姉ちゃん、待って"}})
	if !strings.Contains(instructions, "Mio stays Mio") || !strings.Contains(instructions, "Keep honorifics") || !strings.Contains(instructions, "お姉ちゃん => big sis") {
		t.Fatalf("missing relevant glossary content: %s", instructions)
	}
	if strings.Contains(instructions, "先生") {
		t.Fatalf("irrelevant term was included: %s", instructions)
	}
	if count != 3 {
		t.Fatalf("included entries = %d, want 3", count)
	}
}

func TestLargeStructuredGlossaryFiltersBeforeSerialization(t *testing.T) {
	terms := make(map[string]string, 2000)
	for i := 0; i < 2000; i++ {
		terms[fmt.Sprintf("用語%04d", i)] = fmt.Sprintf("term-%04d", i)
	}
	index := newStructuredGlossaryIndex(&config.StructuredGlossary{Terms: terms})
	instructions, count := glossaryInstructionsIndexed("", index, []translationWindowRow{{Text: "ここでは用語1499だけを使います"}})
	if count != 1 || !strings.Contains(instructions, "用語1499 => term-1499") {
		t.Fatalf("large glossary filtering failed: count=%d instructions=%q", count, instructions)
	}
	if strings.Contains(instructions, "用語1498") {
		t.Fatal("irrelevant mapping was serialized")
	}
}

func TestReleaseContextInstructionsEmptyWhenBothBlank(t *testing.T) {
	if got := releaseContextInstructions("", ""); got != "" {
		t.Fatalf("releaseContextInstructions(\"\", \"\") = %q, want empty", got)
	}
	if got := releaseContextInstructions("   ", "\t"); got != "" {
		t.Fatalf("releaseContextInstructions with whitespace-only input = %q, want empty", got)
	}
}

func TestReleaseContextInstructionsTitleOnly(t *testing.T) {
	got := releaseContextInstructions("Sample Release Title", "")
	if !strings.Contains(got, "Release title: Sample Release Title") {
		t.Fatalf("missing title line: %q", got)
	}
	if strings.Contains(got, "Release story:") {
		t.Fatalf("story line present with no story: %q", got)
	}
	if !strings.Contains(got, "background context only") {
		t.Fatalf("missing guardrail sentence: %q", got)
	}
	if !strings.Contains(got, "spoken Japanese is authoritative") {
		t.Fatalf("missing authority sentence: %q", got)
	}
}

func TestReleaseContextInstructionsTitleAndStory(t *testing.T) {
	got := releaseContextInstructions("  Sample Title  ", "  A story about two rivals.  ")
	if !strings.Contains(got, "Release title: Sample Title") {
		t.Fatalf("missing trimmed title line: %q", got)
	}
	if !strings.Contains(got, "Release story: A story about two rivals.") {
		t.Fatalf("missing trimmed story line: %q", got)
	}
	if !strings.Contains(got, "background context only") {
		t.Fatalf("missing guardrail sentence: %q", got)
	}
	titleIdx := strings.Index(got, "Release title:")
	storyIdx := strings.Index(got, "Release story:")
	guardrailIdx := strings.Index(got, "background context only")
	if !(titleIdx < storyIdx && storyIdx < guardrailIdx) {
		t.Fatalf("unexpected line order: %q", got)
	}
}

func TestReleaseContextInstructionsStoryOnlyGuardrailAlwaysPresent(t *testing.T) {
	got := releaseContextInstructions("", "Only a story, no title.")
	if strings.Contains(got, "Release title:") {
		t.Fatalf("title line present with no title: %q", got)
	}
	if !strings.Contains(got, "Release story: Only a story, no title.") {
		t.Fatalf("missing story line: %q", got)
	}
	if !strings.Contains(got, "Do not copy, summarize, quote, or invent dialogue") {
		t.Fatalf("missing fabrication guardrail: %q", got)
	}
}

func TestReleaseContextInstructionsPreservesEmbeddedNewlinesWithoutBreakingStructure(t *testing.T) {
	got := releaseContextInstructions("Title", "Line one.\nLine two.")
	if !strings.Contains(got, "Line one.\nLine two.") {
		t.Fatalf("embedded newline in story was mangled: %q", got)
	}
	// The block must still end with the single-line guardrail sentence as the
	// final line, regardless of embedded newlines earlier in the story.
	lines := strings.Split(got, "\n")
	last := lines[len(lines)-1]
	if !strings.Contains(last, "spoken Japanese is authoritative") {
		t.Fatalf("guardrail sentence is not the final line: last=%q full=%q", last, got)
	}
}

func TestTranslationMemoryExcludesShortReactions(t *testing.T) {
	memory := NewTranslationMemory()
	memory.store("はい", "Yes")
	if _, ok := memory.lookup("はい"); ok {
		t.Fatal("short reaction was cached")
	}
	memory.store(" 今日は  晴れです ", "It is sunny today.")
	if got, ok := memory.lookup("今日は 晴れです"); !ok || got != "It is sunny today." {
		t.Fatalf("normalized exact memory lookup = %q, %v", got, ok)
	}
}

func TestContainsJapaneseScriptDetectsHiraganaKatakanaKanji(t *testing.T) {
	cases := []struct {
		name string
		text string
		want bool
	}{
		{"hiragana", "こんにちは", true},
		{"katakana", "アイウエオ", true},
		{"kanji", "先生", true},
		{"mixed leak", "Hello こんにちは", true},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := containsJapaneseScript(tc.text); got != tc.want {
				t.Fatalf("containsJapaneseScript(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

func TestContainsJapaneseScriptIgnoresCleanEnglish(t *testing.T) {
	cases := []string{
		"Hello, world!",
		"Spandexer-san said goodbye.",
		"Room 42B, level III",
		"MVSD-610",
		"café naïve résumé",
	}
	for _, text := range cases {
		if containsJapaneseScript(text) {
			t.Fatalf("containsJapaneseScript(%q) = true, want false (no Japanese script, digits/uppercase must not false-positive)", text)
		}
	}
}

func newRepairTestRunner(baseURL string, mixedScriptQA bool) *Runner {
	return &Runner{
		cfg: config.Config{Translation: config.TranslationConfig{
			Mode: "contextual", BatchSize: 24, BaseURL: baseURL, Model: "test-model",
			ContextGapMS: 8000, MixedScriptQA: mixedScriptQA,
		}},
		client: &http.Client{},
		log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func repairTestSegments() []subtitle.Segment {
	return []subtitle.Segment{
		{StartMS: 0, EndMS: 1000, Text: "こんにちは"},
		{StartMS: 20_000, EndMS: 21_000, Text: "さようなら"},
	}
}

func TestRepairMixedScriptLeaksFixesFlaggedRowsOnly(t *testing.T) {
	server, calls := newFakeTranslationServer(t, func(call fakeTranslationCall) map[int]string {
		if isRepairCall(call) {
			if len(call.IDs) != 1 || call.IDs[0] != 0 {
				t.Fatalf("repair call requested unexpected rows: %v (clean row must never be resent)", call.IDs)
			}
			return map[int]string{0: "Hello"}
		}
		return map[int]string{0: "こんにちは leaked", 1: "Goodbye"}
	})
	runner := newRepairTestRunner(server.URL, true)

	out, usage, err := runner.translate(context.Background(), repairTestSegments(), func(string, int, string) {}, NewTranslationMemory())
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out[0].Text != "Hello" || out[1].Text != "Goodbye" {
		t.Fatalf("unexpected output: %#v", out)
	}
	if usage.RowsWithJapaneseScript != 1 || usage.RowsRepaired != 1 {
		t.Fatalf("usage = %#v, want RowsWithJapaneseScript=1 RowsRepaired=1", usage)
	}
	if len(*calls) != 2 {
		t.Fatalf("call count = %d, want 2 (one main batch, one repair batch)", len(*calls))
	}
}

func TestRepairMixedScriptLeaksKeepsOriginalWhenRepairStillLeaks(t *testing.T) {
	const leaked = "こんにちは leaked"
	server, _ := newFakeTranslationServer(t, func(call fakeTranslationCall) map[int]string {
		if isRepairCall(call) {
			return map[int]string{0: "まだ leaked"}
		}
		return map[int]string{0: leaked, 1: "Goodbye"}
	})
	runner := newRepairTestRunner(server.URL, true)

	out, usage, err := runner.translate(context.Background(), repairTestSegments(), func(string, int, string) {}, NewTranslationMemory())
	if err != nil {
		t.Fatal(err)
	}
	if out[0].Text != leaked {
		t.Fatalf("out[0].Text = %q, want original leaked text %q preserved (do not corrupt output)", out[0].Text, leaked)
	}
	if usage.RowsWithJapaneseScript != 1 || usage.RowsRepaired != 0 {
		t.Fatalf("usage = %#v, want RowsWithJapaneseScript=1 RowsRepaired=0", usage)
	}
}

func TestRepairMixedScriptLeaksSkippedWhenDisabled(t *testing.T) {
	server, calls := newFakeTranslationServer(t, func(call fakeTranslationCall) map[int]string {
		return map[int]string{0: "こんにちは leaked", 1: "Goodbye"}
	})
	runner := newRepairTestRunner(server.URL, false)

	out, usage, err := runner.translate(context.Background(), repairTestSegments(), func(string, int, string) {}, NewTranslationMemory())
	if err != nil {
		t.Fatal(err)
	}
	if out[0].Text != "こんにちは leaked" {
		t.Fatalf("out[0].Text = %q, want the leaked text left untouched when QA is disabled", out[0].Text)
	}
	if usage.RowsWithJapaneseScript != 0 || usage.RowsRepaired != 0 {
		t.Fatalf("usage = %#v, want zero (disabled QA never even detects)", usage)
	}
	if len(*calls) != 1 {
		t.Fatalf("call count = %d, want 1 (no repair call when disabled)", len(*calls))
	}
}

func TestRepairMixedScriptLeaksNoOpWhenNothingFlagged(t *testing.T) {
	server, calls := newFakeTranslationServer(t, func(call fakeTranslationCall) map[int]string {
		return map[int]string{0: "Hello", 1: "Goodbye"}
	})
	runner := newRepairTestRunner(server.URL, true)

	out, usage, err := runner.translate(context.Background(), repairTestSegments(), func(string, int, string) {}, NewTranslationMemory())
	if err != nil {
		t.Fatal(err)
	}
	if out[0].Text != "Hello" || out[1].Text != "Goodbye" {
		t.Fatalf("unexpected output: %#v", out)
	}
	if usage.RowsWithJapaneseScript != 0 || usage.RowsRepaired != 0 {
		t.Fatalf("usage = %#v, want zero", usage)
	}
	if len(*calls) != 1 {
		t.Fatalf("call count = %d, want 1 (no repair call when nothing is flagged)", len(*calls))
	}
}

func TestContextualTranslationSuppressesPunctuationBeforeAPI(t *testing.T) {
	runner := &Runner{
		cfg: config.Config{Translation: config.TranslationConfig{Mode: "contextual", BatchSize: 24}},
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	out, usage, err := runner.translate(
		context.Background(),
		[]subtitle.Segment{{StartMS: 0, EndMS: 30_000, Text: "。"}},
		func(string, int, string) {},
		NewTranslationMemory(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 || usage.Batches != 0 || usage.TotalTokens != 0 {
		t.Fatalf("punctuation reached translation path: out=%#v usage=%#v", out, usage)
	}
}

func TestGlossaryTargetsUsedInMatchesOnlyPresentSources(t *testing.T) {
	index := newStructuredGlossaryIndex(&config.StructuredGlossary{Terms: map[string]string{
		"お姉ちゃん": "big sis",
		"先生":    "teacher",
	}})
	targets := glossaryTargetsUsedIn(index, "お姉ちゃん、待って")
	if len(targets) != 1 || targets[0] != "big sis" {
		t.Fatalf("targets = %v, want [big sis]", targets)
	}
}

func TestCollectJobLocalTermsDedupesAndCombinesSources(t *testing.T) {
	index := newStructuredGlossaryIndex(&config.StructuredGlossary{Terms: map[string]string{
		"お姉ちゃん": "Moon Angel",
	}})
	memory := NewTranslationMemory()
	memory.store("今日は 晴れです", "Moon Angel")     // same term as the glossary match; must collapse to one
	memory.store("さようなら みんな", "Crimson Order") // a second, distinct accepted translation-memory form
	source := []subtitle.Segment{{Text: "お姉ちゃん、待って"}}

	terms := collectJobLocalTerms(source, index, memory)
	seen := make(map[string]int, len(terms))
	for _, term := range terms {
		seen[strings.ToLower(term)]++
	}
	if seen["moon angel"] != 1 {
		t.Fatalf("terms = %v, want exactly one case-insensitive occurrence of 'Moon Angel'", terms)
	}
	if seen["crimson order"] != 1 {
		t.Fatalf("terms = %v, want 'Crimson Order' present once", terms)
	}
}

func TestRepairMixedScriptLeaksIncludesJobLocalTermsInPrompt(t *testing.T) {
	var repairSystem string
	server, _ := newFakeTranslationServer(t, func(call fakeTranslationCall) map[int]string {
		if isRepairCall(call) {
			repairSystem = call.System
			return map[int]string{0: "Hello"}
		}
		return map[int]string{0: "こんにちは leaked", 1: "Goodbye"}
	})
	runner := newRepairTestRunner(server.URL, true)
	memory := NewTranslationMemory()
	memory.store("さようなら みんな", "Moon Angel")

	if _, _, err := runner.translate(context.Background(), repairTestSegments(), func(string, int, string) {}, memory); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(repairSystem, "Known proper names/terms already established for this file") || !strings.Contains(repairSystem, "Moon Angel") {
		t.Fatalf("repair system prompt missing job-local terms block: %q", repairSystem)
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
