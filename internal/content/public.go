package content

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"code-sprout-cms/internal/asset"
	"code-sprout-cms/internal/content/contentdb"
	"code-sprout-cms/internal/engagement"
	"code-sprout-cms/internal/markdown"
	"code-sprout-cms/internal/platform/apperr"
	"code-sprout-cms/internal/platform/db"
	"code-sprout-cms/internal/platform/page"
	"code-sprout-cms/internal/taxonomy"
	"code-sprout-cms/internal/user"
)

// Reader serves what anonymous readers see. Everything it returns is identical for every
// caller, so responses can be cached by the CDN; per-user state (liked? bookmarked?) is a
// separate /v1/me call. It reads only from the replica.
type Reader struct {
	db       *db.DB
	assets   Assets
	taxonomy PublicTaxonomy
	users    PublicUsers
	stats    StatsReader
	// videoMedia adds thumbnails and durations to VIDEO rows in any listing; nil when the video
	// domain is not wired (the field then stays empty).
	videoMedia func(ctx context.Context, ids []int64) (map[int64]VideoMedia, error)
}

// VideoMedia is what a listing card shows for a VIDEO item.
type VideoMedia struct {
	ThumbnailURL    string `json:"thumbnailUrl"`
	DurationSeconds int32  `json:"durationSeconds"`
	IsShort         bool   `json:"isShort"`
	// PublishedAt is when it went out on YouTube (the listing order), not when it was synced.
	PublishedAt time.Time `json:"publishedAt"`
}

// SetVideoMedia installs the batch loader the video domain provides. Call during wiring.
func (r *Reader) SetVideoMedia(load func(ctx context.Context, ids []int64) (map[int64]VideoMedia, error)) {
	r.videoMedia = load
}

type PublicTaxonomy interface {
	CategoriesByID(ctx context.Context, ids []int64) (map[int64]taxonomy.Category, error)
	Trail(ctx context.Context, id int64) ([]taxonomy.Category, error)
	TagsByID(ctx context.Context, ids []int64) ([]taxonomy.Tag, error)
	ByURLPath(ctx context.Context, urlPath string) (taxonomy.Category, error)
	TagBySlug(ctx context.Context, kind taxonomy.TagKind, slug string) (taxonomy.Tag, error)
}

type PublicUsers interface {
	PublicProfiles(ctx context.Context, ids []int64) (map[int64]user.PublicUser, error)
	ByHandle(ctx context.Context, handle string) (user.PublicUser, error)
}

type StatsReader interface {
	StatsFor(ctx context.Context, ids []int64) (map[int64]engagement.Stats, error)
}

func NewReader(d *db.DB, assets Assets, tax PublicTaxonomy, users PublicUsers, stats StatsReader) *Reader {
	return &Reader{db: d, assets: assets, taxonomy: tax, users: users, stats: stats}
}

// PublicPage is a live item as readers see it.
type PublicPage struct {
	ID              int64                `json:"id"`
	Type            Type                 `json:"type"`
	Locale          string               `json:"locale"`
	Slug            string               `json:"slug"`
	Title           string               `json:"title"`
	Summary         *string              `json:"summary"`
	Access          string               `json:"access"`
	Unlisted        bool                 `json:"unlisted"` // render with noindex
	Locked          bool                 `json:"locked"`   // PREMIUM: body withheld from the public response
	Body            *string              `json:"body"`
	TOC             []markdown.Heading   `json:"toc"`
	ReadingMinutes  int32                `json:"readingMinutes"`
	WordCount       int32                `json:"wordCount"`
	PublishedAt     time.Time            `json:"publishedAt"`
	LastPublishedAt time.Time            `json:"lastPublishedAt"`
	Author          user.PublicUser      `json:"author"`
	Breadcrumb      []taxonomy.Category  `json:"breadcrumb"` // root-first, ending at the item's category
	Tags            []taxonomy.Tag       `json:"tags"`
	Assets          map[int64]asset.File `json:"assets"` // resolves asset:<id> in the body
	Links           []Link               `json:"links"`  // e.g. PRACTICE problems for an article
	Article         *ArticleFields       `json:"article,omitempty"`
	Blog            *PublicBlog          `json:"blog,omitempty"`
	Stats           engagement.Stats     `json:"stats"`
}

type PublicBlog struct {
	Cover *asset.File `json:"cover"`
}

type Link struct {
	Kind   string `json:"kind"`
	ID     int64  `json:"id"`
	Type   Type   `json:"type"`
	Locale string `json:"locale"`
	Slug   string `json:"slug"`
	Title  string `json:"title"`
	// Path is the target's canonical site path, so links never go through a redirect.
	Path string `json:"path"`
}

// Redirect means the slug was renamed; clients answer with a permanent redirect to Slug.
type Redirect struct {
	Slug string `json:"slug"`
}

// Page returns the live item, or a Redirect when slug is a renamed item's former slug.
func (r *Reader) Page(ctx context.Context, t Type, locale, slug string) (*PublicPage, *Redirect, error) {
	q := contentdb.New(r.db.Replica)
	c, err := q.GetLiveBySlug(ctx, contentdb.GetLiveBySlugParams{Type: string(t), Locale: locale, Slug: slug})
	if db.IsNoRows(err) {
		to, rerr := q.ResolveRedirect(ctx, contentdb.ResolveRedirectParams{Type: string(t), Locale: locale, Slug: slug})
		if db.IsNoRows(rerr) {
			return nil, nil, errNotFound
		}
		if rerr != nil {
			return nil, nil, rerr
		}
		return nil, &Redirect{Slug: to}, nil
	}
	if err != nil {
		return nil, nil, err
	}
	p, err := r.hydratePage(ctx, q, c)
	return p, nil, err
}

func (r *Reader) hydratePage(ctx context.Context, q *contentdb.Queries, c contentdb.Content) (*PublicPage, error) {
	rev, err := q.GetRevision(ctx, contentdb.GetRevisionParams{ContentID: c.ID, RevNo: *c.PublishedRevNo})
	if err != nil {
		return nil, err
	}
	render, err := q.GetRender(ctx, c.ID)
	if err != nil {
		return nil, err
	}
	var toc []markdown.Heading
	if err := json.Unmarshal(render.Toc, &toc); err != nil {
		return nil, err
	}
	var meta Meta
	if err := json.Unmarshal(rev.Meta, &meta); err != nil {
		return nil, err
	}

	p := &PublicPage{
		ID: c.ID, Type: Type(c.Type), Locale: c.Locale, Slug: c.Slug, Title: c.Title, Summary: c.Summary,
		Access: c.Access, Unlisted: c.Visibility == VisibilityUnlisted, TOC: toc,
		ReadingMinutes: render.ReadingMinutes, WordCount: render.WordCount,
		PublishedAt: *c.PublishedAt, LastPublishedAt: *c.LastPublishedAt, Links: []Link{}, Breadcrumb: []taxonomy.Category{},
	}
	// Premium bodies are served to entitled readers through an authenticated endpoint; the
	// public (cached) response carries only what may be shown to everyone.
	if c.Access == AccessPremium {
		p.Locked = true
	} else {
		p.Body = rev.BodyMd
	}

	authors, err := r.users.PublicProfiles(ctx, []int64{c.AuthorID})
	if err != nil {
		return nil, err
	}
	p.Author = authors[c.AuthorID]
	if c.PrimaryCategoryID != nil {
		if p.Breadcrumb, err = r.taxonomy.Trail(ctx, *c.PrimaryCategoryID); err != nil {
			return nil, err
		}
	}
	if p.Tags, err = r.taxonomy.TagsByID(ctx, meta.TagIDs); err != nil {
		return nil, err
	}

	assetIDs, err := q.ListContentAssetIDs(ctx, c.ID)
	if err != nil {
		return nil, err
	}
	if p.Assets, err = r.assets.Describe(ctx, assetIDs); err != nil {
		return nil, err
	}
	if meta.Blog != nil {
		p.Blog = &PublicBlog{}
		if meta.Blog.CoverFileID != nil {
			if f, ok := p.Assets[*meta.Blog.CoverFileID]; ok {
				p.Blog.Cover = &f
			}
		}
	}
	p.Article = meta.Article

	links, err := q.ListLiveLinks(ctx, c.ID)
	if err != nil {
		return nil, err
	}
	for _, l := range links {
		p.Links = append(p.Links, Link{Kind: l.Kind, ID: l.ID, Type: Type(l.Type), Locale: l.Locale, Slug: l.Slug, Title: l.Title,
			Path: PublicPath(Type(l.Type), l.Locale, l.Section, l.Slug)})
	}
	stats, err := r.stats.StatsFor(ctx, []int64{c.ID})
	if err != nil {
		return nil, err
	}
	p.Stats = stats[c.ID]
	return p, nil
}

// ListItem is one entry in a public listing.
type ListItem struct {
	ID              int64              `json:"id"`
	Type            Type               `json:"type"`
	Locale          string             `json:"locale"`
	Slug            string             `json:"slug"`
	Title           string             `json:"title"`
	Summary         *string            `json:"summary"`
	Access          string             `json:"access"`
	PublishedAt     time.Time          `json:"publishedAt"`
	LastPublishedAt time.Time          `json:"lastPublishedAt"`
	ReadingMinutes  int32              `json:"readingMinutes"`
	Category        *taxonomy.Category `json:"category"`
	Author          user.PublicUser    `json:"author"`
	Stats           engagement.Stats   `json:"stats"`
	Cover           *asset.File        `json:"cover"` // blog cover image, for card layouts
	Video           *VideoMedia        `json:"video,omitempty"`
}

type PublishedCursor struct {
	PublishedAt time.Time `json:"p"`
	ID          int64     `json:"i"`
}

// ListFilter selects one listing. At most one of Category, Tag and Author is set; with none,
// Type is required ("latest articles").
type ListFilter struct {
	Type     *Type
	Locale   string
	Category string // URL path, e.g. dsa/algorithms
	Tag      string // kind:slug, e.g. topic:arrays
	Author   string // handle
}

// List returns live, PUBLIC items newest first.
// ResolveTag finds a tag from its kind:slug form (e.g. topic:arrays), as list filters name it.
func (r *Reader) ResolveTag(ctx context.Context, kindSlug string) (taxonomy.Tag, error) {
	kind, slug, ok := strings.Cut(kindSlug, ":")
	if !ok {
		return taxonomy.Tag{}, apperr.Invalid("content.list_filter", "tag must be kind:slug, e.g. topic:arrays.")
	}
	return r.taxonomy.TagBySlug(ctx, taxonomy.TagKind(strings.ToUpper(kind)), slug)
}

func (r *Reader) List(ctx context.Context, f ListFilter, req page.Request[PublishedCursor]) (page.Page[ListItem], error) {
	set := 0
	for _, v := range []string{f.Category, f.Tag, f.Author} {
		if v != "" {
			set++
		}
	}
	if set > 1 {
		return page.Page[ListItem]{}, apperr.Invalid("content.list_filter", "Filter by at most one of category, tag or author.")
	}
	if set == 0 && f.Type == nil {
		return page.Page[ListItem]{}, apperr.Invalid("content.list_filter", "Pass a type, or a category, tag or author.")
	}

	var afterPub *time.Time
	var afterID *int64
	if req.Key != nil {
		afterPub, afterID = &req.Key.PublishedAt, &req.Key.ID
	}
	var typ *string
	if f.Type != nil {
		t := string(*f.Type)
		typ = &t
	}
	limit := int32(req.Limit + 1)
	q := contentdb.New(r.db.Replica)

	var rows []contentdb.Content
	var err error
	switch {
	case f.Category != "":
		cat, cerr := r.taxonomy.ByURLPath(ctx, f.Category)
		if cerr != nil {
			return page.Page[ListItem]{}, cerr
		}
		rows, err = q.ListLiveByCategory(ctx, contentdb.ListLiveByCategoryParams{
			CategoryID: &cat.ID, Type: typ, AfterPublished: afterPub, AfterID: afterID, RowLimit: limit,
		})
	case f.Tag != "":
		tag, terr := r.ResolveTag(ctx, f.Tag)
		if terr != nil {
			return page.Page[ListItem]{}, terr
		}
		rows, err = q.ListLiveByTag(ctx, contentdb.ListLiveByTagParams{
			TagID: tag.ID, Type: typ, AfterPublished: afterPub, AfterID: afterID, RowLimit: limit,
		})
	case f.Author != "":
		u, uerr := r.users.ByHandle(ctx, strings.ToLower(f.Author))
		if uerr != nil {
			return page.Page[ListItem]{}, uerr
		}
		rows, err = q.ListLiveByAuthor(ctx, contentdb.ListLiveByAuthorParams{
			AuthorID: u.ID, AfterPublished: afterPub, AfterID: afterID, RowLimit: limit,
		})
	default:
		rows, err = q.ListLiveByType(ctx, contentdb.ListLiveByTypeParams{
			Type: *typ, Locale: f.Locale, AfterPublished: afterPub, AfterID: afterID, RowLimit: limit,
		})
	}
	if err != nil {
		return page.Page[ListItem]{}, err
	}
	items, err := r.hydrateList(ctx, q, rows)
	if err != nil {
		return page.Page[ListItem]{}, err
	}
	return page.Build(items, req.Limit, func(i ListItem) PublishedCursor {
		return PublishedCursor{PublishedAt: i.PublishedAt, ID: i.ID}
	}), nil
}

// hydrateList attaches categories, authors, reading time and stats with one batched lookup
// each, so a page of 20 costs a fixed handful of queries rather than 20× as many.
func (r *Reader) hydrateList(ctx context.Context, q *contentdb.Queries, rows []contentdb.Content) ([]ListItem, error) {
	ids := make([]int64, len(rows))
	var catIDs, authorIDs []int64
	for i, c := range rows {
		ids[i] = c.ID
		authorIDs = append(authorIDs, c.AuthorID)
		if c.PrimaryCategoryID != nil {
			catIDs = append(catIDs, *c.PrimaryCategoryID)
		}
	}
	cats, err := r.taxonomy.CategoriesByID(ctx, catIDs)
	if err != nil {
		return nil, err
	}
	authors, err := r.users.PublicProfiles(ctx, authorIDs)
	if err != nil {
		return nil, err
	}
	stats, err := r.stats.StatsFor(ctx, ids)
	if err != nil {
		return nil, err
	}
	renders, err := q.ListRenders(ctx, ids)
	if err != nil {
		return nil, err
	}
	reading := make(map[int64]int32, len(renders))
	for _, rr := range renders {
		reading[rr.ContentID] = rr.ReadingMinutes
	}
	covers, err := r.listCovers(ctx, q, rows)
	if err != nil {
		return nil, err
	}
	media, err := r.listVideoMedia(ctx, rows)
	if err != nil {
		return nil, err
	}

	items := make([]ListItem, len(rows))
	for i, c := range rows {
		item := ListItem{
			ID: c.ID, Type: Type(c.Type), Locale: c.Locale, Slug: c.Slug, Title: c.Title, Summary: c.Summary,
			Access: c.Access, PublishedAt: *c.PublishedAt, LastPublishedAt: *c.LastPublishedAt,
			ReadingMinutes: reading[c.ID], Author: authors[c.AuthorID], Stats: stats[c.ID], Cover: covers[c.ID],
		}
		if m, ok := media[c.ID]; ok {
			item.Video = &m
		}
		if c.PrimaryCategoryID != nil {
			if cat, ok := cats[*c.PrimaryCategoryID]; ok {
				item.Category = &cat
			}
		}
		items[i] = item
	}
	return items, nil
}

// listVideoMedia loads card media for the VIDEO rows of a page: one batched call, none when
// the page holds no videos.
func (r *Reader) listVideoMedia(ctx context.Context, rows []contentdb.Content) (map[int64]VideoMedia, error) {
	if r.videoMedia == nil {
		return nil, nil
	}
	var ids []int64
	for _, c := range rows {
		if Type(c.Type) == TypeVideo {
			ids = append(ids, c.ID)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	return r.videoMedia(ctx, ids)
}

// Backlinks returns live items linking to id with kind (e.g. the problems a video solves).
func (r *Reader) Backlinks(ctx context.Context, id int64, kind string, limit int32) ([]Link, error) {
	rows, err := contentdb.New(r.db.Replica).ListLiveBacklinks(ctx, contentdb.ListLiveBacklinksParams{ToID: id, Kind: kind, RowLimit: limit})
	if err != nil {
		return nil, err
	}
	out := make([]Link, len(rows))
	for i, l := range rows {
		out[i] = Link{Kind: kind, ID: l.ID, Type: Type(l.Type), Locale: l.Locale, Slug: l.Slug, Title: l.Title,
			Path: PublicPath(Type(l.Type), l.Locale, l.Section, l.Slug)}
	}
	return out, nil
}

// listCovers resolves blog covers for a page of rows: two batched queries whatever the page
// size, and none when the page holds no blog posts.
func (r *Reader) listCovers(ctx context.Context, q *contentdb.Queries, rows []contentdb.Content) (map[int64]*asset.File, error) {
	var blogIDs []int64
	for _, c := range rows {
		if Type(c.Type) == TypeBlog {
			blogIDs = append(blogIDs, c.ID)
		}
	}
	if len(blogIDs) == 0 {
		return nil, nil
	}
	refs, err := q.ListBlogCovers(ctx, blogIDs)
	if err != nil {
		return nil, err
	}
	fileIDs := make([]int64, len(refs))
	for i, ref := range refs {
		fileIDs[i] = ref.CoverFileID
	}
	files, err := r.assets.Describe(ctx, fileIDs)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]*asset.File, len(refs))
	for _, ref := range refs {
		if f, ok := files[ref.CoverFileID]; ok {
			out[ref.ContentID] = &f
		}
	}
	return out, nil
}

// LiveIDs returns which of ids are live and visible, for domains that only act on live
// content (likes, bookmarks, comments, view counts).
func (r *Reader) LiveIDs(ctx context.Context, ids []int64) ([]int64, error) {
	if len(ids) == 0 {
		return []int64{}, nil
	}
	rows, err := contentdb.New(r.db.Replica).ListLiveByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]int64, len(rows))
	for i, c := range rows {
		out[i] = c.ID
	}
	return out, nil
}

// ItemsByID hydrates live items in the order of ids, skipping any no longer live (a bookmark
// can outlive the page it points to).
func (r *Reader) ItemsByID(ctx context.Context, ids []int64) ([]ListItem, error) {
	if len(ids) == 0 {
		return []ListItem{}, nil
	}
	q := contentdb.New(r.db.Replica)
	rows, err := q.ListLiveByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	items, err := r.hydrateList(ctx, q, rows)
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]ListItem, len(items))
	for _, it := range items {
		byID[it.ID] = it
	}
	ordered := make([]ListItem, 0, len(items))
	for _, id := range ids {
		if it, ok := byID[id]; ok {
			ordered = append(ordered, it)
		}
	}
	return ordered, nil
}
