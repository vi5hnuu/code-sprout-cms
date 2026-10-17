// Package page implements keyset pagination. Every list endpoint returns at most `limit` items
// plus an opaque nextCursor holding the sort key of the last item; the next query resumes with
// `WHERE (sort_key, id) < (cursor)`, an index range scan whose cost does not grow with depth.
// OFFSET is never used: at 1M+ rows, page 10,000 would scan 200,000 rows to throw them away.
package page

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"

	"code-sprout-cms/internal/platform/apperr"
)

const (
	DefaultLimit = 20
	MaxLimit     = 100
)

// Page is the response envelope for every list endpoint.
type Page[T any] struct {
	Items      []T     `json:"items"`
	NextCursor *string `json:"nextCursor"` // nil on the last page
}

// Request is a parsed `?cursor=&limit=`. Key is the decoded cursor, nil on the first page.
type Request[K any] struct {
	Key   *K
	Limit int
}

// Parse reads cursor and limit from the query string. K is the endpoint's cursor key type
// (e.g. struct{ PublishedAt time.Time; ID int64 }).
func Parse[K any](r *http.Request) (Request[K], error) {
	req := Request[K]{Limit: DefaultLimit}
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > MaxLimit {
			return req, apperr.Invalid("page.bad_limit", "limit must be between 1 and 100.")
		}
		req.Limit = n
	}
	if c := r.URL.Query().Get("cursor"); c != "" {
		raw, err := base64.RawURLEncoding.DecodeString(c)
		var key K
		if err != nil || json.Unmarshal(raw, &key) != nil {
			return req, apperr.Invalid("page.bad_cursor", "cursor is invalid; restart from the first page.")
		}
		req.Key = &key
	}
	return req, nil
}

// Build trims a result fetched with Limit+1 rows (the extra row only signals that another
// page exists) and encodes the cursor from the last kept item.
func Build[T, K any](rows []T, limit int, keyOf func(T) K) Page[T] {
	if len(rows) <= limit {
		return Page[T]{Items: rows}
	}
	rows = rows[:limit]
	raw, _ := json.Marshal(keyOf(rows[len(rows)-1]))
	c := base64.RawURLEncoding.EncodeToString(raw)
	return Page[T]{Items: rows, NextCursor: &c}
}
