// Package metrics registers Prometheus collectors for the API server.
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Registry holds process metrics.
type Registry struct {
	HTTPRequests  *prometheus.CounterVec
	HTTPDuration  *prometheus.HistogramVec
	JobsTotal     *prometheus.CounterVec
	QueueDepth    *prometheus.GaugeVec
	SchedulerTick prometheus.Histogram
	Placements    prometheus.Counter
}

// New registers collectors on prometheus.DefaultRegisterer.
func New() *Registry {
	return &Registry{
		HTTPRequests: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "tsuzuku_http_requests_total",
			Help: "HTTP requests handled by the API",
		}, []string{"method", "route", "status"}),
		HTTPDuration: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "tsuzuku_http_request_duration_seconds",
			Help:    "HTTP request latency",
			Buckets: prometheus.DefBuckets,
		}, []string{"method", "route"}),
		JobsTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "tsuzuku_jobs_total",
			Help: "Job lifecycle events",
		}, []string{"event"}),
		QueueDepth: promauto.NewGaugeVec(prometheus.GaugeOpts{
			Name: "tsuzuku_queue_depth",
			Help: "Jobs counted by dashboard bucket",
		}, []string{"bucket"}),
		SchedulerTick: promauto.NewHistogram(prometheus.HistogramOpts{
			Name:    "tsuzuku_scheduler_tick_duration_seconds",
			Help:    "Duration of one scheduling round",
			Buckets: prometheus.DefBuckets,
		}),
		Placements: promauto.NewCounter(prometheus.CounterOpts{
			Name: "tsuzuku_scheduler_placements_total",
			Help: "Jobs placed onto workers",
		}),
	}
}

// Handler serves Prometheus text exposition format.
func Handler() http.Handler {
	return promhttp.Handler()
}

// Middleware records HTTP request metrics.
func (m *Registry) Middleware(next http.Handler) http.Handler {
	if m == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		route := chi.RouteContext(r.Context()).RoutePattern()
		if route == "" {
			route = "unknown"
		}
		status := strconv.Itoa(ww.Status())
		m.HTTPRequests.WithLabelValues(r.Method, route, status).Inc()
		m.HTTPDuration.WithLabelValues(r.Method, route).Observe(time.Since(start).Seconds())
	})
}

// ObserveJob increments a job lifecycle counter (submit, completed, failed, cancelled, blocked).
func (m *Registry) ObserveJob(event string) {
	if m == nil {
		return
	}
	m.JobsTotal.WithLabelValues(event).Inc()
}

// SetQueue sets queue depth gauges from stats buckets.
func (m *Registry) SetQueue(queued, scheduled, running, completed, failed, cancelled float64) {
	if m == nil {
		return
	}
	m.QueueDepth.WithLabelValues("queued").Set(queued)
	m.QueueDepth.WithLabelValues("scheduled").Set(scheduled)
	m.QueueDepth.WithLabelValues("running").Set(running)
	m.QueueDepth.WithLabelValues("completed").Set(completed)
	m.QueueDepth.WithLabelValues("failed").Set(failed)
	m.QueueDepth.WithLabelValues("cancelled").Set(cancelled)
}
