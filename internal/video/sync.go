package video

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"code-sprout-cms/internal/collection"
	"code-sprout-cms/internal/content"
	"code-sprout-cms/internal/platform/db"
	"code-sprout-cms/internal/video/videodb"
)

// Config selects what to mirror and how much YouTube quota it may spend.
type Config struct {
	APIKey            string
	APIBase           string   // "" = the public API (tests point it at a stub)
	ChannelIDs        []string // channels to mirror
	OwnerHandle       string   // the Code Sprout account the videos are published as
	ExcludedPlaylists []string // playlist ids never mirrored as series
	Interval          time.Duration
	DailyQuota        int // units per source per UTC day (YouTube grants 10,000 per project)
	ReconcileEvery    time.Duration
}

// Enabled reports whether the sync can run (it needs a key and at least one channel).
func (c Config) Enabled() bool { return c.APIKey != "" && len(c.ChannelIDs) > 0 }

// Contents is the slice of the content domain the sync writes through.
type Contents interface {
	PublishSynced(ctx context.Context, tx pgx.Tx, ownerID, id int64, in content.SyncedItem) (int64, int32, error)
	ArchiveSynced(ctx context.Context, tx pgx.Tx, ownerID, id int64) error
}

type Problems interface {
	LinkVideoByTitle(ctx context.Context, tx pgx.Tx, videoID int64, videoTitle string) ([]int64, error)
}

type Collections interface {
	UpsertMirrored(ctx context.Context, tx pgx.Tx, ownerID int64, m collection.Mirrored) (int64, error)
}

type Owners interface {
	IDByHandle(ctx context.Context, handle string) (int64, error)
}

// Observer receives sync outcomes for metrics; nil-safe.
type Observer interface {
	SyncRun(result string)
	QuotaSpent(units int)
	VideoSynced(action string)
}

// Syncer mirrors YouTube channels into VIDEO content and SERIES collections.
//
// Cost model: an incremental run reads the newest uploads page and stops at the first page with
// nothing new (usually 2 quota units). A full reconcile (daily) re-reads everything to catch
// edits and removals; it is resumable and stops when the day's budget is spent, continuing the
// next day, so even a huge channel never exceeds the quota.
type Syncer struct {
	db          *db.DB
	yt          *YouTube
	contents    Contents
	problems    Problems
	collections Collections
	owners      Owners
	cfg         Config
	obs         Observer
}

func NewSyncer(d *db.DB, cfg Config, contents Contents, problems Problems, collections Collections, owners Owners, obs Observer) *Syncer {
	if cfg.DailyQuota <= 0 {
		cfg.DailyQuota = 5000
	}
	if cfg.ReconcileEvery <= 0 {
		cfg.ReconcileEvery = 24 * time.Hour
	}
	if cfg.ExcludedPlaylists == nil {
		cfg.ExcludedPlaylists = []string{} // an empty list, never SQL NULL
	}
	return &Syncer{db: d, yt: NewYouTube(cfg.APIKey, cfg.APIBase), contents: contents, problems: problems,
		collections: collections, owners: owners, cfg: cfg, obs: obs}
}

// Run syncs every configured channel now and then every Interval until ctx ends.
func (s *Syncer) Run(ctx context.Context) {
	if !s.cfg.Enabled() {
		slog.Info("video sync off (set YOUTUBE_API_KEY and YOUTUBE_CHANNEL_IDS)")
		return
	}
	interval := s.cfg.Interval
	if interval <= 0 {
		interval = 15 * time.Minute
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if err := s.SyncAll(ctx); err != nil && ctx.Err() == nil {
			slog.Error("video sync", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// SyncAll syncs exactly the configured channels (the configuration is the source of truth):
// each is registered or refreshed, and channels no longer configured stop syncing.
func (s *Syncer) SyncAll(ctx context.Context) error {
	ownerID, err := s.owners.IDByHandle(ctx, s.cfg.OwnerHandle)
	if err != nil {
		return fmt.Errorf("owner @%s: %w", s.cfg.OwnerHandle, err)
	}
	q := videodb.New(s.db.Primary)
	if err := q.DisableOtherSources(ctx, s.cfg.ChannelIDs); err != nil {
		return err
	}
	var errs []error
	for _, ch := range s.cfg.ChannelIDs {
		src, err := q.UpsertSource(ctx, videodb.UpsertSourceParams{ChannelID: ch, OwnerUserID: ownerID, ExcludedPlaylists: s.cfg.ExcludedPlaylists})
		if err != nil {
			return err
		}
		errs = append(errs, s.syncSource(ctx, src))
	}
	return errors.Join(errs...)
}

// syncSource runs one channel under its advisory lock (held on one connection), so several
// workers never sync the same channel twice at once.
func (s *Syncer) syncSource(ctx context.Context, src videodb.VideoSource) error {
	conn, err := s.db.Primary.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	lq := videodb.New(conn)
	got, err := lq.TryLockSource(ctx, int32(src.ID))
	if err != nil || !got {
		return err // another worker is on it
	}
	defer func() { _ = lq.UnlockSource(context.WithoutCancel(ctx), int32(src.ID)) }()

	run := &run{s: s, src: src}
	err = run.incremental(ctx)
	if err == nil && (src.ReconcileStartedAt != nil || src.ReconciledAt == nil || time.Since(*src.ReconciledAt) > s.cfg.ReconcileEvery) {
		err = run.reconcile(ctx)
	}
	result := "ok"
	var msg *string
	switch {
	case errors.Is(err, ErrQuota) || errors.Is(err, errBudget):
		result, err = "quota", nil // resumes on a later tick
	case err != nil:
		result = "error"
		m := err.Error()
		msg = &m
	}
	if s.obs != nil {
		s.obs.SyncRun(result)
	}
	if rerr := videodb.New(s.db.Primary).RecordSync(ctx, videodb.RecordSyncParams{ID: src.ID, LastError: msg}); rerr != nil {
		return rerr
	}
	return err
}

var errBudget = errors.New("daily quota budget spent")

// run is one sync of one source.
type run struct {
	s   *Syncer
	src videodb.VideoSource
}

// spend reserves units of today's budget before a call; errBudget when the day is spent.
func (r *run) spend(ctx context.Context, units int) error {
	_, err := videodb.New(r.s.db.Primary).SpendQuota(ctx, videodb.SpendQuotaParams{ID: r.src.ID, Units: int32(units), DailyLimit: int32(r.s.cfg.DailyQuota)})
	if db.IsNoRows(err) {
		return errBudget
	}
	if err == nil && r.s.obs != nil {
		r.s.obs.QuotaSpent(units)
	}
	return err
}

func (r *run) uploads(ctx context.Context) (string, error) {
	if r.src.UploadsPlaylistID != nil {
		return *r.src.UploadsPlaylistID, nil
	}
	if err := r.spend(ctx, 1); err != nil {
		return "", err
	}
	id, err := r.s.yt.UploadsPlaylist(ctx, r.src.ChannelID)
	if err != nil {
		return "", err
	}
	r.src.UploadsPlaylistID = &id
	return id, videodb.New(r.s.db.Primary).SetUploadsPlaylist(ctx, videodb.SetUploadsPlaylistParams{ID: r.src.ID, PlaylistID: &id})
}

// incremental publishes uploads not seen before, newest first, stopping at the first page that
// holds nothing new.
func (r *run) incremental(ctx context.Context) error {
	playlist, err := r.uploads(ctx)
	if err != nil {
		return err
	}
	token := ""
	for {
		if err := r.spend(ctx, 1); err != nil {
			return err
		}
		entries, next, err := r.s.yt.PlaylistPage(ctx, playlist, token)
		if err != nil {
			return err
		}
		ids := r.own(entries)
		known, err := r.known(ctx, ids)
		if err != nil {
			return err
		}
		var fresh []string
		for _, id := range ids {
			if _, ok := known[id]; !ok {
				fresh = append(fresh, id)
			}
		}
		if len(fresh) == 0 {
			return nil
		}
		if err := r.apply(ctx, fresh); err != nil {
			return err
		}
		if next == "" {
			return nil
		}
		token = next
	}
}

// reconcile re-reads the whole channel (resumable across runs and days), then archives what
// left the channel and refreshes playlists.
func (r *run) reconcile(ctx context.Context) error {
	q := videodb.New(r.s.db.Primary)
	state, err := q.BeginReconcile(ctx, r.src.ID)
	if err != nil {
		return err
	}
	playlist, err := r.uploads(ctx)
	if err != nil {
		return err
	}
	token := ""
	if state.ReconcilePage != nil {
		token = *state.ReconcilePage
	}
	for {
		if err := r.spend(ctx, 1); err != nil {
			return err
		}
		entries, next, err := r.s.yt.PlaylistPage(ctx, playlist, token)
		if err != nil {
			return err
		}
		if err := r.apply(ctx, r.own(entries)); err != nil {
			return err
		}
		if next == "" {
			break
		}
		token = next
		if err := q.SaveReconcilePage(ctx, videodb.SaveReconcilePageParams{ID: r.src.ID, Page: &token}); err != nil {
			return err
		}
	}
	if err := r.archiveUnseen(ctx, state.StartedAt); err != nil {
		return err
	}
	if err := r.playlists(ctx); err != nil {
		return err
	}
	return q.FinishReconcile(ctx, r.src.ID)
}

// own keeps the channel's own uploads (playlists can hold other channels' videos).
func (r *run) own(entries []PlaylistEntry) []string {
	ids := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.OwnerChannel == "" || e.OwnerChannel == r.src.ChannelID {
			ids = append(ids, e.VideoID)
		}
	}
	return ids
}

func (r *run) known(ctx context.Context, ids []string) (map[string]int64, error) {
	rows, err := videodb.New(r.s.db.Replica).ContentIDsByExternal(ctx, ids)
	out := make(map[string]int64, len(rows))
	for _, row := range rows {
		out[row.ExternalID] = row.ContentID
	}
	return out, err
}

// apply fetches details for up to 50 ids (one call) and mirrors each video in its own
// transaction, so one bad video never blocks the rest.
func (r *run) apply(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	if err := r.spend(ctx, 1); err != nil {
		return err
	}
	details, err := r.s.yt.Videos(ctx, ids)
	if err != nil {
		return err
	}
	served := make(map[string]bool, len(details))
	for _, d := range details {
		served[d.ID] = true
		if err := r.s.db.InTx(ctx, func(tx pgx.Tx) error { return r.upsert(ctx, tx, d) }); err != nil {
			slog.Warn("video sync: skipped a video", "video", d.ID, "err", err)
			if r.s.obs != nil {
				r.s.obs.VideoSynced("error")
			}
		}
	}
	// Ids in the playlist that YouTube no longer serves (deleted, private) are gone.
	for _, id := range ids {
		if !served[id] {
			if err := r.removeExternal(ctx, id); err != nil {
				return err
			}
		}
	}
	return videodb.New(r.s.db.Primary).MarkSeen(ctx, ids)
}

const maxTitleRunes = 200

func (r *run) upsert(ctx context.Context, tx pgx.Tx, d Upload) error {
	q := videodb.New(tx)
	ext, err := q.GetExtForUpdate(ctx, d.ID)
	exists := err == nil
	if err != nil && !db.IsNoRows(err) {
		return err
	}
	// Only public, embeddable, finished uploads of this channel are shown.
	if !d.Public || !d.Embeddable || d.Live || d.ChannelID != r.src.ChannelID {
		if exists {
			return r.remove(ctx, tx, ext.ContentID)
		}
		return nil
	}
	title := truncateRunes(strings.TrimSpace(d.Title), maxTitleRunes)
	item := content.SyncedItem{
		Type: content.TypeVideo, Title: title, Summary: summaryFor(d, title), Body: DescriptionMarkdown(d.Description),
		FallbackSlug: content.Slugify(title) + "-" + strings.ToLower(strings.ReplaceAll(d.ID, "_", "-")),
	}
	hash := sourceHash(d)
	chapters, err := json.Marshal(ParseChapters(d.Description))
	if err != nil {
		return err
	}
	media := mediaOf(d)
	owner := r.src.OwnerUserID

	if !exists {
		id, rev, err := r.s.contents.PublishSynced(ctx, tx, owner, 0, item)
		if err != nil {
			return err
		}
		if err := q.InsertExt(ctx, videodb.InsertExtParams{
			ContentID: id, SourceID: r.src.ID, ExternalID: d.ID, DurationS: int32(d.Duration), PublishedAt: d.PublishedAt,
			ThumbnailUrl: media.url, ThumbnailW: media.w, ThumbnailH: media.h, IsShort: d.Duration <= shortSeconds,
			Chapters: chapters, SyncedRevNo: &rev, SourceHash: hash,
		}); err != nil {
			return err
		}
		if _, err := r.s.problems.LinkVideoByTitle(ctx, tx, id, title); err != nil {
			return err
		}
		r.observe("created")
		return nil
	}

	isShort := ext.IsShort || d.Duration <= shortSeconds
	mediaChanged := ext.DurationS != int32(d.Duration) || !ext.PublishedAt.Equal(d.PublishedAt) || ext.IsShort != isShort ||
		ext.ThumbnailUrl != media.url || !equalPtr(ext.ThumbnailW, media.w) || !equalPtr(ext.ThumbnailH, media.h) ||
		!sameChapters(ext.Chapters, d.Description)
	if mediaChanged {
		if err := q.UpdateExtMedia(ctx, videodb.UpdateExtMediaParams{
			ContentID: ext.ContentID, DurationS: int32(d.Duration), PublishedAt: d.PublishedAt, ThumbnailUrl: media.url,
			ThumbnailW: media.w, ThumbnailH: media.h, IsShort: isShort, Chapters: chapters,
		}); err != nil {
			return err
		}
	}
	st, err := content.Revisions(ctx, tx, ext.ContentID)
	if err != nil {
		return err
	}
	// The sync may rewrite the text only while no editor has: the live revision is still the
	// one it published and there is no newer draft in the studio. An item it took down comes
	// back when the video does.
	untouched := st.IsLive && ext.SyncedRevNo != nil && st.Live == *ext.SyncedRevNo && st.Draft == st.Live
	back := !st.IsLive && ext.RemovedAt != nil
	if (untouched && string(ext.SourceHash) != string(hash)) || back {
		_, rev, err := r.s.contents.PublishSynced(ctx, tx, owner, ext.ContentID, item)
		if err != nil {
			return err
		}
		if err := q.SetSyncedRev(ctx, videodb.SetSyncedRevParams{ContentID: ext.ContentID, RevNo: &rev, SourceHash: hash}); err != nil {
			return err
		}
		r.observe("updated")
		return nil
	}
	// Media (thumbnail, duration, chapters) is shown on the cached watch page and in listings,
	// so a change to it alone still refreshes them.
	if mediaChanged && st.IsLive {
		if err := content.Touched(ctx, tx, ext.ContentID); err != nil {
			return err
		}
		r.observe("updated")
		return nil
	}
	r.observe("unchanged")
	return nil
}

// sameChapters reports whether stored chapters (jsonb, so not byte-comparable with fresh JSON)
// match the ones the description now yields.
func sameChapters(stored []byte, description string) bool {
	var old []Chapter
	if err := json.Unmarshal(stored, &old); err != nil {
		return false
	}
	return slices.Equal(old, ParseChapters(description))
}

func equalPtr[T comparable](a, b *T) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// shortSeconds: anything this short is a Short whatever its playlist (YouTube's original limit).
const shortSeconds = 60

// removeExternal takes down a mirrored video YouTube no longer serves.
func (r *run) removeExternal(ctx context.Context, externalID string) error {
	return r.s.db.InTx(ctx, func(tx pgx.Tx) error {
		ext, err := videodb.New(tx).GetExtForUpdate(ctx, externalID)
		if db.IsNoRows(err) {
			return nil
		}
		if err != nil {
			return err
		}
		return r.remove(ctx, tx, ext.ContentID)
	})
}

func (r *run) remove(ctx context.Context, tx pgx.Tx, contentID int64) error {
	if st, err := content.Revisions(ctx, tx, contentID); err != nil || !st.IsLive {
		return err
	}
	if err := r.s.contents.ArchiveSynced(ctx, tx, r.src.OwnerUserID, contentID); err != nil {
		return err
	}
	r.observe("removed")
	return videodb.New(tx).MarkRemoved(ctx, contentID)
}

// archiveUnseen takes down videos a completed full pass did not find on the channel.
func (r *run) archiveUnseen(ctx context.Context, passStart time.Time) error {
	for {
		ids, err := videodb.New(r.s.db.Primary).ListUnseen(ctx, videodb.ListUnseenParams{SourceID: r.src.ID, Before: passStart, RowLimit: 200})
		if err != nil || len(ids) == 0 {
			return err
		}
		for _, id := range ids {
			if err := r.s.db.InTx(ctx, func(tx pgx.Tx) error {
				if err := r.remove(ctx, tx, id); err != nil {
					return err
				}
				// Seen now (as gone), so the next batch moves on.
				return videodb.New(tx).MarkSeenContent(ctx, id)
			}); err != nil {
				return err
			}
		}
	}
}

// playlists mirrors the channel's playlists as SERIES, keeping only the channel's own uploads
// in YouTube's order. Playlists named as shorts also mark their short videos as Shorts.
func (r *run) playlists(ctx context.Context) error {
	excluded := make(map[string]bool, len(r.src.ExcludedPlaylists))
	for _, id := range r.src.ExcludedPlaylists {
		excluded[id] = true
	}
	token := ""
	for {
		if err := r.spend(ctx, 1); err != nil {
			return err
		}
		lists, next, err := r.s.yt.PlaylistsPage(ctx, r.src.ChannelID, token)
		if err != nil {
			return err
		}
		for _, pl := range lists {
			if excluded[pl.ID] {
				continue
			}
			if err := r.playlist(ctx, pl); err != nil {
				return err
			}
		}
		if next == "" {
			return nil
		}
		token = next
	}
}

// maxShortSeconds: YouTube Shorts may be up to three minutes since 2024.
const maxShortSeconds = 180

func (r *run) playlist(ctx context.Context, pl Playlist) error {
	var ids []string
	token := ""
	for {
		if err := r.spend(ctx, 1); err != nil {
			return err
		}
		entries, next, err := r.s.yt.PlaylistPage(ctx, pl.ID, token)
		if err != nil {
			return err
		}
		ids = append(ids, r.own(entries)...)
		if next == "" {
			break
		}
		token = next
	}
	known, err := r.known(ctx, ids)
	if err != nil {
		return err
	}
	var contentIDs []int64
	seen := map[int64]bool{}
	for _, id := range ids {
		if cid, ok := known[id]; ok && !seen[cid] {
			seen[cid] = true
			contentIDs = append(contentIDs, cid)
		}
	}
	if strings.Contains(strings.ToLower(pl.Title), "short") {
		if err := videodb.New(r.s.db.Primary).MarkShorts(ctx, videodb.MarkShortsParams{ExternalIds: ids, MaxSeconds: maxShortSeconds}); err != nil {
			return err
		}
	}
	if len(contentIDs) == 0 {
		return nil // nothing of the channel's own to show
	}
	title := truncateRunes(strings.TrimSpace(pl.Title), maxTitleRunes)
	var desc *string
	if d := strings.TrimSpace(pl.Description); d != "" {
		desc = &d
	}
	return r.s.db.InTx(ctx, func(tx pgx.Tx) error {
		_, err := r.s.collections.UpsertMirrored(ctx, tx, r.src.OwnerUserID, collection.Mirrored{
			Source: "YOUTUBE", ExternalRef: pl.ID, Kind: collection.KindSeries, Slug: content.Slugify(title),
			FallbackSlug: content.Slugify(title) + "-" + strings.ToLower(strings.ReplaceAll(pl.ID[max(0, len(pl.ID)-8):], "_", "-")),
			Title:        title, Description: desc, ContentIDs: contentIDs,
		})
		return err
	})
}

func (r *run) observe(action string) {
	if r.s.obs != nil {
		r.s.obs.VideoSynced(action)
	}
}

type media struct {
	url  string
	w, h *int32
}

func mediaOf(d Upload) media {
	m := media{url: d.Thumbnail.URL}
	if m.url == "" {
		m.url = "https://i.ytimg.com/vi/" + d.ID + "/hqdefault.jpg"
	}
	if d.Thumbnail.Width > 0 && d.Thumbnail.Height > 0 {
		w, h := int32(d.Thumbnail.Width), int32(d.Thumbnail.Height)
		m.w, m.h = &w, &h
	}
	return m
}

func sourceHash(d Upload) []byte {
	sum := sha256.Sum256([]byte(d.Title + "\x00" + d.Description))
	return sum[:]
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n]))
}

// UserOwners resolves the owner account by its public handle (user.Service.ByHandle).
type UserOwners struct {
	ByHandle func(ctx context.Context, handle string) (int64, error)
}

func (u UserOwners) IDByHandle(ctx context.Context, handle string) (int64, error) {
	return u.ByHandle(ctx, handle)
}

// summaryFor is the description's first paragraph when it is long enough for search results;
// otherwise a plain description of what the page is.
func summaryFor(d Upload, title string) *string {
	if s := Summary(d.Description); s != nil && len([]rune(*s)) >= content.MinSummary {
		return s
	}
	mins := max(1, (d.Duration+30)/60)
	s := fmt.Sprintf("Watch “%s”, a %d-minute video walkthrough.", title, mins)
	if d.Duration < 60 {
		s = fmt.Sprintf("Watch “%s”, a short video under a minute long.", title)
	}
	for len([]rune(s)) < content.MinSummary {
		s += " Notes and chapters below."
	}
	return &s
}
