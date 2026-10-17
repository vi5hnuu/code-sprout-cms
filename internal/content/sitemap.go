package content

import (
	"context"
	"net/http"
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

type SitemapChunk struct {
	Type     Type      `json:"type"`
	Chunk    int32     `json:"chunk"`
	URLCount int32     `json:"urlCount"`
	LastMod  time.Time `json:"lastmod"`
}

type SitemapEntry struct {
	Path    string    `json:"path"`
	LastMod time.Time `json:"lastmod"`
}

// Sitemaps keeps sitemap_chunk in step with what is live and serves the chunk files.
type Sitemaps struct{ db *db.DB }

func NewSitemaps(d *db.DB) *Sitemaps { return &Sitemaps{db: d} }

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

func (s *Sitemaps) Chunks(ctx context.Context) ([]SitemapChunk, error) {
	rows, err := contentdb.New(s.db.Replica).ListSitemapChunks(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]SitemapChunk, len(rows))
	for i, r := range rows {
		out[i] = SitemapChunk{Type: Type(r.Type), Chunk: r.Chunk, URLCount: r.UrlCount, LastMod: r.Lastmod}
	}
	return out, nil
}

func (s *Sitemaps) Entries(ctx context.Context, t Type, chunk int32) ([]SitemapEntry, error) {
	lo, hi := int64(chunk)*SitemapChunkSize, int64(chunk+1)*SitemapChunkSize
	rows, err := contentdb.New(s.db.Replica).ListSitemapEntries(ctx, contentdb.ListSitemapEntriesParams{Type: string(t), Lo: lo, Hi: hi})
	if err != nil {
		return nil, err
	}
	out := make([]SitemapEntry, len(rows))
	for i, r := range rows {
		out[i] = SitemapEntry{Path: PublicPath(t, r.Locale, r.Section, r.Slug), LastMod: *r.LastPublishedAt}
	}
	return out, nil
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
		t, ok := parseType(r.PathValue("type"))
		chunk, err := strconv.ParseInt(strings.TrimSuffix(r.PathValue("chunk"), ".xml"), 10, 32)
		if !ok || err != nil || chunk < 0 {
			return apperr.NotFound("sitemap.not_found", "No such sitemap.")
		}
		entries, err := s.Entries(r.Context(), t, int32(chunk))
		if err != nil {
			return err
		}
		httpx.Public(w, time.Hour, 24*time.Hour, "sitemaps")
		httpx.JSON(w, http.StatusOK, map[string]any{"items": entries})
		return nil
	}))
}
