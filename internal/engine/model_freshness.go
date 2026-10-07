package engine

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"javbeaconsubs/internal/subtitle"
)

// Only compare numeric versions within the same known model family. A change
// of provider, model tier, or backend identity is not proof of an upgrade.
var gptModelVersion = regexp.MustCompile(`(?i)^gpt-(\d+)(?:\.(\d+))?(?:-[a-z0-9][a-z0-9.-]*)?$`)

func newerGPTModel(current, previous string) bool {
	parse := func(value string) (int, int, bool) {
		parts := gptModelVersion.FindStringSubmatch(strings.TrimSpace(value))
		if parts == nil {
			return 0, 0, false
		}
		major, _ := strconv.Atoi(parts[1])
		minor, _ := strconv.Atoi(parts[2])
		return major, minor, true
	}
	currentMajor, currentMinor, currentOK := parse(current)
	oldMajor, oldMinor, oldOK := parse(previous)
	return currentOK && oldOK && (currentMajor > oldMajor || currentMajor == oldMajor && currentMinor > oldMinor)
}

// existingModelReplacement reuses the verified project/sidecar provenance
// reader also used on normal skip-existing results. Missing or edited records
// are only replaced when explicitly opted in; a valid record from a different
// or unversioned model remains untouched because its relative age is unknown.
func (r *Runner) existingModelReplacement(englishPath, projectPath string) (bool, string) {
	if !r.overwriteIfModelNewer {
		return false, ""
	}
	var existing Result
	existing.loadExistingProvenance(projectPath, englishPath)
	for _, artifact := range existing.SubtitleProvenance {
		if filepath.Clean(artifact.SubtitlePath) != filepath.Clean(englishPath) {
			continue
		}
		if artifact.Status != subtitle.ProvenanceValid {
			return r.overwriteUnverifiedProvenance, "unverified provenance"
		}
		if newerGPTModel(r.translationBackend(), artifact.Metadata.TranslationBackend) {
			return true, "newer GPT translation model"
		}
		return false, ""
	}
	return r.overwriteUnverifiedProvenance, "missing provenance"
}
