package worker

import (
	"context"
	"errors"
	"log/slog"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Khantdotcom/tsuzuku-runner/internal/workerapi"
)

type fakeProbe struct{}

func (fakeProbe) Capacity(context.Context) (Capacity, error) {
	return Capacity{CPUMillis: 4000, MemoryMB: 8192}, nil
}

func (fakeProbe) Usage(context.Context) (Usage, error) {
	return Usage{CPUPercent: 25, MemoryUsedMB: 1024}, nil
}

type heartbeatCall struct {
	id  uuid.UUID
	req workerapi.HeartbeatRequest
}

// fakeAPI fails the first registerFailures registrations and the heartbeats
// listed in unknownOn (1-based call numbers) with ErrUnknownWorker.
type fakeAPI struct {
	mu               sync.Mutex
	registerFailures int
	unknownOn        map[int]bool
	registers        []workerapi.RegisterRequest
	ids              []uuid.UUID
	heartbeats       []heartbeatCall
	// cancel is returned with every heartbeat.
	cancel []uuid.UUID
}

func (f *fakeAPI) Register(_ context.Context, req workerapi.RegisterRequest) (workerapi.RegisterResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.registers = append(f.registers, req)
	if len(f.registers) <= f.registerFailures {
		return workerapi.RegisterResponse{}, &APIError{Status: 503}
	}
	id := uuid.Must(uuid.NewV7())
	f.ids = append(f.ids, id)
	return workerapi.RegisterResponse{ID: id, Name: req.Name}, nil
}

func (f *fakeAPI) Heartbeat(_ context.Context, id uuid.UUID, req workerapi.HeartbeatRequest) (workerapi.HeartbeatResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.heartbeats = append(f.heartbeats, heartbeatCall{id: id, req: req})
	if f.unknownOn[len(f.heartbeats)] {
		return workerapi.HeartbeatResponse{}, ErrUnknownWorker
	}
	return workerapi.HeartbeatResponse{CancelAttempts: f.cancel}, nil
}

// cancelRunner is a JobRunner that records cancellations.
type cancelRunner struct {
	mu        sync.Mutex
	cancelled []uuid.UUID
}

func (*cancelRunner) Run(context.Context, uuid.UUID, workerapi.ClaimResponse) {}

func (c *cancelRunner) Cancel(id uuid.UUID) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cancelled = append(c.cancelled, id)
	return true
}

func (c *cancelRunner) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.cancelled)
}

func (f *fakeAPI) snapshot() (registers int, ids []uuid.UUID, heartbeats []heartbeatCall) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.registers), append([]uuid.UUID(nil), f.ids...), append([]heartbeatCall(nil), f.heartbeats...)
}

// runAgent starts an agent and returns a function that stops it and waits for Run to return.
func runAgent(t *testing.T, api API) (stop func()) {
	t.Helper()
	agent := NewAgent(api, fakeProbe{}, slog.New(slog.DiscardHandler), Options{
		Name:              "test-worker",
		Slots:             3,
		HeartbeatInterval: 5 * time.Millisecond,
		Version:           "v-test",
		MinBackoff:        time.Millisecond,
		MaxBackoff:        4 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- agent.Run(ctx) }()

	return func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run returned %v, want nil", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("Run did not return after cancellation")
		}
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestAgentRegistersAndSendsHeartbeats(t *testing.T) {
	api := &fakeAPI{}
	stop := runAgent(t, api)
	waitFor(t, "3 heartbeats", func() bool {
		_, _, hb := api.snapshot()
		return len(hb) >= 3
	})
	stop()

	registers, ids, heartbeats := api.snapshot()
	if registers != 1 {
		t.Fatalf("registers = %d, want 1", registers)
	}
	req := api.registers[0]
	if req.Name != "test-worker" || req.Slots != 3 || req.CPUMillis != 4000 || req.MemoryMB != 8192 {
		t.Errorf("register request = %+v", req)
	}
	if req.Metadata.OS != runtime.GOOS || req.Metadata.Arch != runtime.GOARCH || req.Metadata.Version != "v-test" {
		t.Errorf("metadata = %+v", req.Metadata)
	}
	for i, hb := range heartbeats {
		if hb.id != ids[0] {
			t.Errorf("heartbeat %d id = %s, want %s", i, hb.id, ids[0])
		}
		if hb.req.CPUUsedPercent != 25 || hb.req.MemoryUsedMB != 1024 {
			t.Errorf("heartbeat %d usage = %+v", i, hb.req)
		}
	}
}

func TestAgentRetriesRegistration(t *testing.T) {
	api := &fakeAPI{registerFailures: 3}
	stop := runAgent(t, api)
	waitFor(t, "first heartbeat", func() bool {
		_, _, hb := api.snapshot()
		return len(hb) >= 1
	})
	stop()

	if registers, ids, _ := api.snapshot(); registers != 4 || len(ids) != 1 {
		t.Errorf("registers = %d (ids %d), want 4 attempts and 1 success", registers, len(ids))
	}
}

func TestAgentRegistersAgainWhenUnknown(t *testing.T) {
	api := &fakeAPI{unknownOn: map[int]bool{2: true}}
	stop := runAgent(t, api)
	waitFor(t, "heartbeats after re-registration", func() bool {
		_, _, hb := api.snapshot()
		return len(hb) >= 4
	})
	stop()

	_, ids, heartbeats := api.snapshot()
	if len(ids) != 2 {
		t.Fatalf("registrations = %d, want 2", len(ids))
	}
	if heartbeats[0].id != ids[0] || heartbeats[1].id != ids[0] {
		t.Errorf("heartbeats before 404 used %s/%s, want %s", heartbeats[0].id, heartbeats[1].id, ids[0])
	}
	if heartbeats[2].id != ids[1] {
		t.Errorf("heartbeat after re-registration used %s, want %s", heartbeats[2].id, ids[1])
	}
}

func TestAgentStopsWhileRegistering(t *testing.T) {
	api := &fakeAPI{registerFailures: 1 << 30}
	stop := runAgent(t, api)
	waitFor(t, "a few registration attempts", func() bool {
		n, _, _ := api.snapshot()
		return n >= 3
	})
	stop()
}

func TestAgentPassesCancellationsToRunner(t *testing.T) {
	attempt := uuid.New()
	api := &fakeAPI{cancel: []uuid.UUID{attempt}}
	runner := &cancelRunner{}
	agent := NewAgent(api, fakeProbe{}, slog.New(slog.DiscardHandler), Options{
		Name: "test-worker", Slots: 1, HeartbeatInterval: 5 * time.Millisecond, Runner: runner,
	})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- agent.Run(ctx) }()

	waitFor(t, "two cancellations", func() bool { return runner.count() >= 2 })
	cancel()
	<-done
	runner.mu.Lock()
	defer runner.mu.Unlock()
	for _, id := range runner.cancelled {
		if id != attempt {
			t.Errorf("cancelled %s, want %s", id, attempt)
		}
	}
}

func TestNewAgentDefaultsBackoff(t *testing.T) {
	a := NewAgent(&fakeAPI{}, fakeProbe{}, slog.New(slog.DiscardHandler), Options{HeartbeatInterval: time.Second})
	if a.opts.MinBackoff != defaultMinBackoff || a.opts.MaxBackoff != defaultMaxBackoff {
		t.Errorf("backoff = %v..%v, want %v..%v", a.opts.MinBackoff, a.opts.MaxBackoff, defaultMinBackoff, defaultMaxBackoff)
	}
}

func TestAPIErrorMessage(t *testing.T) {
	err := error(&APIError{Status: 401, Detail: "bad token"})
	if got, want := err.Error(), "api returned 401 Unauthorized: bad token"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	if errors.Is(err, ErrUnknownWorker) {
		t.Error("401 must not be treated as an unknown worker")
	}
}
