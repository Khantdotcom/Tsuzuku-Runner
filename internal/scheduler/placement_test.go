package scheduler

import (
	"testing"

	"github.com/google/uuid"
)

func jobIDs(n int) []uuid.UUID {
	ids := make([]uuid.UUID, n)
	for i := range ids {
		ids[i] = uuid.New()
	}
	return ids
}

func TestPlaceSpreadsByFewestActive(t *testing.T) {
	a := Candidate{ID: uuid.New(), Name: "a", Slots: 4, Active: 2}
	b := Candidate{ID: uuid.New(), Name: "b", Slots: 4, Active: 0}
	jobs := jobIDs(4)

	got := Place(jobs, []Candidate{a, b})

	wantWorkers := []string{"b", "b", "a", "b"}
	wantActive := []int{0, 1, 2, 2}
	if len(got) != len(wantWorkers) {
		t.Fatalf("placed %d jobs, want %d", len(got), len(wantWorkers))
	}
	for i, as := range got {
		if as.JobID != jobs[i] {
			t.Errorf("assignment %d is job %s, want %s (jobs keep their order)", i, as.JobID, jobs[i])
		}
		if as.Worker.Name != wantWorkers[i] || as.Worker.Active != wantActive[i] {
			t.Errorf("assignment %d = %s with %d active, want %s with %d", i, as.Worker.Name, as.Worker.Active, wantWorkers[i], wantActive[i])
		}
	}
}

func TestPlaceTiesGoToEarlierWorker(t *testing.T) {
	first := Candidate{ID: uuid.New(), Name: "first", Slots: 1}
	second := Candidate{ID: uuid.New(), Name: "second", Slots: 1}
	got := Place(jobIDs(2), []Candidate{first, second})
	if len(got) != 2 || got[0].Worker.Name != "first" || got[1].Worker.Name != "second" {
		t.Errorf("got %+v, want first then second", got)
	}
}

func TestPlaceStopsWhenSlotsRunOut(t *testing.T) {
	workers := []Candidate{
		{ID: uuid.New(), Name: "full", Slots: 2, Active: 2},
		{ID: uuid.New(), Name: "over", Slots: 1, Active: 3},
		{ID: uuid.New(), Name: "one-free", Slots: 2, Active: 1},
	}
	jobs := jobIDs(3)
	got := Place(jobs, workers)
	if len(got) != 1 || got[0].Worker.Name != "one-free" || got[0].JobID != jobs[0] {
		t.Errorf("got %+v, want only the oldest job on one-free", got)
	}
}

func TestPlaceNothing(t *testing.T) {
	if got := Place(jobIDs(3), nil); len(got) != 0 {
		t.Errorf("no workers: got %+v", got)
	}
	if got := Place(nil, []Candidate{{ID: uuid.New(), Slots: 2}}); len(got) != 0 {
		t.Errorf("no jobs: got %+v", got)
	}
}

func TestPlaceDoesNotModifyInput(t *testing.T) {
	workers := []Candidate{{ID: uuid.New(), Slots: 3, Active: 1}}
	Place(jobIDs(2), workers)
	if workers[0].Active != 1 {
		t.Errorf("Active = %d after Place, want 1", workers[0].Active)
	}
}

func TestCandidateFree(t *testing.T) {
	for _, tt := range []struct{ slots, active, want int }{{2, 0, 2}, {2, 2, 0}, {2, 5, 0}} {
		if got := (Candidate{Slots: tt.slots, Active: tt.active}).Free(); got != tt.want {
			t.Errorf("Free(%d slots, %d active) = %d, want %d", tt.slots, tt.active, got, tt.want)
		}
	}
}
