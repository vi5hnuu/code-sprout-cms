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

type Service struct {
	cfg      Config
	paths    Paths
	sitemaps Sitemaps
	http     *http.Client
}

func NewService(cfg Config, paths Paths, sitemaps Sitemaps) *Service {
	return &Service{cfg: cfg, paths: paths, sitemaps: sitemaps, http: &http.Client{Timeout: 10 * time.Second}}
}

func (s *Service) Register(d *outbox.Dispatcher) {
	d.On(outbox.TopicContentPublished, s.contentChanged)
	d.On(outbox.TopicContentUpdated, s.contentChanged)
	d.On(outbox.TopicContentUnpublished, s.contentChanged)
	d.On(outbox.TopicCategoryChanged, func(ctx context.Context, _ outbox.Event) error {
		return s.invalidate(ctx, "categories", "listings")
	})
	d.On(outbox.TopicTagChanged, func(ctx context.Context, _ outbox.Event) error {
		return s.invalidate(ctx, "tags", "listings")
	})
	d.On(outbox.TopicCommentsChanged, func(ctx context.Context, e outbox.Event) error {
		return s.invalidate(ctx, "comments:"+strconv.FormatInt(e.AggregateID, 10))
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
	if s.cfg.RevalidateURL != "" {
		if err := s.post(ctx, s.cfg.RevalidateURL, map[string]any{"tags": tags, "paths": paths},
			map[string]string{"X-Revalidate-Secret": s.cfg.RevalidateSecret}); err != nil {
			return fmt.Errorf("revalidate: %w", err)
		}
	}
	if s.cfg.CloudflareZoneID != "" {
		endpoint := "https://api.cloudflare.com/client/v4/zones/" + url.PathEscape(s.cfg.CloudflareZoneID) + "/purge_cache"
		if err := s.post(ctx, endpoint, map[string]any{"tags": tags},
			map[string]string{"Authorization": "Bearer " + s.cfg.CloudflareAPIToken}); err != nil {
			return fmt.Errorf("cdn purge: %w", err)
		}
	}
	return nil
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
