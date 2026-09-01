package profile

import (
	"testing"

	"javbeaconsubs/internal/config"
)

func TestResolveUsesLongestCaseInsensitiveSlashNormalizedMapping(t *testing.T) {
	settings := config.ProfilesConfig{DefaultProfile: "standard", DefaultASRMode: "balanced", PathMappings: []config.PathMapping{
		{ID: "broad", Path: `/P2P`, Profile: "jav", Enabled: true, Priority: 10},
		{ID: "specific", Path: `/p2p/jh/`, Profile: "giga", ASRMode: "high_accuracy", Enabled: true},
	}}
	got := Resolve(settings, `D:\P2P\JH\SPSF-57.mp4`, "", "")
	if got.Profile != "giga" || got.ASRMode != "high_accuracy" || got.MatchedMapping != "specific" {
		t.Fatalf("resolution = %#v", got)
	}
}

func TestResolveFieldsIndependently(t *testing.T) {
	settings := config.ProfilesConfig{DefaultProfile: "standard", DefaultASRMode: "fast", PathMappings: []config.PathMapping{{Path: "movies", Profile: "jav", Enabled: true}}}
	got := Resolve(settings, "/data/movies/a.mp4", "giga", "balanced")
	if got.Profile != "giga" || got.ProfileSource != "explicit" || got.ASRMode != "balanced" || got.ASRModeSource != "explicit" {
		t.Fatalf("explicit resolution = %#v", got)
	}
	got = Resolve(settings, "/data/movies/a.mp4", "", "high_accuracy")
	if got.Profile != "jav" || got.ProfileSource != "path_mapping" || got.ASRMode != "high_accuracy" || got.ASRModeSource != "explicit" {
		t.Fatalf("independent resolution = %#v", got)
	}
}

func TestDisabledAndEqualSpecificityPriority(t *testing.T) {
	settings := config.ProfilesConfig{DefaultProfile: "standard", DefaultASRMode: "fast", PathMappings: []config.PathMapping{
		{ID: "disabled", Path: "movies", Profile: "giga", Enabled: false, Priority: 99},
		{ID: "low", Path: "movies", Profile: "jav", Enabled: true, Priority: 1},
		{ID: "high", Path: "movies", ASRMode: "balanced", Enabled: true, Priority: 2},
	}}
	got := Resolve(settings, "/movies/a.mp4", "", "")
	if got.Profile != "standard" || got.ASRMode != "balanced" || got.MatchedMapping != "high" {
		t.Fatalf("priority resolution = %#v", got)
	}
}
