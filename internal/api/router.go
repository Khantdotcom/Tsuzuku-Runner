// Package api implements the Tsuzuku HTTP API.
package api

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/Khantdotcom/tsuzuku-runner/internal/workerapi"
	"github.com/Khantdotcom/tsuzuku-runner/internal/workload"
)

// Config holds the API's dependencies and settings.
type Config struct {
	Logger   *slog.Logger
	DB       Pinger
	Workers  WorkerStore
	Jobs     JobService
	Claimer  Claimer
	Attempts AttemptReporter
	// Assignments, if set, wakes waiting claims as soon as jobs are scheduled.
	Assignments AssignmentSignal
	// Stopping, if set, is closed when the server starts shutting down, so
	// waiting claims return instead of holding shutdown up.
	Stopping <-chan struct{}
	// WorkerToken is the shared bearer token workers must present.
	WorkerToken string
	// WorkerStaleAfter is how long after its last heartbeat a worker is reported offline.
	WorkerStaleAfter time.Duration
	// WorkloadLimits caps the resources a submitted workload may request.
	WorkloadLimits workload.Limits
}

type server struct {
	logger           *slog.Logger
	db               Pinger
	workers          WorkerStore
	jobs             JobService
	claimer          Claimer
	attempts         AttemptReporter
	assignments      AssignmentSignal
	stopping         <-chan struct{}
	workerStaleAfter time.Duration
	workloadLimits   workload.Limits
}

// NewRouter builds the HTTP handler for the API server.
func NewRouter(cfg Config) http.Handler {
	s := &server{
		logger:           cfg.Logger,
		db:               cfg.DB,
		workers:          cfg.Workers,
		jobs:             cfg.Jobs,
		claimer:          cfg.Claimer,
		attempts:         cfg.Attempts,
		assignments:      cfg.Assignments,
		stopping:         cfg.Stopping,
		workerStaleAfter: cfg.WorkerStaleAfter,
		workloadLimits:   cfg.WorkloadLimits,
	}

	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(requestLogger(cfg.Logger))
	r.Use(middleware.Recoverer)

	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		writeProblem(w, r, http.StatusNotFound, "")
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		writeProblem(w, r, http.StatusMethodNotAllowed, "")
	})

	r.Get("/healthz", handleHealthz)
	r.Get("/readyz", s.handleReadyz)

	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/workers", s.handleListWorkers)

		r.Post("/workloads", s.handleSubmitWorkload)
		r.Get("/jobs", s.handleListJobs)
		r.Get("/jobs/{id}", s.handleGetJob)
		r.Get("/jobs/{id}/attempts", s.handleListAttempts)
		r.Get("/jobs/{id}/events", s.handleListEvents)
		r.Get("/jobs/{id}/logs", s.handleListLogs)
		r.Post("/jobs/{id}/cancel", s.handleCancelJob)
		r.Get("/jobs/{id}/evidence", s.handleGetEvidence)
		r.Get("/jobs/{id}/artifacts", s.handleListArtifacts)
		r.Get("/jobs/{id}/artifacts/{artifactID}", s.handleDownloadArtifact)

		r.Group(func(r chi.Router) {
			r.Use(requireWorkerToken(cfg.WorkerToken))
			r.Post("/workers/register", s.handleRegisterWorker)
			r.Post("/workers/{id}/heartbeat", s.handleHeartbeat)
			r.Post("/workers/{id}/claim", s.handleClaim)
			r.Route("/workers/{id}/attempts/{attemptID}", func(r chi.Router) {
				r.Post("/"+workerapi.ActionExecuting, s.handleAttemptExecuting)
				r.Post("/"+workerapi.ActionVerifying, s.handleAttemptVerifying)
				r.Post("/"+workerapi.ActionFinish, s.handleAttemptFinish)
				r.Post("/"+workerapi.ActionLogs, s.handleAttemptLogs)
			})
		})
	})

	return r
}

func requestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

			next.ServeHTTP(ww, r)

			logger.LogAttrs(r.Context(), slog.LevelDebug, "http request",
				slog.String("request_id", middleware.GetReqID(r.Context())),
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", ww.Status()),
				slog.Int("bytes", ww.BytesWritten()),
				slog.Duration("duration", time.Since(start)),
			)
		})
	}
}
