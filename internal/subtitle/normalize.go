package subtitle

import (
	"errors"
	"math"
	"sort"
	"strings"
	"unicode"
)

// NormalizeOptions controls deterministic, post-translation subtitle layout.
// Character limits are splitting targets; text is never truncated.
type NormalizeOptions struct {
	Enabled            bool    `json:"enabled"`
	TargetCharsPerLine int     `json:"target_chars_per_line"`
	MaxCharsPerLine    int     `json:"max_chars_per_line"`
	MaxLines           int     `json:"max_lines"`
	TargetCharsPerCue  int     `json:"target_chars_per_cue"`
	MaxCueDurationMS   int64   `json:"max_cue_duration_ms"`
	MinCueDurationMS   int64   `json:"min_cue_duration_ms"`
	TargetCPS          float64 `json:"target_chars_per_second"`
}

type NormalizationChange struct {
	SourceIndex int
	DurationMS  int64
	Characters  int
	OutputCues  int
	Skipped     string
}

func DefaultNormalizeOptions() NormalizeOptions {
	return NormalizeOptions{
		Enabled: true, TargetCharsPerLine: 40, MaxCharsPerLine: 46, MaxLines: 2,
		TargetCharsPerCue: 80, MaxCueDurationMS: 6000, MinCueDurationMS: 1000, TargetCPS: 17,
	}
}

func NormalizeSubtitles(input []Segment, options NormalizeOptions) ([]Segment, []NormalizationChange, error) {
	if !options.Enabled {
		return append([]Segment(nil), input...), nil, nil
	}
	options = normalizeOptions(options)
	sources := append([]Segment(nil), input...)
	sort.SliceStable(sources, func(i, j int) bool {
		if sources[i].StartMS == sources[j].StartMS {
			return sources[i].EndMS < sources[j].EndMS
		}
		return sources[i].StartMS < sources[j].StartMS
	})

	output := make([]Segment, 0, len(sources))
	changes := make([]NormalizationChange, 0)
	for sourceIndex, source := range sources {
		if source.StartMS < 0 || source.EndMS <= source.StartMS {
			return nil, nil, errors.New("subtitle normalization received an invalid timestamp")
		}
		plain := collapseWhitespace(source.Text)
		if plain == "" {
			continue
		}
		characters := visibleCharacters(plain)
		duration := source.EndMS - source.StartMS
		if !needsSplit(plain, duration, options) {
			source.Text = wrapTwoLines(plain, options)
			output = append(output, source)
			continue
		}

		desired := int(math.Ceil(float64(characters) / float64(options.TargetCharsPerCue)))
		if duration > options.MaxCueDurationMS && characters >= 24 {
			desired = max(desired, int(math.Ceil(float64(duration)/float64(options.MaxCueDurationMS))))
		}
		target := options.TargetCharsPerCue
		if desired > 1 {
			target = int(math.Ceil(float64(characters) / float64(desired)))
			target = max(target, min(24, options.TargetCharsPerLine))
		}
		chunks := semanticChunks(plain, target)
		anchors := validTimingAnchors(source)
		if len(anchors) > 0 && len(chunks) > len(anchors)+1 {
			chunks = coalesceChunks(chunks, len(anchors)+1)
		}
		skipped := ""
		if len(chunks) >= 2 && len(anchors) < len(chunks)-1 {
			skipped = "no trustworthy alignment anchors"
		} else if len(chunks) >= 2 && duration < options.MinCueDurationMS*int64(len(chunks)) {
			skipped = "insufficient aligned duration"
		}
		if len(chunks) < 2 || skipped != "" {
			source.Text = wrapTwoLines(plain, options)
			output = append(output, source)
			if skipped != "" {
				changes = append(changes, NormalizationChange{
					SourceIndex: sourceIndex, DurationMS: duration, Characters: characters,
					OutputCues: 1, Skipped: skipped,
				})
			}
			continue
		}
		children := splitAtAlignmentAnchors(source, chunks, anchors)
		for i := range children {
			children[i].Text = wrapTwoLines(children[i].Text, options)
		}
		output = append(output, children...)
		changes = append(changes, NormalizationChange{
			SourceIndex: sourceIndex, DurationMS: duration, Characters: characters, OutputCues: len(children),
		})
	}
	return output, changes, nil
}

func coalesceChunks(chunks []string, maximum int) []string {
	chunks = append([]string(nil), chunks...)
	for len(chunks) > maximum && len(chunks) > 1 {
		bestIndex, bestSize := 0, int(^uint(0)>>1)
		for index := 0; index < len(chunks)-1; index++ {
			size := visibleCharacters(chunks[index]) + visibleCharacters(chunks[index+1])
			if size < bestSize {
				bestIndex, bestSize = index, size
			}
		}
		chunks[bestIndex] = joinSemanticText(chunks[bestIndex], chunks[bestIndex+1])
		chunks = append(chunks[:bestIndex+1], chunks[bestIndex+2:]...)
	}
	return chunks
}

func joinSemanticText(left, right string) string {
	left, right = strings.TrimSpace(left), strings.TrimSpace(right)
	if left == "" {
		return right
	}
	if right == "" {
		return left
	}
	leftRunes, rightRunes := []rune(left), []rune(right)
	if unicode.Is(unicode.Han, leftRunes[len(leftRunes)-1]) || unicode.Is(unicode.Han, rightRunes[0]) ||
		unicode.Is(unicode.Hiragana, leftRunes[len(leftRunes)-1]) || unicode.Is(unicode.Hiragana, rightRunes[0]) ||
		unicode.Is(unicode.Katakana, leftRunes[len(leftRunes)-1]) || unicode.Is(unicode.Katakana, rightRunes[0]) {
		return left + right
	}
	return left + " " + right
}

func normalizeOptions(value NormalizeOptions) NormalizeOptions {
	defaults := DefaultNormalizeOptions()
	if value.TargetCharsPerLine < 1 {
		value.TargetCharsPerLine = defaults.TargetCharsPerLine
	}
	if value.MaxCharsPerLine < value.TargetCharsPerLine {
		value.MaxCharsPerLine = max(defaults.MaxCharsPerLine, value.TargetCharsPerLine)
	}
	if value.MaxLines < 1 {
		value.MaxLines = defaults.MaxLines
	}
	if value.MaxLines > 2 {
		value.MaxLines = 2
	}
	if value.TargetCharsPerCue < 1 {
		value.TargetCharsPerCue = value.TargetCharsPerLine * value.MaxLines
	}
	if value.MaxCueDurationMS < 1 {
		value.MaxCueDurationMS = defaults.MaxCueDurationMS
	}
	if value.MinCueDurationMS < 1 {
		value.MinCueDurationMS = defaults.MinCueDurationMS
	}
	if value.TargetCPS <= 0 {
		value.TargetCPS = defaults.TargetCPS
	}
	return value
}

func needsSplit(text string, duration int64, options NormalizeOptions) bool {
	characters := visibleCharacters(text)
	if characters > options.TargetCharsPerCue || characters > options.MaxCharsPerLine*options.MaxLines {
		return true
	}
	return duration > options.MaxCueDurationMS && characters > int(options.TargetCPS*float64(options.MaxCueDurationMS)/1000)
}

func semanticChunks(text string, target int) []string {
	runes := []rune(text)
	chunks := make([]string, 0, max(1, len(runes)/max(1, target)))
	for len(runes) > target {
		lower := max(1, target/2)
		upper := min(len(runes), target+max(4, target/4))
		cut := bestBoundary(runes, lower, upper, target)
		if cut <= 0 {
			cut = min(target, len(runes))
		}
		chunk := strings.TrimSpace(string(runes[:cut]))
		if chunk != "" {
			chunks = append(chunks, chunk)
		}
		runes = []rune(strings.TrimSpace(string(runes[cut:])))
	}
	if tail := strings.TrimSpace(string(runes)); tail != "" {
		chunks = append(chunks, tail)
	}
	return chunks
}

func bestBoundary(runes []rune, lower, upper, target int) int {
	for priority := 1; priority <= 4; priority++ {
		best, distance := 0, len(runes)+1
		for i := lower; i <= upper && i <= len(runes); i++ {
			if boundaryPriority(runes, i) != priority {
				continue
			}
			if d := abs(i - target); d < distance {
				best, distance = i, d
			}
		}
		if best > 0 {
			return best
		}
	}
	return 0
}

func boundaryPriority(runes []rune, index int) int {
	if index <= 0 || index > len(runes) {
		return 0
	}
	previous := runes[index-1]
	switch previous {
	case '.', '!', '?', '。', '！', '？', '…':
		return 1
	case ';', ':', '；', '：':
		return 2
	case ',', '，', '、':
		return 3
	}
	if unicode.IsSpace(previous) || (index < len(runes) && unicode.IsSpace(runes[index])) {
		return 4
	}
	return 0
}

func splitAtAlignmentAnchors(source Segment, chunks []string, anchors []int64) []Segment {
	weights := make([]int, len(chunks))
	totalWeight := 0
	for i, chunk := range chunks {
		weights[i] = max(1, visibleCharacters(chunk))
		totalWeight += weights[i]
	}
	result := make([]Segment, 0, len(chunks))
	cumulativeWeight := 0
	start := source.StartMS
	for i, chunk := range chunks {
		cumulativeWeight += weights[i]
		end := source.EndMS
		if i < len(chunks)-1 {
			ideal := source.StartMS + int64(math.Round(float64(source.EndMS-source.StartMS)*float64(cumulativeWeight)/float64(totalWeight)))
			first := 0
			if len(result) > 0 {
				for first < len(anchors) && anchors[first] <= start {
					first++
				}
			}
			last := len(anchors) - (len(chunks) - 1 - i)
			chosen := first
			for candidate := first; candidate <= last; candidate++ {
				if abs64(anchors[candidate]-ideal) < abs64(anchors[chosen]-ideal) {
					chosen = candidate
				}
			}
			end = anchors[chosen]
			anchors = anchors[chosen+1:]
		}
		if i == len(chunks)-1 {
			end = source.EndMS
		}
		result = append(result, Segment{StartMS: start, EndMS: end, Text: chunk})
		start = end
	}
	return result
}

func validTimingAnchors(source Segment) []int64 {
	anchors := make([]int64, 0, len(source.TimingAnchors))
	for _, anchor := range source.TimingAnchors {
		if anchor > source.StartMS && anchor < source.EndMS && (len(anchors) == 0 || anchor > anchors[len(anchors)-1]) {
			anchors = append(anchors, anchor)
		}
	}
	return anchors
}

func wrapTwoLines(text string, options NormalizeOptions) string {
	providedLines := strings.Split(strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n")), "\n")
	if len(providedLines) > 1 && len(providedLines) <= options.MaxLines {
		valid := true
		for index := range providedLines {
			providedLines[index] = collapseWhitespace(providedLines[index])
			if providedLines[index] == "" || visibleCharacters(providedLines[index]) > options.MaxCharsPerLine {
				valid = false
			}
		}
		if valid {
			return strings.Join(providedLines, "\n")
		}
	}
	text = collapseWhitespace(text)
	if visibleCharacters(text) <= options.TargetCharsPerLine || options.MaxLines < 2 {
		return text
	}
	runes := []rune(text)
	target := len(runes) / 2
	best, distance := 0, len(runes)+1
	for i, r := range runes {
		if !unicode.IsSpace(r) {
			continue
		}
		left := strings.TrimSpace(string(runes[:i]))
		right := strings.TrimSpace(string(runes[i+1:]))
		if left == "" || right == "" {
			continue
		}
		if visibleCharacters(left) > options.MaxCharsPerLine || visibleCharacters(right) > options.MaxCharsPerLine {
			continue
		}
		if d := abs(i - target); d < distance {
			best, distance = i, d
		}
	}
	if best > 0 {
		return strings.TrimSpace(string(runes[:best])) + "\n" + strings.TrimSpace(string(runes[best+1:]))
	}
	// Space-free scripts such as Japanese have no ordinary word boundary.
	if len(runes) <= options.MaxCharsPerLine*2 {
		cut := min(options.TargetCharsPerLine, len(runes)/2+len(runes)%2)
		return string(runes[:cut]) + "\n" + string(runes[cut:])
	}
	return text
}

func visibleCharacters(text string) int {
	count := 0
	for _, r := range text {
		if !unicode.IsSpace(r) {
			count++
		}
	}
	return count
}

func collapseWhitespace(text string) string { return strings.Join(strings.Fields(text), " ") }

func abs(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func abs64(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
}
