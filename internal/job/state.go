// Package job owns the job lifecycle: submitting workloads as jobs and moving
// jobs between states. All state changes go through Transition.
package job

import "slices"

// State is a job's lifecycle state. The values match the CHECK constraint on
// jobs.state.
type State string

const (
	Queued    State = "QUEUED"
	Scheduled State = "SCHEDULED"
	Preparing State = "PREPARING"
	Executing State = "EXECUTING"
	Verifying State = "VERIFYING"
	Completed State = "COMPLETED"
	Failed    State = "FAILED"
	Retrying  State = "RETRYING"
	Repairing State = "REPAIRING"
	Blocked   State = "BLOCKED"
	Cancelled State = "CANCELLED"
)

// States lists every state in lifecycle order.
var States = []State{
	Queued, Scheduled, Preparing, Executing, Verifying,
	Completed, Failed, Retrying, Repairing, Blocked, Cancelled,
}

// transitions is the complete set of legal state changes. REPAIRING has none
// yet: the milestone that uses it will add edges here.
var transitions = map[State][]State{
	Queued:    {Scheduled, Cancelled},
	Scheduled: {Preparing, Queued, Cancelled},
	Preparing: {Executing, Failed, Cancelled, Queued, Retrying, Blocked},
	Executing: {Verifying, Failed, Cancelled, Queued, Retrying, Blocked},
	Verifying: {Completed, Failed, Cancelled, Queued, Retrying, Blocked},
	Retrying:  {Queued, Failed, Cancelled},
}

// Valid reports whether s is a known state.
func (s State) Valid() bool {
	return slices.Contains(States, s)
}

// Terminal reports whether s is a final state that a job never leaves.
func (s State) Terminal() bool {
	return s == Completed || s == Failed || s == Cancelled || s == Blocked
}

// CanTransition reports whether a job may move from one state to another.
func CanTransition(from, to State) bool {
	return slices.Contains(transitions[from], to)
}
