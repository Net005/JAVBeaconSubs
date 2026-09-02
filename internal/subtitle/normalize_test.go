package subtitle

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestWrapTwoLinesBalancesLineLengths verifies the line-break metric picks
// the whitespace boundary that minimizes the difference in visible length
// between the two resulting lines, rather than the boundary closest to the
// raw rune-index midpoint (which can leave one very short line next to a
// much longer one for uneven word lengths).
func TestWrapTwoLinesBalancesLineLengths(t *testing.T) {
	// "This is a" (9) / "surprisingly long single word right here" (41) is
	// the closer-to-midpoint split by raw index; "This is a surprisingly"
	// (22) / "long single word right here" (27) is far more balanced and
	// must be preferred.
	text := "This is a surprisingly long single word right here"
	got := wrapTwoLines(text, DefaultNormalizeOptions())
	lines := strings.Split(got, "\n")
	if len(lines) != 2 {
		t.Fatalf("expected two lines, got %q", got)
	}
	left, right := visibleCharacters(lines[0]), visibleCharacters(lines[1])
	if d := abs(left - right); d > 6 {
		t.Fatalf("line lengths not balanced: %q (left=%d, right=%d, diff=%d)", got, left, right, d)
	}
	if compactText(got) != compactText(text) {
		t.Fatalf("wrapping lost or duplicated text:\nwant %q\n got %q", text, got)
	}
}

func TestNormalizeShortCueIsStable(t *testing.T) {
	input := []Segment{{StartMS: 1000, EndMS: 4000, Text: "Hello there."}}
	got, changes, err := NormalizeSubtitles(input, DefaultNormalizeOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].StartMS != input[0].StartMS || got[0].EndMS != input[0].EndMS || got[0].Text != input[0].Text {
		t.Fatalf("normal cue changed: %#v", got)
	}
	if len(changes) != 0 {
		t.Fatalf("normal cue reported changes: %#v", changes)
	}
}

func TestNormalizeOversizedCueUsesOnlyAlignmentAnchors(t *testing.T) {
	text := "But the genius Contra's mind has exposed everything. Their one weakness is that the waves emitted by this terrarium ore will reduce even that tough body to a fragile lump of flesh. The target is the eldest daughter, Moon Angel. With this special high-speed tool, I'll steal her power and shatter her pride."
	source := Segment{
		StartMS: 134510, EndMS: 162550, Text: text,
		TimingAnchors: []int64{139000, 144500, 150200, 156100},
	}
	got, changes, err := NormalizeSubtitles([]Segment{source}, DefaultNormalizeOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) < 3 {
		t.Fatalf("oversized aligned cue was not split: %#v", got)
	}
	if len(changes) != 1 || changes[0].Method != "alignment_anchors" {
		t.Fatalf("expected alignment_anchors method when real anchors exist: %#v", changes)
	}
	if got[0].StartMS != source.StartMS || got[len(got)-1].EndMS != source.EndMS {
		t.Fatalf("timing envelope changed: %#v", got)
	}
	anchors := map[int64]bool{}
	for _, value := range source.TimingAnchors {
		anchors[value] = true
	}
	for index, cue := range got {
		if cue.EndMS <= cue.StartMS || cue.StartMS < source.StartMS || cue.EndMS > source.EndMS {
			t.Fatalf("invalid child timing: %#v", cue)
		}
		if index < len(got)-1 && !anchors[cue.EndMS] {
			t.Fatalf("invented timing boundary %d", cue.EndMS)
		}
		if strings.Count(cue.Text, "\n") > 1 {
			t.Fatalf("cue exceeds two display lines: %q", cue.Text)
		}
	}
	if compactText(joinCueText(got)) != compactText(text) {
		t.Fatalf("normalization lost or duplicated text:\nwant %q\n got %q", text, joinCueText(got))
	}
}

func TestNormalizeWithoutAlignmentAnchorsFallsBackToProportionalSplit(t *testing.T) {
	// Without trustworthy alignment anchors the normalizer must not leave one
	// oversized cue on screen: it falls back to proportional redistribution
	// of the existing timing envelope (the last-resort timing source).
	text := strings.Repeat("A long translated phrase with no trustworthy timing anchor. ", 5)
	source := Segment{StartMS: 1000, EndMS: 29000, Text: text}
	got, changes, err := NormalizeSubtitles([]Segment{source}, DefaultNormalizeOptions())
	if err != nil {
		t.Fatal(err)
	}
	assertCleanSplit(t, source, got)
	if len(changes) != 1 || changes[0].OutputCues != len(got) || changes[0].Skipped != "" || changes[0].Method != "proportional_fallback" {
		t.Fatalf("missing proportional-fallback diagnostic: %#v", changes)
	}
	if compactText(joinCueText(got)) != compactText(text) {
		t.Fatal("text changed while using the proportional timing fallback")
	}
}

// TestNormalizeADN803RegressionSplitsOversizedCueWithoutAnchors is a
// regression fixture for the ADN-803 English track, where a 28.42s cue with
// no alignment anchors previously survived normalization as one giant block
// that rendered as a four-line subtitle wall in Haruna.
func TestNormalizeADN803RegressionSplitsOversizedCueWithoutAnchors(t *testing.T) {
	text := "I saw Toyama on a security camera near my house, so... Ah, I saw it on the news and was going to ask you about it, but they still haven't found her. The police came by the other day and asked me all sorts of questions, but unfortunately, I don't know anything. Sorry I couldn't be of help."
	source := Segment{StartMS: 3_086_860, EndMS: 3_115_280, Text: text}
	got, changes, err := NormalizeSubtitles([]Segment{source}, DefaultNormalizeOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) < 2 {
		t.Fatalf("ADN-803 regression: oversized cue survived as one giant block: %#v", got)
	}
	assertCleanSplit(t, source, got)
	if len(changes) != 1 || changes[0].OutputCues != len(got) {
		t.Fatalf("ADN-803 regression: missing normalization diagnostic: %#v", changes)
	}
	if compactText(joinCueText(got)) != compactText(text) {
		t.Fatalf("ADN-803 regression: translated text was lost or duplicated:\nwant %q\n got %q", text, joinCueText(got))
	}
	// The source text contains "so... Ah, I saw it on the news", the exact
	// pattern that previously produced an orphaned ".." fragment ("658: ...
	// so." / "659: ..  Ah, ..."). Every child cue must keep the ellipsis
	// attached to the phrase it belongs to.
	for index, cue := range got {
		if hasOrphanLeadingEllipsis(cue.Text) {
			t.Fatalf("ADN-803 regression: cue %d begins with an orphaned ellipsis fragment: %q", index, cue.Text)
		}
	}
}

// assertCleanSplit checks the timing invariants every normalized split must
// hold regardless of which timing source produced it: the envelope is
// preserved, children are contiguous with no gaps, overlap, or invalid
// timestamps, and no cue renders as more than two lines.
func assertCleanSplit(t *testing.T, source Segment, got []Segment) {
	t.Helper()
	if len(got) == 0 {
		t.Fatal("normalization produced no cues")
	}
	if got[0].StartMS != source.StartMS || got[len(got)-1].EndMS != source.EndMS {
		t.Fatalf("timing envelope changed: %#v", got)
	}
	for index, cue := range got {
		if cue.EndMS <= cue.StartMS || cue.StartMS < source.StartMS || cue.EndMS > source.EndMS {
			t.Fatalf("invalid child timing: %#v", cue)
		}
		if index > 0 && cue.StartMS != got[index-1].EndMS {
			t.Fatalf("child cues are not contiguous (gap or overlap): %#v", got)
		}
		if strings.Count(cue.Text, "\n") > 1 {
			t.Fatalf("cue exceeds two display lines: %q", cue.Text)
		}
		if hasOrphanLeadingEllipsis(cue.Text) {
			t.Fatalf("cue %d begins with an orphaned ellipsis fragment: %q", index, cue.Text)
		}
	}
}

// TestNeedsSplitIsDensityAwareNotDurationAlone confirms the "do not chase
// duration alone" rule: needsSplit must key off text density (visible
// character count relative to line/cue targets), not raw cue duration. A
// sparse vocalization must be able to sit on screen for a long time without
// being split, and dense text must split even when its window is short.
func TestNeedsSplitIsDensityAwareNotDurationAlone(t *testing.T) {
	options := DefaultNormalizeOptions()
	cases := []struct {
		name     string
		text     string
		duration int64
		want     bool
	}{
		{
			name:     "short_vocalization_long_duration_does_not_split",
			text:     "Yeah.",
			duration: 20000,
			want:     false,
		},
		{
			name:     "at_target_boundary_long_duration_does_not_split",
			text:     strings.Repeat("a", options.TargetCharsPerCue),
			duration: 30000,
			want:     false,
		},
		{
			name:     "short_duration_sparse_text_does_not_split",
			text:     "word word word",
			duration: 1500,
			want:     false,
		},
		{
			name:     "dense_text_splits_even_with_short_duration",
			text:     strings.Repeat("a very dense phrase with plenty of words ", 3),
			duration: 3000,
			want:     true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := needsSplit(collapseWhitespace(tc.text), tc.duration, options); got != tc.want {
				t.Fatalf("needsSplit(%q, %dms) = %v, want %v", tc.text, tc.duration, got, tc.want)
			}
		})
	}
}

// TestNormalizeProportionalFallbackOnlyUsedWithoutAnchors confirms the
// timing-source priority order stays intact: real forced-alignment anchors
// are always preferred, and proportional redistribution is used only when
// none exist, never as a default segmentation method.
func TestNormalizeProportionalFallbackOnlyUsedWithoutAnchors(t *testing.T) {
	text := "But the genius Contra's mind has exposed everything. Their one weakness is that the waves emitted by this terrarium ore will reduce even that tough body to a fragile lump of flesh. The target is the eldest daughter, Moon Angel. With this special high-speed tool, I'll steal her power and shatter her pride."

	withAnchors := Segment{
		StartMS: 134510, EndMS: 162550, Text: text,
		TimingAnchors: []int64{139000, 144500, 150200, 156100},
	}
	got, changes, err := NormalizeSubtitles([]Segment{withAnchors}, DefaultNormalizeOptions())
	if err != nil {
		t.Fatal(err)
	}
	assertCleanSplit(t, withAnchors, got)
	if len(changes) != 1 || changes[0].Method != "alignment_anchors" {
		t.Fatalf("expected alignment_anchors when anchors exist: %#v", changes)
	}

	withoutAnchors := Segment{StartMS: 134510, EndMS: 162550, Text: text}
	got, changes, err = NormalizeSubtitles([]Segment{withoutAnchors}, DefaultNormalizeOptions())
	if err != nil {
		t.Fatal(err)
	}
	assertCleanSplit(t, withoutAnchors, got)
	if len(changes) != 1 || changes[0].Method != "proportional_fallback" {
		t.Fatalf("expected proportional_fallback only once anchors are absent: %#v", changes)
	}
}

func TestNormalizeVeryShortWindowDoesNotCreateMicroCues(t *testing.T) {
	source := Segment{
		StartMS: 1000, EndMS: 1600,
		Text:          strings.Repeat("A long phrase that cannot physically fit its source window. ", 4),
		TimingAnchors: []int64{1150, 1300, 1450},
	}
	got, changes, err := NormalizeSubtitles([]Segment{source}, DefaultNormalizeOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].StartMS != source.StartMS || got[0].EndMS != source.EndMS {
		t.Fatalf("short source timing was overridden: %#v", got)
	}
	if len(changes) != 1 || changes[0].Skipped != "insufficient aligned duration" {
		t.Fatalf("missing short-window diagnostic: %#v", changes)
	}
}

func TestNormalizeLongSentenceDoesNotBreakEnglishWords(t *testing.T) {
	text := "This deliberately long sentence contains ordinary English words and should split at whitespace boundaries without corrupting vocabulary even when there is no sentence-ending punctuation available"
	source := Segment{StartMS: 0, EndMS: 14000, Text: text, TimingAnchors: []int64{4500, 9000}}
	got, _, err := NormalizeSubtitles([]Segment{source}, DefaultNormalizeOptions())
	if err != nil {
		t.Fatal(err)
	}
	if compactText(joinCueText(got)) != compactText(text) {
		t.Fatalf("words were lost or split: %#v", got)
	}
}

func TestNormalizeUnicodeRemainsValid(t *testing.T) {
	text := "これは長い日本語の台詞です。L’été d’Anaïs isn’t simple！これは二番目の自然な文章です。"
	source := Segment{StartMS: 2000, EndMS: 12000, Text: text, TimingAnchors: []int64{6000, 9000}}
	got, _, err := NormalizeSubtitles([]Segment{source}, DefaultNormalizeOptions())
	if err != nil {
		t.Fatal(err)
	}
	for _, cue := range got {
		if !utf8.ValidString(cue.Text) {
			t.Fatalf("invalid UTF-8: %q", cue.Text)
		}
	}
	if compactText(joinCueText(got)) != compactText(text) {
		t.Fatalf("Unicode text changed: %#v", got)
	}
}

// TestNormalizeEllipsisBoundariesNeverOrphanPunctuation exercises the
// punctuation-boundary rules from the "fix orphan ellipsis ownership" TODO:
// ellipses (ASCII "...", ".." and the Unicode "…") must stay attached to the
// phrase that precedes them, never left as a leading fragment on the next
// child cue, regardless of dot count or nearby exclamation/question marks.
func TestNormalizeEllipsisBoundariesNeverOrphanPunctuation(t *testing.T) {
	cases := []struct {
		name string
		text string
	}{
		{
			name: "three_dot_ellipsis_then_interjection",
			text: "Tohyama showed up on a nearby security camera, so... Ah, I saw it on the news and was going to ask you about it, but they still haven't found her.",
		},
		{
			name: "two_dot_ellipsis_then_interjection",
			text: "Tohyama showed up on a nearby security camera, so.. Ah, I saw it on the news and was going to ask you about it, but they still haven't found her.",
		},
		{
			name: "unicode_ellipsis_then_interjection",
			text: "Tohyama showed up on a nearby security camera, so… Ah, I saw it on the news and was going to ask you about it, but they still haven't found her.",
		},
		{
			name: "ellipsis_mid_sentence",
			text: "Something happened while I was walking home from the store... Ah, I remember now, it was raining that entire evening and nobody else was outside.",
		},
		{
			name: "unicode_ellipsis_mid_sentence",
			text: "Wait… what are you doing here so late at night, I thought you were supposed to be at the office finishing the report.",
		},
		{
			name: "hesitation_then_question",
			text: "I don't know what to say about all of this, wait... what happened to the money we were saving for the trip next summer.",
		},
		{
			name: "repeated_ellipses",
			text: "Ah... ah... yes... that is exactly what I told the officer when he came to ask me about it the other night after dinner.",
		},
		{
			name: "exclamation_question_combo",
			text: "Really?! What happened next, I need to know everything about it because I was not there when it actually took place yesterday.",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			source := Segment{StartMS: 0, EndMS: 20000, Text: tc.text}
			got, _, err := NormalizeSubtitles([]Segment{source}, DefaultNormalizeOptions())
			if err != nil {
				t.Fatal(err)
			}
			assertCleanSplit(t, source, got)
			if compactText(joinCueText(got)) != compactText(tc.text) {
				t.Fatalf("text lost or duplicated:\nwant %q\n got %q", tc.text, joinCueText(got))
			}
		})
	}
}

// firstVisibleLine returns a cue's first display line: a splitting artifact
// always shows up at the very start of what the viewer sees.
func firstVisibleLine(text string) string {
	if index := strings.IndexByte(text, '\n'); index >= 0 {
		return text[:index]
	}
	return text
}

// hasOrphanLeadingEllipsis reports whether text begins with a lone "." or
// ".." left behind by a boundary that cut through the middle of a "..."
// run. A complete ellipsis ("..." or the single "…" rune) legitimately
// opening a cue is not an orphan.
func hasOrphanLeadingEllipsis(text string) bool {
	trimmed := strings.TrimLeft(firstVisibleLine(text), " \t")
	dots := 0
	for dots < len(trimmed) && trimmed[dots] == '.' {
		dots++
	}
	return dots == 1 || dots == 2
}

func joinCueText(cues []Segment) string {
	parts := make([]string, len(cues))
	for index, cue := range cues {
		parts[index] = cue.Text
	}
	return strings.Join(parts, " ")
}

func compactText(value string) string {
	return strings.Join(strings.Fields(value), "")
}
