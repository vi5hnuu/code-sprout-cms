// Package metrics exposes Prometheus metrics for the API and the worker: request rate, errors
// and latency per route, database pool use, and the health of the background queues (outbox
// backlog, judge queue). It is served on its own internal address, never on the public port.
package metrics

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics owns a registry (not the global one), so tests and several components never collide.
type Metrics struct {
	reg *prometheus.Registry
}

func New() *Metrics {
	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return &Metrics{reg: reg}
}

// Registerer lets other packages add their own collectors.
func (m *Metrics) Registerer() prometheus.Registerer { return m.reg }

// Handler serves the metrics page.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{Registry: m.reg, Timeout: 10 * time.Second})
}

// Serve exposes /metrics on addr until ctx ends; an empty addr disables it.
func (m *Metrics) Serve(ctx context.Context, addr string) {
	if addr == "" {
		return
	}
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", m.Handler())
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	slog.Info("metrics listening", "addr", addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("metrics server", "err", err)
	}
}

// Router reports which registered pattern a request matches (http.ServeMux does).
type Router interface {
	Handler(r *http.Request) (h http.Handler, pattern string)
}

// HTTP records requests by route pattern (e.g. "GET /v1/public/problems/{slug}"), never by raw
// path: ids in paths would create a new time series per item.
func (m *Metrics) HTTP(router Router) func(http.Handler) http.Handler {
	requests := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total", Help: "HTTP requests by route, method and status code.",
	}, []string{"route", "method", "code"})
	duration := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "http_request_duration_seconds", Help: "HTTP request latency by route and method.",
		Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
	}, []string{"route", "method"})
	m.reg.MustRegister(requests, duration)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, route := router.Handler(r)
			if route == "" {
				route = "unmatched"
			}
			start := time.Now()
			rec := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)
			requests.WithLabelValues(route, r.Method, strconv.Itoa(rec.status)).Inc()
			duration.WithLabelValues(route, r.Method).Observe(time.Since(start).Seconds())
		})
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// Pool reports a pgx pool's connections, so saturation (requests waiting for a connection)
// shows up before latency does.
func (m *Metrics) Pool(name string, pool *pgxpool.Pool) {
	labels := prometheus.Labels{"pool": name}
	gauge := func(metric, help string, read func(*pgxpool.Stat) float64) {
		m.reg.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: metric, Help: help, ConstLabels: labels},
			func() float64 { return read(pool.Stat()) }))
	}
	counter := func(metric, help string, read func(*pgxpool.Stat) float64) {
		m.reg.MustRegister(prometheus.NewCounterFunc(prometheus.CounterOpts{Name: metric, Help: help, ConstLabels: labels},
			func() float64 { return read(pool.Stat()) }))
	}
	gauge("db_pool_connections_in_use", "Connections currently checked out.", func(s *pgxpool.Stat) float64 { return float64(s.AcquiredConns()) })
	gauge("db_pool_connections_idle", "Idle connections.", func(s *pgxpool.Stat) float64 { return float64(s.IdleConns()) })
	gauge("db_pool_connections_max", "Pool size limit.", func(s *pgxpool.Stat) float64 { return float64(s.MaxConns()) })
	counter("db_pool_waits_total", "Acquires that had to wait for a connection.", func(s *pgxpool.Stat) float64 { return float64(s.EmptyAcquireCount()) })
	counter("db_pool_wait_seconds_total", "Time spent waiting for a connection.", func(s *pgxpool.Stat) float64 { return s.AcquireDuration().Seconds() })
}

// Sampled registers gauges read from the database on each scrape, through read with a short
// timeout. A failed read reports the gauge as missing for that scrape, never as zero.
func (m *Metrics) Sampled(read func(ctx context.Context) (map[string]float64, error), help map[string]string) {
	m.reg.MustRegister(&sampled{read: read, descs: descsOf(help)})
}

func descsOf(help map[string]string) map[string]*prometheus.Desc {
	out := make(map[string]*prometheus.Desc, len(help))
	for name, text := range help {
		out[name] = prometheus.NewDesc(name, text, nil, nil)
	}
	return out
}

type sampled struct {
	read  func(ctx context.Context) (map[string]float64, error)
	descs map[string]*prometheus.Desc
}

func (s *sampled) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range s.descs {
		ch <- d
	}
}

func (s *sampled) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	values, err := s.read(ctx)
	if err != nil {
		slog.Warn("metrics sample failed", "err", err)
		return
	}
	for name, v := range values {
		if d, ok := s.descs[name]; ok {
			ch <- prometheus.MustNewConstMetric(d, prometheus.GaugeValue, v)
		}
	}
}

// Events counts handled outbox events by topic and outcome, with handling time.
type Events struct {
	handled  *prometheus.CounterVec
	duration *prometheus.HistogramVec
}

func (m *Metrics) Events() *Events {
	e := &Events{
		handled: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "outbox_events_handled_total", Help: "Outbox events handled by topic and result (ok, error).",
		}, []string{"topic", "result"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "outbox_event_duration_seconds", Help: "Time to handle one outbox event, by topic.",
			Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30},
		}, []string{"topic"}),
	}
	m.reg.MustRegister(e.handled, e.duration)
	return e
}

// Observe records one event's outcome (the outbox dispatcher's OnHandled hook).
func (e *Events) Observe(topic string, err error, took time.Duration) {
	result := "ok"
	if err != nil {
		result = "error"
	}
	e.handled.WithLabelValues(topic, result).Inc()
	e.duration.WithLabelValues(topic).Observe(took.Seconds())
}

// VideoSync counts the video sync's runs, quota and per-video outcomes (video.Observer).
type VideoSync struct {
	runs   *prometheus.CounterVec
	quota  prometheus.Counter
	videos *prometheus.CounterVec
}

func (m *Metrics) VideoSync() *VideoSync {
	v := &VideoSync{
		runs: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "video_sync_runs_total", Help: "Video sync runs per channel by result (ok, quota, error).",
		}, []string{"result"}),
		quota: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "video_sync_quota_units_total", Help: "YouTube API quota units spent.",
		}),
		videos: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "videos_synced_total", Help: "Videos handled by the sync, by action (created, updated, unchanged, removed, error).",
		}, []string{"action"}),
	}
	m.reg.MustRegister(v.runs, v.quota, v.videos)
	return v
}

func (v *VideoSync) SyncRun(result string)     { v.runs.WithLabelValues(result).Inc() }
func (v *VideoSync) QuotaSpent(units int)      { v.quota.Add(float64(units)) }
func (v *VideoSync) VideoSynced(action string) { v.videos.WithLabelValues(action).Inc() }
