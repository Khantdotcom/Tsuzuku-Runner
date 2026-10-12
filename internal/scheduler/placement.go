package scheduler

import "github.com/google/uuid"

// Candidate is an online worker the scheduler may place jobs on.
type Candidate struct {
	ID     uuid.UUID
	Name   string
	Slots  int
	Active int
	// CPUMillis and MemoryMB are the worker's total capacity.
	CPUMillis int
	MemoryMB  int
	// ReservedCPU and ReservedMem are already spoken for by unfinished jobs.
	ReservedCPU int
	ReservedMem int
}

// Free reports how many more jobs the worker can take by slot count.
func (c Candidate) Free() int { return max(c.Slots-c.Active, 0) }

// RemainingCPU is capacity left after reserved work.
func (c Candidate) RemainingCPU() int { return max(c.CPUMillis-c.ReservedCPU, 0) }

// RemainingMem is memory left after reserved work.
func (c Candidate) RemainingMem() int { return max(c.MemoryMB-c.ReservedMem, 0) }

// Fits reports whether the worker can take a job with the given resources.
func (c Candidate) Fits(cpuMillis, memoryMB int) bool {
	return c.Free() > 0 && c.RemainingCPU() >= cpuMillis && c.RemainingMem() >= memoryMB
}

// JobRequest is a placeable job and what it asks for.
type JobRequest struct {
	ID        uuid.UUID
	CPUMillis int
	MemoryMB  int
}

// Assignment places one job on a worker. Worker is the worker as it was when
// the decision was made, before this job was counted.
type Assignment struct {
	JobID  uuid.UUID
	Worker Candidate
}

// Place assigns jobs, in order, to the worker with the fewest active jobs that
// still has a free slot and enough remaining CPU and memory. Ties go to the
// earlier worker in workers. Jobs that fit no worker are skipped so a large
// request does not block smaller ones behind it.
func Place(jobs []JobRequest, workers []Candidate) []Assignment {
	pool := make([]Candidate, len(workers))
	copy(pool, workers)

	var out []Assignment
	for _, job := range jobs {
		best := -1
		for i, w := range pool {
			if !w.Fits(job.CPUMillis, job.MemoryMB) {
				continue
			}
			if best < 0 || w.Active < pool[best].Active {
				best = i
			}
		}
		if best < 0 {
			continue
		}
		out = append(out, Assignment{JobID: job.ID, Worker: pool[best]})
		pool[best].Active++
		pool[best].ReservedCPU += job.CPUMillis
		pool[best].ReservedMem += job.MemoryMB
	}
	return out
}
