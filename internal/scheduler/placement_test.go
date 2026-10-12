package scheduler

import (
	"testing"

	"github.com/google/uuid"
)

func jobReqs(n int, cpu, mem int) []JobRequest {
	out := make([]JobRequest, n)
	for i := range out {
		out[i] = JobRequest{ID: uuid.New(), CPUMillis: cpu, MemoryMB: mem}
	}
	return out
}

func TestPlaceSpreadsByFewestActive(t *testing.T) {
	a := Candidate{ID: uuid.New(), Name: "a", Slots: 4, Active: 2, CPUMillis: 8000, MemoryMB: 16384}
	b := Candidate{ID: uuid.New(), Name: "b", Slots: 4, Active: 0, CPUMillis: 8000, MemoryMB: 16384}
	jobs := jobReqs(2, 1000, 1024)

	got := Place(jobs, []Candidate{a, b})
	if len(got) != 2 {
		t.Fatalf("placed %d, want 2", len(got))
	}
	if got[0].Worker.ID != b.ID || got[1].Worker.ID != b.ID {
		t.Errorf("want both on the emptier worker b; got %+v", got)
	}
}

func TestPlaceTiesGoToEarlierWorker(t *testing.T) {
	first := Candidate{ID: uuid.New(), Name: "first", Slots: 1, CPUMillis: 4000, MemoryMB: 8192}
	second := Candidate{ID: uuid.New(), Name: "second", Slots: 1, CPUMillis: 4000, MemoryMB: 8192}
	got := Place(jobReqs(1, 1000, 1024), []Candidate{first, second})
	if len(got) != 1 || got[0].Worker.ID != first.ID {
		t.Errorf("got %+v, want first worker", got)
	}
}

func TestPlaceStopsWhenSlotsRunOut(t *testing.T) {
	workers := []Candidate{
		{ID: uuid.New(), Name: "full", Slots: 2, Active: 2, CPUMillis: 8000, MemoryMB: 16384},
		{ID: uuid.New(), Name: "over", Slots: 1, Active: 3, CPUMillis: 8000, MemoryMB: 16384},
		{ID: uuid.New(), Name: "one-free", Slots: 2, Active: 1, CPUMillis: 8000, MemoryMB: 16384},
	}
	jobs := jobReqs(3, 1000, 1024)
	got := Place(jobs, workers)
	if len(got) != 1 || got[0].Worker.Name != "one-free" {
		t.Errorf("got %+v, want one job on one-free", got)
	}
}

func TestPlaceSkipsJobThatFitsNowhere(t *testing.T) {
	w := Candidate{ID: uuid.New(), Name: "small", Slots: 2, CPUMillis: 2000, MemoryMB: 4096}
	huge := JobRequest{ID: uuid.New(), CPUMillis: 4000, MemoryMB: 1024}
	small := JobRequest{ID: uuid.New(), CPUMillis: 1000, MemoryMB: 1024}
	got := Place([]JobRequest{huge, small}, []Candidate{w})
	if len(got) != 1 || got[0].JobID != small.ID {
		t.Fatalf("got %+v, want only the small job placed", got)
	}
}

func TestPlaceRespectsCPUAndMemory(t *testing.T) {
	a := Candidate{ID: uuid.New(), Name: "a", Slots: 4, CPUMillis: 2000, MemoryMB: 16384}
	b := Candidate{ID: uuid.New(), Name: "b", Slots: 4, CPUMillis: 8000, MemoryMB: 4096}
	job := JobRequest{ID: uuid.New(), CPUMillis: 4000, MemoryMB: 8192}
	got := Place([]JobRequest{job}, []Candidate{a, b})
	if len(got) != 0 {
		t.Fatalf("placed on %+v, want none: a lacks CPU, b lacks memory", got)
	}
	b.MemoryMB = 16384
	got = Place([]JobRequest{job}, []Candidate{a, b})
	if len(got) != 1 || got[0].Worker.ID != b.ID {
		t.Errorf("got %+v, want worker b", got)
	}
}

func TestPlaceReservesResourcesAcrossJobs(t *testing.T) {
	w := Candidate{ID: uuid.New(), Name: "w", Slots: 4, CPUMillis: 3000, MemoryMB: 8192}
	jobs := []JobRequest{
		{ID: uuid.New(), CPUMillis: 2000, MemoryMB: 1024},
		{ID: uuid.New(), CPUMillis: 2000, MemoryMB: 1024},
		{ID: uuid.New(), CPUMillis: 1000, MemoryMB: 1024},
	}
	got := Place(jobs, []Candidate{w})
	if len(got) != 2 {
		t.Fatalf("placed %d, want 2 (third fits after second is skipped)", len(got))
	}
	if got[0].JobID != jobs[0].ID || got[1].JobID != jobs[2].ID {
		t.Errorf("got job order %+v, want first then third", got)
	}
}

func TestPlaceNothing(t *testing.T) {
	if got := Place(jobReqs(3, 1000, 1024), nil); len(got) != 0 {
		t.Errorf("got %d", len(got))
	}
	if got := Place(nil, []Candidate{{ID: uuid.New(), Slots: 2, CPUMillis: 4000, MemoryMB: 8192}}); len(got) != 0 {
		t.Errorf("got %d", len(got))
	}
}

func TestPlaceDoesNotModifyInput(t *testing.T) {
	workers := []Candidate{{ID: uuid.New(), Slots: 3, Active: 1, CPUMillis: 8000, MemoryMB: 16384, ReservedCPU: 1000, ReservedMem: 512}}
	Place(jobReqs(2, 1000, 1024), workers)
	if workers[0].Active != 1 || workers[0].ReservedCPU != 1000 || workers[0].ReservedMem != 512 {
		t.Errorf("workers mutated: %+v", workers[0])
	}
}

func TestCandidateFreeAndFits(t *testing.T) {
	c := Candidate{Slots: 2, Active: 1, CPUMillis: 4000, MemoryMB: 8192, ReservedCPU: 1000, ReservedMem: 2048}
	if c.Free() != 1 || c.RemainingCPU() != 3000 || c.RemainingMem() != 6144 {
		t.Errorf("remaining = free %d cpu %d mem %d", c.Free(), c.RemainingCPU(), c.RemainingMem())
	}
	if !c.Fits(3000, 6144) || c.Fits(3001, 1) || c.Fits(1, 6145) {
		t.Error("Fits mismatch")
	}
	full := Candidate{Slots: 1, Active: 1, CPUMillis: 4000, MemoryMB: 8192}
	if full.Fits(1, 1) {
		t.Error("full worker must not fit")
	}
}
