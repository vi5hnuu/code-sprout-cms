// Package ratelimit implements a distributed rate limiter on Redis using GCRA (the generic cell
// rate algorithm). GCRA stores one timestamp per key, so the cost per limited identity is a
// few dozen bytes regardless of the limit, and the check is a single atomic script call,
// correct across any number of API replicas.
package ratelimit

import (
	"context"
	"fmt"
	"math"
	"net"
	"net/http"
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

// ClientIP returns the caller address. Behind a CDN the socket peer is the CDN, so the real
// client comes from the header the CDN sets (e.g. CF-Connecting-IP). That header is only
// trusted when configured, because without a proxy in front any client could forge it.
type ClientIP func(r *http.Request) string

func ClientIPFrom(trustedHeader string) ClientIP {
	return func(r *http.Request) string {
		if trustedHeader != "" {
			if ip := r.Header.Get(trustedHeader); net.ParseIP(ip) != nil {
				return ip
			}
		}
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			return r.RemoteAddr
		}
		return host
	}
}

// Middleware limits by user id when signed in, else by IP. `name` namespaces the bucket so
// different endpoints (e.g. comments vs code runs) have independent limits.
//
// If Redis is unavailable requests are allowed through: a limiter outage should degrade
// protection, not take the whole API down with it.
func (l *Limiter) Middleware(name string, lim Limit, clientIP ClientIP) httpx.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := name + ":ip:" + clientIP(r)
			if p, ok := auth.FromContext(r.Context()); ok {
				key = name + ":u:" + strconv.FormatInt(p.UserID, 10)
			}
			ok, retry, err := l.Allow(r.Context(), key, lim)
			if err != nil {
				next.ServeHTTP(w, r)
				return
			}
			if !ok {
				w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(retry.Seconds()))))
				httpx.Error(w, r, apperr.RateLimited("Too many requests. Please slow down."))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
