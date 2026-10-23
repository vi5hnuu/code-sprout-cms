// Command api serves the content HTTP API (/v1/public, /v1/me, /v1/studio, /v1/admin).
// It is stateless; run as many replicas as traffic needs behind the load balancer.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"

	"code-sprout-cms/internal/access"
	"code-sprout-cms/internal/account"
	"code-sprout-cms/internal/asset"
	"code-sprout-cms/internal/auth"
	"code-sprout-cms/internal/collection"
	"code-sprout-cms/internal/content"
	"code-sprout-cms/internal/engagement"
	"code-sprout-cms/internal/judge"
	"code-sprout-cms/internal/library"
	"code-sprout-cms/internal/outbox"
	"code-sprout-cms/internal/platform/apperr"
	"code-sprout-cms/internal/platform/config"
	"code-sprout-cms/internal/platform/db"
	"code-sprout-cms/internal/platform/httpx"
	"code-sprout-cms/internal/platform/metrics"
	"code-sprout-cms/internal/platform/ratelimit"
	"code-sprout-cms/internal/platform/storage"
	"code-sprout-cms/internal/problem"
	"code-sprout-cms/internal/roles"
	"code-sprout-cms/internal/search"
	"code-sprout-cms/internal/submission"
	"code-sprout-cms/internal/taxonomy"
	"code-sprout-cms/internal/user"
	"code-sprout-cms/internal/video"
)

func main() {
	if err := run(); err != nil {
		slog.Error("api stopped", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel})))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	database, err := db.Open(ctx, cfg.DatabaseURL, cfg.DatabaseReplicaURL, cfg.DatabaseMaxConns)
	if err != nil {
		return err
	}
	defer database.Close()

	rdb := redis.NewClient(&redis.Options{Addr: cfg.RedisAddr, Password: cfg.RedisPassword})
	defer rdb.Close()
	if err := rdb.Ping(ctx).Err(); err != nil {
		return err
	}

	verifier, err := auth.NewJWKSVerifier(ctx, cfg.Auth.JWKSURL, cfg.Auth.Issuer, cfg.Auth.Audience)
	if err != nil {
		return err
	}

	policy := access.NewPolicy(database.Primary)
	taxonomies := taxonomy.NewService(database, policy)
	assets := asset.NewService(database, storage.FromConfig(cfg.Storage), asset.ConfigFrom(cfg.Storage))
	users := user.NewService(database.Primary, database.Replica, assets)
	contents := content.NewService(database, policy, assets, taxonomies)
	reader := content.NewReader(database, assets, taxonomies, users, engagement.NewStatsReader(database))
	engagements := engagement.NewService(database, rdb, reader, users, policy)
	limiter := ratelimit.New(rdb)
	clientIP := ratelimit.ClientIPFrom(cfg.TrustedClientIPHeader, cfg.InternalProxySecret)

	// Rate-limit bucket names, kept as a typed set so a typo at a call site is a
	// compile-time error (unknown identifier) rather than silently routing traffic
	// into an unlimited bucket. Keep each name short; the limiter prefixes keys
	// with it in Redis.
	type bucket string
	const (
		bucketGlobal      bucket = "global"
		bucketUploads     bucket = "uploads"
		bucketActions     bucket = "actions"
		bucketComments    bucket = "comments"
		bucketEvents      bucket = "events"
		bucketSearch      bucket = "search"
		bucketRuns        bucket = "runs"
		bucketSubmissions bucket = "submissions"
	)
	rl := func(name bucket, limit ratelimit.Limit) httpx.Middleware {
		return limiter.Middleware(string(name), limit, clientIP)
	}

	api := http.NewServeMux()
	user.NewHandler(users).Routes(api, auth.RequireUser)
	account.NewHandler(account.NewService(database, users.Forget,
		engagement.EraseUser, library.EraseUser, submission.EraseUser, collection.EraseUser, access.EraseUser, asset.EraseUser, users.EraseUser,
	)).Routes(api, auth.RequireUser)
	taxonomy.NewHandler(taxonomies).Routes(api, auth.RequireUser)
	asset.NewHandler(assets).Routes(api, auth.RequireUser,
		rl(bucketUploads, ratelimit.PerMinute(30, 10)))
	content.NewHandler(contents).Routes(api, auth.RequireUser)
	content.NewPublicHandler(reader).Routes(api)
	engagement.NewHandler(engagements, clientIP).Routes(api, auth.RequireUser, engagement.Limits{
		Actions:  rl(bucketActions, ratelimit.PerMinute(120, 30)),
		Comments: rl(bucketComments, ratelimit.PerMinute(10, 3)),
		Events:   rl(bucketEvents, ratelimit.PerMinute(120, 30)),
	})
	library.NewHandler(engagements, reader).Routes(api, auth.RequireUser)
	library.NewNotes(database, reader).Routes(api, auth.RequireUser)
	collection.NewHandler(collection.NewService(database, reader, policy)).Routes(api, auth.RequireUser)
	roles.NewHandler(access.NewGrants(database, policy), users, taxonomies).Routes(api, auth.RequireUser)

	content.NewSitemaps(database, assets).Routes(api)

	problems := problem.NewService(database, contents, reader, assets, policy)
	searchIndex := search.NewMeili(cfg.Search.URL, cfg.Search.APIKey, cfg.Search.Index)
	enqueue := func(ctx context.Context, topic string, id int64, payload any) error {
		return outbox.Publish(ctx, database.Primary, topic, id, payload)
	}
	search.NewHandler(searchIndex, search.NewIndexer(searchIndex, reader, problems, enqueue), policy).Routes(api, auth.RequireUser,
		rl(bucketSearch, ratelimit.PerMinute(60, 20)))
	problem.NewHandler(problems).Routes(api, auth.RequireUser)
	video.NewHandler(video.NewService(database, reader)).Routes(api)
	submissions := submission.NewService(database, rdb, judge.New(cfg.Judge.URL, cfg.Judge.APIKey, cfg.Judge.Runtimes), problems, problems, reader)
	submission.NewHandler(submissions, clientIP).Routes(api, auth.RequireUser, submission.Limits{
		Runs:        rl(bucketRuns, ratelimit.PerMinute(20, 5)),
		Submissions: rl(bucketSubmissions, ratelimit.PerMinute(10, 3)),
	})
	// Unmatched paths answer in the same problem+json shape as every other error.
	api.Handle("/", httpx.HandlerFunc(func(http.ResponseWriter, *http.Request) error {
		return apperr.NotFound("route.not_found", "No such endpoint.")
	}))

	// Order matters: the request id exists before anything logs, and AccessLog wraps Recover
	// so a panic is still recorded as a 500 with its request id.
	m := metrics.New()
	m.Pool("primary", database.Primary)
	if database.Replica != database.Primary {
		m.Pool("replica", database.Replica)
	}
	go m.Serve(ctx, cfg.APIMetricsAddr)

	apiHandler := httpx.Chain(api,
		m.HTTP(api),
		httpx.WithRequestID,
		httpx.AccessLog,
		httpx.Recover,
		httpx.CORS(cfg.CORSOrigins),
		auth.Authenticate(verifier, users),
		// Global ceiling per user/IP; individual routes add tighter, purpose-specific limits.
		rl(bucketGlobal, ratelimit.PerMinute(600, 100)),
	)

	// Health probes bypass the API middleware: a load balancer polling every few seconds from
	// one IP must never be rate limited, and probes would only add noise to the access log.
	root := http.NewServeMux()
	registerHealth(root, database, rdb)
	root.Handle("/", apiHandler)

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           root,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}

	errc := make(chan error, 1)
	go func() {
		slog.Info("api listening", "addr", cfg.HTTPAddr)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
	}

	slog.Info("api shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// registerHealth exposes liveness (process up) and readiness (dependencies reachable). The
// load balancer routes traffic only to ready replicas; liveness never checks dependencies,
// so a database blip does not get every replica restarted at once.
func registerHealth(mux *http.ServeMux, database *db.DB, rdb *redis.Client) {
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.Handle("GET /readyz", httpx.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := database.Ping(ctx); err != nil {
			return err
		}
		if err := rdb.Ping(ctx).Err(); err != nil {
			return err
		}
		w.WriteHeader(http.StatusOK)
		return nil
	}))
}
