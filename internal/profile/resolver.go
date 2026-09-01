package profile

import (
	"strings"

	"javbeaconsubs/internal/config"
)

type Resolution struct {
	Profile        string `json:"profile"`
	ASRMode        string `json:"asr_mode"`
	ProfileSource  string `json:"profile_source"`
	ASRModeSource  string `json:"asr_mode_source"`
	MatchedMapping string `json:"matched_mapping,omitempty"`
}

func NormalizePathForMapping(value string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(value), "\\", "/"))
}

func Resolve(settings config.ProfilesConfig, sourcePath, explicitProfile, explicitMode string) Resolution {
	settings.DefaultProfile = config.NormalizeProfile(settings.DefaultProfile)
	if settings.DefaultProfile == "" {
		settings.DefaultProfile = "jav"
	}
	settings.DefaultASRMode = config.NormalizeASRMode(settings.DefaultASRMode)
	if settings.DefaultASRMode == "" {
		settings.DefaultASRMode = "balanced"
	}
	explicitProfile = config.NormalizeProfile(explicitProfile)
	explicitMode = config.NormalizeASRMode(explicitMode)
	resolved := Resolution{Profile: settings.DefaultProfile, ASRMode: settings.DefaultASRMode, ProfileSource: "default", ASRModeSource: "default"}
	path := NormalizePathForMapping(sourcePath)
	bestIndex, bestLength, bestPriority := -1, -1, -1<<30
	for index, mapping := range settings.PathMappings {
		match := NormalizePathForMapping(mapping.Path)
		if !mapping.Enabled || match == "" || !strings.Contains(path, match) {
			continue
		}
		if len(match) > bestLength || len(match) == bestLength && mapping.Priority > bestPriority {
			bestIndex, bestLength, bestPriority = index, len(match), mapping.Priority
		}
	}
	if bestIndex >= 0 {
		mapping := settings.PathMappings[bestIndex]
		resolved.MatchedMapping = mapping.ID
		if resolved.MatchedMapping == "" {
			resolved.MatchedMapping = mapping.Path
		}
		if mapping.Profile != "" {
			resolved.Profile, resolved.ProfileSource = mapping.Profile, "path_mapping"
		}
		if mapping.ASRMode != "" {
			resolved.ASRMode, resolved.ASRModeSource = mapping.ASRMode, "path_mapping"
		}
	}
	if explicitProfile != "" {
		resolved.Profile, resolved.ProfileSource = explicitProfile, "explicit"
	}
	if explicitMode != "" {
		resolved.ASRMode, resolved.ASRModeSource = explicitMode, "explicit"
	}
	return resolved
}
