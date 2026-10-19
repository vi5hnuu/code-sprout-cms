// Package config loads process configuration from environment variables once at startup.
// Every binary reads the same variables (see .env.example), and a missing required value
// fails the process immediately rather than surfacing later as a confusing runtime error.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"code-sprout-cms/internal/platform/langs"
)

type Config struct {
	HTTPAddr string
	// Internal addresses for /metrics (Prometheus); empty disables. Never the public port.
	APIMetricsAddr    string
	WorkerMetricsAddr string
	LogLevel          slog.Level
	ShutdownTimeout   time.Duration
	// Header carrying the real client IP when running behind a CDN/proxy (e.g.
	// CF-Connecting-IP). Empty means use the socket peer, which is right with no proxy.
	TrustedClientIPHeader string
	// Shared secret the web server sends (X-Internal-Proxy) to have its relayed client address
	// (X-Client-IP) trusted. Empty disables the mechanism.
	InternalProxySecret string
	// Browser origins allowed to call the API with credentials (the Next.js site).
	CORSOrigins []string

	DatabaseURL        string
	DatabaseReplicaURL string // falls back to DatabaseURL when unset
	DatabaseMaxConns   int32

	RedisAddr     string
	RedisPassword string

	Auth     AuthConfig
	Storage  StorageConfig
	Judge    JudgeConfig
	Search   SearchConfig
	YouTube  YouTubeConfig
	Delivery DeliveryConfig
}

// SearchConfig points at Meilisearch. Optional: without a URL, search answers 503.
// YouTubeConfig mirrors channels' uploads as videos (internal/video). Without an API key the
// sync stays off and everything else works.
type YouTubeConfig struct {
	APIKey            string
	ChannelIDs        []string
	OwnerHandle       string
	ExcludedPlaylists []string
	SyncInterval      time.Duration
	DailyQuota        int
}

type SearchConfig struct {
	URL    string
	APIKey string
	Index  string
}

// DeliveryConfig controls refreshing caches and search engines after content changes. Every
// part is optional; unset parts are skipped.
type DeliveryConfig struct {
	RevalidateURL      string
	RevalidateSecret   string
	CloudflareZoneID   string
	CloudflareAPIToken string
	SiteURL            string
	IndexNowKey        string
}

// JudgeConfig points at ls-judge. Optional: without a URL, running code answers 503 (useful
// for local work on content features without the judge).
type JudgeConfig struct {
	URL    string
	APIKey string
	// Runtimes pins each language to an exact judge runtime id (defaults in langs, overridden
	// by JUDGE_RUNTIMES="cpp=cpp-23,python=python-3.13").
	Runtimes map[string]string
}

type StorageConfig struct {
	Endpoint      string
	Region        string
	AccessKey     string
	SecretKey     string
	PublicBucket  string
	PrivateBucket string
	PublicBaseURL string
}

type AuthConfig struct {
	JWKSURL  string
	Issuer   string
	Audience string
}

func Load() (Config, error) {
	var errs []error
	req := func(key string) string {
		v := strings.TrimSpace(os.Getenv(key))
		if v == "" {
			errs = append(errs, fmt.Errorf("%s is required", key))
		}
		return v
	}

	cfg := Config{
		HTTPAddr:          envOr("HTTP_ADDR", ":9300"),
		APIMetricsAddr:    envOr("API_METRICS_ADDR", "127.0.0.1:9311"),
		WorkerMetricsAddr: envOr("WORKER_METRICS_ADDR", "127.0.0.1:9312"),
		ShutdownTimeout:   durationOr("SHUTDOWN_TIMEOUT", 20*time.Second, &errs),

		TrustedClientIPHeader: os.Getenv("TRUSTED_CLIENT_IP_HEADER"),
		InternalProxySecret:   os.Getenv("INTERNAL_PROXY_SECRET"),
		CORSOrigins:           listOr("CORS_ORIGINS"),

		DatabaseURL:        req("DATABASE_URL"),
		DatabaseReplicaURL: os.Getenv("DATABASE_REPLICA_URL"),
		DatabaseMaxConns:   int32(intOr("DATABASE_MAX_CONNS", 40, &errs)),

		RedisAddr:     req("REDIS_ADDR"),
		RedisPassword: os.Getenv("REDIS_PASSWORD"),

		Auth: AuthConfig{
			JWKSURL:  req("AUTH_JWKS_URL"),
			Issuer:   req("AUTH_ISSUER"),
			Audience: req("AUTH_AUDIENCE"),
		},

		Judge:  JudgeConfig{URL: os.Getenv("JUDGE_URL"), APIKey: os.Getenv("JUDGE_API_KEY"), Runtimes: judgeRuntimes(&errs)},
		Search: SearchConfig{URL: os.Getenv("SEARCH_URL"), APIKey: os.Getenv("SEARCH_API_KEY"), Index: envOr("SEARCH_INDEX", "content")},
		YouTube: YouTubeConfig{
			APIKey: os.Getenv("YOUTUBE_API_KEY"), ChannelIDs: listOr("YOUTUBE_CHANNEL_IDS"), OwnerHandle: os.Getenv("YOUTUBE_OWNER_HANDLE"),
			ExcludedPlaylists: listOr("YOUTUBE_EXCLUDED_PLAYLISTS"),
			SyncInterval:      durationOr("YOUTUBE_SYNC_INTERVAL", 15*time.Minute, &errs),
			DailyQuota:        intOr("YOUTUBE_DAILY_QUOTA", 5000, &errs),
		},
		Delivery: DeliveryConfig{
			RevalidateURL: os.Getenv("WEB_REVALIDATE_URL"), RevalidateSecret: os.Getenv("WEB_REVALIDATE_SECRET"),
			CloudflareZoneID: os.Getenv("CLOUDFLARE_ZONE_ID"), CloudflareAPIToken: os.Getenv("CLOUDFLARE_API_TOKEN"),
			SiteURL: os.Getenv("SITE_URL"), IndexNowKey: os.Getenv("INDEXNOW_KEY"),
		},

		Storage: StorageConfig{
			Endpoint:      req("STORAGE_ENDPOINT"),
			Region:        envOr("STORAGE_REGION", "auto"),
			AccessKey:     req("STORAGE_ACCESS_KEY"),
			SecretKey:     req("STORAGE_SECRET_KEY"),
			PublicBucket:  req("STORAGE_PUBLIC_BUCKET"),
			PrivateBucket: req("STORAGE_PRIVATE_BUCKET"),
			PublicBaseURL: strings.TrimRight(req("STORAGE_PUBLIC_BASE_URL"), "/"),
		},
	}
	if cfg.DatabaseReplicaURL == "" {
		cfg.DatabaseReplicaURL = cfg.DatabaseURL
	}
	if err := cfg.LogLevel.UnmarshalText([]byte(envOr("LOG_LEVEL", "info"))); err != nil {
		errs = append(errs, fmt.Errorf("LOG_LEVEL: %w", err))
	}
	return cfg, errors.Join(errs...)
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func listOr(key string) []string {
	var out []string
	for _, v := range strings.Split(os.Getenv(key), ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func intOr(key string, def int, errs *[]error) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		*errs = append(*errs, fmt.Errorf("%s must be a positive integer", key))
		return def
	}
	return n
}

func durationOr(key string, def time.Duration, errs *[]error) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		*errs = append(*errs, fmt.Errorf("%s must be a positive duration like 20s", key))
		return def
	}
	return d
}

// judgeRuntimes applies JUDGE_RUNTIMES overrides ("lang=runtime-id,…") to the default pins.
func judgeRuntimes(errs *[]error) map[string]string {
	out := langs.DefaultRuntimes()
	for _, pair := range strings.Split(os.Getenv("JUDGE_RUNTIMES"), ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		lang, id, ok := strings.Cut(pair, "=")
		lang, id = strings.TrimSpace(lang), strings.TrimSpace(id)
		if !ok || id == "" || !langs.Runnable(lang) {
			*errs = append(*errs, fmt.Errorf("JUDGE_RUNTIMES: %q is not lang=runtime-id for a runnable language", pair))
			continue
		}
		out[lang] = id
	}
	return out
}
