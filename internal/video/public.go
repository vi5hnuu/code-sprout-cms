package video

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"code-sprout-cms/internal/content"
	"code-sprout-cms/internal/platform/apperr"
	"code-sprout-cms/internal/platform/db"
	"code-sprout-cms/internal/platform/httpx"
	"code-sprout-cms/internal/platform/page"
	"code-sprout-cms/internal/video/videodb"
)

// Service serves videos to readers. Pages and listings are built from content (so likes,
// comments and caching are shared) plus the video's own fields.
type Service struct {
	db     *db.DB
	reader *content.Reader
}

func NewService(d *db.DB, reader *content.Reader) *Service {
	s := &Service{db: d, reader: reader}
	reader.SetVideoMedia(s.Media)
	return s
}

// Details is the video part of a watch page.
type Details struct {
	Provider        string    `json:"provider"` // YOUTUBE
	ExternalID      string    `json:"externalId"`
	EmbedURL        string    `json:"embedUrl"` // privacy-enhanced player
	WatchURL        string    `json:"watchUrl"` // on YouTube itself
	DurationSeconds int32     `json:"durationSeconds"`
	PublishedAt     time.Time `json:"publishedAt"` // on YouTube
	Thumbnail       Thumbnail `json:"thumbnail"`
	IsShort         bool      `json:"isShort"`
	Chapters        []Chapter `json:"chapters"`
	// Problems is where the video is the solution ("Practice this problem").
	Problems []content.Link `json:"problems"`
}

type Page struct {
	content.PublicPage
	Video Details `json:"video"`
}

var errNotFound = apperr.NotFound("video.not_found", "No such video.")

// maxProblems bounds the "practice" links on a watch page.
const maxProblems = 5

func (s *Service) Page(ctx context.Context, locale, slug string) (*Page, *content.Redirect, error) {
	p, redirect, err := s.reader.Page(ctx, content.TypeVideo, locale, slug)
	if err != nil || redirect != nil {
		return nil, redirect, err
	}
	ext, err := videodb.New(s.db.Replica).GetExt(ctx, p.ID)
	if db.IsNoRows(err) {
		return nil, nil, errNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	var chapters []Chapter
	if err := json.Unmarshal(ext.Chapters, &chapters); err != nil {
		return nil, nil, err
	}
	problems, err := s.reader.Backlinks(ctx, p.ID, "VIDEO", maxProblems)
	if err != nil {
		return nil, nil, err
	}
	d := Details{
		Provider: ext.Provider, ExternalID: ext.ExternalID,
		EmbedURL:        "https://www.youtube-nocookie.com/embed/" + url.PathEscape(ext.ExternalID),
		WatchURL:        "https://www.youtube.com/watch?v=" + url.QueryEscape(ext.ExternalID),
		DurationSeconds: ext.DurationS, PublishedAt: ext.PublishedAt, IsShort: ext.IsShort,
		Thumbnail: Thumbnail{URL: ext.ThumbnailUrl}, Chapters: chapters, Problems: problems,
	}
	if ext.ThumbnailW != nil && ext.ThumbnailH != nil {
		d.Thumbnail.Width, d.Thumbnail.Height = int(*ext.ThumbnailW), int(*ext.ThumbnailH)
	}
	if d.Chapters == nil {
		d.Chapters = []Chapter{}
	}
	return &Page{PublicPage: *p, Video: d}, nil, nil
}

// List returns live videos newest first (by YouTube publish time), optionally only Shorts or
// only regular videos. Keyset paged; a page may be shorter than the limit when some videos were
// taken down, and the cursor still advances.
func (s *Service) List(ctx context.Context, short *bool, req page.Request[content.PublishedCursor]) (page.Page[content.ListItem], error) {
	params := videodb.ListVideoIDsParams{IsShort: short, RowLimit: int32(req.Limit + 1)}
	if req.Key != nil {
		params.AfterPublished, params.AfterID = &req.Key.PublishedAt, &req.Key.ID
	}
	rows, err := videodb.New(s.db.Replica).ListVideoIDs(ctx, params)
	if err != nil {
		return page.Page[content.ListItem]{}, err
	}
	ids := make([]int64, len(rows))
	at := make(map[int64]time.Time, len(rows))
	for i, r := range rows {
		ids[i] = r.ContentID
		at[r.ContentID] = r.PublishedAt
	}
	built := page.Build(ids, req.Limit, func(id int64) content.PublishedCursor {
		return content.PublishedCursor{PublishedAt: at[id], ID: id}
	})
	items, err := s.reader.ItemsByID(ctx, built.Items)
	if err != nil {
		return page.Page[content.ListItem]{}, err
	}
	return page.Page[content.ListItem]{Items: items, NextCursor: built.NextCursor}, nil
}

// Media is the batch loader the content reader uses to put thumbnails on VIDEO list rows.
func (s *Service) Media(ctx context.Context, ids []int64) (map[int64]content.VideoMedia, error) {
	rows, err := videodb.New(s.db.Replica).MediaByIDs(ctx, ids)
	out := make(map[int64]content.VideoMedia, len(rows))
	for _, r := range rows {
		out[r.ContentID] = content.VideoMedia{ThumbnailURL: r.ThumbnailUrl, DurationSeconds: r.DurationS, IsShort: r.IsShort}
	}
	return out, err
}

// ── HTTP ──

type Handler struct{ svc *Service }

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

func (h *Handler) Routes(mux *http.ServeMux) {
	mux.Handle("GET /v1/public/videos", httpx.HandlerFunc(h.list))
	mux.Handle("GET /v1/public/videos/{slug}", httpx.HandlerFunc(h.page))
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) error {
	req, err := page.Parse[content.PublishedCursor](r)
	if err != nil {
		return err
	}
	var short *bool
	switch r.URL.Query().Get("format") {
	case "":
	case "short":
		v := true
		short = &v
	case "long":
		v := false
		short = &v
	default:
		return apperr.ValidationFailed(map[string]string{"format": "format must be short or long."})
	}
	res, err := h.svc.List(r.Context(), short, req)
	if err != nil {
		return err
	}
	httpx.Public(w, time.Minute, time.Hour, "listings")
	httpx.JSON(w, http.StatusOK, res)
	return nil
}

func (h *Handler) page(w http.ResponseWriter, r *http.Request) error {
	loc := r.URL.Query().Get("locale")
	if loc == "" {
		loc = "en"
	}
	p, redirect, err := h.svc.Page(r.Context(), loc, r.PathValue("slug"))
	if err != nil {
		return err
	}
	if redirect != nil {
		w.Header().Set("Location", "/v1/public/videos/"+url.PathEscape(redirect.Slug)+"?locale="+url.QueryEscape(loc))
		httpx.Public(w, 5*time.Minute, 24*time.Hour)
		httpx.JSON(w, http.StatusMovedPermanently, map[string]any{"redirect": redirect})
		return nil
	}
	httpx.Public(w, 5*time.Minute, 24*time.Hour, "content:"+strconv.FormatInt(p.ID, 10))
	httpx.JSON(w, http.StatusOK, p)
	return nil
}
