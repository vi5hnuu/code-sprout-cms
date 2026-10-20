package video_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"code-sprout-cms/internal/access"
	"code-sprout-cms/internal/arenatest"
	"code-sprout-cms/internal/collection"
	"code-sprout-cms/internal/content"
	"code-sprout-cms/internal/judge"
	"code-sprout-cms/internal/platform/page"
	"code-sprout-cms/internal/platform/testenv"
	"code-sprout-cms/internal/problem"
	"code-sprout-cms/internal/video"
)

// fakeYouTube serves the four read calls the sync makes from in-memory state the test edits.
type fakeYouTube struct {
	mu        sync.Mutex
	channel   string
	uploads   []string             // newest first
	videos    map[string]fakeVideo // by id; absent = deleted
	playlists []fakePlaylist
	calls     int
}

type fakeVideo struct {
	channel, title, desc, duration string
	public                         bool
	published                      time.Time
}

type fakePlaylist struct {
	id, title string
	items     []string // video ids (any channel)
}

func (f *fakeYouTube) handler() http.Handler {
	mux := http.NewServeMux()
	write := func(w http.ResponseWriter, v any) { _ = json.NewEncoder(w).Encode(v) }
	mux.HandleFunc("/channels", func(w http.ResponseWriter, r *http.Request) {
		f.count()
		write(w, map[string]any{"items": []any{map[string]any{"contentDetails": map[string]any{"relatedPlaylists": map[string]any{"uploads": "UU-" + f.channel}}}}})
	})
	mux.HandleFunc("/playlistItems", func(w http.ResponseWriter, r *http.Request) {
		f.count()
		f.mu.Lock()
		defer f.mu.Unlock()
		ids := f.uploads
		if pl := r.URL.Query().Get("playlistId"); pl != "UU-"+f.channel {
			ids = nil
			for _, p := range f.playlists {
				if p.id == pl {
					ids = p.items
				}
			}
		}
		items := []any{}
		for _, id := range ids {
			owner := f.channel
			if v, ok := f.videos[id]; ok {
				owner = v.channel
			}
			items = append(items, map[string]any{"snippet": map[string]any{"videoOwnerChannelId": owner, "resourceId": map[string]any{"videoId": id}}})
		}
		write(w, map[string]any{"items": items})
	})
	mux.HandleFunc("/videos", func(w http.ResponseWriter, r *http.Request) {
		f.count()
		f.mu.Lock()
		defer f.mu.Unlock()
		items := []any{}
		for _, id := range strings.Split(r.URL.Query().Get("id"), ",") {
			v, ok := f.videos[id]
			if !ok {
				continue
			}
			privacy := "public"
			if !v.public {
				privacy = "private"
			}
			items = append(items, map[string]any{
				"id": id,
				"snippet": map[string]any{"channelId": v.channel, "title": v.title, "description": v.desc, "publishedAt": v.published,
					"liveBroadcastContent": "none", "thumbnails": map[string]any{"high": map[string]any{"url": "https://i.ytimg.com/vi/" + id + "/hq.jpg", "width": 480, "height": 360}}},
				"contentDetails": map[string]any{"duration": v.duration},
				"status":         map[string]any{"privacyStatus": privacy, "embeddable": true},
			})
		}
		write(w, map[string]any{"items": items})
	})
	mux.HandleFunc("/playlists", func(w http.ResponseWriter, r *http.Request) {
		f.count()
		f.mu.Lock()
		defer f.mu.Unlock()
		items := []any{}
		for _, p := range f.playlists {
			items = append(items, map[string]any{"id": p.id, "snippet": map[string]any{"title": p.title}})
		}
		write(w, map[string]any{"items": items})
	})
	return mux
}

func (f *fakeYouTube) count() { f.mu.Lock(); f.calls++; f.mu.Unlock() }

func (f *fakeYouTube) edit(fn func(f *fakeYouTube)) { f.mu.Lock(); fn(f); f.mu.Unlock() }

// The sync mirrors the channel's own public uploads, links exact-title problems, builds series
// in YouTube's order, respects editors, follows removals and returns, and stops at its quota.
func TestSyncMirrorsAChannel(t *testing.T) {
	e := arenatest.New(t)
	ctx := context.Background()
	tag := testenv.Unique("vid")
	problemID := e.Publish(tag, []problem.Sample{{Input: "1", Output: "1"}}, []judge.TestCase{{Stdin: tag, Expected: "1"}}, nil)

	owner := testenv.User(t, e.DB)
	handle := strings.ReplaceAll(tag, "-", "_")
	if _, err := e.DB.Primary.Exec(ctx, `UPDATE app_user SET handle = $1 WHERE id = $2`, handle, owner.UserID); err != nil {
		t.Fatal(err)
	}

	channel := "UC" + strings.ReplaceAll(tag, "-", "")
	solution, short, other, private, techShort := tag+"-a", tag+"-b", tag+"-c", tag+"-d", tag+"-e"
	day := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	yt := &fakeYouTube{
		channel: channel,
		uploads: []string{techShort, private, short, solution},
		videos: map[string]fakeVideo{
			solution:  {channel, "07. Sum " + tag + " – Two Pointers | LeetCode", "Solving it in O(n).\n\n0:00 Intro\n1:00 Idea\n3:30 Code", "PT8M5S", true, day},
			short:     {channel, "Quick tip " + tag, "A tip.", "PT45S", true, day.Add(time.Hour)},
			other:     {"UCsomeoneelse", "Not mine", "", "PT3M", true, day},
			private:   {channel, "Draft " + tag, "", "PT2M", false, day},
			techShort: {channel, "Tech short " + tag, "", "PT2M", true, day.Add(2 * time.Hour)},
		},
		playlists: []fakePlaylist{
			{id: "PL-top-" + tag, title: "Top 150 " + tag, items: []string{solution, other, short}},
			{id: "PL-shorts-" + tag, title: "tech shorts " + tag, items: []string{techShort}},
		},
	}
	srv := httptest.NewServer(yt.handler())
	defer srv.Close()

	collections := collection.NewService(e.DB, e.Reader, access.NewPolicy(e.DB.Primary))
	newSyncer := func(quota int) *video.Syncer {
		return video.NewSyncer(e.DB, video.Config{APIKey: "k", APIBase: srv.URL, ChannelIDs: []string{channel}, OwnerHandle: handle, DailyQuota: quota},
			e.Contents, e.Problems, collections, video.UserOwners{ByHandle: func(ctx context.Context, _ string) (int64, error) { return owner.UserID, nil }}, nil)
	}
	videos := video.NewService(e.DB, e.Reader)
	if err := newSyncer(1000).SyncAll(ctx); err != nil {
		t.Fatal(err)
	}

	// Own public uploads only.
	idOf := map[string]int64{}
	for _, ext := range []string{solution, short, techShort, private, other} {
		var id int64
		err := e.DB.Primary.QueryRow(ctx, `SELECT content_id FROM video_ext WHERE external_id = $1`, ext).Scan(&id)
		if err == nil {
			idOf[ext] = id
		}
	}
	if len(idOf) != 3 || idOf[private] != 0 || idOf[other] != 0 {
		t.Fatalf("mirrored %v, want the three public own uploads", idOf)
	}

	// The watch page: chapters, and the problem it solves.
	slugOf := func(id int64) string {
		var slug string
		if err := e.DB.Primary.QueryRow(ctx, `SELECT slug FROM content WHERE id = $1`, id).Scan(&slug); err != nil {
			t.Fatal(err)
		}
		return slug
	}
	page, _, err := videos.Page(ctx, "en", slugOf(idOf[solution]))
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Video.Chapters) != 3 || page.Video.DurationSeconds != 485 || page.Video.IsShort {
		t.Errorf("video details = %+v", page.Video)
	}
	if len(page.Video.Problems) != 1 || page.Video.Problems[0].ID != problemID {
		t.Errorf("practice links = %+v, want the problem", page.Video.Problems)
	}
	var linked int64
	if err := e.DB.Primary.QueryRow(ctx, `SELECT to_id FROM content_link WHERE from_id = $1 AND kind = 'VIDEO'`, problemID).Scan(&linked); err != nil || linked != idOf[solution] {
		t.Fatalf("problem's video link = %d, %v", linked, err)
	}

	// A video chosen in the studio wins over the automatic match, survives syncs, and clearing
	// it lets the sync link the match again. The editorial setting becomes a link too.
	linkOf := func(kind string) (to int64) {
		_ = e.DB.Primary.QueryRow(ctx, `SELECT to_id FROM content_link WHERE from_id = $1 AND kind = $2`, problemID, kind).Scan(&to)
		return
	}
	editorialID := e.PublishArticle(tag + "-editorial")
	studio, err := e.Problems.Studio(ctx, e.Editor, problemID)
	if err != nil {
		t.Fatal(err)
	}
	settings := studio.Settings
	shortID := idOf[short]
	settings.VideoContentID, settings.EditorialContentID = &shortID, &editorialID
	// Both videos' pages list the problem ("Practice this problem"), so both refresh.
	refreshes := func(id int64) (n int) {
		_ = e.DB.Primary.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE topic = 'content.updated' AND aggregate_id = $1`, id).Scan(&n)
		return
	}
	oldVideo := linkOf("VIDEO")
	oldBefore, newBefore := refreshes(oldVideo), refreshes(shortID)
	if _, err := e.Problems.UpdateSettings(ctx, e.Editor, problemID, settings); err != nil {
		t.Fatal(err)
	}
	if linkOf("VIDEO") != idOf[short] || linkOf("EDITORIAL") != editorialID {
		t.Fatalf("studio choice not applied: video %d editorial %d", linkOf("VIDEO"), linkOf("EDITORIAL"))
	}
	if refreshes(oldVideo) != oldBefore+1 || refreshes(shortID) != newBefore+1 {
		t.Errorf("the videos whose problem link changed were not refreshed")
	}
	forceReconcile(t, e, channel)
	if err := newSyncer(1000).SyncAll(ctx); err != nil {
		t.Fatal(err)
	}
	if linkOf("VIDEO") != idOf[short] {
		t.Fatal("the sync replaced an editor's video choice")
	}
	settings.VideoContentID = nil
	if _, err := e.Problems.UpdateSettings(ctx, e.Editor, problemID, settings); err != nil {
		t.Fatal(err)
	}
	if linkOf("VIDEO") != 0 {
		t.Fatal("clearing the choice kept its link")
	}
	settings.VideoContentID = &problemID
	if _, err := e.Problems.UpdateSettings(ctx, e.Editor, problemID, settings); err == nil {
		t.Error("a problem was accepted as a video solution")
	}

	// Shorts: by duration, and by membership of a playlist named as shorts.
	for ext, want := range map[string]bool{short: true, techShort: true, solution: false} {
		var isShort bool
		_ = e.DB.Primary.QueryRow(ctx, `SELECT is_short FROM video_ext WHERE external_id = $1`, ext).Scan(&isShort)
		if isShort != want {
			t.Errorf("%s is_short = %v, want %v", ext, isShort, want)
		}
	}

	// Series: own uploads only, in the playlist's order.
	series, err := collections.PublicBySlug(ctx, collection.KindSeries, content.Slugify("Top 150 "+tag), pageReq())
	if err != nil {
		t.Fatal(err)
	}
	if len(series.Items) != 2 || series.Items[0].ID != idOf[solution] || series.Items[1].ID != idOf[short] {
		t.Errorf("series items = %v", ids(series.Items))
	}

	// A second run changes nothing.
	revs := func(id int64) (n int) {
		_ = e.DB.Primary.QueryRow(ctx, `SELECT count(*) FROM content_revision WHERE content_id = $1`, id).Scan(&n)
		return
	}
	before := revs(idOf[solution])
	forceReconcile(t, e, channel)
	if err := newSyncer(1000).SyncAll(ctx); err != nil {
		t.Fatal(err)
	}
	if after := revs(idOf[solution]); after != before {
		t.Errorf("an unchanged video got %d new revisions", after-before)
	}

	// A title edited on YouTube is followed while no editor has touched the text…
	yt.edit(func(f *fakeYouTube) {
		v := f.videos[solution]
		v.title = "07. Sum " + tag + " – Two Pointers (2026)"
		f.videos[solution] = v
	})
	forceReconcile(t, e, channel)
	if err := newSyncer(1000).SyncAll(ctx); err != nil {
		t.Fatal(err)
	}
	if got := titleOf(t, e, idOf[solution]); !strings.HasSuffix(got, "(2026)") {
		t.Fatalf("title not refreshed: %q", got)
	}
	// …and left alone once an editor has: first while the edit is an unpublished draft (the
	// sync must not publish over it), then once it is live.
	admin := testenv.Admin(t, e.DB)
	d, err := e.Contents.Get(ctx, admin, idOf[solution])
	if err != nil {
		t.Fatal(err)
	}
	in := content.DraftInput{Meta: d.Meta, Body: "Notes by the editor."}
	in.Title = "Editor's title"
	saved, err := e.Contents.SaveDraft(ctx, admin, d.ID, d.DraftRevNo, in)
	if err != nil {
		t.Fatal(err)
	}
	yt.edit(func(f *fakeYouTube) { v := f.videos[solution]; v.title = "Retitled on YouTube"; f.videos[solution] = v })
	forceReconcile(t, e, channel)
	if err := newSyncer(1000).SyncAll(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.Contents.Get(ctx, admin, d.ID); got.DraftRevNo != saved.DraftRevNo || got.Meta.Title != "Editor's title" {
		t.Fatalf("the sync replaced an editor's unpublished draft: rev %d title %q", got.DraftRevNo, got.Meta.Title)
	}
	if _, err := e.Contents.Publish(ctx, admin, d.ID); err != nil {
		t.Fatal(err)
	}
	// A media change alone (the video was re-cut) still refreshes the cached pages.
	updates := func() (n int) {
		_ = e.DB.Primary.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE topic = 'content.updated' AND aggregate_id = $1`, d.ID).Scan(&n)
		return n
	}
	before = updates()
	yt.edit(func(f *fakeYouTube) { v := f.videos[solution]; v.duration = "PT9M59S"; f.videos[solution] = v })
	forceReconcile(t, e, channel)
	if err := newSyncer(1000).SyncAll(ctx); err != nil {
		t.Fatal(err)
	}
	if updates() != before+1 {
		t.Errorf("a duration change did not refresh the page")
	}
	yt.edit(func(f *fakeYouTube) { v := f.videos[solution]; v.desc = "New description"; f.videos[solution] = v })
	forceReconcile(t, e, channel)
	if err := newSyncer(1000).SyncAll(ctx); err != nil {
		t.Fatal(err)
	}
	if got := titleOf(t, e, idOf[solution]); got != "Editor's title" {
		t.Errorf("the sync overwrote an editor: title %q", got)
	}

	// Deleted on YouTube → taken down; back on YouTube → published again.
	gone := yt.videos[short]
	yt.edit(func(f *fakeYouTube) { delete(f.videos, short) })
	forceReconcile(t, e, channel)
	if err := newSyncer(1000).SyncAll(ctx); err != nil {
		t.Fatal(err)
	}
	if statusOf(t, e, idOf[short]) != "ARCHIVED" {
		t.Errorf("deleted video still live")
	}
	yt.edit(func(f *fakeYouTube) { f.videos[short] = gone })
	forceReconcile(t, e, channel)
	if err := newSyncer(1000).SyncAll(ctx); err != nil {
		t.Fatal(err)
	}
	if statusOf(t, e, idOf[short]) != "PUBLISHED" {
		t.Errorf("returned video not republished")
	}

	// Out of quota: the run stops quietly and records it, without errors.
	if _, err := e.DB.Primary.Exec(ctx, `UPDATE video_source SET quota_day = (now() AT TIME ZONE 'utc')::date, quota_used = 1000 WHERE channel_id = $1`, channel); err != nil {
		t.Fatal(err)
	}
	calls := yt.calls
	if err := newSyncer(1000).SyncAll(ctx); err != nil {
		t.Fatalf("a spent budget must not be an error: %v", err)
	}
	if yt.calls != calls {
		t.Errorf("called YouTube %d times with no budget left", yt.calls-calls)
	}
}

func forceReconcile(t *testing.T, e *arenatest.Env, channel string) {
	t.Helper()
	if _, err := e.DB.Primary.Exec(context.Background(), `UPDATE video_source SET reconciled_at = now() - interval '2 days' WHERE channel_id = $1`, channel); err != nil {
		t.Fatal(err)
	}
}

func titleOf(t *testing.T, e *arenatest.Env, id int64) (title string) {
	t.Helper()
	if err := e.DB.Primary.QueryRow(context.Background(), `SELECT title FROM content WHERE id = $1`, id).Scan(&title); err != nil {
		t.Fatal(err)
	}
	return
}

func statusOf(t *testing.T, e *arenatest.Env, id int64) (status string) {
	t.Helper()
	if err := e.DB.Primary.QueryRow(context.Background(), `SELECT status FROM content WHERE id = $1`, id).Scan(&status); err != nil {
		t.Fatal(err)
	}
	return
}

func pageReq() page.Request[float64] { return page.Request[float64]{Limit: 50} }

func ids(items []content.ListItem) []int64 {
	out := make([]int64, len(items))
	for i, it := range items {
		out[i] = it.ID
	}
	return out
}
