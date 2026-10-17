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
