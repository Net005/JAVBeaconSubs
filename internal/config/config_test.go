package config

import "testing"

func TestDefaultASRUsesQwenWithBoundedFallbacks(t *testing.T) {
	value := defaults().Whisper
	if value.Backend != "qwen" || value.Mode != "balanced" || value.Profile != "jav" || value.QwenPython == "" || value.QwenModel == "" || value.QwenRevision == "" || value.AlignerModel == "" || value.AlignerRevision == "" || value.ReazonPython == "" || value.ReazonBatchScript == "" || value.ReazonModel == "" || value.MaxSegmentSec != 30 || value.ReazonEnabled || !value.WhisperEnabled || value.WhisperDevice != "auto" || value.WhisperCPUTimeout != 7200 || value.Threads != 12 || value.BeamSize != 5 || value.BestOf != 5 || value.WhisperCUDAMinMB != 4096 || !value.GPUFallbackCPU {
		t.Fatalf("unexpected ASR defaults: %#v", value)
	}
}

func TestNormalizeProfileCanonicalizesLegacyAliases(t *testing.T) {
	for input, want := range map[string]string{"standard": "standard", "jav": "jav", "giga": "giga", "tokusatsu": "giga", "AKIBA": "giga"} {
		if got := NormalizeProfile(input); got != want {
			t.Fatalf("NormalizeProfile(%q) = %q, want %q", input, got, want)
		}
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

func TestSubtitleOutputDefaults(t *testing.T) {
	value := defaults().Output
	if !value.NormalizeSubtitles || value.TargetLineChars != 40 || value.MaxLineChars != 46 || value.MaxLines != 2 ||
		value.TargetCueChars != 80 || value.MaxCueDurationMS != 6000 || value.MinCueDurationMS != 1000 ||
		value.TargetCPS != 17 || !value.WriteProvenance || !value.WriteASS || value.ExtremeCPS != 40 {
		t.Fatalf("unexpected subtitle output defaults: %#v", value)
	}
}

func TestTranslationMixedScriptQADefaultsEnabled(t *testing.T) {
	if value := defaults().Translation; !value.MixedScriptQA {
		t.Fatalf("mixed_script_qa_enabled default = %v, want true", value.MixedScriptQA)
	}
}
