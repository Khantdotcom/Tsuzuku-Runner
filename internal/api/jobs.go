package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/Khantdotcom/tsuzuku-runner/internal/job"
	"github.com/Khantdotcom/tsuzuku-runner/internal/store/db"
	"github.com/Khantdotcom/tsuzuku-runner/internal/workload"
)

const (
	defaultPageSize = 50
	maxPageSize     = 200
	maxLogPageSize  = 1000
)

// JobService is what the job endpoints need. *job.Service implements it.
type JobService interface {
	Submit(ctx context.Context, spec workload.Spec, idempotencyKey string) (job.Submission, error)
	Find(ctx context.Context, ref job.Ref) (db.Job, error)
	Detail(ctx context.Context, j db.Job) (job.Detail, error)
	List(ctx context.Context, p job.ListParams) ([]db.ListJobsRow, error)
	Attempts(ctx context.Context, jobID uuid.UUID) ([]db.JobAttempt, error)
	Events(ctx context.Context, jobID uuid.UUID, after int64, limit int32) ([]db.JobEvent, error)
	Logs(ctx context.Context, jobID uuid.UUID, after int64, limit int32) ([]db.LogChunk, error)
}

type jobView struct {
	ID                uuid.UUID  `json:"id"`
	Number            int64      `json:"number"`
	State             string     `json:"state"`
	AssignedWorkerID  *uuid.UUID `json:"assigned_worker_id"`
	ScheduledAt       *time.Time `json:"scheduled_at"`
	CancelRequestedAt *time.Time `json:"cancel_requested_at"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
	StartedAt         *time.Time `json:"started_at"`
	FinishedAt        *time.Time `json:"finished_at"`
}

type workloadSummaryView struct {
	Repository string `json:"repository"`
	Revision   string `json:"revision"`
	Command    string `json:"command"`
	Image      string `json:"image"`
}

type jobListItemView struct {
	jobView
	Workload workloadSummaryView `json:"workload"`
}

type workloadView struct {
	ID        uuid.UUID       `json:"id"`
	Spec      json.RawMessage `json:"spec"`
	CreatedAt time.Time       `json:"created_at"`
}

type transitionView struct {
	From   *string   `json:"from"`
	To     string    `json:"to"`
	Actor  string    `json:"actor"`
	Reason string    `json:"reason"`
	At     time.Time `json:"at"`
}

type jobDetailView struct {
	jobView
	Workload    workloadView     `json:"workload"`
	Transitions []transitionView `json:"transitions"`
}

type attemptView struct {
	ID            uuid.UUID  `json:"id"`
	AttemptNumber int32      `json:"attempt_number"`
	WorkerID      uuid.UUID  `json:"worker_id"`
	Status        string     `json:"status"`
	ExitCode      *int32     `json:"exit_code"`
	Error         *string    `json:"error"`
	StartedAt     time.Time  `json:"started_at"`
	FinishedAt    *time.Time `json:"finished_at"`
}

type eventView struct {
	ID        int64           `json:"id"`
	Type      string          `json:"type"`
	AttemptID *uuid.UUID      `json:"attempt_id"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"created_at"`
}

// logChunkView carries raw output bytes, which encoding/json writes as base64.
type logChunkView struct {
	ID        int64     `json:"id"`
	AttemptID uuid.UUID `json:"attempt_id"`
	Seq       int32     `json:"seq"`
	Stream    string    `json:"stream"`
	Data      []byte    `json:"data"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *server) handleSubmitWorkload(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("Idempotency-Key")
	if _, sent := r.Header["Idempotency-Key"]; sent {
		if err := workload.ValidateIdempotencyKey(key); err != nil {
			writeProblem(w, r, http.StatusBadRequest, err.Error())
			return
		}
	}

	var req workload.Request
	if !decodeJSON(w, r, &req, true) {
		return
	}
	spec, err := req.Normalize(s.workloadLimits)
	if err != nil {
		writeProblem(w, r, http.StatusUnprocessableEntity, err.Error())
		return
	}

	sub, err := s.jobs.Submit(r.Context(), spec, key)
	if errors.Is(err, job.ErrIdempotencyMismatch) {
		writeProblem(w, r, http.StatusConflict, "this Idempotency-Key was already used for a different workload")
		return
	}
	if err != nil {
		s.internalError(w, r, "submit workload", err)
		return
	}

	w.Header().Set("Location", "/api/v1/jobs/"+sub.Job.ID.String())
	status := http.StatusCreated
	if sub.Replayed {
		w.Header().Set("Idempotent-Replayed", "true")
		status = http.StatusOK
	} else {
		s.logger.InfoContext(r.Context(), "workload submitted",
			"job_id", sub.Job.ID, "job_number", sub.Job.Number, "repository", spec.Repository)
	}
	writeJSON(w, status, newJobDetailView(sub.Detail))
}

func (s *server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	var p job.ListParams

	if v := query.Get("state"); v != "" {
		p.State = job.State(v)
		if !p.State.Valid() {
			writeProblem(w, r, http.StatusBadRequest, "state must be one of the job states, for example QUEUED")
			return
		}
	}
	if v := query.Get("before"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 1 {
			writeProblem(w, r, http.StatusBadRequest, "before must be a positive job number")
			return
		}
		p.Before = n
	}
	limit, ok := parseLimit(w, r, maxPageSize)
	if !ok {
		return
	}
	p.Limit = limit

	rows, err := s.jobs.List(r.Context(), p)
	if err != nil {
		s.internalError(w, r, "list jobs", err)
		return
	}
	items := make([]jobListItemView, 0, len(rows))
	for _, row := range rows {
		items = append(items, jobListItemView{
			jobView: newJobView(row.Job),
			Workload: workloadSummaryView{
				Repository: row.RepositoryURL, Revision: row.Revision, Command: row.Command, Image: row.Image,
			},
		})
	}
	var nextBefore *int64
	if len(rows) == int(limit) {
		nextBefore = &rows[len(rows)-1].Job.Number
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": items, "next_before": nextBefore})
}

func (s *server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	j, ok := s.findJob(w, r)
	if !ok {
		return
	}
	detail, err := s.jobs.Detail(r.Context(), j)
	if err != nil {
		s.internalError(w, r, "load job", err)
		return
	}
	writeJSON(w, http.StatusOK, newJobDetailView(detail))
}

func (s *server) handleListAttempts(w http.ResponseWriter, r *http.Request) {
	j, ok := s.findJob(w, r)
	if !ok {
		return
	}
	attempts, err := s.jobs.Attempts(r.Context(), j.ID)
	if err != nil {
		s.internalError(w, r, "list attempts", err)
		return
	}
	views := make([]attemptView, 0, len(attempts))
	for _, a := range attempts {
		views = append(views, attemptView{
			ID: a.ID, AttemptNumber: a.AttemptNumber, WorkerID: a.WorkerID, Status: a.Status,
			ExitCode: a.ExitCode, Error: a.Error, StartedAt: a.StartedAt, FinishedAt: a.FinishedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"attempts": views})
}

func (s *server) handleListEvents(w http.ResponseWriter, r *http.Request) {
	j, ok := s.findJob(w, r)
	if !ok {
		return
	}
	after, ok := parseAfter(w, r)
	if !ok {
		return
	}
	limit, ok := parseLimit(w, r, maxPageSize)
	if !ok {
		return
	}
	events, err := s.jobs.Events(r.Context(), j.ID, after, limit)
	if err != nil {
		s.internalError(w, r, "list events", err)
		return
	}
	views := make([]eventView, 0, len(events))
	for _, e := range events {
		views = append(views, eventView{ID: e.ID, Type: e.Type, AttemptID: e.AttemptID, Payload: e.Payload, CreatedAt: e.CreatedAt})
		after = e.ID
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": views, "next_after": after})
}

func (s *server) handleListLogs(w http.ResponseWriter, r *http.Request) {
	j, ok := s.findJob(w, r)
	if !ok {
		return
	}
	after, ok := parseAfter(w, r)
	if !ok {
		return
	}
	limit, ok := parseLimit(w, r, maxLogPageSize)
	if !ok {
		return
	}
	chunks, err := s.jobs.Logs(r.Context(), j.ID, after, limit)
	if err != nil {
		s.internalError(w, r, "list logs", err)
		return
	}
	views := make([]logChunkView, 0, len(chunks))
	for _, c := range chunks {
		views = append(views, logChunkView{
			ID: c.ID, AttemptID: c.AttemptID, Seq: c.Seq, Stream: c.Stream, Data: c.Data, CreatedAt: c.CreatedAt,
		})
		after = c.ID
	}
	writeJSON(w, http.StatusOK, map[string]any{"chunks": views, "next_after": after})
}

// findJob resolves the {id} URL parameter. On failure it writes the response
// and returns false.
func (s *server) findJob(w http.ResponseWriter, r *http.Request) (db.Job, bool) {
	ref, err := job.ParseRef(chi.URLParam(r, "id"))
	if err != nil {
		writeProblem(w, r, http.StatusBadRequest, err.Error())
		return db.Job{}, false
	}
	j, err := s.jobs.Find(r.Context(), ref)
	if errors.Is(err, job.ErrNotFound) {
		writeProblem(w, r, http.StatusNotFound, "job not found")
		return db.Job{}, false
	}
	if err != nil {
		s.internalError(w, r, "find job", err)
		return db.Job{}, false
	}
	return j, true
}

func parseLimit(w http.ResponseWriter, r *http.Request, maxLimit int32) (int32, bool) {
	v := r.URL.Query().Get("limit")
	if v == "" {
		return min(defaultPageSize, maxLimit), true
	}
	n, err := strconv.ParseInt(v, 10, 32)
	if err != nil || n < 1 || n > int64(maxLimit) {
		writeProblem(w, r, http.StatusBadRequest, "limit must be between 1 and "+strconv.Itoa(int(maxLimit)))
		return 0, false
	}
	return int32(n), true
}

func parseAfter(w http.ResponseWriter, r *http.Request) (int64, bool) {
	v := r.URL.Query().Get("after")
	if v == "" {
		return 0, true
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		writeProblem(w, r, http.StatusBadRequest, "after must be a non-negative id")
		return 0, false
	}
	return n, true
}

func newJobView(j db.Job) jobView {
	return jobView{
		ID: j.ID, Number: j.Number, State: j.State, AssignedWorkerID: j.AssignedWorkerID,
		ScheduledAt: j.ScheduledAt, CancelRequestedAt: j.CancelRequestedAt,
		CreatedAt: j.CreatedAt, UpdatedAt: j.UpdatedAt, StartedAt: j.StartedAt, FinishedAt: j.FinishedAt,
	}
}

func newJobDetailView(d job.Detail) jobDetailView {
	transitions := make([]transitionView, 0, len(d.Transitions))
	for _, t := range d.Transitions {
		transitions = append(transitions, transitionView{
			From: t.FromState, To: t.ToState, Actor: t.Actor, Reason: t.Reason, At: t.CreatedAt,
		})
	}
	return jobDetailView{
		jobView:     newJobView(d.Job),
		Workload:    workloadView{ID: d.Workload.ID, Spec: d.Workload.Spec, CreatedAt: d.Workload.CreatedAt},
		Transitions: transitions,
	}
}
