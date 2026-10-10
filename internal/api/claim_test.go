package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Khantdotcom/tsuzuku-runner/internal/job"
	"github.com/Khantdotcom/tsuzuku-runner/internal/scheduler"
	"github.com/Khantdotcom/tsuzuku-runner/internal/store/db"
	"github.com/Khantdotcom/tsuzuku-runner/internal/workerapi"
	"github.com/Khantdotcom/tsuzuku-runner/internal/workload"
)

// fakeClaimer answers each Claim call with fn(call), counting from 1.
type fakeClaimer struct {
	mu    sync.Mutex
	calls int
	fn    func(call int) (job.Claimed, bool, error)
}

func (f *fakeClaimer) Claim(context.Context, uuid.UUID) (job.Claimed, bool, error) {
	f.mu.Lock()
	f.calls++
	call := f.calls
	f.mu.Unlock()
	if f.fn == nil {
		return job.Claimed{}, false, nil
	}
	return f.fn(call)
}

func (f *fakeClaimer) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func claimedJob() job.Claimed {
	return job.Claimed{
		Job:     db.Job{ID: uuid.New(), Number: 12, State: "PREPARING"},
		Attempt: db.JobAttempt{ID: uuid.New(), AttemptNumber: 1},
		Spec:    workload.Spec{Command: "go test ./...", Runtime: workload.Runtime{Image: "golang:1.27"}},
	}
}

type claimSetup struct {
	claimer  *fakeClaimer
	workers  *fakeWorkerStore
	notifier *scheduler.Notifier
	stopping chan struct{}
}

func (cs *claimSetup) router() http.Handler {
	if cs.claimer == nil {
		cs.claimer = &fakeClaimer{}
	}
	if cs.workers == nil {
		cs.workers = &fakeWorkerStore{}
	}
	if cs.notifier == nil {
		cs.notifier = scheduler.NewNotifier()
	}
	if cs.stopping == nil {
		cs.stopping = make(chan struct{})
	}
	return NewRouter(Config{
		Logger:           slog.New(slog.DiscardHandler),
		DB:               fakePinger{},
		Workers:          cs.workers,
		Jobs:             &fakeJobService{},
		Claimer:          cs.claimer,
		Assignments:      cs.notifier,
		Stopping:         cs.stopping,
		WorkerToken:      testToken,
		WorkerStaleAfter: testStaleAfter,
		WorkloadLimits:   workload.DefaultLimits(),
	})
}

var claimWorker = uuid.MustParse("01920000-0000-7000-8000-000000000001")

func claimPath(query string) string {
	return workerapi.ClaimPath(claimWorker) + query
}

func TestClaimReturnsJob(t *testing.T) {
	want := claimedJob()
	cs := &claimSetup{claimer: &fakeClaimer{fn: func(int) (job.Claimed, bool, error) { return want, true, nil }}}

	rec := serve(t, cs.router(), http.MethodPost, claimPath(""), "", testToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body)
	}
	var got workerapi.ClaimResponse
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.JobID != want.Job.ID || got.JobNumber != 12 || got.AttemptID != want.Attempt.ID || got.AttemptNumber != 1 ||
		got.Spec.Command != "go test ./..." {
		t.Errorf("response = %+v", got)
	}
}

func TestClaimWithoutWaitReturnsNoContent(t *testing.T) {
	cs := &claimSetup{}
	rec := serve(t, cs.router(), http.MethodPost, claimPath(""), "", testToken)
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("status = %d, body %q; want an empty 204", rec.Code, rec.Body)
	}
	if cs.claimer.count() != 1 {
		t.Errorf("claims = %d, want 1", cs.claimer.count())
	}
}

func TestClaimWaitsThenGivesUp(t *testing.T) {
	cs := &claimSetup{}
	start := time.Now()
	rec := serve(t, cs.router(), http.MethodPost, claimPath("?wait=300ms"), "", testToken)
	if elapsed := time.Since(start); elapsed < 300*time.Millisecond {
		t.Errorf("returned after %s, want at least the 300ms wait", elapsed)
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
}

func TestClaimWakesOnAssignment(t *testing.T) {
	want := claimedJob()
	var assigned sync.WaitGroup
	assigned.Add(1)
	cs := &claimSetup{claimer: &fakeClaimer{fn: func(call int) (job.Claimed, bool, error) {
		if call == 1 {
			assigned.Done()
			return job.Claimed{}, false, nil
		}
		return want, true, nil
	}}}
	h := cs.router()

	go func() {
		assigned.Wait()
		cs.notifier.Broadcast()
	}()
	start := time.Now()
	rec := serve(t, h, http.MethodPost, claimPath("?wait=10s"), "", testToken)
	elapsed := time.Since(start)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if elapsed > time.Second {
		t.Errorf("took %s, want a prompt wake-up, well under the %s re-check", elapsed, claimRecheckInterval)
	}
}

func TestClaimReturnsOnShutdown(t *testing.T) {
	cs := &claimSetup{}
	h := cs.router()
	go func() {
		time.Sleep(50 * time.Millisecond)
		close(cs.stopping)
	}()
	start := time.Now()
	rec := serve(t, h, http.MethodPost, claimPath("?wait=10s"), "", testToken)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("took %s, want an immediate answer once shutdown starts", elapsed)
	}
}

func TestClaimStopsWhenClientLeaves(t *testing.T) {
	cs := &claimSetup{}
	h := cs.router()
	ctx, cancel := context.WithCancel(t.Context())
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, claimPath("?wait=10s"), nil)
	req.Header.Set("Authorization", "Bearer "+testToken)

	done := make(chan struct{})
	go func() {
		h.ServeHTTP(httptest.NewRecorder(), req)
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handler kept waiting after the client went away")
	}
}

func TestClaimErrors(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		token      string
		setup      claimSetup
		wantStatus int
	}{
		{"no token", claimPath(""), "", claimSetup{}, http.StatusUnauthorized},
		{"bad worker id", "/api/v1/workers/nope/claim", testToken, claimSetup{}, http.StatusBadRequest},
		{"unparsable wait", claimPath("?wait=soon"), testToken, claimSetup{}, http.StatusBadRequest},
		{"negative wait", claimPath("?wait=-1s"), testToken, claimSetup{}, http.StatusBadRequest},
		{"wait too long", claimPath("?wait=31s"), testToken, claimSetup{}, http.StatusBadRequest},
		{"unknown worker", claimPath(""), testToken, claimSetup{workers: &fakeWorkerStore{missing: true}}, http.StatusNotFound},
		{"worker lookup fails", claimPath(""), testToken, claimSetup{workers: &fakeWorkerStore{existsErr: errors.New("boom")}}, http.StatusInternalServerError},
		{
			"claim fails", claimPath(""), testToken,
			claimSetup{claimer: &fakeClaimer{fn: func(int) (job.Claimed, bool, error) { return job.Claimed{}, false, errors.New("boom") }}},
			http.StatusInternalServerError,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := serve(t, tt.setup.router(), http.MethodPost, tt.path, "", tt.token)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			decodeProblem(t, rec)
			if tt.setup.claimer != nil && tt.wantStatus == http.StatusBadRequest && tt.setup.claimer.count() != 0 {
				t.Error("Claim called for an invalid request")
			}
		})
	}
}
