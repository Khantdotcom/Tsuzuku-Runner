package worker

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Khantdotcom/tsuzuku-runner/internal/workerapi"
)

// fakeJobs hands out a new claim on every call, failing the calls listed in
// failOn (1-based).
type fakeJobs struct {
	mu     sync.Mutex
	calls  int
	failOn map[int]bool
	ids    []uuid.UUID
	waits  []time.Duration
}

func (f *fakeJobs) Claim(ctx context.Context, id uuid.UUID, wait time.Duration) (workerapi.ClaimResponse, bool, error) {
	f.mu.Lock()
	f.calls++
	call := f.calls
	f.ids = append(f.ids, id)
	f.waits = append(f.waits, wait)
	f.mu.Unlock()
	if ctx.Err() != nil {
		return workerapi.ClaimResponse{}, false, ctx.Err()
	}
	if f.failOn[call] {
		return workerapi.ClaimResponse{}, false, errors.New("connection refused")
	}
	return workerapi.ClaimResponse{JobID: uuid.New(), AttemptID: uuid.New(), JobNumber: int64(call)}, true, nil
}

func (f *fakeJobs) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// fakeRunner holds every job until release is closed or ctx is cancelled.
type fakeRunner struct {
	mu        sync.Mutex
	release   chan struct{}
	running   int
	maxSeen   int
	finished  int
	cancelled int
}

func (f *fakeRunner) Run(ctx context.Context, _ uuid.UUID, _ workerapi.ClaimResponse) {
	f.mu.Lock()
	f.running++
	f.maxSeen = max(f.maxSeen, f.running)
	f.mu.Unlock()

	select {
	case <-f.release:
	case <-ctx.Done():
		time.Sleep(20 * time.Millisecond)
		f.mu.Lock()
		f.cancelled++
		f.mu.Unlock()
	}

	f.mu.Lock()
	f.running--
	f.finished++
	f.mu.Unlock()
}

func (f *fakeRunner) stats() (running, maxSeen, finished, cancelled int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.running, f.maxSeen, f.finished, f.cancelled
}

func startClaimingAgent(t *testing.T, jobs JobSource, runner JobRunner, slots int) (*fakeAPI, func()) {
	t.Helper()
	api := &fakeAPI{}
	agent := NewAgent(api, fakeProbe{}, slog.New(slog.DiscardHandler), Options{
		Name:              "claimer",
		Slots:             slots,
		HeartbeatInterval: 10 * time.Millisecond,
		MinBackoff:        time.Millisecond,
		MaxBackoff:        4 * time.Millisecond,
		Jobs:              jobs,
		Runner:            runner,
		ClaimWait:         time.Second,
	})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- agent.Run(ctx) }()
	return api, func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run returned %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("Run did not return after cancellation")
		}
	}
}

func TestAgentClaimsUpToSlots(t *testing.T) {
	jobs := &fakeJobs{}
	runner := &fakeRunner{release: make(chan struct{})}
	api, stop := startClaimingAgent(t, jobs, runner, 2)

	waitFor(t, "two running jobs", func() bool {
		running, _, _, _ := runner.stats()
		return running == 2
	})
	time.Sleep(50 * time.Millisecond)
	if n := jobs.count(); n != 2 {
		t.Errorf("claims = %d while both slots are busy, want 2", n)
	}

	close(runner.release)
	waitFor(t, "more jobs after slots free up", func() bool {
		_, _, finished, _ := runner.stats()
		return finished >= 6
	})
	stop()

	if _, maxSeen, _, _ := runner.stats(); maxSeen > 2 {
		t.Errorf("ran %d jobs at once with 2 slots", maxSeen)
	}
	_, ids, _ := api.snapshot()
	jobs.mu.Lock()
	defer jobs.mu.Unlock()
	for i, id := range jobs.ids {
		if id != ids[0] {
			t.Fatalf("claim %d used worker id %s, want %s", i, id, ids[0])
		}
	}
	if jobs.waits[0] != time.Second {
		t.Errorf("claim wait = %s", jobs.waits[0])
	}
}

func TestAgentRetriesFailedClaims(t *testing.T) {
	jobs := &fakeJobs{failOn: map[int]bool{1: true, 2: true}}
	runner := &fakeRunner{release: make(chan struct{})}
	close(runner.release)
	_, stop := startClaimingAgent(t, jobs, runner, 1)

	waitFor(t, "a job after failed claims", func() bool {
		_, _, finished, _ := runner.stats()
		return finished >= 1
	})
	stop()
	if n := jobs.count(); n < 3 {
		t.Errorf("claims = %d, want retries after failures", n)
	}
}

func TestAgentWaitsForRunningJobsOnShutdown(t *testing.T) {
	jobs := &fakeJobs{}
	runner := &fakeRunner{release: make(chan struct{})}
	_, stop := startClaimingAgent(t, jobs, runner, 3)

	waitFor(t, "three running jobs", func() bool {
		running, _, _, _ := runner.stats()
		return running == 3
	})
	stop()

	if running, _, finished, cancelled := runner.stats(); running != 0 || finished != 3 || cancelled != 3 {
		t.Errorf("after Run returned: running %d finished %d cancelled %d; want all 3 jobs stopped first", running, finished, cancelled)
	}
}

func TestNewAgentCapsClaimWait(t *testing.T) {
	a := NewAgent(&fakeAPI{}, fakeProbe{}, slog.New(slog.DiscardHandler), Options{HeartbeatInterval: time.Second, ClaimWait: time.Hour})
	if a.opts.ClaimWait != workerapi.MaxClaimWait {
		t.Errorf("claim wait = %s, want capped at %s", a.opts.ClaimWait, workerapi.MaxClaimWait)
	}
	b := NewAgent(&fakeAPI{}, fakeProbe{}, slog.New(slog.DiscardHandler), Options{HeartbeatInterval: time.Second})
	if b.opts.ClaimWait != defaultClaimWait {
		t.Errorf("default claim wait = %s", b.opts.ClaimWait)
	}
}
