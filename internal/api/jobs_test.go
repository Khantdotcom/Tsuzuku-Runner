package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Khantdotcom/tsuzuku-runner/internal/job"
	"github.com/Khantdotcom/tsuzuku-runner/internal/store/db"
	"github.com/Khantdotcom/tsuzuku-runner/internal/workload"
)

const validWorkload = `{"repository":"https://github.com/example/app","revision":"main","command":"go test ./..."}`

type submitCall struct {
	spec workload.Spec
	key  string
}

type fakeJobService struct {
	submits   []submitCall
	submitErr error
	replayed  bool

	job     db.Job
	findErr error
	refs    []job.Ref

	listParams []job.ListParams
	listRows   []db.ListJobsRow

	attempts []db.JobAttempt
	events   []db.JobEvent
	chunks   []db.LogChunk
	afters   []int64
	limits   []int32

	cancelImmediate bool
	cancelErr       error
	cancels         int
	evidence        job.Evidence
	artifacts       []db.Artifact
	artifact        db.Artifact
	content         string
	openErr         error
}

func (f *fakeJobService) Cancel(_ context.Context, _ uuid.UUID) (db.Job, bool, error) {
	f.cancels++
	j := f.job
	if f.cancelImmediate {
		j.State = "CANCELLED"
	} else {
		now := time.Now()
		j.CancelRequestedAt = &now
	}
	return j, f.cancelImmediate, f.cancelErr
}

func (f *fakeJobService) Evidence(context.Context, uuid.UUID) (job.Evidence, error) {
	return f.evidence, nil
}

func (f *fakeJobService) Artifacts(context.Context, uuid.UUID) ([]db.Artifact, error) {
	return f.artifacts, nil
}

func (f *fakeJobService) OpenArtifact(context.Context, uuid.UUID, uuid.UUID) (db.Artifact, io.ReadCloser, error) {
	if f.openErr != nil {
		return db.Artifact{}, nil, f.openErr
	}
	return f.artifact, io.NopCloser(strings.NewReader(f.content)), nil
}

func (f *fakeJobService) Submit(_ context.Context, spec workload.Spec, key string) (job.Submission, error) {
	f.submits = append(f.submits, submitCall{spec, key})
	if f.submitErr != nil {
		return job.Submission{}, f.submitErr
	}
	raw, _ := json.Marshal(spec)
	return job.Submission{
		Detail: job.Detail{
			Job:      f.job,
			Workload: db.Workload{ID: f.job.WorkloadID, Spec: raw},
			Transitions: []db.StateTransition{
				{JobID: f.job.ID, ToState: "QUEUED", Actor: "api", Reason: "workload submitted"},
			},
		},
		Replayed: f.replayed,
	}, nil
}

func (f *fakeJobService) Find(_ context.Context, ref job.Ref) (db.Job, error) {
	f.refs = append(f.refs, ref)
	return f.job, f.findErr
}

func (f *fakeJobService) Detail(_ context.Context, j db.Job) (job.Detail, error) {
	return job.Detail{Job: j, Workload: db.Workload{ID: j.WorkloadID, Spec: json.RawMessage(`{}`)}}, nil
}

func (f *fakeJobService) List(_ context.Context, p job.ListParams) ([]db.ListJobsRow, error) {
	f.listParams = append(f.listParams, p)
	return f.listRows, nil
}

func (f *fakeJobService) Attempts(context.Context, uuid.UUID) ([]db.JobAttempt, error) {
	return f.attempts, nil
}

func (f *fakeJobService) Events(_ context.Context, _ uuid.UUID, after int64, limit int32) ([]db.JobEvent, error) {
	f.afters = append(f.afters, after)
	f.limits = append(f.limits, limit)
	return f.events, nil
}

func (f *fakeJobService) Logs(_ context.Context, _ uuid.UUID, after int64, limit int32) ([]db.LogChunk, error) {
	f.afters = append(f.afters, after)
	f.limits = append(f.limits, limit)
	return f.chunks, nil
}

func newJobsRouter(jobs *fakeJobService) http.Handler {
	if jobs.job.ID == uuid.Nil {
		jobs.job = db.Job{ID: uuid.New(), Number: 7, WorkloadID: uuid.New(), State: "QUEUED", CreatedAt: time.Now()}
	}
	return NewRouter(Config{
		Logger:           slog.New(slog.DiscardHandler),
		DB:               fakePinger{},
		Workers:          &fakeWorkerStore{},
		Jobs:             jobs,
		WorkerToken:      testToken,
		WorkerStaleAfter: testStaleAfter,
		WorkloadLimits:   workload.DefaultLimits(),
	})
}

func submit(t *testing.T, h http.Handler, body string, key *string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/workloads", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if key != nil {
		req.Header.Set("Idempotency-Key", *key)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestSubmitWorkload(t *testing.T) {
	jobs := &fakeJobService{}
	h := newJobsRouter(jobs)
	key := "deploy-42"

	rec := submit(t, h, validWorkload, &key)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", rec.Code, rec.Body)
	}
	if got, want := rec.Header().Get("Location"), "/api/v1/jobs/"+jobs.job.ID.String(); got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}
	if got := rec.Header().Get("Idempotent-Replayed"); got != "" {
		t.Errorf("Idempotent-Replayed = %q on a new job", got)
	}
	if len(jobs.submits) != 1 {
		t.Fatalf("Submit calls = %d, want 1", len(jobs.submits))
	}
	call := jobs.submits[0]
	if call.key != key {
		t.Errorf("key = %q, want %q", call.key, key)
	}
	if call.spec.Runtime.Image != workload.DefaultImage || call.spec.TimeoutSeconds != workload.DefaultTimeoutSeconds {
		t.Errorf("spec defaults not applied: %+v", call.spec)
	}

	var body struct {
		ID          uuid.UUID `json:"id"`
		Number      int64     `json:"number"`
		State       string    `json:"state"`
		Workload    struct{ Spec workload.Spec }
		Transitions []struct {
			From *string `json:"from"`
			To   string  `json:"to"`
		} `json:"transitions"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.ID != jobs.job.ID || body.Number != 7 || body.State != "QUEUED" {
		t.Errorf("job = %+v", body)
	}
	if body.Workload.Spec.Repository != "https://github.com/example/app" {
		t.Errorf("workload spec = %+v", body.Workload.Spec)
	}
	if len(body.Transitions) != 1 || body.Transitions[0].From != nil || body.Transitions[0].To != "QUEUED" {
		t.Errorf("transitions = %+v", body.Transitions)
	}
}

func TestSubmitWorkloadWithoutKey(t *testing.T) {
	jobs := &fakeJobService{}
	rec := submit(t, newJobsRouter(jobs), validWorkload, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", rec.Code)
	}
	if len(jobs.submits) != 1 || jobs.submits[0].key != "" {
		t.Errorf("submits = %+v, want one call with no key", jobs.submits)
	}
}

func TestSubmitWorkloadReplay(t *testing.T) {
	jobs := &fakeJobService{replayed: true}
	key := "deploy-42"
	rec := submit(t, newJobsRouter(jobs), validWorkload, &key)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Idempotent-Replayed"); got != "true" {
		t.Errorf("Idempotent-Replayed = %q, want true", got)
	}
	if rec.Header().Get("Location") == "" {
		t.Error("Location missing on replay")
	}
}

func TestSubmitWorkloadErrors(t *testing.T) {
	empty, spaced, long := "", "has space", strings.Repeat("k", 256)
	tests := []struct {
		name       string
		body       string
		key        *string
		submitErr  error
		wantStatus int
		wantDetail string
	}{
		{"empty key", validWorkload, &empty, nil, http.StatusBadRequest, "Idempotency-Key"},
		{"key with space", validWorkload, &spaced, nil, http.StatusBadRequest, "Idempotency-Key"},
		{"key too long", validWorkload, &long, nil, http.StatusBadRequest, "Idempotency-Key"},
		{"not json", "nope", nil, nil, http.StatusBadRequest, "JSON object"},
		{"empty body", "", nil, nil, http.StatusBadRequest, "JSON object"},
		{"two objects", validWorkload + validWorkload, nil, nil, http.StatusBadRequest, "JSON object"},
		{
			"unknown field",
			`{"repository":"https://github.com/example/app","revision":"main","command":"x","comand":"y"}`,
			nil, nil, http.StatusBadRequest, `unknown field "comand"`,
		},
		{
			"wrong type",
			`{"repository":"https://github.com/example/app","revision":"main","command":"x","resources":{"cpu":"two"}}`,
			nil, nil, http.StatusBadRequest, "resources.cpu must be a JSON number",
		},
		{
			"oversized",
			`{"command":"` + strings.Repeat("x", maxBodyBytes) + `"}`,
			nil, nil, http.StatusBadRequest, "at most 65536 bytes",
		},
		{"missing command", `{"repository":"https://github.com/example/app","revision":"main"}`, nil, nil, http.StatusUnprocessableEntity, "command"},
		{
			"http repository",
			`{"repository":"http://github.com/example/app","revision":"main","command":"x"}`,
			nil, nil, http.StatusUnprocessableEntity, "https://",
		},
		{
			"cpu over limit",
			`{"repository":"https://github.com/example/app","revision":"main","command":"x","resources":{"cpu":64}}`,
			nil, nil, http.StatusUnprocessableEntity, "resources.cpu",
		},
		{"key reused", validWorkload, nil, job.ErrIdempotencyMismatch, http.StatusConflict, "different workload"},
		{"store failure", validWorkload, nil, errors.New("boom"), http.StatusInternalServerError, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jobs := &fakeJobService{submitErr: tt.submitErr}
			rec := submit(t, newJobsRouter(jobs), tt.body, tt.key)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, tt.wantStatus, rec.Body)
			}
			p := decodeProblem(t, rec)
			if !strings.Contains(p.Detail, tt.wantDetail) {
				t.Errorf("detail = %q, want it to contain %q", p.Detail, tt.wantDetail)
			}
			if tt.submitErr == nil && len(jobs.submits) != 0 {
				t.Errorf("Submit called for a rejected request")
			}
			if tt.wantStatus == http.StatusInternalServerError && strings.Contains(rec.Body.String(), "boom") {
				t.Error("internal error leaked to the client")
			}
		})
	}
}

func TestListJobs(t *testing.T) {
	jobs := &fakeJobService{listRows: []db.ListJobsRow{
		{Job: db.Job{ID: uuid.New(), Number: 9, State: "QUEUED"}, RepositoryURL: "https://github.com/a/b", Revision: "main", Command: "make", Image: "golang:1.27"},
		{Job: db.Job{ID: uuid.New(), Number: 8, State: "QUEUED"}, RepositoryURL: "https://github.com/a/b", Revision: "main", Command: "make", Image: "golang:1.27"},
	}}
	h := newJobsRouter(jobs)

	rec := serve(t, h, http.MethodGet, "/api/v1/jobs?state=QUEUED&before=10&limit=2", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body)
	}
	if want := (job.ListParams{State: job.Queued, Before: 10, Limit: 2}); jobs.listParams[0] != want {
		t.Errorf("ListParams = %+v, want %+v", jobs.listParams[0], want)
	}
	var body struct {
		Jobs []struct {
			Number   int64 `json:"number"`
			Workload struct {
				Repository string `json:"repository"`
				Image      string `json:"image"`
			} `json:"workload"`
		} `json:"jobs"`
		NextBefore *int64 `json:"next_before"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Jobs) != 2 || body.Jobs[0].Number != 9 || body.Jobs[0].Workload.Repository != "https://github.com/a/b" {
		t.Errorf("jobs = %+v", body.Jobs)
	}
	if body.NextBefore == nil || *body.NextBefore != 8 {
		t.Errorf("next_before = %v, want 8 for a full page", body.NextBefore)
	}

	rec = serve(t, h, http.MethodGet, "/api/v1/jobs", "", "")
	if want := (job.ListParams{Limit: defaultPageSize}); jobs.listParams[1] != want {
		t.Errorf("default ListParams = %+v, want %+v", jobs.listParams[1], want)
	}
	if !strings.Contains(rec.Body.String(), `"next_before":null`) {
		t.Errorf("short page should have null next_before: %s", rec.Body)
	}
}

func TestListJobsEmpty(t *testing.T) {
	rec := serve(t, newJobsRouter(&fakeJobService{}), http.MethodGet, "/api/v1/jobs", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"jobs":[]`) {
		t.Errorf("body = %s, want an empty jobs array", rec.Body)
	}
}

func TestListJobsBadParams(t *testing.T) {
	for _, q := range []string{"state=DONE", "state=queued", "before=0", "before=x", "limit=0", "limit=201", "limit=x"} {
		t.Run(q, func(t *testing.T) {
			jobs := &fakeJobService{}
			rec := serve(t, newJobsRouter(jobs), http.MethodGet, "/api/v1/jobs?"+q, "", "")
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}
			decodeProblem(t, rec)
			if len(jobs.listParams) != 0 {
				t.Error("List called for invalid parameters")
			}
		})
	}
}

func TestGetJob(t *testing.T) {
	jobs := &fakeJobService{}
	h := newJobsRouter(jobs)

	rec := serve(t, h, http.MethodGet, "/api/v1/jobs/"+jobs.job.ID.String(), "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	rec = serve(t, h, http.MethodGet, "/api/v1/jobs/7", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("by number: status = %d, want 200", rec.Code)
	}
	if jobs.refs[0] != (job.Ref{ID: jobs.job.ID}) || jobs.refs[1] != (job.Ref{Number: 7}) {
		t.Errorf("refs = %+v", jobs.refs)
	}
	if !strings.Contains(rec.Body.String(), `"transitions":[]`) {
		t.Errorf("body = %s, want an empty transitions array", rec.Body)
	}
}

func TestGetJobErrors(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		findErr    error
		wantStatus int
	}{
		{"bad id", "/api/v1/jobs/abc", nil, http.StatusBadRequest},
		{"zero number", "/api/v1/jobs/0", nil, http.StatusBadRequest},
		{"missing", "/api/v1/jobs/12", job.ErrNotFound, http.StatusNotFound},
		{"store failure", "/api/v1/jobs/12", errors.New("boom"), http.StatusInternalServerError},
		{"missing attempts", "/api/v1/jobs/12/attempts", job.ErrNotFound, http.StatusNotFound},
		{"missing events", "/api/v1/jobs/12/events", job.ErrNotFound, http.StatusNotFound},
		{"missing logs", "/api/v1/jobs/12/logs", job.ErrNotFound, http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := serve(t, newJobsRouter(&fakeJobService{findErr: tt.findErr}), http.MethodGet, tt.path, "", "")
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			p := decodeProblem(t, rec)
			if tt.wantStatus == http.StatusNotFound && p.Detail != "job not found" {
				t.Errorf("detail = %q", p.Detail)
			}
		})
	}
}

func TestListAttempts(t *testing.T) {
	code := int32(0)
	jobs := &fakeJobService{attempts: []db.JobAttempt{{ID: uuid.New(), AttemptNumber: 1, Status: "SUCCEEDED", ExitCode: &code}}}
	rec := serve(t, newJobsRouter(jobs), http.MethodGet, "/api/v1/jobs/7/attempts", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"attempt_number":1`) || !strings.Contains(rec.Body.String(), `"exit_code":0`) {
		t.Errorf("body = %s", rec.Body)
	}
}

func TestListEvents(t *testing.T) {
	jobs := &fakeJobService{events: []db.JobEvent{
		{ID: 11, Type: job.EventCreated, Payload: json.RawMessage(`{"number":7}`)},
		{ID: 14, Type: job.EventStateChanged, Payload: json.RawMessage(`{"to":"SCHEDULED"}`)},
	}}
	h := newJobsRouter(jobs)

	rec := serve(t, h, http.MethodGet, "/api/v1/jobs/7/events?after=10&limit=5", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if jobs.afters[0] != 10 || jobs.limits[0] != 5 {
		t.Errorf("after, limit = %d, %d; want 10, 5", jobs.afters[0], jobs.limits[0])
	}
	var body struct {
		Events []struct {
			ID      int64           `json:"id"`
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		} `json:"events"`
		NextAfter int64 `json:"next_after"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Events) != 2 || body.Events[0].Type != job.EventCreated || string(body.Events[0].Payload) != `{"number":7}` {
		t.Errorf("events = %+v", body.Events)
	}
	if body.NextAfter != 14 {
		t.Errorf("next_after = %d, want 14", body.NextAfter)
	}

	jobs.events = nil
	rec = serve(t, h, http.MethodGet, "/api/v1/jobs/7/events?after=14", "", "")
	if !strings.Contains(rec.Body.String(), `"next_after":14`) {
		t.Errorf("an empty page should keep the cursor: %s", rec.Body)
	}

	for _, q := range []string{"after=-1", "after=x", "limit=0", "limit=201"} {
		if rec := serve(t, h, http.MethodGet, "/api/v1/jobs/7/events?"+q, "", ""); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", q, rec.Code)
		}
	}
}

func TestListLogs(t *testing.T) {
	jobs := &fakeJobService{chunks: []db.LogChunk{{ID: 3, Seq: 0, Stream: "stdout", Data: []byte("hello\n")}}}
	h := newJobsRouter(jobs)

	rec := serve(t, h, http.MethodGet, "/api/v1/jobs/7/logs", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if jobs.limits[0] != defaultPageSize {
		t.Errorf("limit = %d, want %d", jobs.limits[0], defaultPageSize)
	}
	var body struct {
		Chunks []struct {
			Stream string `json:"stream"`
			Data   []byte `json:"data"`
		} `json:"chunks"`
		NextAfter int64 `json:"next_after"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Chunks) != 1 || string(body.Chunks[0].Data) != "hello\n" || body.NextAfter != 3 {
		t.Errorf("body = %+v", body)
	}

	if rec := serve(t, h, http.MethodGet, "/api/v1/jobs/7/logs?limit=1000", "", ""); rec.Code != http.StatusOK {
		t.Errorf("limit=1000: status = %d, want 200", rec.Code)
	}
	if rec := serve(t, h, http.MethodGet, "/api/v1/jobs/7/logs?limit=1001", "", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("limit=1001: status = %d, want 400", rec.Code)
	}
}
