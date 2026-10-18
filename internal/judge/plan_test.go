package judge

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestEncodedLenMatchesEncodingJSON(t *testing.T) {
	for _, s := range []string{"", "plain 123", "line\nbreak\ttab\r", `quote " back \`, "<a&b>", "\x01\x1f", "héllo ✓", "\u2028x\u2029", string([]byte{0xff, 'a'})} {
		b, _ := json.Marshal(s)
		if got, want := EncodedLen(s), len(b)-2; got != want {
			t.Errorf("EncodedLen(%q) = %d, want %d", s, got, want)
		}
	}
}

func TestPlanJobsSplitsByCount(t *testing.T) {
	cases := make([]TestCase, 250)
	spans, err := PlanJobs(cases, "code")
	if err != nil {
		t.Fatal(err)
	}
	want := []Span{{0, 100}, {100, 200}, {200, 250}}
	if len(spans) != len(want) {
		t.Fatalf("spans = %v, want %v", spans, want)
	}
	for i := range want {
		if spans[i] != want[i] {
			t.Fatalf("spans = %v, want %v", spans, want)
		}
	}
}

func TestPlanJobsSplitsBySize(t *testing.T) {
	big := strings.Repeat("9", 10<<20) // 10 MiB per case: three fit under 30 MiB only twice
	cases := []TestCase{{Stdin: big}, {Stdin: big}, {Stdin: big}, {Stdin: "1"}}
	spans, err := PlanJobs(cases, "code")
	if err != nil {
		t.Fatal(err)
	}
	for _, sp := range spans {
		if size := JobSize(cases, "code", sp); size > MaxJobBytes {
			t.Fatalf("job %v is %d bytes, over %d", sp, size, MaxJobBytes)
		}
	}
	if spans[0] != (Span{0, 2}) || spans[len(spans)-1].End != len(cases) {
		t.Fatalf("unexpected spans %v", spans)
	}
}

func TestPlanJobsRejectsOversizedCase(t *testing.T) {
	_, err := PlanJobs([]TestCase{{Stdin: strings.Repeat("x", MaxCaseBytes)}}, "")
	if !errors.Is(err, ErrCaseTooLarge) {
		t.Fatalf("err = %v, want ErrCaseTooLarge", err)
	}
}

func TestPlanBatchesRespectsCountAndBytes(t *testing.T) {
	sizes := make([]int, 45)
	for i := range sizes {
		sizes[i] = 1 << 10
	}
	if got := PlanBatches(sizes); len(got) != 3 || got[0] != (Span{0, 20}) || got[2] != (Span{40, 45}) {
		t.Fatalf("by count: %v", got)
	}
	heavy := []int{25 << 20, 25 << 20, 25 << 20}
	if got := PlanBatches(heavy); len(got) != 2 || got[0] != (Span{0, 2}) {
		t.Fatalf("by bytes: %v", got)
	}
}
