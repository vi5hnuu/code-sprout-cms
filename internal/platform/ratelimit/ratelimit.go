// Package ratelimit implements a distributed rate limiter on Redis using GCRA (the generic cell
// rate algorithm). GCRA stores one timestamp per key, so the cost per limited identity is a
// few dozen bytes regardless of the limit, and the check is a single atomic script call,
// correct across any number of API replicas.
package ratelimit

import (
	"context"
	"crypto/subtle"
	"fmt"
	"math"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"code-sprout-cms/internal/auth"
	"code-sprout-cms/internal/platform/apperr"
	"code-sprout-cms/internal/platform/httpx"
)

// Limit allows Burst requests at once, refilling one every Period/Rate.
type Limit struct {
	Rate   int
	Period time.Duration
	Burst  int
}

func PerMinute(n, burst int) Limit { return Limit{Rate: n, Period: time.Minute, Burst: burst} }

// KEYS[1] = key; ARGV = emission interval (µs), burst tolerance (µs).
// The key's value is the theoretical arrival time (TAT). Time comes from Redis so app
// servers with skewed clocks still agree.
var gcra = redis.NewScript(`
local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000000 + tonumber(t[2])
local interval = tonumber(ARGV[1])
local tolerance = tonumber(ARGV[2])
local tat = tonumber(redis.call('GET', KEYS[1]) or now)
if tat < now then tat = now end
local allow_at = tat - tolerance
if now < allow_at then
  return {0, allow_at - now}
end
local new_tat = tat + interval
redis.call('SET', KEYS[1], new_tat, 'PX', math.ceil((new_tat - now) / 1000) + 1)
return {1, 0}
`)

type Limiter struct{ rdb redis.Scripter }

func New(rdb redis.Scripter) *Limiter { return &Limiter{rdb: rdb} }

// Allow reports whether one more request for key fits the limit, and if not, how long to wait.
func (l *Limiter) Allow(ctx context.Context, key string, lim Limit) (bool, time.Duration, error) {
	interval := lim.Period.Microseconds() / int64(lim.Rate)
	tolerance := interval * int64(lim.Burst-1)
	res, err := gcra.Run(ctx, l.rdb, []string{"rl:" + key}, interval, tolerance).Int64Slice()
	if err != nil {
		return false, 0, fmt.Errorf("ratelimit %s: %w", key, err)
	}
	return res[0] == 1, time.Duration(res[1]) * time.Microsecond, nil
}

const (
	// InternalProxyHeader carries the shared secret by which the web server proves a request is
	// its own (server-side rendering, or a reader's call relayed through its /api/cms proxy).
	InternalProxyHeader = "X-Internal-Proxy"
	// ClientIPHeader is the reader's address, as the web server saw it. Trusted only on proven
	// internal requests; on any other request it is ignored, so it cannot be forged.
	ClientIPHeader = "X-Client-IP"
	// ClientIDHeader is the reader's first-party anonymous id: a random value the web server
	// sets in a cookie on the first visit and relays here. Trusted only on proven internal
	// requests, like the address.
	ClientIDHeader = "X-Client-Id"
)

// Client is who sent a request, as far as the API can tell without a session.
//
// The address alone is a poor identity for readers: carrier-grade NAT (most Indian mobile
// traffic) puts thousands of people behind one IPv4, so per-address limits throttle a crowd
// for one person's behaviour and per-address view de-duplication counts a crowd as one. The
// anonymous id tells readers behind one address apart. It is a cookie the reader can clear,
// so it is never trusted *more* than the address: limits apply to both (see Middleware).
type Client struct {
	// IP is the reader's address, or "" for unattributed server traffic (a page render that
	// serves many readers at once).
	IP string
	// ID is the reader's anonymous id, or "" when there is none: a first visit (the cookie is
	// being set by this very response), cookies blocked, crawlers and scripts.
	ID string
}

// Key is the finest identity available, for de-duplicating what a reader did (views, shares,
// anonymous runs): the id when present, else the address bucket. "" means unattributed.
func (c Client) Key() string {
	switch {
	case c.ID != "":
		return "c:" + c.ID
	case c.IP != "":
		return "ip:" + addressKey(c.IP)
	}
	return ""
}

// Identify resolves the caller of a request.
type Identify func(r *http.Request) Client

// IdentifyFrom builds the caller resolver.
//
// Behind a CDN the socket peer is the CDN, so the real client comes from the header the CDN
// sets (e.g. CF-Connecting-IP). That header is only trusted when configured, because without
// a proxy in front any client could forge it.
//
// The web server calls the API for every reader: rendering pages and relaying browser calls.
// Without proof those calls would all share the web server's address, so one busy site would
// exhaust per-address limits (and anonymous readers would share one identity). With the shared
// proxySecret, a proven request uses the reader's address and id from ClientIPHeader and
// ClientIDHeader; a proven request without them (a page render, not tied to any reader) is
// unattributed. The id is accepted only from proven requests and only in its generated shape,
// so a direct caller cannot pick an identity.
func IdentifyFrom(trustedHeader, proxySecret string) Identify {
	secret := []byte(proxySecret)
	return func(r *http.Request) Client {
		if len(secret) > 0 {
			if proof := r.Header.Get(InternalProxyHeader); proof != "" && subtle.ConstantTimeCompare([]byte(proof), secret) == 1 {
				var c Client
				if ip := r.Header.Get(ClientIPHeader); net.ParseIP(ip) != nil {
					c.IP = ip
				}
				if id := r.Header.Get(ClientIDHeader); c.IP != "" && clientIDShape.MatchString(id) {
					c.ID = id
				}
				return c
			}
		}
		if trustedHeader != "" {
			if ip := r.Header.Get(trustedHeader); net.ParseIP(ip) != nil {
				return Client{IP: ip}
			}
		}
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			return Client{IP: r.RemoteAddr}
		}
		return Client{IP: host}
	}
}

// clientIDShape is what the web server generates (URL-safe base64 of random bytes); anything
// else is ignored rather than becoming a Redis key.
var clientIDShape = regexp.MustCompile(`^[A-Za-z0-9_-]{16,64}$`)

// sharedAddressFactor is how many readers' worth of traffic one address may carry when its
// readers identify themselves individually. Carrier-grade NAT pools are large, so this is
// generous; it exists only so clearing the cookie in a loop cannot escape limits altogether.
const sharedAddressFactor = 25

// addressKey is the bucket for a client address.
//
// IPv4 addresses are keyed as-is: one bucket per /32.
//
// IPv6 addresses are keyed by their /56 prefix. ISPs commonly allocate a /56 (sometimes a
// /48) to each residential or SMB customer, so one /56 is a single consumer-of-the-service
// and belongs in one bucket. Masking narrower (/64, the single LAN subnet) let a determined
// caller rotate across their /64s to bypass per-bucket limits — /56 closes that gap. The
// cost is that a shared /56 (hostels, campuses) shares one bucket; raise `MaskV6` here to
// loosen if that proves a problem in prod.
func addressKey(ip string) string {
	parsed := net.ParseIP(ip)
	if parsed == nil || parsed.To4() != nil {
		return ip
	}
	return parsed.Mask(net.CIDRMask(56, 128)).String() + "/56"
}

// Middleware limits by user id when signed in, else by the reader's anonymous id with a looser
// cap per address, else by address alone. `name` namespaces the bucket so different endpoints
// (e.g. comments vs code runs) have independent limits. Unattributed server traffic (page
// renders by the web server) is not limited per address: it stands for many readers at once
// and is bounded by the web tier's own caching.
//
// If Redis is unavailable requests are allowed through: a limiter outage should degrade
// protection, not take the whole API down with it.
func (l *Limiter) Middleware(name string, lim Limit, identify Identify) httpx.Middleware {
	shared := Limit{Rate: lim.Rate * sharedAddressFactor, Period: lim.Period, Burst: lim.Burst * sharedAddressFactor}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var checks []check
			if p, ok := auth.FromContext(r.Context()); ok {
				checks = []check{{name + ":u:" + strconv.FormatInt(p.UserID, 10), lim}}
			} else {
				c := identify(r)
				switch {
				case c.ID != "":
					checks = []check{{name + ":c:" + c.ID, lim}, {name + ":ip:" + addressKey(c.IP), shared}}
				case c.IP != "":
					checks = []check{{name + ":ip:" + addressKey(c.IP), lim}}
				default:
					next.ServeHTTP(w, r)
					return
				}
			}
			for _, ch := range checks {
				ok, retry, err := l.Allow(r.Context(), ch.key, ch.lim)
				if err != nil {
					break
				}
				if !ok {
					w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(retry.Seconds()))))
					httpx.Error(w, r, apperr.RateLimited("Too many requests. Please slow down."))
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// check is one bucket a request must fit.
type check struct {
	key string
	lim Limit
}
