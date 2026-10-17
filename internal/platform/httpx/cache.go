package httpx

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Public marks a response as identical for every caller so the CDN (and the Next.js data
// cache) may store it. sMaxAge is how long the CDN serves it without asking; afterwards it
// serves the stale copy for up to `stale` while refetching in the background, so readers
// never wait on the API. Browsers get a short max-age so a publish shows up quickly.
//
// surrogateKeys tag the response (e.g. "content:42", "category:7") so a change can purge
// exactly the cached responses that include it, instead of waiting for expiry.
func Public(w http.ResponseWriter, sMaxAge, stale time.Duration, surrogateKeys ...string) {
	w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=60, s-maxage=%d, stale-while-revalidate=%d",
		int(sMaxAge.Seconds()), int(stale.Seconds())))
	if len(surrogateKeys) > 0 {
		// Cloudflare reads Cache-Tag; Fastly and others read Surrogate-Key.
		w.Header().Set("Cache-Tag", strings.Join(surrogateKeys, ","))
		w.Header().Set("Surrogate-Key", strings.Join(surrogateKeys, " "))
	}
}
