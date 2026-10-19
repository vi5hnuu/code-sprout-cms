package video

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// isoDuration matches YouTube's ISO 8601 durations: P#DT#H#M#S (any part optional).
var isoDuration = regexp.MustCompile(`^P(?:(\d+)D)?(?:T(?:(\d+)H)?(?:(\d+)M)?(?:(\d+)S)?)?$`)

// ParseDuration turns "PT1H2M3S" into seconds. Live streams report "P0D" (0).
func ParseDuration(s string) (int, error) {
	m := isoDuration.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("not an ISO 8601 duration: %q", s)
	}
	total := 0
	for i, unit := range []int{86400, 3600, 60, 1} {
		if m[i+1] == "" {
			continue
		}
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return 0, err
		}
		total += n * unit
	}
	return total, nil
}

// Chapter is a labelled point in a video, from a "0:00 Intro" line of its description.
type Chapter struct {
	T     int    `json:"t"`
	Label string `json:"label"`
}

// chapterLine is a timestamp (m:ss or h:mm:ss) at the start of a line, then its label.
var chapterLine = regexp.MustCompile(`^\s*(?:(\d{1,2}):)?(\d{1,2}):(\d{2})\s*[-–—:|)]*\s*(.+?)\s*$`)

// ParseChapters reads YouTube-style chapters: they count only when the list starts at 0:00 and
// has at least three ascending entries, the same rule YouTube applies before showing them.
func ParseChapters(description string) []Chapter {
	var out []Chapter
	for _, line := range strings.Split(description, "\n") {
		m := chapterLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		h, _ := strconv.Atoi(m[1])
		mins, _ := strconv.Atoi(m[2])
		secs, _ := strconv.Atoi(m[3])
		t := h*3600 + mins*60 + secs
		if len(out) > 0 && t <= out[len(out)-1].T {
			continue
		}
		out = append(out, Chapter{T: t, Label: m[4]})
	}
	if len(out) < 3 || out[0].T != 0 {
		return nil
	}
	return out
}

// mdSpecial are characters with Markdown meaning at the start of a line or inline; escaping them
// keeps a description plain text whatever it contains (headings, lists, directives, HTML).
var mdSpecial = strings.NewReplacer(
	`\`, `\\`, "`", "\\`", `*`, `\*`, `_`, `\_`, `[`, `\[`, `]`, `\]`, `<`, `\<`, `>`, `\>`, `#`, `\#`, `|`, `\|`, `:`, `\:`, `~`, `\~`,
)

var bareURL = regexp.MustCompile(`https?://[^\s<>()]+[^\s<>().,;:!?'"]`)

// DescriptionMarkdown turns a video description into safe Markdown notes: the text escaped so
// nothing in it becomes a heading, list, directive or HTML, paragraphs kept, and bare links made
// clickable (the only markup a description gets).
func DescriptionMarkdown(description string) string {
	paras := strings.Split(strings.ReplaceAll(strings.TrimSpace(description), "\r\n", "\n"), "\n\n")
	for i, p := range paras {
		lines := strings.Split(p, "\n")
		for j, line := range lines {
			lines[j] = linkify(strings.TrimRight(line, " \t"))
		}
		// Two trailing spaces keep the description's own line breaks.
		paras[i] = strings.Join(lines, "  \n")
	}
	return strings.TrimSpace(strings.Join(paras, "\n\n"))
}

// linkify escapes a line and turns its URLs into Markdown autolinks (URLs are not escaped).
func linkify(line string) string {
	var b strings.Builder
	last := 0
	for _, loc := range bareURL.FindAllStringIndex(line, -1) {
		b.WriteString(mdSpecial.Replace(line[last:loc[0]]))
		b.WriteString("<" + line[loc[0]:loc[1]] + ">")
		last = loc[1]
	}
	b.WriteString(mdSpecial.Replace(line[last:]))
	return b.String()
}

const maxSummary = 280

// Summary is the description's first meaningful paragraph (skipping chapter lists and links),
// cut at a word boundary; nil when there is none.
func Summary(description string) *string {
	for _, p := range strings.Split(strings.ReplaceAll(description, "\r\n", "\n"), "\n") {
		p = strings.TrimSpace(p)
		if p == "" || chapterLine.MatchString(p) || bareURL.MatchString(p) || strings.HasPrefix(p, "#") {
			continue
		}
		if utf8.RuneCountInString(p) > maxSummary {
			r := []rune(p)[:maxSummary]
			cut := strings.LastIndexByte(string(r), ' ')
			if cut < maxSummary/2 {
				cut = len(string(r))
			}
			p = strings.TrimRight(string(r)[:cut], " ,;:-") + "…"
		}
		return &p
	}
	return nil
}
