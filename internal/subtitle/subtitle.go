package subtitle

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

type Segment struct {
	StartMS int64  `json:"start_ms"`
	EndMS   int64  `json:"end_ms"`
	Text    string `json:"text"`
}

var (
	spaceRE        = regexp.MustCompile(`\s+`)
	bracketNoiseRE = regexp.MustCompile(`(?i)^\s*[\[(（【]?(music|applause|silence|noise|音楽|拍手|無音)[\])）】]?\s*$`)
)

func Clean(in []Segment) []Segment {
	out := make([]Segment, 0, len(in))
	for _, seg := range in {
		seg.Text = strings.TrimSpace(spaceRE.ReplaceAllString(seg.Text, " "))
		if seg.Text == "" || bracketNoiseRE.MatchString(seg.Text) || seg.EndMS <= seg.StartMS {
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

func RenderSRT(segments []Segment, maxChars, maxLines int, marker string) string {
	var b strings.Builder
	if marker != "" {
		fmt.Fprintf(&b, "%s\n", marker)
	}
	for i, seg := range segments {
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
	for _, seg := range segments {
		text := strings.ReplaceAll(reflow(seg.Text, maxChars, maxLines), "\n", `\N`)
		text = strings.ReplaceAll(text, "{", `\{`)
		fmt.Fprintf(&b, "Dialogue: 0,%s,%s,Default,,0,0,0,,%s\n", assTimestamp(seg.StartMS), assTimestamp(seg.EndMS), text)
	}
	return b.String()
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
	if maxChars <= 0 || maxLines <= 0 || len([]rune(text)) <= maxChars {
		return text
	}
	words := strings.Fields(text)
	if len(words) < 2 {
		return text
	}
	lines := []string{""}
	for _, word := range words {
		last := len(lines) - 1
		candidate := word
		if lines[last] != "" {
			candidate = lines[last] + " " + word
		}
		if len([]rune(candidate)) <= maxChars || len(lines) == maxLines {
			lines[last] = candidate
		} else {
			lines = append(lines, word)
		}
	}
	return strings.Join(lines, "\n")
}
