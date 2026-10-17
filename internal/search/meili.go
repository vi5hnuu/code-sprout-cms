// Package search keeps a full-text index of live public content and answers reader searches.
//
// The engine sits behind Index, fed only by outbox events (see indexer.go), so the database
// stays the source of truth: the index can be rebuilt from scratch at any time, and swapping
// Meilisearch for OpenSearch later is a new Index implementation plus a reindex.
package search

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Document is one searchable item.
type Document struct {
	ID          int64    `json:"id"`
	Type        string   `json:"type"`
	Locale      string   `json:"locale"`
	Slug        string   `json:"slug"`
	Title       string   `json:"title"`
	Summary     string   `json:"summary"`
	Body        string   `json:"body"`
	Tags        []string `json:"tags"`
	Category    string   `json:"category"`
	Difficulty  string   `json:"difficulty,omitempty"`
	Access      string   `json:"access"`
	PublishedAt int64    `json:"publishedAt"` // unix seconds, sortable
}

type Query struct {
	Text       string
	Type       string
	Locale     string
	Difficulty string
	Limit      int
	Offset     int
}

// Highlight markers wrap matched terms in Title/Snippet. They are Unicode private-use
// characters, never present in real text, so clients HTML-escape the string first and then
// swap the markers for <mark> tags: no engine-generated HTML reaches the page.
const (
	HighlightStart = ""
	HighlightEnd   = ""
)

type Hit struct {
	ID          int64  `json:"id"`
	Type        string `json:"type"`
	Locale      string `json:"locale"`
	Slug        string `json:"slug"`
	Title       string `json:"title"`
	Snippet     string `json:"snippet"`
	Category    string `json:"category"`
	Difficulty  string `json:"difficulty,omitempty"`
	PublishedAt int64  `json:"publishedAt"`
}

type Result struct {
	Hits           []Hit `json:"hits"`
	EstimatedTotal int   `json:"estimatedTotal"`
	Offset         int   `json:"offset"`
	Limit          int   `json:"limit"`
}

type Index interface {
	Configure(ctx context.Context) error
	Upsert(ctx context.Context, docs []Document) error
	Delete(ctx context.Context, ids []int64) error
	Search(ctx context.Context, q Query) (Result, error)
}

var ErrUnavailable = errors.New("search is unavailable")

// Meili is a minimal Meilisearch REST client (documents, delete, search, settings).
type Meili struct {
	base  string
	key   string
	index string
	http  *http.Client
}

// NewMeili builds a client; an empty url yields one whose calls fail with ErrUnavailable.
func NewMeili(baseURL, apiKey, index string) *Meili {
	return &Meili{base: strings.TrimRight(baseURL, "/"), key: apiKey, index: index, http: &http.Client{Timeout: 10 * time.Second}}
}

// Configure applies the index settings (idempotent). Meilisearch creates the index on the
// first settings update or document write, so there is no separate create step.
func (m *Meili) Configure(ctx context.Context) error {
	settings := map[string]any{
		// Order is relevance weight: a title match beats a tag beats the summary beats the body.
		"searchableAttributes": []string{"title", "tags", "summary", "body"},
		"filterableAttributes": []string{"type", "locale", "difficulty", "access", "category"},
		"sortableAttributes":   []string{"publishedAt"},
		"displayedAttributes":  []string{"id", "type", "locale", "slug", "title", "summary", "body", "category", "difficulty", "publishedAt"},
	}
	return m.do(ctx, http.MethodPatch, "/indexes/"+url.PathEscape(m.index)+"/settings", settings, nil)
}

func (m *Meili) Upsert(ctx context.Context, docs []Document) error {
	if len(docs) == 0 {
		return nil
	}
	return m.do(ctx, http.MethodPost, "/indexes/"+url.PathEscape(m.index)+"/documents?primaryKey=id", docs, nil)
}

func (m *Meili) Delete(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	return m.do(ctx, http.MethodPost, "/indexes/"+url.PathEscape(m.index)+"/documents/delete-batch", ids, nil)
}

func (m *Meili) Search(ctx context.Context, q Query) (Result, error) {
	var filters []string
	add := func(field, value string) {
		if value != "" {
			filters = append(filters, fmt.Sprintf("%s = %q", field, value))
		}
	}
	add("type", q.Type)
	add("locale", q.Locale)
	add("difficulty", q.Difficulty)
	body := map[string]any{
		"q": q.Text, "limit": q.Limit, "offset": q.Offset, "filter": strings.Join(filters, " AND "),
		"attributesToRetrieve":  []string{"id", "type", "locale", "slug", "title", "category", "difficulty", "publishedAt"},
		"attributesToHighlight": []string{"title"},
		"attributesToCrop":      []string{"summary", "body"},
		"cropLength":            24,
		"highlightPreTag":       HighlightStart,
		"highlightPostTag":      HighlightEnd,
	}
	var raw struct {
		Hits []struct {
			Hit
			Formatted struct {
				Title   string `json:"title"`
				Summary string `json:"summary"`
				Body    string `json:"body"`
			} `json:"_formatted"`
		} `json:"hits"`
		EstimatedTotal int `json:"estimatedTotalHits"`
	}
	if err := m.do(ctx, http.MethodPost, "/indexes/"+url.PathEscape(m.index)+"/search", body, &raw); err != nil {
		return Result{}, err
	}
	res := Result{Hits: make([]Hit, len(raw.Hits)), EstimatedTotal: raw.EstimatedTotal, Offset: q.Offset, Limit: q.Limit}
	for i, h := range raw.Hits {
		hit := h.Hit
		hit.Title = h.Formatted.Title
		// Prefer the body excerpt when the match is there; fall back to the summary.
		hit.Snippet = h.Formatted.Summary
		if strings.Contains(h.Formatted.Body, HighlightStart) || hit.Snippet == "" {
			hit.Snippet = h.Formatted.Body
		}
		res.Hits[i] = hit
	}
	return res, nil
}

func (m *Meili) do(ctx context.Context, method, path string, body, out any) error {
	if m.base == "" {
		return ErrUnavailable
	}
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, m.base+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if m.key != "" {
		req.Header.Set("Authorization", "Bearer "+m.key)
	}
	resp, err := m.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		if resp.StatusCode >= 500 {
			return fmt.Errorf("%w: status %d", ErrUnavailable, resp.StatusCode)
		}
		return fmt.Errorf("meilisearch %s %s: %d %s", method, path, resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
