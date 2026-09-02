package subtitle

import (
	"strings"
	"testing"
	"unicode/utf8"
)

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
	got, _, err := NormalizeSubtitles([]Segment{source}, DefaultNormalizeOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) < 3 {
		t.Fatalf("oversized aligned cue was not split: %#v", got)
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

func TestNormalizeWithoutAlignmentAnchorsNeverInventsTiming(t *testing.T) {
	text := strings.Repeat("A long translated phrase with no trustworthy timing anchor. ", 5)
	source := Segment{StartMS: 1000, EndMS: 29000, Text: text}
	got, changes, err := NormalizeSubtitles([]Segment{source}, DefaultNormalizeOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].StartMS != source.StartMS || got[0].EndMS != source.EndMS {
		t.Fatalf("normalizer invented timestamps without anchors: %#v", got)
	}
	if len(changes) != 1 || changes[0].Skipped != "no trustworthy alignment anchors" {
		t.Fatalf("missing explicit normalization diagnostic: %#v", changes)
	}
	if compactText(got[0].Text) != compactText(text) {
		t.Fatal("text changed while splitting was unavailable")
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
