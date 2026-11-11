// Package delivery keeps everything outside the database in step after content changes:
// sitemap chunks, the Next.js data cache (revalidateTag), CDN caches (purge by cache tag) and
// search engines (IndexNow). It runs only from outbox events in the worker; every step is
// idempotent, so redelivery is harmless.
package delivery

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"code-sprout-cms/internal/content"
	"code-sprout-cms/internal/outbox"
)

type Config struct {
	// Next.js revalidation endpoint and shared secret; empty disables.
	RevalidateURL    string
	RevalidateSecret string
	// Cloudflare zone purge by cache tag; empty disables.
	CloudflareZoneID   string
	CloudflareAPIToken string
	// Public site origin (https://...) and IndexNow key; empty disables IndexNow. The web app
	// serves the key at /{key}.txt as IndexNow requires.
	SiteURL     string
	IndexNowKey string
}

type Paths interface {
	PathOf(ctx context.Context, id int64) (string, bool, error)
}

type Sitemaps interface {
	Refresh(ctx context.Context, t content.Type, contentID int64) error
}

// Replica answers whether the read replica has caught up with the primary (db.DB implements
// it). Revalidation makes the web re-read content through the replica, so it must not run
// before the replica has the change.
type Replica interface {
	WaitReplicaCaughtUp(ctx context.Context, timeout time.Duration) error
}

// replicaWait bounds how long one event waits for the replica before it is retried later.
const replicaWait = 5 * time.Second

type Service struct {
	cfg      Config
	paths    Paths
	sitemaps Sitemaps
	replica  Replica
	http     *http.Client
}

func NewService(cfg Config, paths Paths, sitemaps Sitemaps, replica Replica) *Service {
	return &Service{cfg: cfg, paths: paths, sitemaps: sitemaps, replica: replica, http: &http.Client{Timeout: 10 * time.Second}}
}

// Subscriber is where delivery registers its event handlers (the outbox dispatcher).
type Subscriber interface {
	On(topic string, h outbox.Handler)
}

// Register subscribes delivery to the events that change what readers and crawlers see.
func (s *Service) Register(d Subscriber) {
	d.On(outbox.TopicContentPublished, s.contentChanged)
	d.On(outbox.TopicContentUpdated, s.contentChanged)
	d.On(outbox.TopicContentUnpublished, s.contentChanged)
	d.On(outbox.TopicCategoryChanged, func(ctx context.Context, _ outbox.Event) error {
		return s.invalidate(ctx, "categories", "listings")
	})
	d.On(outbox.TopicTagChanged, func(ctx context.Context, _ outbox.Event) error {
		return s.invalidate(ctx, "tags", "listings")
	})
	// A thread is read live through the web server's proxy (never from the Next.js data cache),
	// so the only stale copy a comment can leave behind is the CDN's; the Next.js call is
	// skipped. Comments are the most frequent event by far, and the dispatcher coalesces a burst
	// on one thread into a single purge.
	d.On(outbox.TopicCommentsChanged, func(ctx context.Context, e outbox.Event) error {
		return s.purgeCDN(ctx, []string{"comments:" + strconv.FormatInt(e.AggregateID, 10)})
	})
}

func (s *Service) contentChanged(ctx context.Context, e outbox.Event) error {
	var ev outbox.ContentEvent
	if err := json.Unmarshal(e.Payload, &ev); err != nil {
		return err
	}
	if err := s.sitemaps.Refresh(ctx, content.Type(ev.Type), e.AggregateID); err != nil {
		return err
	}
	// Everything below makes readers (and the web's re-render) re-read this content from the
	// replica; a lagging replica would re-cache the old version until the next publish.
	if err := s.replica.WaitReplicaCaughtUp(ctx, replicaWait); err != nil {
		return err
	}
	path, ok, err := s.paths.PathOf(ctx, e.AggregateID)
	if err != nil {
		return err
	}
	var paths []string
	if ok {
		paths = append(paths, path)
		if ev.OldSlug != "" {
			// Same section, old slug: the old page must start redirecting, and crawlers told.
			paths = append(paths, path[:strings.LastIndexByte(path, '/')+1]+url.PathEscape(ev.OldSlug))
		}
	}
	// The web caches pages by path (it cannot know a page's content id before fetching it),
	// so pages are revalidated by path, API responses by tag.
	if err := s.refresh(ctx, paths, "content:"+strconv.FormatInt(e.AggregateID, 10), "listings", "sitemaps", "search"); err != nil {
		return err
	}
	return s.indexNow(ctx, paths)
}

// invalidate drops cached responses carrying these tags in Next.js and at the CDN.
func (s *Service) invalidate(ctx context.Context, tags ...string) error {
	return s.refresh(ctx, nil, tags...)
}

// refresh revalidates pages by path and data by tag in Next.js, and purges the tags at the CDN.
func (s *Service) refresh(ctx context.Context, paths []string, tags ...string) error {
	if err := s.revalidateWeb(ctx, paths, tags); err != nil {
		return err
	}
	return s.purgeCDN(ctx, tags)
}

// revalidateWeb asks the Next.js app to drop its cached pages (by path) and data (by tag).
func (s *Service) revalidateWeb(ctx context.Context, paths, tags []string) error {
	if s.cfg.RevalidateURL == "" {
		return nil
	}
	// Always arrays: a nil slice marshals as JSON null, which the endpoint rejects.
	if err := s.post(ctx, s.cfg.RevalidateURL, map[string]any{"tags": nonNil(tags), "paths": nonNil(paths)},
		map[string]string{"X-Revalidate-Secret": s.cfg.RevalidateSecret}); err != nil {
		return fmt.Errorf("revalidate: %w", err)
	}
	return nil
}

// purgeCDN drops every CDN-cached API response carrying one of the tags (Cache-Tag header).
func (s *Service) purgeCDN(ctx context.Context, tags []string) error {
	if s.cfg.CloudflareZoneID == "" || len(tags) == 0 {
		return nil
	}
	endpoint := "https://api.cloudflare.com/client/v4/zones/" + url.PathEscape(s.cfg.CloudflareZoneID) + "/purge_cache"
	if err := s.post(ctx, endpoint, map[string]any{"tags": tags},
		map[string]string{"Authorization": "Bearer " + s.cfg.CloudflareAPIToken}); err != nil {
		return fmt.Errorf("cdn purge: %w", err)
	}
	return nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// indexNow tells Bing, Yandex and other IndexNow engines that these URLs changed. (Google does
// not support IndexNow; it relies on sitemap lastmod.)
func (s *Service) indexNow(ctx context.Context, paths []string) error {
	if s.cfg.IndexNowKey == "" || s.cfg.SiteURL == "" || len(paths) == 0 {
		return nil
	}
	site, err := url.Parse(s.cfg.SiteURL)
	if err != nil {
		return err
	}
	list := make([]string, len(paths))
	for i, p := range paths {
		list[i] = strings.TrimRight(s.cfg.SiteURL, "/") + p
	}
	return s.post(ctx, "https://api.indexnow.org/indexnow", map[string]any{
		"host": site.Host, "key": s.cfg.IndexNowKey,
		"keyLocation": strings.TrimRight(s.cfg.SiteURL, "/") + "/" + s.cfg.IndexNowKey + ".txt",
		"urlList":     list,
	}, nil)
}

func (s *Service) post(ctx context.Context, endpoint string, body any, headers map[string]string) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s: status %d: %s", endpoint, resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return nil
}
