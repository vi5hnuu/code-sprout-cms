package markdown

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The fixtures in spec/markdown are shared with the web and Android renderers: each .md has a
// .json golden describing what a conforming implementation must derive from it. Regenerate
// after an intentional change with:  go test ./internal/markdown -run Fixtures -update
var update = flag.Bool("update", false, "rewrite spec/markdown/*.json goldens")

type golden struct {
	Analysis
	PlainText string `json:"plainText"`
}

func TestFixtures(t *testing.T) {
	files, err := filepath.Glob("../../spec/markdown/*.md")
	if err != nil || len(files) == 0 {
		t.Fatalf("no fixtures found (%v)", err)
	}
	for _, path := range files {
		t.Run(filepath.Base(path), func(t *testing.T) {
			src, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			a := Analyze(src)
			got, err := json.MarshalIndent(golden{Analysis: *a, PlainText: a.PlainText}, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, '\n')
			goldenPath := strings.TrimSuffix(path, ".md") + ".json"
			if *update {
				if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatalf("missing golden (run with -update): %v", err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("analysis differs from %s:\n%s", goldenPath, got)
			}
		})
	}
}

func issueCodes(a *Analysis) []string {
	codes := make([]string, len(a.Issues))
	for i, is := range a.Issues {
		codes[i] = is.Code
	}
	return codes
}

func TestValidArticleHasNoIssues(t *testing.T) {
	src, err := os.ReadFile("../../spec/markdown/article-full.md")
	if err != nil {
		t.Fatal(err)
	}
	a := Analyze(src)
	if !a.Valid() {
		t.Fatalf("unexpected issues: %+v", a.Issues)
	}
	if len(a.Snippets) != 2 || a.Snippets[0].Language != "python" || a.Snippets[1].Language != "cpp" {
		t.Fatalf("snippets = %+v", a.Snippets)
	}
	if len(a.AssetIDs) != 2 || a.AssetIDs[0] != 42 || a.AssetIDs[1] != 77 {
		t.Fatalf("assets = %v", a.AssetIDs)
	}
	if len(a.ProblemSlugs) != 1 || a.ProblemSlugs[0] != "search-in-rotated-array" {
		t.Fatalf("problems = %v", a.ProblemSlugs)
	}
	if strings.Contains(a.PlainText, "def search") {
		t.Fatal("code must not be indexed as prose")
	}
}

func TestUnsafeInputIsRejected(t *testing.T) {
	src, _ := os.ReadFile("../../spec/markdown/unsafe.md")
	got := strings.Join(issueCodes(Analyze(src)), ",")
	for _, want := range []string{"heading.h1", "html.not_allowed", "link.unsafe", "image.not_asset", "image.no_alt", "code.indented"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in %s", want, got)
		}
	}
}

func TestCodeInsideContainerDoesNotCloseIt(t *testing.T) {
	src := []byte(":::callout{kind=note}\n```text\n:::\n```\nafter\n:::\n")
	a := Analyze(src)
	if !a.Valid() {
		t.Fatalf("issues: %+v", a.Issues)
	}
	if !strings.Contains(a.PlainText, "after") {
		t.Fatalf("paragraph after the code block escaped the container: %q", a.PlainText)
	}
}

func TestOversizeBodyRejectedWithoutParsing(t *testing.T) {
	a := Analyze(bytes.Repeat([]byte("a"), MaxBodyBytes+1))
	if len(a.Issues) != 1 || a.Issues[0].Code != "body.too_large" {
		t.Fatalf("issues = %+v", a.Issues)
	}
}

func TestSlugger(t *testing.T) {
	s := NewSlugger()
	for _, tc := range []struct{ in, want string }{
		{"Two Sum", "two-sum"}, {"Two Sum", "two-sum-1"}, {"two-sum-1", "two-sum-1-1"},
		{"C++ & Java", "c--java"}, {"!!!", "section"}, {"!!!", "section-1"},
	} {
		if got := s.Slug(tc.in); got != tc.want {
			t.Errorf("Slug(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// A body at the size limit, in the shape of a real article, bounds publish-time cost.
func BenchmarkAnalyzeLargeArticle(b *testing.B) {
	section, _ := os.ReadFile("../../spec/markdown/article-full.md")
	var body bytes.Buffer
	for body.Len()+len(section) < MaxBodyBytes {
		body.Write(section)
	}
	b.SetBytes(int64(body.Len()))
	b.ResetTimer()
	for b.Loop() {
		Analyze(body.Bytes())
	}
}

func TestCommentProfile(t *testing.T) {
	ok := AnalyzeComment([]byte("Nice! Try `lo + (hi-lo)/2`:\n\n```go\nmid := lo + (hi-lo)/2\n```\n\nSee [this](https://example.com)."))
	if !ok.Valid() {
		t.Fatalf("plain comment rejected: %+v", ok.Issues)
	}
	bad := AnalyzeComment([]byte("## Title\n\n![x](asset:1)\n\n::problem{slug=two-sum}\n\n<b>x</b>\n"))
	got := strings.Join(issueCodes(bad), ",")
	for _, want := range []string{"comment.heading", "comment.image", "comment.directive", "html.not_allowed"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in %s", want, got)
		}
	}
}
