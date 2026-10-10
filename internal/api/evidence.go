package api

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/Khantdotcom/tsuzuku-runner/internal/job"
	"github.com/Khantdotcom/tsuzuku-runner/internal/store/db"
)

type artifactView struct {
	ID          uuid.UUID  `json:"id"`
	AttemptID   *uuid.UUID `json:"attempt_id"`
	Name        string     `json:"name"`
	ContentType string     `json:"content_type"`
	SizeBytes   int64      `json:"size_bytes"`
	SHA256      string     `json:"sha256"`
	CreatedAt   time.Time  `json:"created_at"`
	// URL downloads the content.
	URL string `json:"url"`
}

type checkView struct {
	Name       string     `json:"name"`
	Kind       string     `json:"kind"`
	Command    *string    `json:"command"`
	Status     string     `json:"status"`
	ExitCode   *int32     `json:"exit_code"`
	Output     string     `json:"output"`
	StartedAt  *time.Time `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
}

type verificationView struct {
	ID         uuid.UUID   `json:"id"`
	AttemptID  uuid.UUID   `json:"attempt_id"`
	Status     string      `json:"status"`
	StartedAt  time.Time   `json:"started_at"`
	FinishedAt *time.Time  `json:"finished_at"`
	Checks     []checkView `json:"checks"`
}

type failureView struct {
	ID        uuid.UUID       `json:"id"`
	AttemptID *uuid.UUID      `json:"attempt_id"`
	Category  string          `json:"category"`
	Message   string          `json:"message"`
	Details   json.RawMessage `json:"details"`
	CreatedAt time.Time       `json:"created_at"`
}

func (s *server) handleCancelJob(w http.ResponseWriter, r *http.Request) {
	j, ok := s.findJob(w, r)
	if !ok {
		return
	}
	cancelled, immediate, err := s.jobs.Cancel(r.Context(), j.ID)
	switch {
	case errors.Is(err, job.ErrNotFound):
		writeProblem(w, r, http.StatusNotFound, "job not found")
		return
	case errors.Is(err, job.ErrJobFinished), errors.Is(err, job.ErrConflict):
		writeProblem(w, r, http.StatusConflict, err.Error())
		return
	case err != nil:
		s.internalError(w, r, "cancel job", err)
		return
	}
	status := http.StatusAccepted
	if immediate {
		status = http.StatusOK
	}
	s.logger.InfoContext(r.Context(), "job cancellation requested",
		"job_id", j.ID, "job_number", j.Number, "immediate", immediate)
	writeJSON(w, status, newJobView(cancelled))
}

func (s *server) handleGetEvidence(w http.ResponseWriter, r *http.Request) {
	j, ok := s.findJob(w, r)
	if !ok {
		return
	}
	e, err := s.jobs.Evidence(r.Context(), j.ID)
	if err != nil {
		s.internalError(w, r, "load evidence", err)
		return
	}
	verifications := make([]verificationView, 0, len(e.Verifications))
	for _, v := range e.Verifications {
		checks := make([]checkView, 0, len(v.Checks))
		for _, c := range v.Checks {
			checks = append(checks, checkView{
				Name: c.Name, Kind: c.Kind, Command: c.Command, Status: c.Status, ExitCode: c.ExitCode,
				Output: c.Output, StartedAt: c.StartedAt, FinishedAt: c.FinishedAt,
			})
		}
		verifications = append(verifications, verificationView{
			ID: v.Run.ID, AttemptID: v.Run.AttemptID, Status: v.Run.Status,
			StartedAt: v.Run.StartedAt, FinishedAt: v.Run.FinishedAt, Checks: checks,
		})
	}
	failures := make([]failureView, 0, len(e.Failures))
	for _, f := range e.Failures {
		failures = append(failures, failureView{
			ID: f.ID, AttemptID: f.AttemptID, Category: f.Category, Message: f.Message,
			Details: f.Details, CreatedAt: f.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"artifacts":     artifactViews(j, e.Artifacts),
		"verifications": verifications,
		"failures":      failures,
	})
}

func (s *server) handleListArtifacts(w http.ResponseWriter, r *http.Request) {
	j, ok := s.findJob(w, r)
	if !ok {
		return
	}
	artifacts, err := s.jobs.Artifacts(r.Context(), j.ID)
	if err != nil {
		s.internalError(w, r, "list artifacts", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"artifacts": artifactViews(j, artifacts)})
}

func (s *server) handleDownloadArtifact(w http.ResponseWriter, r *http.Request) {
	j, ok := s.findJob(w, r)
	if !ok {
		return
	}
	artifactID, err := uuid.Parse(chi.URLParam(r, "artifactID"))
	if err != nil {
		writeProblem(w, r, http.StatusBadRequest, "artifact id must be a UUID")
		return
	}
	a, content, err := s.jobs.OpenArtifact(r.Context(), j.ID, artifactID)
	if errors.Is(err, job.ErrArtifactNotFound) {
		writeProblem(w, r, http.StatusNotFound, "artifact not found")
		return
	}
	if err != nil {
		s.internalError(w, r, "open artifact", err)
		return
	}
	defer func() { _ = content.Close() }()

	h := w.Header()
	h.Set("Content-Type", a.ContentType)
	h.Set("Content-Length", strconv.FormatInt(a.SizeBytes, 10))
	h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": a.Name}))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("ETag", `"`+a.Sha256+`"`)
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, content); err != nil {
		s.logger.WarnContext(r.Context(), "send artifact", "artifact_id", a.ID, "err", err)
	}
}

func artifactViews(j db.Job, artifacts []db.Artifact) []artifactView {
	views := make([]artifactView, 0, len(artifacts))
	for _, a := range artifacts {
		views = append(views, artifactView{
			ID: a.ID, AttemptID: a.AttemptID, Name: a.Name, ContentType: a.ContentType,
			SizeBytes: a.SizeBytes, SHA256: a.Sha256, CreatedAt: a.CreatedAt,
			URL: "/api/v1/jobs/" + j.ID.String() + "/artifacts/" + a.ID.String(),
		})
	}
	return views
}
