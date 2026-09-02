package subtitle

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

type Segment struct {
	StartMS       int64   `json:"start_ms"`
	EndMS         int64   `json:"end_ms"`
	Text          string  `json:"text"`
	TimingAnchors []int64 `json:"timing_anchors_ms,omitempty"`
}

var (
	spaceRE        = regexp.MustCompile(`\s+`)
	bracketNoiseRE = regexp.MustCompile(`(?i)^\s*[\[(（【]?(music|applause|silence|noise|音楽|拍手|無音)[\])）】]?\s*$`)
)

func Clean(in []Segment) []Segment {
	out := make([]Segment, 0, len(in))
	for _, seg := range in {
		seg.Text = strings.TrimSpace(spaceRE.ReplaceAllString(seg.Text, " "))
		if !HasMeaningfulTranscript(seg.Text) || bracketNoiseRE.MatchString(seg.Text) || seg.EndMS <= seg.StartMS {
			continue
		}
		if seg.StartMS < 0 {
			seg.StartMS = 0
		}
		if len(out) > 0 {
			prev := &out[len(out)-1]
			if duplicate(*prev, seg) {
				if seg.EndMS > prev.EndMS {
					prev.EndMS = seg.EndMS
				}
				continue
			}
			if seg.StartMS < prev.EndMS && seg.StartMS >= prev.StartMS {
				seg.StartMS = prev.EndMS
			}
		}
		if seg.EndMS-seg.StartMS < 100 {
			seg.EndMS = seg.StartMS + 100
		}
		out = append(out, seg)
	}
	return out
}

// HasMeaningfulTranscript rejects punctuation, whitespace, and formatting-only
// ASR output while retaining Japanese, Latin text, acronyms, and numbers.
func HasMeaningfulTranscript(text string) bool {
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			return true
		}
	}
	return false
}

func duplicate(a, b Segment) bool {
	na, nb := normalize(a.Text), normalize(b.Text)
	if na == "" || nb == "" {
		return false
	}
	overlaps := b.StartMS <= a.EndMS+180
	return overlaps && (na == nb || (len(na) >= 8 && len(nb) >= 8 && (strings.Contains(na, nb) || strings.Contains(nb, na))))
}

func normalize(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || unicode.IsPunct(r) {
			return -1
		}
		return unicode.ToLower(r)
	}, s)
}

func RenderSRT(segments []Segment, maxChars, maxLines int) string {
	var b strings.Builder
	for i, seg := range strictSegments(segments) {
		fmt.Fprintf(&b, "%d\n%s --> %s\n%s\n\n", i+1, timestamp(seg.StartMS), timestamp(seg.EndMS), reflow(seg.Text, maxChars, maxLines))
	}
	return b.String()
}

func RenderASS(segments []Segment, maxChars, maxLines int, title string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[Script Info]\nTitle: %s\nScriptType: v4.00+\nWrapStyle: 0\nScaledBorderAndShadow: yes\nYCbCr Matrix: TV.709\n\n", strings.ReplaceAll(title, "\n", " "))
	b.WriteString("[V4+ Styles]\nFormat: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding\n")
	b.WriteString("Style: Default,Arial,48,&H00FFFFFF,&H000000FF,&H00101010,&H80000000,-1,0,0,0,100,100,0,0,1,2,1,2,60,60,42,1\n\n")
	b.WriteString("[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n")
	for _, seg := range strictSegments(segments) {
		text := strings.ReplaceAll(reflow(seg.Text, maxChars, maxLines), "\n", `\N`)
		text = strings.ReplaceAll(text, "{", `\{`)
		fmt.Fprintf(&b, "Dialogue: 0,%s,%s,Default,,0,0,0,,%s\n", assTimestamp(seg.StartMS), assTimestamp(seg.EndMS), text)
	}
	return b.String()
}

func strictSegments(input []Segment) []Segment {
	segments := append([]Segment(nil), input...)
	sort.SliceStable(segments, func(i, j int) bool {
		if segments[i].StartMS == segments[j].StartMS {
			return segments[i].EndMS < segments[j].EndMS
		}
		return segments[i].StartMS < segments[j].StartMS
	})
	output := segments[:0]
	for _, segment := range segments {
		// Serialization must never invent or move trustworthy ASR/alignment
		// timestamps. Recognition cleanup happens earlier in the pipeline.
		segment.Text = strings.TrimSpace(segment.Text)
		if segment.EndMS <= segment.StartMS || !HasMeaningfulTranscript(segment.Text) {
			continue
		}
		output = append(output, segment)
	}
	return output
}

func timestamp(ms int64) string {
	if ms < 0 {
		ms = 0
	}
	h := ms / 3_600_000
	ms %= 3_600_000
	m := ms / 60_000
	ms %= 60_000
	s := ms / 1000
	ms %= 1000
	return fmt.Sprintf("%02d:%02d:%02d,%03d", h, m, s, ms)
}

func assTimestamp(ms int64) string {
	if ms < 0 {
		ms = 0
	}
	h := ms / 3_600_000
	ms %= 3_600_000
	m := ms / 60_000
	ms %= 60_000
	s := ms / 1000
	cs := (ms % 1000) / 10
	return fmt.Sprintf("%d:%02d:%02d.%02d", h, m, s, cs)
}

func reflow(text string, maxChars, maxLines int) string {
	return wrapTwoLines(text, NormalizeOptions{
		Enabled: true, TargetCharsPerLine: maxChars, MaxCharsPerLine: maxChars, MaxLines: maxLines,
	})
}
