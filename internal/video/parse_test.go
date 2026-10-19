package video

import (
	"strings"
	"testing"
)

func TestParseDuration(t *testing.T) {
	for in, want := range map[string]int{"PT15M33S": 933, "PT1H2M3S": 3723, "PT45S": 45, "P1DT1S": 86401, "P0D": 0, "PT2H": 7200} {
		got, err := ParseDuration(in)
		if err != nil || got != want {
			t.Errorf("%s = %d, %v; want %d", in, got, err, want)
		}
	}
	if _, err := ParseDuration("15:33"); err == nil {
		t.Error("accepted a non-ISO duration")
	}
}

func TestParseChaptersFollowsYouTubesRules(t *testing.T) {
	desc := "Two pointers, explained.\n\n0:00 Intro\n1:05 - Brute force\n4:30 Optimal\n1:02:03 Wrap up\nhttps://x.dev"
	got := ParseChapters(desc)
	if len(got) != 4 || got[1].T != 65 || got[1].Label != "Brute force" || got[3].T != 3723 {
		t.Fatalf("chapters = %+v", got)
	}
	if ParseChapters("0:30 Late start\n1:00 a\n2:00 b") != nil {
		t.Error("chapters not starting at 0:00 must be ignored")
	}
	if ParseChapters("0:00 a\n1:00 b") != nil {
		t.Error("fewer than three chapters must be ignored")
	}
}

func TestDescriptionMarkdownIsInert(t *testing.T) {
	md := DescriptionMarkdown("# not a heading\n::problem{slug=x}\n<script>alert(1)</script>\nSee https://leetcode.com/problems/two-sum/.\n\nSecond para")
	for _, bad := range []string{"\n# ", "\n::problem", "<script>"} {
		if strings.Contains("\n"+md, bad) {
			t.Errorf("markup survived: %q in %q", bad, md)
		}
	}
	if !strings.Contains(md, "<https://leetcode.com/problems/two-sum/>.") {
		t.Errorf("link not made clickable: %q", md)
	}
	if !strings.Contains(md, "\n\nSecond para") {
		t.Errorf("paragraphs lost: %q", md)
	}
}

func TestSummarySkipsChaptersAndLinks(t *testing.T) {
	got := Summary("https://x.dev\n0:00 Intro\n\nTrapping rain water with two pointers in O(n).\nMore text")
	if got == nil || *got != "Trapping rain water with two pointers in O(n)." {
		t.Fatalf("summary = %v", got)
	}
	long := Summary(strings.Repeat("word ", 100))
	if long == nil || !strings.HasSuffix(*long, "…") || len([]rune(*long)) > maxSummary+1 {
		t.Errorf("long summary = %q", *long)
	}
	if Summary("") != nil {
		t.Error("empty description has no summary")
	}
}
