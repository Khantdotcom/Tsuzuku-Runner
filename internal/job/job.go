package job

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Khantdotcom/tsuzuku-runner/internal/store"
	"github.com/Khantdotcom/tsuzuku-runner/internal/store/db"
	"github.com/Khantdotcom/tsuzuku-runner/internal/workload"
)

// ActorAPI is the actor recorded for changes made through the HTTP API.
const ActorAPI = "api"

// Event types written to job_events.
const (
	EventCreated      = "job.created"
	EventStateChanged = "job.state_changed"
)

const idempotencyKeyConstraint = "workloads_idempotency_key_key"

var (
	// ErrNotFound means the job does not exist.
	ErrNotFound = errors.New("job not found")
	// ErrIllegalTransition means the transition table does not allow the change.
	ErrIllegalTransition = errors.New("illegal job state transition")
	// ErrConflict means the job was no longer in the expected state, usually
	// because another actor changed it first.
	ErrConflict = errors.New("job state changed concurrently")
	// ErrIdempotencyMismatch means the idempotency key was already used for a
	// different request.
	ErrIdempotencyMismatch = errors.New("idempotency key was already used with a different request")
)

// Change describes one state transition.
type Change struct {
	JobID  uuid.UUID
	From   State
	To     State
	Actor  string
	Reason string
}

// Transition moves a job from c.From to c.To, records the transition, and
// writes a timeline event. q must be bound to a transaction so the three
// writes commit together. It returns ErrIllegalTransition without touching
// the database, ErrConflict if the job is no longer in c.From, and
// ErrNotFound if the job does not exist.
func Transition(ctx context.Context, q *db.Queries, c Change) (db.Job, error) {
	if !CanTransition(c.From, c.To) {
		return db.Job{}, fmt.Errorf("%w: %s to %s", ErrIllegalTransition, c.From, c.To)
	}
	if c.Actor == "" {
		return db.Job{}, errors.New("transition actor must not be empty")
	}

	j, err := q.TransitionJob(ctx, db.TransitionJobParams{
		ID:           c.JobID,
		FromState:    string(c.From),
		ToState:      string(c.To),
		MarkStarted:  c.To == Preparing,
		MarkFinished: c.To.Terminal(),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		current, getErr := q.GetJob(ctx, c.JobID)
		if errors.Is(getErr, pgx.ErrNoRows) {
			return db.Job{}, ErrNotFound
		}
		if getErr != nil {
			return db.Job{}, fmt.Errorf("load job after failed transition: %w", getErr)
		}
		return db.Job{}, fmt.Errorf("%w: job %d is %s, not %s", ErrConflict, current.Number, current.State, c.From)
	}
	if err != nil {
		return db.Job{}, fmt.Errorf("update job state: %w", err)
	}

	from := string(c.From)
	if _, err := q.RecordTransition(ctx, db.RecordTransitionParams{
		JobID: j.ID, FromState: &from, ToState: j.State, Actor: c.Actor, Reason: c.Reason,
	}); err != nil {
		return db.Job{}, fmt.Errorf("record transition: %w", err)
	}
	payload, err := json.Marshal(map[string]string{
		"from": from, "to": j.State, "actor": c.Actor, "reason": c.Reason,
	})
	if err != nil {
		return db.Job{}, err
	}
	if _, err := q.CreateJobEvent(ctx, db.CreateJobEventParams{
		JobID: j.ID, Type: EventStateChanged, Payload: payload,
	}); err != nil {
		return db.Job{}, fmt.Errorf("record state change event: %w", err)
	}
	return j, nil
}

// Detail is a job together with its workload and state history.
type Detail struct {
	Job         db.Job
	Workload    db.Workload
	Transitions []db.StateTransition
}

// Submission is the result of Submit. Replayed is true when an earlier
// submission with the same idempotency key was returned instead of a new job.
type Submission struct {
	Detail
	Replayed bool
}

// Ref identifies a job by UUID or by its human-friendly number.
type Ref struct {
	ID     uuid.UUID
	Number int64
}

// ParseRef parses a job UUID or a positive job number such as "123".
func ParseRef(s string) (Ref, error) {
	if id, err := uuid.Parse(s); err == nil {
		return Ref{ID: id}, nil
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil && n > 0 {
		return Ref{Number: n}, nil
	}
	return Ref{}, errors.New("job id must be a UUID or a job number")
}

// ListParams filters and pages a job listing.
type ListParams struct {
	// State limits the listing to one state; empty means every state.
	State State
	// Before returns only jobs numbered below it; zero means from the newest.
	Before int64
	Limit  int32
}

// Service reads and submits jobs.
type Service struct {
	pool *pgxpool.Pool
	q    *db.Queries
}

// NewService returns a Service backed by pool.
func NewService(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool, q: db.New(pool)}
}

// Submit stores spec as a workload with a QUEUED job, its first transition,
// and a creation event, all in one transaction. With a non-empty
// idempotencyKey, a repeated identical submission returns the original job,
// and a different submission under the same key returns ErrIdempotencyMismatch.
func (s *Service) Submit(ctx context.Context, spec workload.Spec, idempotencyKey string) (Submission, error) {
	if idempotencyKey != "" {
		sub, found, err := s.replay(ctx, spec, idempotencyKey)
		if err != nil || found {
			return sub, err
		}
	}

	detail, err := s.create(ctx, spec, idempotencyKey)
	var pgErr *pgconn.PgError
	if idempotencyKey != "" && errors.As(err, &pgErr) && pgErr.ConstraintName == idempotencyKeyConstraint {
		// A concurrent request with the same key committed first.
		sub, found, err := s.replay(ctx, spec, idempotencyKey)
		if err == nil && !found {
			err = errors.New("idempotency key conflicted but no workload holds it")
		}
		return sub, err
	}
	if err != nil {
		return Submission{}, err
	}
	return Submission{Detail: detail}, nil
}

func (s *Service) create(ctx context.Context, spec workload.Spec, idempotencyKey string) (Detail, error) {
	rawSpec, err := json.Marshal(spec)
	if err != nil {
		return Detail{}, fmt.Errorf("encode workload spec: %w", err)
	}
	workloadID, err := uuid.NewV7()
	if err != nil {
		return Detail{}, err
	}
	jobID, err := uuid.NewV7()
	if err != nil {
		return Detail{}, err
	}
	var key *string
	if idempotencyKey != "" {
		key = &idempotencyKey
	}

	var d Detail
	err = store.WithTx(ctx, s.pool, func(q *db.Queries) error {
		var err error
		d.Workload, err = q.CreateWorkload(ctx, db.CreateWorkloadParams{
			ID:                  workloadID,
			IdempotencyKey:      key,
			RepositoryURL:       spec.Repository,
			Revision:            spec.Revision,
			Command:             spec.Command,
			Image:               spec.Runtime.Image,
			VerificationCommand: spec.VerificationCommand(),
			AcceptanceCriteria:  spec.AcceptanceCriteria,
			CpuMillis:           int32(spec.CPUMillis()),        //nolint:gosec // bounded by workload.Limits
			MemoryMB:            int32(spec.Resources.MemoryMB), //nolint:gosec // bounded by workload.Limits
			TimeoutSeconds:      int32(spec.TimeoutSeconds),     //nolint:gosec // bounded by workload.Limits
			NetworkEnabled:      spec.Runtime.Network,
			Spec:                rawSpec,
		})
		if err != nil {
			return fmt.Errorf("create workload: %w", err)
		}
		d.Job, err = q.CreateJob(ctx, db.CreateJobParams{ID: jobID, WorkloadID: workloadID})
		if err != nil {
			return fmt.Errorf("create job: %w", err)
		}
		tr, err := q.RecordTransition(ctx, db.RecordTransitionParams{
			JobID: jobID, ToState: d.Job.State, Actor: ActorAPI, Reason: "workload submitted",
		})
		if err != nil {
			return fmt.Errorf("record transition: %w", err)
		}
		d.Transitions = []db.StateTransition{tr}
		payload, err := json.Marshal(map[string]any{"number": d.Job.Number, "state": d.Job.State})
		if err != nil {
			return err
		}
		if _, err := q.CreateJobEvent(ctx, db.CreateJobEventParams{
			JobID: jobID, Type: EventCreated, Payload: payload,
		}); err != nil {
			return fmt.Errorf("record creation event: %w", err)
		}
		return nil
	})
	if err != nil {
		return Detail{}, err
	}
	return d, nil
}

// replay looks up an earlier submission by idempotency key. found is false
// when the key has not been used.
func (s *Service) replay(ctx context.Context, spec workload.Spec, key string) (Submission, bool, error) {
	w, err := s.q.GetWorkloadByIdempotencyKey(ctx, &key)
	if errors.Is(err, pgx.ErrNoRows) {
		return Submission{}, false, nil
	}
	if err != nil {
		return Submission{}, false, fmt.Errorf("look up idempotency key: %w", err)
	}

	var stored workload.Spec
	if err := json.Unmarshal(w.Spec, &stored); err != nil {
		return Submission{}, false, fmt.Errorf("decode stored workload spec: %w", err)
	}
	if !reflect.DeepEqual(stored, spec) {
		return Submission{}, true, ErrIdempotencyMismatch
	}

	j, err := s.q.GetFirstJobForWorkload(ctx, w.ID)
	if err != nil {
		return Submission{}, false, fmt.Errorf("load job for workload %s: %w", w.ID, err)
	}
	transitions, err := s.q.ListTransitions(ctx, j.ID)
	if err != nil {
		return Submission{}, false, fmt.Errorf("list transitions: %w", err)
	}
	return Submission{Detail: Detail{Job: j, Workload: w, Transitions: transitions}, Replayed: true}, true, nil
}

// Find returns the job ref points to, or ErrNotFound.
func (s *Service) Find(ctx context.Context, ref Ref) (db.Job, error) {
	var (
		j   db.Job
		err error
	)
	if ref.ID != uuid.Nil {
		j, err = s.q.GetJob(ctx, ref.ID)
	} else {
		j, err = s.q.GetJobByNumber(ctx, ref.Number)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Job{}, ErrNotFound
	}
	return j, err
}

// Detail loads the workload and state history of j.
func (s *Service) Detail(ctx context.Context, j db.Job) (Detail, error) {
	w, err := s.q.GetWorkload(ctx, j.WorkloadID)
	if err != nil {
		return Detail{}, fmt.Errorf("load workload: %w", err)
	}
	transitions, err := s.q.ListTransitions(ctx, j.ID)
	if err != nil {
		return Detail{}, fmt.Errorf("list transitions: %w", err)
	}
	return Detail{Job: j, Workload: w, Transitions: transitions}, nil
}

// List returns jobs newest first.
func (s *Service) List(ctx context.Context, p ListParams) ([]db.ListJobsRow, error) {
	params := db.ListJobsParams{RowLimit: p.Limit}
	if p.State != "" {
		state := string(p.State)
		params.State = &state
	}
	if p.Before > 0 {
		params.Before = &p.Before
	}
	return s.q.ListJobs(ctx, params)
}

// Attempts returns a job's attempts in order.
func (s *Service) Attempts(ctx context.Context, jobID uuid.UUID) ([]db.JobAttempt, error) {
	return s.q.ListAttempts(ctx, jobID)
}

// Events returns up to limit timeline events with IDs above after.
func (s *Service) Events(ctx context.Context, jobID uuid.UUID, after int64, limit int32) ([]db.JobEvent, error) {
	return s.q.ListJobEvents(ctx, db.ListJobEventsParams{JobID: jobID, AfterID: after, RowLimit: limit})
}

// Logs returns up to limit log chunks with IDs above after.
func (s *Service) Logs(ctx context.Context, jobID uuid.UUID, after int64, limit int32) ([]db.LogChunk, error) {
	return s.q.ListLogChunks(ctx, db.ListLogChunksParams{JobID: jobID, AfterID: after, RowLimit: limit})
}
