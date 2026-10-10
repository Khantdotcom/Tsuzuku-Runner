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
	logs      [][]workerapi.LogChunk
	verify    bool
	truncated bool
	err       error
}

func (f *fakeAttempts) AppendLogs(_ context.Context, _, _ uuid.UUID, chunks []workerapi.LogChunk) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logs = append(f.logs, chunks)
	return f.truncated, f.err
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
	return len(f.executing) + len(f.verifying) + len(f.finished) + len(f.logs)
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

func TestAttemptLogs(t *testing.T) {
	for _, truncated := range []bool{false, true} {
		t.Run(fmt.Sprint(truncated), func(t *testing.T) {
			f := &fakeAttempts{truncated: truncated}
			body := mustJSON(t, workerapi.LogsRequest{Chunks: []workerapi.LogChunk{
				{Seq: 0, Stream: workerapi.StreamStdout, Data: []byte("hello\n")},
				{Seq: 1, Stream: workerapi.StreamStderr, Data: []byte{0xff, 0x00}},
			}})
			rec := serve(t, attemptsRouter(f), http.MethodPost, attemptPath(workerapi.ActionLogs), body, testToken)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body)
			}
			var resp workerapi.LogsResponse
			if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil || resp.Truncated != truncated {
				t.Errorf("response = %+v, err %v", resp, err)
			}
			if len(f.logs) != 1 || len(f.logs[0]) != 2 || string(f.logs[0][1].Data) != "\xff\x00" {
				t.Errorf("stored = %+v", f.logs)
			}
		})
	}
}

func TestAttemptLogsAcceptsAFullBatch(t *testing.T) {
	chunks := make([]workerapi.LogChunk, 0, 4)
	for i := range 4 {
		chunks = append(chunks, workerapi.LogChunk{Seq: i, Stream: workerapi.StreamStdout, Data: make([]byte, workerapi.MaxLogBatchBytes/4)})
	}
	f := &fakeAttempts{}
	rec := serve(t, attemptsRouter(f), http.MethodPost, attemptPath(workerapi.ActionLogs),
		mustJSON(t, workerapi.LogsRequest{Chunks: chunks}), testToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; a full batch must fit the body limit: %s", rec.Code, rec.Body)
	}
}

func TestAttemptLogsValidation(t *testing.T) {
	logs := func(chunks ...workerapi.LogChunk) string {
		return mustJSON(t, workerapi.LogsRequest{Chunks: chunks})
	}
	tooMany := make([]workerapi.LogChunk, workerapi.MaxLogBatchChunks+1)
	for i := range tooMany {
		tooMany[i] = workerapi.LogChunk{Seq: i, Stream: workerapi.StreamStdout}
	}
	tests := []struct {
		name, body string
	}{
		{"no chunks", `{"chunks":[]}`},
		{"too many chunks", logs(tooMany...)},
		{"negative seq", logs(workerapi.LogChunk{Seq: -1, Stream: workerapi.StreamStdout})},
		{"huge seq", `{"chunks":[{"seq":4294967296,"stream":"stdout","data":""}]}`},
		{"unknown stream", logs(workerapi.LogChunk{Stream: "stdin"})},
		{"too much output", logs(
			workerapi.LogChunk{Seq: 0, Stream: workerapi.StreamStdout, Data: make([]byte, workerapi.MaxLogBatchBytes)},
			workerapi.LogChunk{Seq: 1, Stream: workerapi.StreamStdout, Data: []byte("x")},
		)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeAttempts{}
			rec := serve(t, attemptsRouter(f), http.MethodPost, attemptPath(workerapi.ActionLogs), tt.body, testToken)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422; body = %s", rec.Code, rec.Body)
			}
			decodeProblem(t, rec)
			if f.calls() != 0 {
				t.Error("service called for an invalid batch")
			}
		})
	}
}

func TestAttemptFinishWithVerification(t *testing.T) {
	f := &fakeAttempts{}
	body := mustJSON(t, workerapi.FinishRequest{Verification: &workerapi.VerificationResult{
		Command:    "make lint",
		Result:     workerapi.StepResult{ExitCode: 2, DurationMS: 1500},
		Runtime:    testRuntime(workerapi.RoleVerify),
		OutputTail: "lint failed",
	}})
	rec := serve(t, attemptsRouter(f), http.MethodPost, attemptPath(workerapi.ActionFinish), body, testToken)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body = %s", rec.Code, rec.Body)
	}
	if len(f.finished) != 1 || f.finished[0].Verification == nil || f.finished[0].Verification.Result.ExitCode != 2 {
		t.Errorf("finish = %+v", f.finished)
	}
}

func TestAttemptFinishValidation(t *testing.T) {
	verification := func(change func(*workerapi.VerificationResult)) string {
		v := workerapi.VerificationResult{Command: "make lint", Runtime: testRuntime(workerapi.RoleVerify)}
		change(&v)
		return mustJSON(t, workerapi.FinishRequest{Verification: &v})
	}
	tests := []struct {
		name, body string
	}{
		{"stage without error", `{"stage":"checkout"}`},
		{"unknown stage", `{"error":"x","stage":"lunch"}`},
		{"wrong verification role", verification(func(v *workerapi.VerificationResult) { v.Runtime.Role = workerapi.RoleExecute })},
		{"no verification command", verification(func(v *workerapi.VerificationResult) { v.Command = "" })},
		{"bad verification exit code", verification(func(v *workerapi.VerificationResult) { v.Result.ExitCode = 999 })},
		{"long output", verification(func(v *workerapi.VerificationResult) { v.OutputTail = strings.Repeat("x", maxOutputTail+1) })},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeAttempts{}
			rec := serve(t, attemptsRouter(f), http.MethodPost, attemptPath(workerapi.ActionFinish), tt.body, testToken)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422; body = %s", rec.Code, rec.Body)
			}
			decodeProblem(t, rec)
			if f.calls() != 0 {
				t.Error("service called for an invalid finish")
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
