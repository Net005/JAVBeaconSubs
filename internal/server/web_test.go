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
