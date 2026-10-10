package scheduler

import "github.com/google/uuid"

// Candidate is an online worker the scheduler may place jobs on.
type Candidate struct {
	ID     uuid.UUID
	Name   string
	Slots  int
	Active int
}

// Free reports how many more jobs the worker can take.
func (c Candidate) Free() int { return max(c.Slots-c.Active, 0) }

// Assignment places one job on a worker. Worker is the worker as it was when
// the decision was made, before this job was counted.
type Assignment struct {
	JobID  uuid.UUID
	Worker Candidate
}

// Place assigns jobs, in order, to the worker with the fewest active jobs
// that still has a free slot. Ties go to the earlier worker in workers. Jobs
// left over once every slot is taken are not assigned.
func Place(jobs []uuid.UUID, workers []Candidate) []Assignment {
	pool := make([]Candidate, len(workers))
	copy(pool, workers)

	var out []Assignment
	for _, job := range jobs {
		best := -1
		for i, w := range pool {
			if w.Free() > 0 && (best < 0 || w.Active < pool[best].Active) {
				best = i
			}
		}
		if best < 0 {
			break
		}
		out = append(out, Assignment{JobID: job, Worker: pool[best]})
		pool[best].Active++
	}
	return out
}
