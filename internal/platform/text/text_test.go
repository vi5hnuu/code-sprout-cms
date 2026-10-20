package text

import (
	"testing"
	"unicode/utf8"
)

func TestClip(t *testing.T) {
	cases := []struct {
		in   string
		max  int
		want string
	}{
		{"hello", 10, "hello"},
		{"hello", 3, "hel"},
		{"héllo", 2, "h"},    // "é" is two bytes; never cut in half
		{"日本語", 4, "日"},      // three-byte characters
		{"a\x00b", 10, "ab"}, // NUL is dropped
		{"a\xffb", 10, "a�b"},
	}
	for _, c := range cases {
		got := Clip(c.in, c.max)
		if got != c.want || !utf8.ValidString(got) {
			t.Errorf("Clip(%q, %d) = %q, want %q", c.in, c.max, got, c.want)
		}
	}
}
