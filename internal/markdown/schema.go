package markdown

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// The directive whitelist. A directive not listed here is rejected, so nothing reaches a
// renderer that the web and Android clients do not both implement. Adding a directive means:
// add it here, document it in docs/sprout-markdown.md, add a fixture under spec/markdown/,
// and ship the renderers before content uses it.

type attrSpec struct {
	required bool
	check    func(string) error
}

type directiveSpec struct {
	form  DirectiveForm
	attrs map[string]attrSpec
	// labelRequired: the [label] is the visible title (e.g. the summary of a details block).
	labelRequired bool
	// fencedCodeOnly: the container may hold nothing but fenced code blocks (code tabs).
	fencedCodeOnly bool
}

var (
	slugPattern    = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	youtubePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)
	// highlight="1,3-5": lines to emphasise in a code block.
	highlightPattern = regexp.MustCompile(`^\d+(-\d+)?(,\d+(-\d+)?)*$`)
)

func oneOf(values ...string) func(string) error {
	return func(v string) error {
		for _, allowed := range values {
			if v == allowed {
				return nil
			}
		}
		return fmt.Errorf("must be one of %s", strings.Join(values, ", "))
	}
}

func matches(re *regexp.Regexp, what string) func(string) error {
	return func(v string) error {
		if !re.MatchString(v) {
			return fmt.Errorf("must be %s", what)
		}
		return nil
	}
}

func positiveID(v string) error {
	if n, err := strconv.ParseInt(v, 10, 64); err != nil || n <= 0 {
		return fmt.Errorf("must be a positive numeric id")
	}
	return nil
}

func nonEmpty(v string) error {
	if strings.TrimSpace(v) == "" || v == "true" {
		return fmt.Errorf("must not be empty")
	}
	return nil
}

var directives = map[string]directiveSpec{
	// :::callout[Optional title]{kind=tip}
	"callout": {
		form:  ContainerDirective,
		attrs: map[string]attrSpec{"kind": {check: oneOf("note", "tip", "info", "warning", "danger")}},
	},
	// :::details[Show solution]: collapsed by default.
	"details": {form: ContainerDirective, labelRequired: true},
	// :::code-tabs wrapping one fenced block per language, shown as tabs (GFG-style).
	"code-tabs": {form: ContainerDirective, fencedCodeOnly: true},
	// ::problem{slug=two-sum}: a "Try it yourself" card linking a practice problem.
	"problem": {
		form:  LeafDirective,
		attrs: map[string]attrSpec{"slug": {required: true, check: matches(slugPattern, "a problem slug")}},
	},
	// ::quiz{id=123}
	"quiz": {
		form:  LeafDirective,
		attrs: map[string]attrSpec{"id": {required: true, check: positiveID}},
	},
	// ::video{file=987} (uploaded) or ::video{youtube=dQw4w9WgXcQ}; exactly one source.
	"video": {
		form: LeafDirective,
		attrs: map[string]attrSpec{
			"file":    {check: positiveID},
			"youtube": {check: matches(youtubePattern, "an 11-character YouTube id")},
			"title":   {check: nonEmpty},
		},
	},
}

// Attributes allowed in a fenced code block's info string after the language:
//
//	```python runnable title="Binary search" stdin="5\n1 2 3 4 5" highlight="3-4"
var fenceAttrs = map[string]attrSpec{
	"runnable":  {check: oneOf("true")},
	"title":     {check: nonEmpty},
	"stdin":     {},
	"highlight": {check: matches(highlightPattern, "line numbers like 1,3-5")},
}

// RunnableLanguages are the fence languages that may carry `runnable`. Fence names follow the
// highlighter's names; the run endpoint maps them to judge runtimes and re-checks availability.
var RunnableLanguages = map[string]bool{
	"c": true, "cpp": true, "csharp": true, "go": true, "java": true, "javascript": true,
	"kotlin": true, "python": true, "rust": true, "typescript": true,
}
