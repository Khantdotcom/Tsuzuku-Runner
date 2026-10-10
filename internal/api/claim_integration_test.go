//go:build integration

package api_test

import (
	"errors"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Khantdotcom/tsuzuku-runner/internal/scheduler"
	"github.com/Khantdotcom/tsuzuku-runner/internal/worker"
	"github.com/Khantdotcom/tsuzuku-runner/internal/workerapi"
)

type claimResult struct {
	claim workerapi.ClaimResponse
	found bool
	err   error
}

func TestScheduleAndClaimOverHTTP(t *testing.T) {
	srv, pool, notifier := newServerWithPool(t, testStale)
	sched := scheduler.New(pool, slog.New(slog.DiscardHandler), scheduler.Options{
		Interval: time.Second, StaleAfter: testStale, Notifier: notifier,
	})
	client := worker.NewClient(srv.URL, token)

	reg, err := client.Register(t.Context(), workerapi.RegisterRequest{Name: "claimer", Slots: 1, CPUMillis: 1000, MemoryMB: 1024})
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := client.Claim(t.Context(), reg.ID, 0); err != nil || found {
		t.Fatalf("empty claim: found %v, err %v", found, err)
	}

	// A claim that is already waiting picks up a job as soon as it is scheduled.
	results := make(chan claimResult, 1)
	go func() {
		c, found, err := client.Claim(t.Context(), reg.ID, 20*time.Second)
		results <- claimResult{c, found, err}
	}()
	time.Sleep(200 * time.Millisecond)

	resp := submitWorkload(t, srv.URL, workloadJSON("go test ./..."), "")
	var submitted jobBody
	resp.decode(t, &submitted)
	if placed, err := sched.Tick(t.Context()); err != nil || len(placed) != 1 {
		t.Fatalf("Tick placed %d, err %v", len(placed), err)
	}
	scheduledAt := time.Now()

	var got claimResult
	select {
	case got = <-results:
	case <-time.After(5 * time.Second):
		t.Fatal("waiting claim did not return after the job was scheduled")
	}
	if wake := time.Since(scheduledAt); wake > time.Second {
		t.Errorf("claim returned %s after scheduling, want well under a second", wake)
	}
	if got.err != nil || !got.found {
		t.Fatalf("claim: found %v, err %v", got.found, got.err)
	}
	if got.claim.JobID != submitted.ID || got.claim.AttemptNumber != 1 || got.claim.Spec.Command != "go test ./..." {
		t.Errorf("claim = %+v", got.claim)
	}

	var detail jobBody
	if status := getJSON(t, srv.URL+"/api/v1/jobs/"+submitted.ID.String(), &detail); status != http.StatusOK {
		t.Fatalf("get job status = %d", status)
	}
	if detail.State != "PREPARING" || detail.StartedAt == nil || len(detail.Transitions) != 3 {
		t.Errorf("job = %s, started_at %v, %d transitions; want PREPARING, started, 3 transitions",
			detail.State, detail.StartedAt, len(detail.Transitions))
	}
	var attempts struct {
		Attempts []struct {
			ID       uuid.UUID `json:"id"`
			WorkerID uuid.UUID `json:"worker_id"`
			Status   string    `json:"status"`
		} `json:"attempts"`
	}
	getJSON(t, srv.URL+"/api/v1/jobs/"+submitted.ID.String()+"/attempts", &attempts)
	if len(attempts.Attempts) != 1 || attempts.Attempts[0].ID != got.claim.AttemptID ||
		attempts.Attempts[0].WorkerID != reg.ID || attempts.Attempts[0].Status != "RUNNING" {
		t.Errorf("attempts = %+v", attempts.Attempts)
	}

	start := time.Now()
	if _, found, err := client.Claim(t.Context(), reg.ID, 500*time.Millisecond); err != nil || found {
		t.Errorf("claim after taking the only job: found %v, err %v", found, err)
	}
	if elapsed := time.Since(start); elapsed < 500*time.Millisecond {
		t.Errorf("empty long-poll returned after %s, want the full 500ms wait", elapsed)
	}
}

func TestClaimUnknownWorkerOverHTTP(t *testing.T) {
	srv := newServer(t, testStale)
	_, _, err := worker.NewClient(srv.URL, token).Claim(t.Context(), uuid.Must(uuid.NewV7()), 0)
	if !errors.Is(err, worker.ErrUnknownWorker) {
		t.Errorf("err = %v, want ErrUnknownWorker", err)
	}
}
