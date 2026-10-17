package markdown

import (
	"strconv"
	"strings"
	"unicode"
)

// Slugger generates heading anchor ids. The algorithm is part of the Sprout Markdown spec
// because the TOC stored by the API must link to the ids the clients render:
//
//  1. lower-case the heading text;
//  2. keep letters, digits, '-' and '_'; turn each space into '-'; drop everything else;
//  3. an empty result becomes "section";
//  4. repeats get "-1", "-2", ... in document order.
//
// This matches github-slugger for ordinary headings, which keeps anchors familiar.
type Slugger struct{ seen map[string]int }

func NewSlugger() *Slugger { return &Slugger{seen: map[string]int{}} }

func (s *Slugger) Slug(text string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(text) {
		switch {
		case r == ' ':
			b.WriteByte('-')
		case r == '-' || r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.Is(unicode.Mn, r):
			b.WriteRune(r)
		}
	}
	base := b.String()
	if base == "" {
		base = "section"
	}
	slug := base
	for {
		n, taken := s.seen[slug]
		if !taken {
			break
		}
		s.seen[slug] = n + 1
		slug = base + "-" + strconv.Itoa(n+1)
	}
	s.seen[slug] = 0
	return slug
}
