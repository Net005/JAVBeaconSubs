package subtitle

// DensityAnomaly flags a cue whose visible-character density (characters
// per second of cue duration) is extreme enough to warrant human review.
// This is a diagnostic-only signal: detection never changes Segment
// timing or text (TODO Part 26 - "do not invent timing, do not rewrite
// canonical text").
type DensityAnomaly struct {
	Index          int     `json:"index"`
	StartMS        int64   `json:"start_ms"`
	EndMS          int64   `json:"end_ms"`
	Characters     int     `json:"characters"`
	CharsPerSecond float64 `json:"chars_per_second"`
}

// DetectDensityAnomalies flags segments whose density exceeds extremeCPS.
// This is deliberately independent of needsSplit's own thresholds:
// needsSplit only considers a cue "too dense to leave alone" when its
// total character count crosses a fixed ceiling, or when a long cue
// (> MaxCueDurationMS) is also character-dense - so a *short* cue with a
// merely moderate character count (e.g. 50 characters in ~1 second) never
// gets flagged for splitting at all, even though 50 chars/sec is far
// outside any readable pace and there is no valid way to split a ~1
// second cue into multiple cues without inventing timing. extremeCPS <= 0
// disables the check.
func DetectDensityAnomalies(segments []Segment, extremeCPS float64) []DensityAnomaly {
	if extremeCPS <= 0 {
		return nil
	}
	var anomalies []DensityAnomaly
	for i, seg := range segments {
		duration := seg.EndMS - seg.StartMS
		if duration <= 0 {
			continue
		}
		characters := visibleCharacters(seg.Text)
		cps := float64(characters) / (float64(duration) / 1000)
		if cps > extremeCPS {
			anomalies = append(anomalies, DensityAnomaly{Index: i, StartMS: seg.StartMS, EndMS: seg.EndMS, Characters: characters, CharsPerSecond: cps})
		}
	}
	return anomalies
}
