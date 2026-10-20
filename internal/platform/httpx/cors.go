package httpx

import (
	"net/http"
	"slices"
	"strings"
)

// CORS allows the listed browser origins (exact match) to call the API. Most reads happen
// server-side in Next.js; CORS matters for the client islands (likes, bookmarks, comments,
// code runs) that call the API straight from the browser with a bearer token.
func CORS(allowedOrigins []string) Middleware {
	allowMethods := strings.Join([]string{"GET", "POST", "PUT", "PATCH", "DELETE"}, ", ")
	allowHeaders := strings.Join([]string{"Authorization", "Content-Type", "If-Match", "Idempotency-Key", "X-Request-Id"}, ", ")
	exposeHeaders := strings.Join([]string{"ETag", "Retry-After", "X-Request-Id"}, ", ")

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Responses differ by Origin, so shared caches (CDN) must key on it, also for requests
			// without one: otherwise a cached copy without CORS headers is served to browsers.
			w.Header().Add("Vary", "Origin")
			origin := r.Header.Get("Origin")
			if origin == "" || !slices.Contains(allowedOrigins, origin) {
				next.ServeHTTP(w, r)
				return
			}
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Expose-Headers", exposeHeaders)
			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				h.Set("Access-Control-Allow-Methods", allowMethods)
				h.Set("Access-Control-Allow-Headers", allowHeaders)
				h.Set("Access-Control-Max-Age", "600")
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
