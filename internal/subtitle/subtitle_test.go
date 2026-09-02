package subtitle

import (
	"strings"
	"testing"
)

func TestCleanRemovesOnlyOverlappingDuplicates(t *testing.T) {
	in := []Segment{
		{StartMS: 0, EndMS: 1000, Text: "  こんにちは。 "},
		{StartMS: 900, EndMS: 1600, Text: "こんにちは"},
		{StartMS: 5000, EndMS: 5500, Text: "こんにちは"},
		{StartMS: 6000, EndMS: 7000, Text: "[Music]"},
	}
	out := Clean(in)
	if len(out) != 2 {
		t.Fatalf("got %d segments, want 2: %#v", len(out), out)
	}
	if out[0].EndMS != 1600 {
		t.Fatalf("duplicate duration was not merged: %#v", out[0])
	}
}

func TestHasMeaningfulTranscriptAndCleanSuppressFormattingOnlyRows(t *testing.T) {
	for _, value := range []string{"。", "、。", "...", "「」", "()", "　", "。\n"} {
		if HasMeaningfulTranscript(value) {
			t.Errorf("%q was treated as meaningful", value)
		}
	}
	for _, value := range []string{"あ", "はい", "A", "NHK", "123"} {
		if !HasMeaningfulTranscript(value) {
			t.Errorf("%q was rejected", value)
		}
	}
	cleaned := Clean([]Segment{
		{StartMS: 0, EndMS: 30_000, Text: "。"},
		{StartMS: 31_000, EndMS: 32_000, Text: "あ"},
	})
	if len(cleaned) != 1 || cleaned[0].Text != "あ" {
		t.Fatalf("Clean retained formatting-only ASR output: %#v", cleaned)
	}
}

func TestRenderSRT(t *testing.T) {
	got := RenderSRT([]Segment{{StartMS: 1234, EndMS: 3661001, Text: "A deliberately long subtitle line that should wrap cleanly"}}, 30, 2)
	for _, want := range []string{"1\n00:00:01,234 --> 01:01:01,001", "\n\n"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
	// A custom line before cue 1 was confirmed to prevent Haruna from loading
	// generated subtitles. Keep the serialized payload strictly conventional.
	if !strings.HasPrefix(got, "1\n") || strings.Contains(got, "; generated-by=javbeaconsubs-v1") {
		t.Fatalf("SRT contains a non-standard header: %q", got)
	}
}

func TestRenderSRTPreservesTrustworthyTiming(t *testing.T) {
	got := RenderSRT([]Segment{
		{StartMS: 2000, EndMS: 4000, Text: "Second"},
		{StartMS: 1000, EndMS: 2500, Text: "First"},
	}, 40, 2)
	if !strings.Contains(got, "00:00:01,000 --> 00:00:02,500") ||
		!strings.Contains(got, "00:00:02,000 --> 00:00:04,000") {
		t.Fatalf("serializer altered aligned timestamps: %q", got)
	}
}

func TestRenderASS(t *testing.T) {
	got := RenderASS([]Segment{{StartMS: 1234, EndMS: 3661001, Text: "First line\nSecond line"}}, 30, 2, "Example")
	for _, want := range []string{"[Script Info]", "[V4+ Styles]", "Dialogue: 0,0:00:01.23,1:01:01.00", `First line\NSecond line`} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
}
