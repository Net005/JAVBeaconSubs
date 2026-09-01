package config

import "testing"

func TestDefaultASRUsesBoundedReazonSpeech(t *testing.T) {
	value := defaults().Whisper
	if value.Backend != "reazon" || value.ReazonModel == "" || value.ChunkSeconds != 45 || value.OverlapSeconds != 2 || value.MaxSegmentSec != 60 || !value.FallbackWhisper {
		t.Fatalf("unexpected ASR defaults: %#v", value)
	}
}

func TestNormalizeStructuredGlossaryRemovesBlanksAndDuplicates(t *testing.T) {
	value := TranslationConfig{
		Mode: "direct",
		StructuredGlossary: &StructuredGlossary{
			Style: []string{"Keep honorifics", "", " Keep honorifics "},
			Terms: map[string]string{"先生": "teacher", " 先生 ": "teacher", "": "ignored", "空": ""},
		},
	}
	if err := NormalizeTranslation(&value); err != nil {
		t.Fatal(err)
	}
	if len(value.StructuredGlossary.Style) != 1 || len(value.StructuredGlossary.Terms) != 1 || value.StructuredGlossary.Terms["先生"] != "teacher" {
		t.Fatalf("normalized glossary = %#v", value.StructuredGlossary)
	}
}

func TestNormalizeStructuredGlossaryRejectsConflictingTrimmedSources(t *testing.T) {
	value := TranslationConfig{Mode: "direct", StructuredGlossary: &StructuredGlossary{Terms: map[string]string{"先生": "teacher", " 先生 ": "instructor"}}}
	if err := NormalizeTranslation(&value); err == nil {
		t.Fatal("conflicting duplicate source was accepted")
	}
}
