package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"

	"javbeaconsubs/internal/config"
	"javbeaconsubs/internal/subtitle"
)

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
