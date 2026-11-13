// Package locales is the single list of languages content can be written in. A page's language
// is part of its public URL (/{locale}/… for anything but English; see content.PublicPath and
// the web's contentPath), so the list decides both which locales a page may have and which
// path prefixes no top-level section may take.
//
// Adding a language is deliberate work across the system (the web's interface messages, fonts,
// routing for its pages), which is why this is code and not configuration.
package locales

import (
	"slices"
	"strings"
)

// Default is the language of a page that has no prefix in its URL.
const Default = "en"

var content = []string{"en", "hi"}

// IsContent reports whether pages may be written in code.
func IsContent(code string) bool { return slices.Contains(content, code) }

// List returns the content languages, for error messages and docs.
func List() string { return strings.Join(content, ", ") }
