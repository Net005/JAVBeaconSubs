package subtitle

import (
	"strings"
	"testing"
)

// denseJapaneseText returns exactly n meaningful Japanese runes, built from
// a repeated real phrase (rather than a single repeated character) so the
// fixture reads like a plausible dense transcript, not synthetic noise.
func denseJapaneseText(n int) string {
	const base = "非常に密度の高い日本語のテキストです。"
	baseLen := len([]rune(base))
	repeats := n/baseLen + 2
	runes := []rune(strings.Repeat(base, repeats))
	return string(runes[:n])
}

// TestDetectDensityAnomaliesFlagsExtremeCPS is the TODO Part 27 regression:
// a segment with ~50+ meaningful Japanese characters packed into ~1 second
// (55 chars/sec) must be flagged, while a normal-density segment must not.
func TestDetectDensityAnomaliesFlagsExtremeCPS(t *testing.T) {
	segments := []Segment{
		{StartMS: 0, EndMS: 1000, Text: denseJapaneseText(55)},
		{StartMS: 2000, EndMS: 5000, Text: "普通の文です"},
	}
	anomalies := DetectDensityAnomalies(segments, 40)
	if len(anomalies) != 1 {
		t.Fatalf("anomalies = %d, want 1: %#v", len(anomalies), anomalies)
	}
	if anomalies[0].Index != 0 {
		t.Fatalf("flagged index = %d, want 0 (the dense segment)", anomalies[0].Index)
	}
	if anomalies[0].Characters != 55 {
		t.Fatalf("characters = %d, want 55", anomalies[0].Characters)
	}
	if anomalies[0].CharsPerSecond <= 40 {
		t.Fatalf("chars_per_second = %v, want > 40", anomalies[0].CharsPerSecond)
	}
	if anomalies[0].StartMS != 0 || anomalies[0].EndMS != 1000 {
		t.Fatalf("anomaly timing = [%d,%d], want [0,1000] (detection must report, never alter, timing)", anomalies[0].StartMS, anomalies[0].EndMS)
	}
}

func TestDetectDensityAnomaliesDisabledAtZeroThreshold(t *testing.T) {
	segments := []Segment{{StartMS: 0, EndMS: 1000, Text: denseJapaneseText(100)}}
	if got := DetectDensityAnomalies(segments, 0); got != nil {
		t.Fatalf("DetectDensityAnomalies with threshold 0 = %#v, want nil (disabled)", got)
	}
	if got := DetectDensityAnomalies(segments, -5); got != nil {
		t.Fatalf("DetectDensityAnomalies with negative threshold = %#v, want nil (disabled)", got)
	}
}

// TestDetectDensityAnomaliesNeverModifiesInput proves the gap this feature
// closes: a short but extremely dense cue survives NormalizeSubtitles'
// own splitting logic completely unmodified (needsSplit only considers a
// cue for splitting when its total character count or its duration crosses
// a fixed ceiling - a 55-character, 1-second cue crosses neither), and
// DetectDensityAnomalies itself never mutates the segment it flags.
func TestDetectDensityAnomaliesNeverModifiesInput(t *testing.T) {
	input := []Segment{{StartMS: 0, EndMS: 1000, Text: denseJapaneseText(55)}}
	normalized, _, err := NormalizeSubtitles(input, DefaultNormalizeOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(normalized) != 1 {
		t.Fatalf("normalization split or dropped the dense cue: got %d segments, want 1 (this is exactly the gap density QA exists to catch)", len(normalized))
	}
	before := normalized[0]
	anomalies := DetectDensityAnomalies(normalized, 40)
	if len(anomalies) != 1 {
		t.Fatalf("anomalies = %d, want 1", len(anomalies))
	}
	after := normalized[0]
	if before.StartMS != after.StartMS || before.EndMS != after.EndMS || before.Text != after.Text {
		t.Fatalf("DetectDensityAnomalies modified the segment: before=%#v after=%#v", before, after)
	}
	// Normalization is still allowed to line-wrap the cue into two visible
	// lines (a newline is display formatting, not a content change); what
	// must never happen is a character being dropped, added, or the cue
	// being split into more than one segment or having its timing moved.
	if before.StartMS != 0 || before.EndMS != 1000 {
		t.Fatalf("normalization altered the source cue's timing: %#v", before)
	}
	if compactText(before.Text) != denseJapaneseText(55) {
		t.Fatalf("normalization altered the source cue's content: got %q, want (compact) %q", before.Text, denseJapaneseText(55))
	}
}
