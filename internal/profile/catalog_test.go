package profile

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestBundledRecognitionVocabularyValidatesAndScopes(t *testing.T) {
	value, err := LoadRecognition(filepath.Join("..", "..", "vocabulary", "javbeaconsubs_japanese_recognition_vocabulary_v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(value.Scopes["global"]) < 500 || len(value.Scopes["jav"]) < 150 || len(value.Scopes["giga"]) < 690 {
		t.Fatalf("unexpected bundled vocabulary sizes: global=%d jav=%d giga=%d", len(value.Scopes["global"]), len(value.Scopes["jav"]), len(value.Scopes["giga"]))
	}
	if len(value.Profiles) == 0 || len(value.Activation) == 0 || len(value.Rules) == 0 || len(value.ObservedButNotPromoted) == 0 {
		t.Fatal("recognition vocabulary metadata was not preserved")
	}
	roundTrip, err := json.Marshal(value)
	if err != nil || !json.Valid(roundTrip) {
		t.Fatalf("marshal recognition vocabulary: %v", err)
	}
	terms, scopes := value.Terms("tokusatsu", "", 24)
	if len(terms) != 24 || len(scopes) != 2 || scopes[1] != "giga" {
		t.Fatalf("terms=%d scopes=%v", len(terms), scopes)
	}
}

func TestBundledTranslationGlossaryValidatesAndResolvesOverrides(t *testing.T) {
	value, err := LoadTranslationGlossary(filepath.Join("..", "..", "javbeaconsubs_translation_glossary_v2.json"))
	if err != nil {
		t.Fatal(err)
	}
	resolved, scopes := value.Resolve("akiba", "")
	if len(resolved.Terms) < 1000 || len(scopes) != 2 || scopes[1] != "giga" {
		t.Fatalf("terms=%d scopes=%v", len(resolved.Terms), scopes)
	}
	if len(value.Precedence) == 0 || len(value.Profiles) == 0 || len(value.ImportRules) == 0 {
		t.Fatal("translation glossary metadata was not preserved")
	}
}

func TestResolveOverrideKeyCatalogIDWinsOverReleaseTitle(t *testing.T) {
	value := TranslationGlossary{TitleOrSeriesOverrides: TranslationOverrides{
		"ADN-803":       {{Japanese: "先生", English: "instructor"}},
		"Release Title": {{Japanese: "先生", English: "teacher"}},
	}}
	if got := value.ResolveOverrideKey("ADN-803", "Release Title"); got != "ADN-803" {
		t.Fatalf("ResolveOverrideKey = %q, want catalog ID to win", got)
	}
}

func TestResolveOverrideKeyFallsBackToReleaseTitle(t *testing.T) {
	value := TranslationGlossary{TitleOrSeriesOverrides: TranslationOverrides{
		"Release Title": {{Japanese: "先生", English: "teacher"}},
	}}
	if got := value.ResolveOverrideKey("ADN-803", "Release Title"); got != "Release Title" {
		t.Fatalf("ResolveOverrideKey = %q, want release title fallback", got)
	}
	if got := value.ResolveOverrideKey("", "Release Title"); got != "Release Title" {
		t.Fatalf("ResolveOverrideKey with empty catalog ID = %q, want release title fallback", got)
	}
}

func TestResolveOverrideKeyReturnsEmptyWhenNeitherMatches(t *testing.T) {
	value := TranslationGlossary{TitleOrSeriesOverrides: TranslationOverrides{
		"Some Other Title": {{Japanese: "先生", English: "teacher"}},
	}}
	if got := value.ResolveOverrideKey("ADN-803", "Release Title"); got != "" {
		t.Fatalf("ResolveOverrideKey = %q, want empty", got)
	}
	if got := value.ResolveOverrideKey("", ""); got != "" {
		t.Fatalf("ResolveOverrideKey with both empty = %q, want empty", got)
	}
	if got := value.ResolveOverrideKey("  ", "  "); got != "" {
		t.Fatalf("ResolveOverrideKey with whitespace-only = %q, want empty", got)
	}
}

func TestSameScopeDuplicatesRejectedButCrossScopeOverridesAllowed(t *testing.T) {
	value := TranslationGlossary{Format: GlossaryFormat, Version: GlossaryVersion, Scopes: map[string][]TranslationTerm{
		"global": {{Japanese: "先生", English: "teacher"}},
		"jav":    {{Japanese: "先生", English: "instructor"}},
		"giga":   {},
	}}
	if err := ValidateTranslationGlossary(value); err != nil {
		t.Fatal(err)
	}
	value.Scopes["global"] = append(value.Scopes["global"], TranslationTerm{Japanese: "先生", English: "sensei"})
	if err := ValidateTranslationGlossary(value); err == nil {
		t.Fatal("same-scope duplicate was accepted")
	}
}
