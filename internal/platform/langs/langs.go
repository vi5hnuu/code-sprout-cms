// Package langs is the single list of programming languages users can run code in. Markdown
// fence names, problem templates and judge requests all use these identifiers; ls-judge maps
// each to its installed runtime.
package langs

import (
	"slices"
	"strings"
)

var runnable = map[string]bool{
	"c": true, "cpp": true, "csharp": true, "go": true, "java": true, "javascript": true,
	"kotlin": true, "python": true, "rust": true, "typescript": true,
}

func Runnable(lang string) bool { return runnable[lang] }

// List returns the runnable languages, sorted (for error messages and docs).
func List() string {
	out := make([]string, 0, len(runnable))
	for l := range runnable {
		out = append(out, l)
	}
	slices.Sort(out)
	return strings.Join(out, ", ")
}

// defaultRuntimes pins each language to an exact ls-judge runtime id. Sending a bare language
// name would let the judge pick whichever version of it was installed most recently, so a
// problem's verdicts could change when the judge's catalogue does. Override per deployment
// with JUDGE_RUNTIMES (see config).
var defaultRuntimes = map[string]string{
	"c": "c-17", "cpp": "cpp-20", "csharp": "csharp-13", "go": "go-1.24", "java": "java-21",
	"javascript": "javascript-22", "kotlin": "kotlin-2", "python": "python-3.12", "rust": "rust-1.87",
	"typescript": "typescript-5",
}

// DefaultRuntimes returns a copy of the built-in language → judge runtime pins.
func DefaultRuntimes() map[string]string {
	out := make(map[string]string, len(defaultRuntimes))
	for k, v := range defaultRuntimes {
		out[k] = v
	}
	return out
}
