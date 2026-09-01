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
