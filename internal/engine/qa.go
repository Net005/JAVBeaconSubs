package engine

import (
	"regexp"
	"strings"

	"javbeaconsubs/internal/subtitle"
)

// ProperNameVariant flags a capitalized-phrase candidate in translated
// English text that closely but not exactly matches a term already
// established as correct for this file (TODO Part 22). Diagnostic only -
// nothing is ever auto-corrected from this.
type ProperNameVariant struct {
	RowIndex       int    `json:"row_index"`
	Candidate      string `json:"candidate"`
	LikelyIntended string `json:"likely_intended"`
	EditDistance   int    `json:"edit_distance"`
}

// properNameCandidateRE matches a single capitalized word with at least
// one lowercase letter after its capital - this deliberately excludes
// catalog codes (ADN-803), acronyms (MVSD), and other all-caps tokens
// that are not name-like. Deliberately single-word only: an earlier
// multi-word version (joining consecutive capitalized words into one
// phrase) false-matched ordinary sentence-initial capitalization against
// the next word (e.g. "Then Moonligt" as one candidate), corrupting the
// comparison; multi-word proper names are out of scope for this pass.
var properNameCandidateRE = regexp.MustCompile(`\b\p{Lu}[\p{Ll}'-]{3,}\b`)

// levenshteinDistance is a standard O(n*m) edit-distance implementation
// (no third-party dependency for this one small utility).
func levenshteinDistance(a, b string) int {
	ar := []rune(a)
	br := []rune(b)
	if len(ar) == 0 {
		return len(br)
	}
	if len(br) == 0 {
		return len(ar)
	}
	prev := make([]int, len(br)+1)
	curr := make([]int, len(br)+1)
	for j := 0; j <= len(br); j++ {
		prev[j] = j
	}
	for i := 1; i <= len(ar); i++ {
		curr[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			del := prev[j] + 1
			ins := curr[j-1] + 1
			sub := prev[j-1] + cost
			min := del
			if ins < min {
				min = ins
			}
			if sub < min {
				min = sub
			}
			curr[j] = min
		}
		prev, curr = curr, prev
	}
	return prev[len(br)]
}

// DetectProperNameVariants scans out for candidate proper-noun phrases
// that are NOT an exact (case-insensitive) match to any term in
// knownTerms but are suspiciously close to exactly one of them -
// evidence-anchored (Part 22: "prefer evidence from... glossary...
// repeated context"), never a blind comparison between two unknown
// strings (Part 22: "do not blindly merge by edit distance" - there is
// no merging here at all, only flagging). Conservative by design to
// keep false positives low: both the candidate and the reference term
// must be at least 5 characters, and edit distance must be 1 or 2.
// Results are deduplicated by candidate text and capped at 30 entries.
func DetectProperNameVariants(out []subtitle.Segment, knownTerms []string) []ProperNameVariant {
	if len(knownTerms) == 0 || len(out) == 0 {
		return nil
	}
	known := make([]string, 0, len(knownTerms))
	knownExact := make(map[string]bool, len(knownTerms))
	for _, term := range knownTerms {
		term = strings.TrimSpace(term)
		if len([]rune(term)) < 5 {
			continue
		}
		known = append(known, term)
		knownExact[strings.ToLower(term)] = true
	}
	if len(known) == 0 {
		return nil
	}

	var variants []ProperNameVariant
	seenCandidates := make(map[string]bool)
	for rowIndex, seg := range out {
		if len(variants) >= 30 {
			break
		}
		for _, candidate := range properNameCandidateRE.FindAllString(seg.Text, -1) {
			if len(variants) >= 30 {
				break
			}
			if len([]rune(candidate)) < 5 {
				continue
			}
			lowerCandidate := strings.ToLower(candidate)
			if knownExact[lowerCandidate] {
				continue
			}
			if seenCandidates[lowerCandidate] {
				continue
			}
			bestTerm := ""
			bestDistance := -1
			ambiguous := false
			for _, term := range known {
				if strings.ToLower(term) == lowerCandidate {
					continue
				}
				d := levenshteinDistance(lowerCandidate, strings.ToLower(term))
				if d < 1 || d > 2 {
					continue
				}
				if bestDistance == -1 || d < bestDistance {
					bestDistance = d
					bestTerm = term
					ambiguous = false
				} else if d == bestDistance && term != bestTerm {
					ambiguous = true
				}
			}
			if bestDistance == -1 || ambiguous {
				continue
			}
			seenCandidates[lowerCandidate] = true
			variants = append(variants, ProperNameVariant{
				RowIndex:       rowIndex,
				Candidate:      candidate,
				LikelyIntended: bestTerm,
				EditDistance:   bestDistance,
			})
		}
	}
	return variants
}
