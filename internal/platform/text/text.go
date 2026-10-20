// Package text holds small string helpers shared by domains that store free-form text.
package text

import (
	"strings"
	"unicode/utf8"
)

// Clip makes s safe to store in a Postgres text column and at most maxBytes long: invalid
// UTF-8 becomes U+FFFD, NUL bytes (which Postgres rejects) are dropped, and the cut never
// splits a character. Meant for text the system does not control, such as compiler output
// or error messages from other services.
func Clip(s string, maxBytes int) string {
	s = strings.ReplaceAll(strings.ToValidUTF8(s, "�"), "\x00", "")
	if len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
