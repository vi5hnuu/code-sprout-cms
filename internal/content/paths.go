package content

import (
	"context"
	"net/url"

	"code-sprout-cms/internal/content/contentdb"
	"code-sprout-cms/internal/platform/db"
)

// PublicPath is the canonical site path of an item. It is the one definition of the URL
// scheme; the web app routes the same way, and sitemaps and IndexNow are built from it.
//
//	ARTICLE     /{section}/{slug}  (section = root category slug; /articles/{slug} if none)
//	BLOG        /blog/{slug}
//	PROBLEM     /problems/{slug}
//	TOPIC_PAGE  /topics/{slug}
//	QUIZ        /quiz/{slug}
//
// Locales other than "en" are prefixed: /hi/blog/{slug}.
func PublicPath(t Type, locale, section, slug string) string {
	s := url.PathEscape(slug)
	var p string
	switch t {
	case TypeArticle:
		if section == "" {
			section = "articles"
		}
		p = "/" + url.PathEscape(section) + "/" + s
	case TypeBlog:
		p = "/blog/" + s
	case TypeProblem:
		p = "/problems/" + s
	case TypeTopicPage:
		p = "/topics/" + s
	case TypeQuiz:
		p = "/quiz/" + s
	}
	if locale != "" && locale != "en" {
		p = "/" + locale + p
	}
	return p
}

// PathOf returns an item's current canonical path (also for items no longer live, so the old
// URL can be revalidated or reported as gone).
func (r *Reader) PathOf(ctx context.Context, id int64) (string, bool, error) {
	pp, err := contentdb.New(r.db.Replica).GetPathParts(ctx, id)
	if db.IsNoRows(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return PublicPath(Type(pp.Type), pp.Locale, pp.Section, pp.Slug), true, nil
}
