package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Khantdotcom/tsuzuku-runner/internal/job"
	"github.com/Khantdotcom/tsuzuku-runner/internal/workerapi"
	"github.com/Khantdotcom/tsuzuku-runner/internal/workload"
)

type fakeAttempts struct {
	mu        sync.Mutex
	executing []workerapi.ExecutingRequest
	verifying []workerapi.VerifyingRequest
	finished  []workerapi.FinishRequest
	verify    bool
	err       error
}

func (f *fakeAttempts) Executing(_ context.Context, _, _ uuid.UUID, req workerapi.ExecutingRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.executing = append(f.executing, req)
	return f.err
}

func (f *fakeAttempts) Verifying(_ context.Context, _, _ uuid.UUID, req workerapi.VerifyingRequest) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.verifying = append(f.verifying, req)
	return f.verify, f.err
}

func (f *fakeAttempts) Finish(_ context.Context, _, _ uuid.UUID, req workerapi.FinishRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.finished = append(f.finished, req)
	return f.err
}

func (f *fakeAttempts) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.executing) + len(f.verifying) + len(f.finished)
}

func attemptsRouter(f *fakeAttempts) http.Handler {
	return NewRouter(Config{
		Logger:           slog.New(slog.DiscardHandler),
		DB:               fakePinger{},
		Workers:          &fakeWorkerStore{},
		Jobs:             &fakeJobService{},
		Attempts:         f,
		WorkerToken:      testToken,
		WorkerStaleAfter: testStaleAfter,
		WorkloadLimits:   workload.DefaultLimits(),
	})
}

var testAttempt = uuid.MustParse("01920000-0000-7000-8000-0000000000a1")

func attemptPath(action string) string {
	return workerapi.AttemptPath(claimWorker, testAttempt, action)
}

func testRuntime(role string) workerapi.Runtime {
	start := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	return workerapi.Runtime{
		Role: role, Image: "golang:1.27", ContainerID: "abc123", VolumeName: "tsuzuku-x",
		CPUMillis: 1000, MemoryMB: 512, StartedAt: start, FinishedAt: start.Add(3 * time.Second),
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const testCommit = "7fd1a60b01f91b314f59955a4e4d4e80d8edf11d"

func TestAttemptExecuting(t *testing.T) {
	f := &fakeAttempts{}
	body := mustJSON(t, workerapi.ExecutingRequest{Commit: testCommit, Runtime: testRuntime(workerapi.RolePrepare)})

	rec := serve(t, attemptsRouter(f), http.MethodPost, attemptPath(workerapi.ActionExecuting), body, testToken)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body = %s", rec.Code, rec.Body)
	}
	if len(f.executing) != 1 || f.executing[0].Commit != testCommit || f.executing[0].Runtime.ContainerID != "abc123" {
		t.Errorf("executing = %+v", f.executing)
	}
}

func TestAttemptVerifying(t *testing.T) {
	for _, verify := range []bool{true, false} {
		t.Run(fmt.Sprint(verify), func(t *testing.T) {
			f := &fakeAttempts{verify: verify}
			body := mustJSON(t, workerapi.VerifyingRequest{
				Execution: workerapi.StepResult{ExitCode: 1, DurationMS: 3000},
				Runtime:   testRuntime(workerapi.RoleExecute),
			})

			rec := serve(t, attemptsRouter(f), http.MethodPost, attemptPath(workerapi.ActionVerifying), body, testToken)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body)
			}
			var resp workerapi.VerifyingResponse
			if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
				t.Fatal(err)
			}
			if resp.Verify != verify {
				t.Errorf("verify = %v, want %v", resp.Verify, verify)
			}
			if len(f.verifying) != 1 || f.verifying[0].Execution.ExitCode != 1 {
				t.Errorf("verifying = %+v", f.verifying)
			}
		})
	}
}

func TestAttemptFinish(t *testing.T) {
	rt := testRuntime(workerapi.RolePrepare)
	bodies := map[string]string{
		"decide":       `{}`,
		"error":        mustJSON(t, workerapi.FinishRequest{Error: "checkout failed", Runtime: &rt}),
		"cancelled":    `{"cancelled":true}`,
		"empty object": `{"error":""}`,
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			f := &fakeAttempts{}
			rec := serve(t, attemptsRouter(f), http.MethodPost, attemptPath(workerapi.ActionFinish), body, testToken)
			if rec.Code != http.StatusNoContent {
				t.Fatalf("status = %d, want 204; body = %s", rec.Code, rec.Body)
			}
			if len(f.finished) != 1 {
				t.Errorf("finish calls = %d", len(f.finished))
			}
		})
	}
}

func TestAttemptValidation(t *testing.T) {
	good := testRuntime(workerapi.RoleExecute)
	withRuntime := func(change func(*workerapi.Runtime)) string {
		rt := good
		change(&rt)
		return mustJSON(t, workerapi.VerifyingRequest{Runtime: rt})
	}
	verifying := attemptPath(workerapi.ActionVerifying)
	executing := attemptPath(workerapi.ActionExecuting)
	finish := attemptPath(workerapi.ActionFinish)

	tests := []struct {
		name, path, body string
		want             int
	}{
		{"no token", verifying, "", 0},
		{"bad worker id", "/api/v1/workers/nope/attempts/" + testAttempt.String() + "/finish", `{}`, http.StatusBadRequest},
		{"bad attempt id", "/api/v1/workers/" + claimWorker.String() + "/attempts/nope/finish", `{}`, http.StatusBadRequest},
		{"unknown field", finish, `{"surprise":1}`, http.StatusBadRequest},
		{"malformed json", finish, `{`, http.StatusBadRequest},
		{"wrong role", verifying, withRuntime(func(r *workerapi.Runtime) { r.Role = workerapi.RolePrepare }), http.StatusUnprocessableEntity},
		{"no image", verifying, withRuntime(func(r *workerapi.Runtime) { r.Image = "" }), http.StatusUnprocessableEntity},
		{"zero cpu", verifying, withRuntime(func(r *workerapi.Runtime) { r.CPUMillis = 0 }), http.StatusUnprocessableEntity},
		{"zero memory", verifying, withRuntime(func(r *workerapi.Runtime) { r.MemoryMB = 0 }), http.StatusUnprocessableEntity},
		{"no start time", verifying, withRuntime(func(r *workerapi.Runtime) { r.StartedAt = time.Time{} }), http.StatusUnprocessableEntity},
		{"finished before start", verifying, withRuntime(func(r *workerapi.Runtime) { r.FinishedAt = r.StartedAt.Add(-time.Second) }), http.StatusUnprocessableEntity},
		{"long container id", verifying, withRuntime(func(r *workerapi.Runtime) { r.ContainerID = strings.Repeat("a", 129) }), http.StatusUnprocessableEntity},
		{
			"bad exit code", verifying,
			mustJSON(t, workerapi.VerifyingRequest{Execution: workerapi.StepResult{ExitCode: 300}, Runtime: good}),
			http.StatusUnprocessableEntity,
		},
		{
			"negative duration", verifying,
			mustJSON(t, workerapi.VerifyingRequest{Execution: workerapi.StepResult{DurationMS: -1}, Runtime: good}),
			http.StatusUnprocessableEntity,
		},
		{
			"short commit", executing,
			mustJSON(t, workerapi.ExecutingRequest{Commit: "7fd1a60", Runtime: testRuntime(workerapi.RolePrepare)}),
			http.StatusUnprocessableEntity,
		},
		{
			"uppercase commit", executing,
			mustJSON(t, workerapi.ExecutingRequest{Commit: strings.ToUpper(testCommit), Runtime: testRuntime(workerapi.RolePrepare)}),
			http.StatusUnprocessableEntity,
		},
		{"unknown finish role", finish, mustJSON(t, workerapi.FinishRequest{Error: "x", Runtime: &workerapi.Runtime{Role: "debug"}}), http.StatusUnprocessableEntity},
		{"cancelled with error", finish, `{"cancelled":true,"error":"x"}`, http.StatusUnprocessableEntity},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeAttempts{}
			token := testToken
			want := tt.want
			if want == 0 {
				token, want = "", http.StatusUnauthorized
			}
			rec := serve(t, attemptsRouter(f), http.MethodPost, tt.path, tt.body, token)
			if rec.Code != want {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, want, rec.Body)
			}
			decodeProblem(t, rec)
			if f.calls() != 0 {
				t.Error("service called for an invalid request")
			}
		})
	}
}

func TestAttemptServiceErrors(t *testing.T) {
	tests := []struct {
		err  error
		want int
	}{
		{job.ErrAttemptNotFound, http.StatusNotFound},
		{fmt.Errorf("%w: attempt 1 is FAILED", job.ErrAttemptFinished), http.StatusConflict},
		{fmt.Errorf("%w: job 3 is EXECUTING", job.ErrConflict), http.StatusConflict},
		{job.ErrIllegalTransition, http.StatusConflict},
		{errors.New("connection reset"), http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.err.Error(), func(t *testing.T) {
			rec := serve(t, attemptsRouter(&fakeAttempts{err: tt.err}), http.MethodPost, attemptPath(workerapi.ActionFinish), `{}`, testToken)
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d", rec.Code, tt.want)
			}
			p := decodeProblem(t, rec)
			if tt.want == http.StatusInternalServerError && strings.Contains(p.Detail, "connection reset") {
				t.Error("internal error details leaked to the worker")
			}
		})
	}
}
