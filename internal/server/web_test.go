package server

import (
	"net/http"
	"net/http/httptest"
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

func TestEmbeddedLogoAssetIsServed(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/assets/javbeaconsubs-site-logo.png", nil)
	response := httptest.NewRecorder()
	(&Server{}).Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.HasPrefix(response.Header().Get("Content-Type"), "image/png") || response.Body.Len() < 1000 {
		t.Fatalf("logo response: status=%d type=%q bytes=%d", response.Code, response.Header().Get("Content-Type"), response.Body.Len())
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

func TestWebUIProvidesASSExportToggle(t *testing.T) {
	html, err := assets.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(html)
	for _, required := range []string{"writeAss", "write_ass", "Also export"} {
		if !strings.Contains(page, required) {
			t.Fatalf("web UI is missing the ASS export toggle %q", required)
		}
	}
}

func TestWebUIExplainsPrimaryJapaneseASR(t *testing.T) {
	html, err := assets.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(html)
	for _, required := range []string{"Japanese speech recognition", "Qwen3-ASR-1.7B", "ReazonSpeech", "Qwen3-ForcedAligner", "JAVBEACONSUBS_ASR_BACKEND", "High Accuracy"} {
		if !strings.Contains(page, required) {
			t.Fatalf("web ASR setup is missing %q", required)
		}
	}
}

func TestWebUIUsesJAVBeaconBrandAndNoPurpleTheme(t *testing.T) {
	html, err := assets.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(html)
	for _, required := range []string{"#8f1725", "#e44b59", "javbeaconsubs-site-logo.png", "favicon.ico", "apple-touch-icon.png"} {
		if !strings.Contains(page, required) {
			t.Fatalf("JAVBeacon theme is missing %q", required)
		}
	}
	for _, removed := range []string{"--violet", "#8d7cff", "#6554dd"} {
		if strings.Contains(page, removed) {
			t.Fatalf("old purple theme remains: %q", removed)
		}
	}
}

func TestWebUIShowsVersionAndProvenanceDownloads(t *testing.T) {
	html, err := assets.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(html)
	for _, required := range []string{"appVersion", "h.version", "en-provenance", "ja-provenance", "EN proof", "JA proof"} {
		if !strings.Contains(page, required) {
			t.Fatalf("version/provenance UI is missing %q", required)
		}
	}
}

func TestWebUIProvidesJAVBeaconAutoDetectionAndFreshLookupTests(t *testing.T) {
	html, err := assets.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(html)
	for _, required := range []string{
		"autoDetectRelease", "auto_detect_release", "javbeaconTestFilePath",
		"javbeaconTestVideoID", "javbeaconTestFilename",
		"clearJAVBeaconTestResults()", "Clear results", "stash_file_path",
	} {
		if !strings.Contains(page, required) {
			t.Fatalf("JAVBeacon auto-detection UI is missing %q", required)
		}
	}
}

func TestWebUIGroupsLargeJobDownloadsBySourceFile(t *testing.T) {
	html, err := assets.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(html)
	for _, required := range []string{
		"outputArtifactLinks", "output-list", "output-file-name",
		"output-file-path", "scrollable", "max-height:min(460px,52vh)",
		"flex:1 1 100%", "results.length>3", "::-webkit-scrollbar",
		"r.input||j.files?.[i]", "${i+1} / ${results.length}",
	} {
		if !strings.Contains(page, required) {
			t.Fatalf("per-file output list is missing %q", required)
		}
	}
}

func TestWebUIDisplaysFriendlyJobMetadataCards(t *testing.T) {
	html, err := assets.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(html)
	for _, required := range []string{
		"decorateJobMetadata", "job-meta-chip", "job-meta-label",
		"High Accuracy", "Path rule", "Recognition", "Content profile",
		"Total files", "Completed", "Scanned path", "commonOutputDirectory",
		"card.querySelector('.pill.complete,.pill.skipped')",
		"new MutationObserver(decorateJobMetadata)",
	} {
		if !strings.Contains(page, required) {
			t.Fatalf("friendly job metadata display is missing %q", required)
		}
	}
}
