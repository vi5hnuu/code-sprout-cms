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

	"code-sprout-cms/internal/auth"
	"code-sprout-cms/internal/platform/apperr"
	"code-sprout-cms/internal/platform/config"
	"code-sprout-cms/internal/platform/db"
	"code-sprout-cms/internal/platform/httpx"
	"code-sprout-cms/internal/platform/ratelimit"
	"code-sprout-cms/internal/user"
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

	users := user.NewService(database.Primary)
	limiter := ratelimit.New(rdb)
	clientIP := ratelimit.ClientIPFrom(cfg.TrustedClientIPHeader)

	api := http.NewServeMux()
	user.NewHandler(users).Routes(api, auth.RequireUser)
	// Unmatched paths answer in the same problem+json shape as every other error.
	api.Handle("/", httpx.HandlerFunc(func(http.ResponseWriter, *http.Request) error {
		return apperr.NotFound("route.not_found", "No such endpoint.")
	}))

	// Order matters: the request id exists before anything logs, and AccessLog wraps Recover
	// so a panic is still recorded as a 500 with its request id.
	apiHandler := httpx.Chain(api,
		httpx.WithRequestID,
		httpx.AccessLog,
		httpx.Recover,
		httpx.CORS(cfg.CORSOrigins),
		auth.Authenticate(verifier, users),
		// Global ceiling per user/IP; individual routes add tighter, purpose-specific limits.
		limiter.Middleware("global", ratelimit.PerMinute(600, 100), clientIP),
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
