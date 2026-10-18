package content

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"code-sprout-cms/internal/content/contentdb"
	"code-sprout-cms/internal/platform/apperr"
	"code-sprout-cms/internal/platform/db"
	"code-sprout-cms/internal/platform/httpx"
)

// Sitemaps are split into chunks of fixed id ranges per type (chunk = id / 50,000), never
// OFFSET pages: new items only ever land in the newest chunk, so older sitemap files stay
// byte-stable and crawlers re-fetch only chunks whose lastmod changed. 50,000 is the
// sitemaps.org per-file URL limit; a sitemap index can list 50,000 chunks (2.5B URLs).
const SitemapChunkSize = 50_000

// Sitemap kinds beyond the content types: the hub pages that list content. Readers and
// crawlers reach millions of items through them, so every one is in a sitemap too.
const (
	SitemapCategory   = "CATEGORY"
	SitemapTag        = "TAG"
	SitemapCollection = "COLLECTION"
)

type SitemapChunk struct {
	Type     string    `json:"type"` // a content Type or a hub kind (CATEGORY, TAG, COLLECTION)
	Chunk    int32     `json:"chunk"`
	URLCount int32     `json:"urlCount"`
	LastMod  time.Time `json:"lastmod"`
}

type SitemapEntry struct {
	Path    string    `json:"path"`
	LastMod time.Time `json:"lastmod"`
	// Images are absolute URLs of the page's key images (a blog post's cover), for the
	// sitemap image extension.
	Images []string `json:"images,omitempty"`
}

// Sitemaps keeps sitemap_chunk in step with what is live and serves the chunk files.
type Sitemaps struct {
	db     *db.DB
	assets Assets
}

func NewSitemaps(d *db.DB, assets Assets) *Sitemaps { return &Sitemaps{db: d, assets: assets} }

// Refresh recounts the chunk holding contentID (after it was published, updated or taken down).
// Idempotent; one index-only count over at most 50,000 entries.
func (s *Sitemaps) Refresh(ctx context.Context, t Type, contentID int64) error {
	chunk := int32(contentID / SitemapChunkSize)
	lo, hi := int64(chunk)*SitemapChunkSize, int64(chunk+1)*SitemapChunkSize
	q := contentdb.New(s.db.Primary)
	n, err := q.CountSitemapRange(ctx, contentdb.CountSitemapRangeParams{Type: string(t), Lo: lo, Hi: hi})
	if err != nil {
		return err
	}
	return q.UpsertSitemapChunk(ctx, contentdb.UpsertSitemapChunkParams{Type: string(t), Chunk: chunk, UrlCount: n})
}

// Chunks lists every sitemap file: content chunks (maintained on publish) plus hub chunks
// (small aggregates over the taxonomy tables).
func (s *Sitemaps) Chunks(ctx context.Context) ([]SitemapChunk, error) {
	q := contentdb.New(s.db.Replica)
	rows, err := q.ListSitemapChunks(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]SitemapChunk, 0, len(rows)+3)
	for _, r := range rows {
		out = append(out, SitemapChunk{Type: r.Type, Chunk: r.Chunk, URLCount: r.UrlCount, LastMod: r.Lastmod})
	}
	cats, err := q.ListCategorySitemapChunks(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range cats {
		out = append(out, SitemapChunk{Type: SitemapCategory, Chunk: r.Chunk, URLCount: r.UrlCount, LastMod: r.Lastmod})
	}
	tags, err := q.ListTagSitemapChunks(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range tags {
		out = append(out, SitemapChunk{Type: SitemapTag, Chunk: r.Chunk, URLCount: r.UrlCount, LastMod: r.Lastmod})
	}
	cols, err := q.ListCollectionSitemapChunks(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range cols {
		out = append(out, SitemapChunk{Type: SitemapCollection, Chunk: r.Chunk, URLCount: r.UrlCount, LastMod: r.Lastmod})
	}
	return out, nil
}

// Entries lists the pages of one chunk. kind is a content Type or a hub kind.
func (s *Sitemaps) Entries(ctx context.Context, kind string, chunk int32) ([]SitemapEntry, error) {
	lo, hi := int64(chunk)*SitemapChunkSize, int64(chunk+1)*SitemapChunkSize
	q := contentdb.New(s.db.Replica)
	switch kind {
	case SitemapCategory:
		rows, err := q.ListCategorySitemapEntries(ctx, contentdb.ListCategorySitemapEntriesParams{Lo: lo, Hi: hi})
		if err != nil {
			return nil, err
		}
		out := make([]SitemapEntry, len(rows))
		for i, r := range rows {
			out[i] = SitemapEntry{Path: "/category/" + strings.ReplaceAll(r.Path, ".", "/"), LastMod: r.Lastmod}
		}
		return out, nil
	case SitemapTag:
		rows, err := q.ListTagSitemapEntries(ctx, contentdb.ListTagSitemapEntriesParams{Lo: lo, Hi: hi})
		if err != nil {
			return nil, err
		}
		out := make([]SitemapEntry, len(rows))
		for i, r := range rows {
			out[i] = SitemapEntry{Path: "/tag/" + strings.ToLower(r.Kind) + "/" + url.PathEscape(r.Slug), LastMod: r.Lastmod}
		}
		return out, nil
	case SitemapCollection:
		rows, err := q.ListCollectionSitemapEntries(ctx, contentdb.ListCollectionSitemapEntriesParams{Lo: lo, Hi: hi})
		if err != nil {
			return nil, err
		}
		out := make([]SitemapEntry, 0, len(rows))
		for _, r := range rows {
			if base, ok := collectionPaths[r.Kind]; ok {
				out = append(out, SitemapEntry{Path: base + url.PathEscape(r.Slug), LastMod: r.UpdatedAt})
			}
		}
		return out, nil
	}
	t := Type(kind)
	rows, err := q.ListSitemapEntries(ctx, contentdb.ListSitemapEntriesParams{Type: string(t), Lo: lo, Hi: hi})
	if err != nil {
		return nil, err
	}
	out := make([]SitemapEntry, len(rows))
	for i, r := range rows {
		out[i] = SitemapEntry{Path: PublicPath(t, r.Locale, r.Section, r.Slug), LastMod: *r.LastPublishedAt}
	}
	if t == TypeBlog {
		if err := s.attachCovers(ctx, q, rows, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// collectionPaths are the web routes of editorial collections, by kind.
var collectionPaths = map[string]string{"STUDY_PLAN": "/study-plans/", "SERIES": "/series/", "COURSE": "/courses/"}

// attachCovers adds each post's cover to its entry: two batched lookups for the whole chunk.
func (s *Sitemaps) attachCovers(ctx context.Context, q *contentdb.Queries, rows []contentdb.ListSitemapEntriesRow, out []SitemapEntry) error {
	ids := make([]int64, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	refs, err := q.ListBlogCovers(ctx, ids)
	if err != nil {
		return err
	}
	fileIDs := make([]int64, len(refs))
	cover := make(map[int64]int64, len(refs))
	for i, ref := range refs {
		fileIDs[i] = ref.CoverFileID
		cover[ref.ContentID] = ref.CoverFileID
	}
	files, err := s.assets.Describe(ctx, fileIDs)
	if err != nil {
		return err
	}
	for i, r := range rows {
		if f, ok := files[cover[r.ID]]; ok && f.URL != nil {
			out[i].Images = []string{*f.URL}
		}
	}
	return nil
}

// sitemapKind parses the {type} path value: a content type or a hub kind.
func sitemapKind(v string) (string, bool) {
	if t, ok := parseType(v); ok {
		return string(t), true
	}
	switch k := strings.ToUpper(v); k {
	case SitemapCategory, SitemapTag, SitemapCollection:
		return k, true
	}
	return "", false
}

func (s *Sitemaps) Routes(mux *http.ServeMux) {
	mux.Handle("GET /v1/public/sitemaps", httpx.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
		chunks, err := s.Chunks(r.Context())
		if err != nil {
			return err
		}
		httpx.Public(w, time.Hour, 24*time.Hour, "sitemaps")
		httpx.JSON(w, http.StatusOK, map[string]any{"items": chunks})
		return nil
	}))
	mux.Handle("GET /v1/public/sitemaps/{type}/{chunk}", httpx.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
		kind, ok := sitemapKind(r.PathValue("type"))
		chunk, err := strconv.ParseInt(strings.TrimSuffix(r.PathValue("chunk"), ".xml"), 10, 32)
		if !ok || err != nil || chunk < 0 {
			return apperr.NotFound("sitemap.not_found", "No such sitemap.")
		}
		entries, err := s.Entries(r.Context(), kind, int32(chunk))
		if err != nil {
			return err
		}
		httpx.Public(w, time.Hour, 24*time.Hour, "sitemaps")
		httpx.JSON(w, http.StatusOK, map[string]any{"items": entries})
		return nil
	}))
}
