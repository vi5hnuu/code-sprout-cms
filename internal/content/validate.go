package content

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"code-sprout-cms/internal/markdown"
	"code-sprout-cms/internal/platform/apperr"
)

const (
	maxTitle   = 200
	maxSlug    = 120
	maxSummary = 300
	maxTags    = 10
	maxMessage = 500
)

var (
	slugPattern   = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	localePattern = regexp.MustCompile(`^[a-z]{2}(-[A-Z]{2})?$`)
	articleLevels = []string{"BEGINNER", "INTERMEDIATE", "ADVANCED"}
)

// Slugify derives a URL slug from a title: accents stripped ("Ünïcödé" → "unicode"), anything
// that is not a-z/0-9 becomes a single hyphen, trimmed to maxSlug on a word boundary. Titles
// with no Latin letters or digits at all (e.g. fully Hindi) yield "" and the author must
// supply a slug.
func Slugify(title string) string {
	var b strings.Builder
	hyphen := false
	for _, r := range norm.NFKD.String(strings.ToLower(title)) {
		switch {
		case unicode.Is(unicode.Mn, r): // combining accent left by NFKD
			continue
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			hyphen = false
		case b.Len() > 0 && !hyphen:
			b.WriteByte('-')
			hyphen = true
		}
	}
	s := strings.TrimSuffix(b.String(), "-")
	if len(s) > maxSlug {
		s = s[:maxSlug]
		if i := strings.LastIndexByte(s, '-'); i > 0 {
			s = s[:i]
		}
	}
	return s
}

// normalize fills defaults and canonicalises the draft so equal drafts marshal identically.
func normalize(t Type, in *DraftInput) {
	in.Title = strings.TrimSpace(in.Title)
	in.Slug = strings.TrimSpace(in.Slug)
	if in.Slug == "" {
		in.Slug = Slugify(in.Title)
	}
	if in.Summary != nil {
		trimmed := strings.TrimSpace(*in.Summary)
		in.Summary = &trimmed
		if trimmed == "" {
			in.Summary = nil
		}
	}
	if in.Visibility == "" {
		in.Visibility = VisibilityPublic
	}
	if in.Access == "" {
		in.Access = AccessFree
	}
	slices.Sort(in.TagIDs)
	in.TagIDs = slices.Compact(in.TagIDs)
	if in.TagIDs == nil {
		in.TagIDs = []int64{}
	}
	// Type-specific fields only exist for their type; stray ones are dropped, not stored.
	if t == TypeArticle {
		if in.Article == nil {
			in.Article = &ArticleFields{}
		}
	} else {
		in.Article = nil
	}
	if t == TypeBlog {
		if in.Blog == nil {
			in.Blog = &BlogFields{}
		}
	} else {
		in.Blog = nil
	}
}

// validateDraft checks what must hold for any save. Content rules (Markdown issues, required
// summary, existing assets) are publish-time checks: drafts may be incomplete.
func validateDraft(in DraftInput) error {
	f := map[string]string{}
	if n := len([]rune(in.Title)); n == 0 || n > maxTitle {
		f["title"] = fmt.Sprintf("Title is required and at most %d characters.", maxTitle)
	}
	if in.Slug == "" {
		f["slug"] = "Add a slug: the title has no letters or digits to build one from."
	} else if len(in.Slug) > maxSlug || !slugPattern.MatchString(in.Slug) {
		f["slug"] = fmt.Sprintf("Use lower-case letters, digits and single hyphens (max %d).", maxSlug)
	}
	if in.Summary != nil && len([]rune(*in.Summary)) > maxSummary {
		f["summary"] = fmt.Sprintf("Summary is at most %d characters.", maxSummary)
	}
	if len(in.TagIDs) > maxTags {
		f["tagIds"] = fmt.Sprintf("At most %d tags.", maxTags)
	}
	if in.Visibility != VisibilityPublic && in.Visibility != VisibilityUnlisted && in.Visibility != VisibilityPrivate {
		f["visibility"] = "visibility must be PUBLIC, UNLISTED or PRIVATE."
	}
	if in.Access != AccessFree && in.Access != AccessPremium {
		f["access"] = "access must be FREE or PREMIUM."
	}
	if len(in.Body) > markdown.MaxBodyBytes {
		f["body"] = fmt.Sprintf("Body is at most %d bytes.", markdown.MaxBodyBytes)
	}
	if in.Message != nil && len(*in.Message) > maxMessage {
		f["message"] = fmt.Sprintf("Revision message is at most %d characters.", maxMessage)
	}
	if in.Article != nil && in.Article.Level != nil && !slices.Contains(articleLevels, *in.Article.Level) {
		f["article.level"] = "level must be BEGINNER, INTERMEDIATE or ADVANCED."
	}
	if len(f) > 0 {
		return apperr.ValidationFailed(f)
	}
	return nil
}

func validateCreate(in CreateInput) error {
	f := map[string]string{}
	if !localePattern.MatchString(in.Locale) {
		f["locale"] = "locale must look like en or pt-BR."
	}
	if len(f) > 0 {
		return apperr.ValidationFailed(f)
	}
	return nil
}

// PublishIssue is one reason an item cannot be submitted or published yet. Line is set for
// Markdown problems; Field for metadata problems.
type PublishIssue struct {
	Field   string `json:"field,omitempty"`
	Line    int    `json:"line,omitempty"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func notPublishable(issues []PublishIssue) error {
	return &apperr.Error{
		Kind: apperr.KindInvalid, Code: "content.not_publishable",
		Detail:  fmt.Sprintf("Fix %d issue(s) before submitting or publishing.", len(issues)),
		Details: map[string]any{"issues": issues},
	}
}

// metaIssues are the metadata rules a live page needs: search engines show the summary as
// the description, and the category decides the URL section and breadcrumb.
func metaIssues(m Meta) []PublishIssue {
	var out []PublishIssue
	if m.Summary == nil || len([]rune(*m.Summary)) < 50 {
		out = append(out, PublishIssue{Field: "summary", Code: "meta.summary",
			Message: "Add a summary of at least 50 characters; search results show it."})
	}
	if m.CategoryID == nil {
		out = append(out, PublishIssue{Field: "categoryId", Code: "meta.category", Message: "Choose a category."})
	}
	return out
}

func bodyIssues(a *markdown.Analysis) []PublishIssue {
	out := make([]PublishIssue, 0, len(a.Issues))
	for _, is := range a.Issues {
		out = append(out, PublishIssue{Line: is.Line, Code: is.Code, Message: is.Message})
	}
	return out
}

func marshalMeta(m Meta) []byte {
	b, err := json.Marshal(m)
	if err != nil {
		panic(fmt.Sprintf("marshal content meta: %v", err)) // Meta has only JSON-safe fields
	}
	return b
}
