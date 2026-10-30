package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"code-sprout-cms/internal/asset"
	"code-sprout-cms/internal/auth"
	"code-sprout-cms/internal/collection"
	"code-sprout-cms/internal/content"
	"code-sprout-cms/internal/platform/apperr"
	"code-sprout-cms/internal/platform/db"
	"code-sprout-cms/internal/platform/page"
	"code-sprout-cms/internal/problem"
	"code-sprout-cms/internal/taxonomy"
	"code-sprout-cms/internal/user"
)

//go:embed content
var bodies embed.FS

type seeder struct {
	users       *user.Service
	taxonomy    *taxonomy.Service
	assets      *asset.Service
	contents    *content.Service
	problems    *problem.Service
	collections *collection.Service
	db          *db.DB

	authors    map[string]auth.Principal // author key → acting principal
	categories map[string]int64          // path → id
	tags       map[string]int64          // "kind:slug" → id
	existing   map[string]content.Summary
	ids        map[string]int64 // "type:slug" → content id, for collections and the daily problem
}

func (s *seeder) run(ctx context.Context) error {
	s.authors, s.categories, s.tags = map[string]auth.Principal{}, map[string]int64{}, map[string]int64{}
	s.existing, s.ids = map[string]content.Summary{}, map[string]int64{}

	steps := []struct {
		name string
		fn   func(context.Context) error
	}{
		{"authors", s.seedAuthors},
		{"categories", s.seedCategories},
		{"tags", s.seedTags},
		// Problems first: articles embed them (::problem), and an embed must point at a live one.
		{"problems", s.seedProblems},
		{"pages", s.seedPages},
		{"collections", s.seedCollections},
		{"problem of the day", s.seedProblemOfTheDay},
	}
	for _, st := range steps {
		if err := st.fn(ctx); err != nil {
			return fmt.Errorf("%s: %w", st.name, err)
		}
		slog.Info("seeded", "step", st.name)
	}
	return nil
}

// ── Authors ──────────────────────────────────────────────────────────────────

// Sample authors are local accounts with a "seed:" subject, so they can never collide with a
// real auth user. They act as site owners (IsAdmin) to publish without role grants.
func (s *seeder) seedAuthors(ctx context.Context) error {
	for _, a := range authors {
		sub := "seed:" + a.key
		id, err := s.users.ResolveID(ctx, sub, a.name)
		if err != nil {
			return err
		}
		handle, bio := a.handle, a.bio
		if _, err := s.users.UpdateProfile(ctx, id, user.UpdateProfile{Handle: &handle, DisplayName: a.name, Bio: &bio}); err != nil {
			return fmt.Errorf("profile %s: %w", a.handle, err)
		}
		p := auth.Principal{UserID: id, Subject: sub, IsAdmin: true}
		s.authors[a.key] = p
		if err := s.loadExisting(ctx, p); err != nil {
			return err
		}
	}
	return nil
}

// loadExisting indexes what an author already has, so a re-run resumes instead of duplicating.
func (s *seeder) loadExisting(ctx context.Context, p auth.Principal) error {
	req := page.Request[content.ListCursor]{Limit: page.MaxLimit}
	for {
		pg, err := s.contents.ListMine(ctx, p, "", req)
		if err != nil {
			return err
		}
		for _, it := range pg.Items {
			s.existing[key(it.Type, it.Slug)] = it
		}
		if pg.NextCursor == nil || len(pg.Items) == 0 {
			return nil
		}
		last := pg.Items[len(pg.Items)-1]
		req.Key = &content.ListCursor{UpdatedAt: last.UpdatedAt, ID: last.ID}
	}
}

func key(t content.Type, slug string) string { return string(t) + ":" + slug }

// ── Taxonomy ─────────────────────────────────────────────────────────────────

func (s *seeder) seedCategories(ctx context.Context) error {
	admin := s.authors[authors[0].key]
	for i, c := range categories {
		existing, err := s.taxonomy.ByURLPath(ctx, c.path)
		if err == nil {
			s.categories[c.path] = existing.ID
			continue
		}
		if !isNotFound(err) {
			return err
		}
		var parent *int64
		slug := c.path
		if at := strings.LastIndexByte(c.path, '/'); at >= 0 {
			id, ok := s.categories[c.path[:at]]
			if !ok {
				return fmt.Errorf("category %s listed before its parent", c.path)
			}
			parent, slug = &id, c.path[at+1:]
		}
		desc, pos := c.description, float64(i)
		created, err := s.taxonomy.Create(ctx, admin, taxonomy.CreateCategory{ParentID: parent, Slug: slug, Name: c.name, Description: &desc, Position: &pos})
		if err != nil {
			return fmt.Errorf("category %s: %w", c.path, err)
		}
		s.categories[c.path] = created.ID
	}
	return nil
}

func (s *seeder) seedTags(ctx context.Context) error {
	admin := s.authors[authors[0].key]
	for _, t := range tags {
		existing, err := s.taxonomy.TagBySlug(ctx, t.kind, t.slug)
		if err == nil {
			s.tags[tagKey(t.kind, t.slug)] = existing.ID
			continue
		}
		if !isNotFound(err) {
			return err
		}
		created, err := s.taxonomy.CreateTag(ctx, admin, taxonomy.CreateTag{Kind: t.kind, Slug: t.slug, Name: t.name})
		if err != nil {
			return fmt.Errorf("tag %s: %w", t.slug, err)
		}
		s.tags[tagKey(t.kind, t.slug)] = created.ID
	}
	return nil
}

func tagKey(k taxonomy.TagKind, slug string) string { return string(k) + ":" + slug }

// tagIDs resolves "kind:slug" references from the catalog.
func (s *seeder) tagIDs(refs []string) ([]int64, error) {
	out := make([]int64, 0, len(refs))
	for _, r := range refs {
		kind, slug, _ := strings.Cut(r, ":")
		id, ok := s.tags[tagKey(taxonomy.TagKind(strings.ToUpper(kind)), slug)]
		if !ok {
			return nil, fmt.Errorf("unknown tag %q", r)
		}
		out = append(out, id)
	}
	return out, nil
}

func (s *seeder) meta(title, slug, summary, categoryPath string, tagRefs []string) (content.Meta, error) {
	cat, ok := s.categories[categoryPath]
	if !ok {
		return content.Meta{}, fmt.Errorf("unknown category %q", categoryPath)
	}
	ids, err := s.tagIDs(tagRefs)
	if err != nil {
		return content.Meta{}, err
	}
	return content.Meta{
		Title: title, Slug: slug, Summary: &summary, CategoryID: &cat, TagIDs: ids,
		Visibility: content.VisibilityPublic, Access: content.AccessFree,
	}, nil
}

func body(path string) (string, error) {
	b, err := bodies.ReadFile("content/" + path)
	return string(b), err
}

// ── Problems ─────────────────────────────────────────────────────────────────

func (s *seeder) seedProblems(ctx context.Context) error {
	for _, ps := range problemCatalog {
		p := s.authors[ps.author]
		id, status, err := s.ensureProblem(ctx, p, ps)
		if err != nil {
			return fmt.Errorf("problem %s: %w", ps.slug, err)
		}
		if status != content.StatusPublished {
			if err := s.setUpJudging(ctx, p, id, ps); err != nil {
				return fmt.Errorf("problem %s: %w", ps.slug, err)
			}
			if _, err := s.contents.Publish(ctx, p, id); err != nil {
				return fmt.Errorf("publish problem %s: %w", ps.slug, err)
			}
		}
		s.ids[key(content.TypeProblem, ps.slug)] = id
	}
	return nil
}

func (s *seeder) ensureProblem(ctx context.Context, p auth.Principal, ps problemSpec) (int64, content.Status, error) {
	if it, ok := s.existing[key(content.TypeProblem, ps.slug)]; ok {
		return it.ID, it.Status, nil
	}
	m, err := s.meta(ps.title, ps.slug, ps.summary, ps.category, ps.tags)
	if err != nil {
		return 0, "", err
	}
	md, err := body("problems/" + ps.slug + ".md")
	if err != nil {
		return 0, "", err
	}
	d, err := s.problems.Create(ctx, p, problem.CreateInput{
		DraftInput: content.DraftInput{Meta: m, Body: md}, Difficulty: ps.difficulty,
	})
	if err != nil {
		return 0, "", err
	}
	return d.ID, d.Status, nil
}

// setUpJudging fills whatever the problem still lacks: samples, starter code, hints, tests.
func (s *seeder) setUpJudging(ctx context.Context, p auth.Principal, id int64, ps problemSpec) error {
	v, err := s.problems.Studio(ctx, p, id)
	if err != nil {
		return err
	}
	if len(v.Samples) == 0 {
		if _, err := s.problems.ReplaceSamples(ctx, p, id, ps.samples); err != nil {
			return err
		}
	}
	if len(v.Templates) == 0 {
		for _, lang := range templateLanguages {
			if _, err := s.problems.PutTemplate(ctx, p, id, problem.Template{Language: lang, StarterCode: ps.starter[lang]}); err != nil {
				return fmt.Errorf("template %s: %w", lang, err)
			}
		}
	}
	if len(v.Hints) == 0 && len(ps.hints) > 0 {
		if _, err := s.problems.ReplaceHints(ctx, p, id, ps.hints); err != nil {
			return err
		}
	}
	if len(v.TestSets) == 0 {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		for _, c := range ps.cases() {
			if err := enc.Encode(map[string]string{"input": c.input, "output": c.output}); err != nil {
				return err
			}
		}
		fileID, err := s.assets.StoreInternal(ctx, asset.PurposeTestSet, "application/x-ndjson", ".ndjson", buf.Bytes())
		if err != nil {
			return err
		}
		if _, err := s.problems.AddTestSet(ctx, p, id, fileID); err != nil {
			return err
		}
	}
	return nil
}

// ── Articles, topic pages, blog posts ────────────────────────────────────────

func (s *seeder) seedPages(ctx context.Context) error {
	for _, ps := range pageCatalog {
		p := s.authors[ps.author]
		k := key(ps.typ, ps.slug)
		id, status := int64(0), content.Status("")
		if it, ok := s.existing[k]; ok {
			id, status = it.ID, it.Status
		} else {
			d, err := s.createPage(ctx, p, ps)
			if err != nil {
				return fmt.Errorf("%s %s: %w", ps.typ, ps.slug, err)
			}
			id, status = d.ID, d.Status
		}
		if status != content.StatusPublished {
			if _, err := s.contents.Publish(ctx, p, id); err != nil {
				return fmt.Errorf("publish %s %s: %w", ps.typ, ps.slug, err)
			}
		}
		s.ids[k] = id
	}
	return nil
}

func (s *seeder) createPage(ctx context.Context, p auth.Principal, ps pageSpec) (content.Draft, error) {
	m, err := s.meta(ps.title, ps.slug, ps.summary, ps.category, ps.tags)
	if err != nil {
		return content.Draft{}, err
	}
	switch ps.typ {
	case content.TypeArticle:
		level := ps.level
		m.Article = &content.ArticleFields{Level: &level}
	case content.TypeBlog:
		cover, err := s.uploadImage(ctx, p, drawCover(ps.slug, ps.coverHue))
		if err != nil {
			return content.Draft{}, fmt.Errorf("cover: %w", err)
		}
		m.Blog = &content.BlogFields{CoverFileID: &cover}
	}
	md, err := body(strings.ToLower(string(ps.typ)) + "/" + ps.slug + ".md")
	if err != nil {
		return content.Draft{}, err
	}
	return s.contents.Create(ctx, p, content.CreateInput{Type: ps.typ, Locale: "en", DraftInput: content.DraftInput{Meta: m, Body: md}})
}

// uploadImage takes the same path as a browser upload: a presigned PUT straight to storage,
// then completion, which verifies the bytes and records the image's dimensions.
func (s *seeder) uploadImage(ctx context.Context, p auth.Principal, png []byte) (int64, error) {
	sum := sha256.Sum256(png)
	t, err := s.assets.CreateUpload(ctx, p, asset.CreateUpload{
		Purpose: asset.PurposeAsset, Mime: "image/png", SizeBytes: int64(len(png)), SHA256: hex.EncodeToString(sum[:]),
	})
	if err != nil {
		return 0, err
	}
	if t.AlreadyUploaded || t.Upload == nil {
		return t.File.ID, nil
	}
	req, err := http.NewRequestWithContext(ctx, t.Upload.Method, t.Upload.URL, bytes.NewReader(png))
	if err != nil {
		return 0, err
	}
	for k, v := range t.Upload.Headers {
		req.Header.Set(k, v)
	}
	req.ContentLength = int64(len(png))
	res, err := (&http.Client{Timeout: time.Minute}).Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		return 0, fmt.Errorf("storage answered %s: %s", res.Status, msg)
	}
	f, err := s.assets.Complete(ctx, p, t.File.ID)
	if err != nil {
		return 0, err
	}
	return f.ID, nil
}

// ── Collections and the daily problem ────────────────────────────────────────

func (s *seeder) seedCollections(ctx context.Context) error {
	for _, cs := range collectionCatalog {
		_, err := s.collections.PublicBySlug(ctx, cs.kind, cs.slug, page.Request[float64]{Limit: 1})
		if err == nil {
			continue
		}
		if !isNotFound(err) {
			return err
		}
		p := s.authors[cs.author]
		desc := cs.description
		c, err := s.collections.Create(ctx, p, collection.Input{Kind: cs.kind, Slug: cs.slug, Title: cs.title, Description: &desc, Visibility: content.VisibilityPublic})
		if err != nil {
			return fmt.Errorf("collection %s: %w", cs.slug, err)
		}
		for _, ref := range cs.items {
			t, slug, _ := strings.Cut(ref, ":")
			id, ok := s.ids[key(content.Type(strings.ToUpper(t)), slug)]
			if !ok {
				return fmt.Errorf("collection %s: unknown item %q", cs.slug, ref)
			}
			if err := s.collections.AddItem(ctx, p, c.ID, id); err != nil {
				return fmt.Errorf("collection %s: add %s: %w", cs.slug, ref, err)
			}
		}
	}
	return nil
}

// seedProblemOfTheDay schedules a week from today (UTC), cycling through the problems.
func (s *seeder) seedProblemOfTheDay(ctx context.Context) error {
	admin := s.authors[authors[0].key]
	today := time.Now().UTC().Truncate(24 * time.Hour)
	for d := range 7 {
		ps := problemCatalog[d%len(problemCatalog)]
		if err := s.problems.ScheduleOfDay(ctx, admin, today.AddDate(0, 0, d), s.ids[key(content.TypeProblem, ps.slug)]); err != nil {
			return err
		}
	}
	return nil
}

func isNotFound(err error) bool {
	e, ok := apperr.As(err)
	return ok && e.Kind == apperr.KindNotFound
}
