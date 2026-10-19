package problem

import (
	"context"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5"

	"code-sprout-cms/internal/content"
	"code-sprout-cms/internal/problem/problemdb"
)

// maxTitleWords bounds the phrases tried per video title: problem titles are short, and the
// candidate count stays tiny (≤ words × maxTitleWords) however long a video title is.
const maxTitleWords = 8

// LinkVideoByTitle links videoID as the video solution of the live problem whose exact title
// appears in videoTitle. Problems keep what they have: an editor's chosen video, or an earlier
// automatic link (so "Part 1" and "Part 2" of one problem never flip the link back and forth).
// The longest matching title wins ("Jump Game II" over "Jump Game"). Returns the linked ids.
//
// Cost does not grow with the catalogue: the candidate phrases are matched in one query on the
// normalised-title index.
func (s *Service) LinkVideoByTitle(ctx context.Context, tx pgx.Tx, videoID int64, videoTitle string) ([]int64, error) {
	phrases := TitlePhrases(videoTitle)
	if len(phrases) == 0 {
		return nil, nil
	}
	matches, err := content.LiveProblemsTitled(ctx, tx, phrases)
	if err != nil || len(matches) == 0 {
		return nil, err
	}
	var best []int64
	bestWords := 0
	for _, m := range matches {
		n := len(strings.Fields(m.Norm))
		switch {
		case n > bestWords:
			best, bestWords = []int64{m.ID}, n
		case n == bestWords:
			best = append(best, m.ID)
		}
	}
	open, err := problemdb.New(tx).WithoutChosenVideo(ctx, best)
	if err != nil || len(open) == 0 {
		return nil, err
	}
	linked, err := content.WithLinkOfKind(ctx, tx, open, "VIDEO")
	if err != nil {
		return nil, err
	}
	var out []int64
	for _, id := range open {
		if linked[id] {
			continue
		}
		if err := content.SetLinks(ctx, tx, id, "VIDEO", []int64{videoID}); err != nil {
			return nil, err
		}
		if err := content.Touched(ctx, tx, id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, nil
}

// NormTitle is the Go twin of the database's norm_title(): lower case, every run of characters
// that are not letters or digits collapsed to one space, trimmed.
func NormTitle(s string) string {
	var b strings.Builder
	space := false
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			b.WriteRune(r)
			space = false
			continue
		}
		space = true
	}
	return b.String()
}

// TitlePhrases lists every run of up to maxTitleWords consecutive words of a title, normalised:
// the strings a problem title could equal. Single short words ("a", "of") are left out.
func TitlePhrases(title string) []string {
	words := strings.Fields(NormTitle(title))
	seen := map[string]bool{}
	var out []string
	for i := range words {
		for n := 1; n <= maxTitleWords && i+n <= len(words); n++ {
			p := strings.Join(words[i:i+n], " ")
			if n == 1 && len(p) < 4 {
				continue
			}
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	return out
}
