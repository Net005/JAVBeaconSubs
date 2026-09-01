package subtitle

import (
	"strings"
	"testing"
)

func TestCleanRemovesOnlyOverlappingDuplicates(t *testing.T) {
	in := []Segment{
		{StartMS: 0, EndMS: 1000, Text: "  こんにちは。 "},
		{StartMS: 900, EndMS: 1600, Text: "こんにちは"},
		{StartMS: 5000, EndMS: 5500, Text: "こんにちは"},
		{StartMS: 6000, EndMS: 7000, Text: "[Music]"},
	}
	out := Clean(in)
	if len(out) != 2 {
		t.Fatalf("got %d segments, want 2: %#v", len(out), out)
	}
	if out[0].EndMS != 1600 {
		t.Fatalf("duplicate duration was not merged: %#v", out[0])
	}
}

func TestRenderSRT(t *testing.T) {
	got := RenderSRT([]Segment{{StartMS: 1234, EndMS: 3661001, Text: "A deliberately long subtitle line that should wrap cleanly"}}, 30, 2, "; generated-by=javbeaconsubs")
	for _, want := range []string{"; generated-by=javbeaconsubs", "00:00:01,234 --> 01:01:01,001", "\n\n"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
}

func TestRenderASS(t *testing.T) {
	got := RenderASS([]Segment{{StartMS: 1234, EndMS: 3661001, Text: "First line\nSecond line"}}, 30, 2, "Example")
	for _, want := range []string{"[Script Info]", "[V4+ Styles]", "Dialogue: 0,0:00:01.23,1:01:01.00", `First line\NSecond line`} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
}
