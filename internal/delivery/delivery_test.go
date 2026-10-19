package delivery

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"code-sprout-cms/internal/content"
	"code-sprout-cms/internal/outbox"
)

// recorder answers every request 200 and keeps what was sent.
type recorder struct {
	mu   sync.Mutex
	sent []sentRequest
}

type sentRequest struct {
	URL    string
	Header http.Header
	Body   map[string]any
}

func (r *recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	var body map[string]any
	b, _ := io.ReadAll(req.Body)
	_ = json.Unmarshal(b, &body)
	r.mu.Lock()
	r.sent = append(r.sent, sentRequest{URL: req.URL.String(), Header: req.Header.Clone(), Body: body})
	r.mu.Unlock()
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("{}")), Header: http.Header{}}, nil
}

type fixedPath struct {
	path string
	live bool
}

func (f fixedPath) PathOf(context.Context, int64) (string, bool, error) { return f.path, f.live, nil }

type sitemapCalls struct{ refreshed []int64 }

func (s *sitemapCalls) Refresh(_ context.Context, _ content.Type, id int64) error {
	s.refreshed = append(s.refreshed, id)
	return nil
}

// handlers stands in for the outbox dispatcher.
type handlers map[string][]outbox.Handler

func (h handlers) On(topic string, fn outbox.Handler) { h[topic] = append(h[topic], fn) }

func newService(cfg Config, paths Paths) (*Service, *recorder, *sitemapCalls, handlers) {
	rec := &recorder{}
	maps := &sitemapCalls{}
	s := NewService(cfg, paths, maps)
	s.http = &http.Client{Transport: rec}
	h := handlers{}
	s.Register(h)
	return s, rec, maps, h
}

func fire(t *testing.T, h handlers, topic string, id int64, payload any) {
	t.Helper()
	raw, _ := json.Marshal(payload)
	for _, fn := range h[topic] {
		if err := fn(context.Background(), outbox.Event{Topic: topic, AggregateID: id, Payload: raw}); err != nil {
			t.Fatal(err)
		}
	}
}

func stringsOf(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

// A renamed article: its sitemap chunk refreshes, the new and old paths revalidate (the old one
// starts redirecting), its data tags drop, and search engines hear about both URLs.
func TestRenamedArticleRefreshesPathsTagsSitemapAndIndexNow(t *testing.T) {
	cfg := Config{RevalidateURL: "https://web/api/revalidate", RevalidateSecret: "s3cret", SiteURL: "https://codesprout.dev", IndexNowKey: "k"}
	_, rec, maps, h := newService(cfg, fixedPath{path: "/dsa/new-slug", live: true})
	fire(t, h, outbox.TopicContentUpdated, 42, outbox.ContentEvent{Type: string(content.TypeArticle), OldSlug: "old-slug"})

	if len(maps.refreshed) != 1 || maps.refreshed[0] != 42 {
		t.Fatalf("sitemap refreshes = %v, want [42]", maps.refreshed)
	}
	if len(rec.sent) != 2 {
		t.Fatalf("sent %d requests, want revalidate + IndexNow", len(rec.sent))
	}
	reval := rec.sent[0]
	if reval.Header.Get("X-Revalidate-Secret") != "s3cret" {
		t.Error("revalidation is not authenticated")
	}
	if got := strings.Join(stringsOf(reval.Body["paths"]), ","); got != "/dsa/new-slug,/dsa/old-slug" {
		t.Errorf("paths = %s", got)
	}
	if got := strings.Join(stringsOf(reval.Body["tags"]), ","); got != "content:42,listings,sitemaps,search" {
		t.Errorf("tags = %s", got)
	}
	ping := rec.sent[1]
	if !strings.HasPrefix(ping.URL, "https://api.indexnow.org/") || ping.Body["host"] != "codesprout.dev" {
		t.Errorf("IndexNow request = %s %v", ping.URL, ping.Body)
	}
	if got := strings.Join(stringsOf(ping.Body["urlList"]), ","); got != "https://codesprout.dev/dsa/new-slug,https://codesprout.dev/dsa/old-slug" {
		t.Errorf("IndexNow urls = %s", got)
	}
}

// An unpublished page has no path any more: its tags still drop (so cached copies go), and no
// search engine is told about a URL that no longer exists.
func TestUnpublishedPageDropsTagsWithoutPaths(t *testing.T) {
	cfg := Config{RevalidateURL: "https://web/api/revalidate", SiteURL: "https://codesprout.dev", IndexNowKey: "k"}
	_, rec, _, h := newService(cfg, fixedPath{live: false})
	fire(t, h, outbox.TopicContentUnpublished, 7, outbox.ContentEvent{Type: string(content.TypeBlog)})
	if len(rec.sent) != 1 {
		t.Fatalf("sent %d requests, want only revalidation", len(rec.sent))
	}
	if p := rec.sent[0].Body["paths"]; p != nil {
		t.Errorf("paths = %v, want none", p)
	}
}

// Taxonomy and comment changes drop exactly the tags that cache them, at Next.js and the CDN.
func TestTaxonomyAndCommentChangesPurgeTheirTags(t *testing.T) {
	cfg := Config{RevalidateURL: "https://web/api/revalidate", CloudflareZoneID: "zone", CloudflareAPIToken: "tok"}
	_, rec, _, h := newService(cfg, fixedPath{})
	fire(t, h, outbox.TopicCategoryChanged, 1, outbox.CategoryEvent{Paths: []string{"dsa"}})
	fire(t, h, outbox.TopicCommentsChanged, 9, outbox.CommentsEvent{ContentID: 9})
	want := []string{"categories,listings", "categories,listings", "comments:9", "comments:9"}
	if len(rec.sent) != len(want) {
		t.Fatalf("sent %d requests, want %d", len(rec.sent), len(want))
	}
	for i, r := range rec.sent {
		if got := strings.Join(stringsOf(r.Body["tags"]), ","); got != want[i] {
			t.Errorf("request %d (%s) tags = %s, want %s", i, r.URL, got, want[i])
		}
	}
	if auth := rec.sent[1].Header.Get("Authorization"); auth != "Bearer tok" {
		t.Errorf("CDN purge auth = %q", auth)
	}
}

// With nothing configured (local development), events are no-ops rather than errors.
func TestUnconfiguredDeliveryIsANoOp(t *testing.T) {
	_, rec, _, h := newService(Config{}, fixedPath{path: "/blog/x", live: true})
	fire(t, h, outbox.TopicContentPublished, 3, outbox.ContentEvent{Type: string(content.TypeBlog)})
	if len(rec.sent) != 0 {
		t.Fatalf("sent %d requests with nothing configured", len(rec.sent))
	}
}
