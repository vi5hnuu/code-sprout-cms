// Package markdown validates and analyses content bodies written in Sprout Markdown:
// CommonMark + GFM + a whitelist of directives (docs/sprout-markdown.md).
//
// It never renders. Rendering happens in the clients (remark on web, a Compose renderer on
// Android); this package is the gatekeeper that guarantees every stored body is something
// those renderers understand and that nothing unsafe (raw HTML, script URLs) gets through,
// and it derives what the read path needs without re-parsing: TOC, plain text for search,
// reading time, and the assets/problems a body references.
package markdown

import (
	"bytes"
	"fmt"
	"math"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/text"

	"code-sprout-cms/internal/platform/langs"
)

const (
	// MaxBodyBytes bounds a single body. A long GFG-style article with every example in seven
	// languages is ~150 KiB; 1 MiB leaves ample room while bounding parse cost.
	MaxBodyBytes = 1 << 20
	// maxContainerDepth limits directive nesting (callout inside details inside ...).
	maxContainerDepth = 3
	wordsPerMinute    = 200
	assetScheme       = "asset:"
)

// Issue is a problem an author must fix before the body can be submitted or published.
// Drafts may be saved with issues; the studio shows them inline by line number.
type Issue struct {
	Line    int    `json:"line"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Heading struct {
	Level int    `json:"level"`
	Text  string `json:"text"`
	ID    string `json:"id"` // anchor id; clients must generate the same (see Slugger)
}

type Snippet struct {
	Line     int    `json:"line"`
	Language string `json:"language"`
}

type Analysis struct {
	Issues         []Issue   `json:"issues"`
	TOC            []Heading `json:"toc"` // h2 and h3
	PlainText      string    `json:"-"`
	WordCount      int       `json:"wordCount"`
	ReadingMinutes int       `json:"readingMinutes"`
	Snippets       []Snippet `json:"snippets"` // runnable code blocks
	AssetIDs       []int64   `json:"assetIds"` // sorted, unique
	ProblemSlugs   []string  `json:"problemSlugs"`
}

func (a *Analysis) Valid() bool { return len(a.Issues) == 0 }

var md = goldmark.New(goldmark.WithExtensions(extension.GFM, directiveExtension{}))

// profile selects which constructs a body may use.
type profile struct {
	maxBytes   int
	restricted bool // comments: no directives, images or headings
}

var (
	contentProfile = profile{maxBytes: MaxBodyBytes}
	commentProfile = profile{maxBytes: MaxCommentBytes, restricted: true}
)

// MaxCommentBytes bounds a comment (the database enforces the same limit).
const MaxCommentBytes = 20000

// Analyze parses a content body and reports issues plus derived data. Safe for concurrent use.
func Analyze(body []byte) *Analysis { return analyze(body, contentProfile) }

// AnalyzeComment checks a reader comment: plain Markdown with code and links, but no
// directives, images or headings, so comments cannot impersonate article content.
func AnalyzeComment(body []byte) *Analysis { return analyze(body, commentProfile) }

func analyze(body []byte, prof profile) *Analysis {
	a := &Analysis{Issues: []Issue{}, TOC: []Heading{}, Snippets: []Snippet{}, AssetIDs: []int64{}, ProblemSlugs: []string{}}
	if len(body) > prof.maxBytes {
		a.Issues = append(a.Issues, Issue{Line: 1, Code: "body.too_large",
			Message: fmt.Sprintf("Body is %d bytes; the limit is %d.", len(body), prof.maxBytes)})
		return a
	}
	w := walker{a: a, src: body, lines: lineStarts(body), slugger: NewSlugger(), assets: map[int64]bool{}, prof: prof}
	doc := md.Parser().Parse(text.NewReader(body))
	_ = ast.Walk(doc, w.visit)

	a.PlainText = collapseBlankLines(w.plain.String())
	a.WordCount = len(strings.Fields(a.PlainText))
	a.ReadingMinutes = max(1, int(math.Ceil(float64(a.WordCount)/wordsPerMinute)))
	for id := range w.assets {
		a.AssetIDs = append(a.AssetIDs, id)
	}
	slices.Sort(a.AssetIDs)
	slices.Sort(a.ProblemSlugs)
	a.ProblemSlugs = slices.Compact(a.ProblemSlugs)
	sort.SliceStable(a.Issues, func(i, j int) bool { return a.Issues[i].Line < a.Issues[j].Line })
	// One problem can surface as several identical issues (an inline <b>...</b> is two HTML
	// nodes); the author only needs to see it once.
	a.Issues = slices.Compact(a.Issues)
	return a
}

type walker struct {
	prof    profile
	a       *Analysis
	src     []byte
	lines   []int
	slugger *Slugger
	assets  map[int64]bool
	plain   bytes.Buffer
}

func (w *walker) issue(n ast.Node, code, format string, args ...any) {
	w.a.Issues = append(w.a.Issues, Issue{Line: w.lineOf(n), Code: code, Message: fmt.Sprintf(format, args...)})
}

func (w *walker) visit(n ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		if n.Type() == ast.TypeBlock {
			w.plain.WriteByte('\n')
		}
		return ast.WalkContinue, nil
	}
	switch n := n.(type) {
	case *ast.HTMLBlock, *ast.RawHTML:
		w.issue(n, "html.not_allowed", "Raw HTML is not allowed; use Markdown or a directive.")
		return ast.WalkSkipChildren, nil
	case *ast.Heading:
		if w.prof.restricted {
			w.issue(n, "comment.heading", "Headings are not available in comments.")
			return ast.WalkSkipChildren, nil
		}
		w.heading(n)
	case *ast.FencedCodeBlock:
		w.fence(n)
		return ast.WalkSkipChildren, nil
	case *ast.CodeBlock:
		w.issue(n, "code.indented", "Use a fenced code block (```lang) so the language is known.")
		return ast.WalkSkipChildren, nil
	case *ast.Image:
		if w.prof.restricted {
			w.issue(n, "comment.image", "Images are not available in comments.")
			return ast.WalkSkipChildren, nil
		}
		w.image(n)
		return ast.WalkSkipChildren, nil
	case *ast.Link:
		w.checkURL(n, string(n.Destination))
	case *ast.AutoLink:
		w.checkURL(n, string(n.URL(w.src)))
	case *Directive:
		if w.prof.restricted {
			w.issue(n, "comment.directive", "Embeds are not available in comments.")
			return ast.WalkSkipChildren, nil
		}
		w.directive(n)
	case *ast.Text:
		w.plain.Write(n.Segment.Value(w.src))
		if n.SoftLineBreak() || n.HardLineBreak() {
			w.plain.WriteByte(' ')
		}
	case *ast.String:
		w.plain.Write(n.Value)
	}
	return ast.WalkContinue, nil
}

func (w *walker) heading(h *ast.Heading) {
	if h.Level == 1 {
		w.issue(h, "heading.h1", "Start sections at ## — the title is the page's only top-level heading.")
	}
	title := strings.TrimSpace(inlineText(h, w.src))
	id := w.slugger.Slug(title)
	if h.Level == 2 || h.Level == 3 {
		w.a.TOC = append(w.a.TOC, Heading{Level: h.Level, Text: title, ID: id})
	}
}

func (w *walker) fence(f *ast.FencedCodeBlock) {
	lang, attrs, err := parseFenceInfo(f, w.src)
	if err != "" {
		w.issue(f, "code.bad_info", "Code block options: %s.", err)
		return
	}
	for _, key := range sortedKeys(attrs) {
		val := attrs[key]
		spec, ok := fenceAttrs[key]
		if !ok {
			w.issue(f, "code.unknown_option", "Unknown code block option %q.", key)
			continue
		}
		if spec.check != nil {
			if cerr := spec.check(val); cerr != nil {
				w.issue(f, "code.bad_option", "Code block option %q %v.", key, cerr)
			}
		}
	}
	if attrs["runnable"] == "true" {
		if !langs.Runnable(lang) {
			w.issue(f, "code.not_runnable", "Language %q cannot be run; runnable languages: %s.", lang, langs.List())
			return
		}
		w.a.Snippets = append(w.a.Snippets, Snippet{Line: w.lineOf(f), Language: lang})
	}
}

func (w *walker) image(img *ast.Image) {
	alt := strings.TrimSpace(inlineText(img, w.src))
	w.plain.WriteString(alt + "\n") // alt text is good search text
	if alt == "" {
		w.issue(img, "image.no_alt", "Images need alt text (it is read by screen readers and search engines).")
	}
	dest := string(img.Destination)
	id, ok := strings.CutPrefix(dest, assetScheme)
	n, err := strconv.ParseInt(id, 10, 64)
	if !ok || err != nil || n <= 0 {
		w.issue(img, "image.not_asset", "Images must be uploaded (asset:<id>), not linked from %q.", dest)
		return
	}
	w.assets[n] = true
}

// Links may point at the web (http/https), an e-mail address, or a page/anchor on this site.
// Anything else (javascript:, data:, file:) is rejected outright.
func (w *walker) checkURL(n ast.Node, raw string) {
	if raw == "" || strings.HasPrefix(raw, "#") || (strings.HasPrefix(raw, "/") && !strings.HasPrefix(raw, "//")) {
		return
	}
	u, err := url.Parse(raw)
	if err == nil {
		switch strings.ToLower(u.Scheme) {
		case "http", "https", "mailto":
			return
		case "":
			if u.Host == "" { // relative path like "binary-search"
				return
			}
		}
	}
	w.issue(n, "link.unsafe", "Link %q is not allowed; use http(s), mailto, or a site path.", raw)
}

func (w *walker) directive(d *Directive) {
	spec, ok := directives[d.Name]
	if !ok {
		w.issue(d, "directive.unknown", "Unknown directive %q; available: %s.", d.Name, joinKeys(directives))
		return
	}
	if spec.form != d.Form {
		want := map[DirectiveForm]string{LeafDirective: "::" + d.Name, ContainerDirective: ":::" + d.Name + " ... :::"}[spec.form]
		w.issue(d, "directive.wrong_form", "Write %q as %s.", d.Name, want)
		return
	}
	if d.AttrErr != "" {
		w.issue(d, "directive.bad_attributes", "%s: %s.", d.Name, d.AttrErr)
		return
	}
	if spec.labelRequired && d.Label == "" {
		w.issue(d, "directive.no_label", "%s needs a [label], e.g. :::%s[Show answer].", d.Name, d.Name)
	}
	for _, key := range sortedKeys(d.Attrs) {
		val := d.Attrs[key]
		as, ok := spec.attrs[key]
		if !ok {
			w.issue(d, "directive.unknown_attribute", "%s does not accept %q.", d.Name, key)
			continue
		}
		if as.check != nil {
			if err := as.check(val); err != nil {
				w.issue(d, "directive.bad_attribute", "%s %s %v.", d.Name, key, err)
			}
		}
	}
	for _, key := range sortedKeys(spec.attrs) {
		if _, present := d.Attrs[key]; spec.attrs[key].required && !present {
			w.issue(d, "directive.missing_attribute", "%s requires %s=.", d.Name, key)
		}
	}
	if d.Form == ContainerDirective && containerDepth(d) > maxContainerDepth {
		w.issue(d, "directive.too_deep", "Directives may nest at most %d levels.", maxContainerDepth)
	}
	if spec.fencedCodeOnly {
		if d.ChildCount() == 0 {
			w.issue(d, "directive.empty", "%s must contain at least one code block.", d.Name)
		}
		for c := d.FirstChild(); c != nil; c = c.NextSibling() {
			if c.Kind() != ast.KindFencedCodeBlock {
				w.issue(c, "directive.code_only", "%s may only contain fenced code blocks.", d.Name)
			}
		}
	}

	w.plain.WriteString(d.Label + "\n" + d.Attrs["title"] + "\n")
	switch d.Name {
	case "problem":
		if slug := d.Attrs["slug"]; slugPattern.MatchString(slug) {
			w.a.ProblemSlugs = append(w.a.ProblemSlugs, slug)
		}
	case "video":
		_, file := d.Attrs["file"]
		_, yt := d.Attrs["youtube"]
		if file == yt {
			w.issue(d, "directive.video_source", "video needs exactly one of file= or youtube=.")
		}
		if id, err := strconv.ParseInt(d.Attrs["file"], 10, 64); file && err == nil && id > 0 {
			w.assets[id] = true
		}
	}
}

func containerDepth(n ast.Node) int {
	depth := 0
	for p := n; p != nil; p = p.Parent() {
		if d, ok := p.(*Directive); ok && d.Form == ContainerDirective {
			depth++
		}
	}
	return depth
}

// parseFenceInfo splits "python runnable title=\"x\"" into the language and its options.
func parseFenceInfo(f *ast.FencedCodeBlock, src []byte) (lang string, attrs map[string]string, errMsg string) {
	if f.Info == nil {
		return "", map[string]string{}, ""
	}
	info := strings.TrimSpace(string(f.Info.Segment.Value(src)))
	lang, rest, _ := strings.Cut(info, " ")
	attrs, errMsg = parseAttrs(strings.TrimSpace(rest))
	return strings.ToLower(lang), attrs, errMsg
}

// inlineText concatenates the literal text under n (used for heading titles and alt text).
func inlineText(n ast.Node, src []byte) string {
	var b strings.Builder
	_ = ast.Walk(n, func(c ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch t := c.(type) {
		case *ast.Text:
			b.Write(t.Segment.Value(src))
			if t.SoftLineBreak() {
				b.WriteByte(' ')
			}
		case *ast.String:
			b.Write(t.Value)
		}
		return ast.WalkContinue, nil
	})
	return b.String()
}

// collapseBlankLines trims each line and drops empty ones, so the search text stores one line
// per block without the padding that nested structure leaves behind.
func collapseBlankLines(s string) string {
	var b strings.Builder
	for line := range strings.Lines(s) {
		if line = strings.TrimSpace(line); line != "" {
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(line)
		}
	}
	return b.String()
}

func lineStarts(src []byte) []int {
	starts := []int{0}
	for i, c := range src {
		if c == '\n' {
			starts = append(starts, i+1)
		}
	}
	return starts
}

// lineOf maps a node to its 1-based source line via the first source offset found in it or,
// failing that, in its nearest ancestor (empty inline nodes carry no segment of their own).
func (w *walker) lineOf(n ast.Node) int {
	for p := n; p != nil; p = p.Parent() {
		if off, ok := firstOffset(p); ok {
			return sort.SearchInts(w.lines, off+1)
		}
	}
	return 1
}

func firstOffset(n ast.Node) (int, bool) {
	switch t := n.(type) {
	case *Directive:
		return t.Offset, true
	case *ast.Text:
		return t.Segment.Start, true
	case *ast.FencedCodeBlock:
		if t.Info != nil {
			return t.Info.Segment.Start, true
		}
	}
	if n.Type() == ast.TypeBlock && n.Lines().Len() > 0 {
		return n.Lines().At(0).Start, true
	}
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		if off, ok := firstOffset(c); ok {
			return off, true
		}
	}
	return 0, false
}

// sortedKeys gives map iteration a fixed order. Output must be deterministic: the fixtures are
// a conformance spec, and authors should see issues in a stable order.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func joinKeys[V any](m map[string]V) string { return strings.Join(sortedKeys(m), ", ") }
