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
