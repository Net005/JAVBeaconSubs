package engine

import (
	"testing"

	"javbeaconsubs/internal/subtitle"
)

func TestDetectProperNameVariantsFlagsCloseSpellingAgainstKnownTerm(t *testing.T) {
	out := []subtitle.Segment{
		{Text: "Everyone waited for Moonlight to arrive."},
		{Text: "Then Moonligt appeared suddenly."},
	}
	variants := DetectProperNameVariants(out, []string{"Moonlight"})
	if len(variants) != 1 {
		t.Fatalf("variants = %#v, want exactly 1", variants)
	}
	v := variants[0]
	if v.RowIndex != 1 || v.Candidate != "Moonligt" || v.LikelyIntended != "Moonlight" || v.EditDistance != 1 {
		t.Fatalf("variant = %#v, want RowIndex=1 Candidate=Moonligt LikelyIntended=Moonlight EditDistance=1", v)
	}
}

func TestDetectProperNameVariantsIgnoresExactMatches(t *testing.T) {
	out := []subtitle.Segment{
		{Text: "Moonlight shone brightly over the harbor."},
	}
	variants := DetectProperNameVariants(out, []string{"Moonlight"})
	if len(variants) != 0 {
		t.Fatalf("variants = %#v, want none for an exact match", variants)
	}
}

func TestDetectProperNameVariantsIgnoresShortOrUnrelatedCandidates(t *testing.T) {
	out := []subtitle.Segment{
		{Text: "Erin walked away without a word."},   // candidate below the 5-char floor
		{Text: "Skyline stretched out before them."}, // candidate far from any known term
	}
	variants := DetectProperNameVariants(out, []string{"Moonlight"})
	if len(variants) != 0 {
		t.Fatalf("variants = %#v, want none (short candidate and unrelated candidate must both be ignored)", variants)
	}
}

func TestDetectProperNameVariantsSkipsCatalogCodesAndAcronyms(t *testing.T) {
	out := []subtitle.Segment{
		{Text: "Scene from ADN-803 is now playing."},
		{Text: "The MVSD release ships next week."},
		{Text: "Moonligt returned home at last."},
	}
	variants := DetectProperNameVariants(out, []string{"Moonlight"})
	if len(variants) != 1 || variants[0].Candidate != "Moonligt" {
		t.Fatalf("variants = %#v, want only the genuine near-miss 'Moonligt' (catalog codes and acronyms must never be candidates)", variants)
	}
}

func TestLevenshteinDistanceTableCases(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"abc", "", 3},
		{"", "abc", 3},
		{"abc", "abc", 0},
		{"kitten", "sitting", 3},
		{"moonlight", "moonligt", 1},
	}
	for _, tc := range cases {
		if got := levenshteinDistance(tc.a, tc.b); got != tc.want {
			t.Fatalf("levenshteinDistance(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}
