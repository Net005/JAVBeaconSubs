package server

import (
	"strings"
	"testing"
)

func TestWebUIUsesExplicitExternalIDLookup(t *testing.T) {
	html, err := assets.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(html)
	if strings.Contains(page, "external.value") {
		t.Fatal("web UI relies on window.external instead of the reference input")
	}
	if !strings.Contains(page, "el('externalId').value") {
		t.Fatal("web UI does not explicitly resolve the reference input")
	}
}

func TestWebUIProvidesValidatedLocalGlossaryImport(t *testing.T) {
	html, err := assets.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(html)
	for _, required := range []string{"translationTermsFile", "importTermFile", "parseTermMappings", "Conflicting translations", "Blank lines and identical duplicates are ignored"} {
		if !strings.Contains(page, required) {
			t.Fatalf("web glossary import is missing %q", required)
		}
	}
}

func TestWebUICanShowEveryGlossaryMapping(t *testing.T) {
	html, err := assets.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(html)
	for _, required := range []string{"translationTermsToggle", "Show all ${count} mappings", "editor.scrollTop=0", "editor.scrollHeight+'px'"} {
		if !strings.Contains(page, required) {
			t.Fatalf("web glossary overview is missing %q", required)
		}
	}
}

func TestWebUIExposesJapaneseSidecarAndOwnershipSetup(t *testing.T) {
	html, err := assets.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(html)
	for _, required := range []string{"keepJapanese", "keep_japanese", "Also keep Japanese", "PUID", "PGID", "GUID"} {
		if !strings.Contains(page, required) {
			t.Fatalf("web sidecar/setup UI is missing %q", required)
		}
	}
}

func TestWebUIExplainsPrimaryJapaneseASR(t *testing.T) {
	html, err := assets.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(html)
	for _, required := range []string{"Japanese speech recognition", "ReazonSpeech", "JAVBEACONSUBS_ASR_BACKEND", "validated fallback"} {
		if !strings.Contains(page, required) {
			t.Fatalf("web ASR setup is missing %q", required)
		}
	}
}
