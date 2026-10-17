package markdown

import (
	"fmt"
	"strings"
)

type directiveHead struct {
	name    string
	label   string
	attrs   map[string]string
	attrErr string
}

// parseDirectiveHead parses `name[label]{attrs}` (label and attrs optional). ok=false means the
// line is not a directive at all and stays ordinary text; a malformed {attrs} block still yields
// a directive, with attrErr set so the author gets a precise message instead of silent text.
func parseDirectiveHead(s []byte) (directiveHead, bool) {
	var h directiveHead
	i := 0
	for i < len(s) && isNameChar(s[i], i == 0) {
		i++
	}
	if i == 0 {
		return h, false
	}
	h.name = string(s[:i])

	if i < len(s) && s[i] == '[' {
		end := strings.IndexByte(string(s[i:]), ']')
		if end < 0 {
			return h, false
		}
		h.label = strings.TrimSpace(string(s[i+1 : i+end]))
		i += end + 1
	}
	if i < len(s) && s[i] == '{' {
		end := closingBrace(s[i:])
		if end < 0 {
			h.attrErr = "unterminated {...} attributes"
			return h, true
		}
		h.attrs, h.attrErr = parseAttrs(string(s[i+1 : i+end]))
		i += end + 1
	}
	if strings.TrimSpace(string(s[i:])) != "" {
		return h, false
	}
	return h, true
}

func isNameChar(c byte, first bool) bool {
	if c >= 'a' && c <= 'z' {
		return true
	}
	return !first && (c == '-' || (c >= '0' && c <= '9'))
}

// closingBrace finds the '}' that ends the attribute block, skipping braces inside quotes.
func closingBrace(s []byte) int {
	inQuote := false
	for i := 1; i < len(s); i++ {
		switch s[i] {
		case '"':
			inQuote = !inQuote
		case '\\':
			i++
		case '}':
			if !inQuote {
				return i
			}
		}
	}
	return -1
}

// parseAttrs reads space-separated `key=value` or `key="quoted value"` pairs. Quoted values
// support \n (newline), \" and \\ escapes. Bare keys are true-valued flags.
func parseAttrs(s string) (map[string]string, string) {
	attrs := map[string]string{}
	i := 0
	for {
		for i < len(s) && s[i] == ' ' {
			i++
		}
		if i >= len(s) {
			return attrs, ""
		}
		start := i
		for i < len(s) && s[i] != '=' && s[i] != ' ' {
			i++
		}
		key := s[start:i]
		if !validAttrKey(key) {
			return nil, fmt.Sprintf("invalid attribute name %q", key)
		}
		if _, dup := attrs[key]; dup {
			return nil, fmt.Sprintf("attribute %q given twice", key)
		}
		if i >= len(s) || s[i] != '=' {
			attrs[key] = "true"
			continue
		}
		i++ // '='
		if i < len(s) && s[i] == '"' {
			var b strings.Builder
			i++
			closed := false
			for i < len(s) {
				c := s[i]
				if c == '\\' && i+1 < len(s) {
					if s[i+1] == 'n' {
						b.WriteByte('\n')
					} else {
						b.WriteByte(s[i+1])
					}
					i += 2
					continue
				}
				if c == '"' {
					closed = true
					i++
					break
				}
				b.WriteByte(c)
				i++
			}
			if !closed {
				return nil, fmt.Sprintf("unterminated quoted value for %q", key)
			}
			attrs[key] = b.String()
			continue
		}
		start = i
		for i < len(s) && s[i] != ' ' {
			i++
		}
		if start == i {
			return nil, fmt.Sprintf("attribute %q has no value", key)
		}
		attrs[key] = s[start:i]
	}
}

func validAttrKey(k string) bool {
	if k == "" {
		return false
	}
	for i := 0; i < len(k); i++ {
		if !isNameChar(k[i], i == 0) {
			return false
		}
	}
	return true
}
