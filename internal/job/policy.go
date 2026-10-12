package job

import "time"

// FailureAction is what the platform does after a classified attempt failure.
type FailureAction int

const (
	// ActionFail ends the job as FAILED.
	ActionFail FailureAction = iota
	// ActionRetry puts the job in RETRYING with backoff when attempts remain.
	ActionRetry
	// ActionBlock ends the job as BLOCKED (needs a human or a new submission).
	ActionBlock
)

// DefaultMaxAttempts is how many attempts a job may make before a retryable
// failure becomes terminal.
const DefaultMaxAttempts = 3

// DefaultRetryBackoffBase is the wait after the first failed attempt.
const DefaultRetryBackoffBase = time.Second

// DefaultRetryBackoffCap caps exponential backoff between retries.
const DefaultRetryBackoffCap = 5 * time.Minute

// ActionFor returns the policy action for a failure category.
func ActionFor(category string) FailureAction {
	switch category {
	case CategoryInfrastructure, CategoryNetwork, CategoryTimeout, CategoryResource, CategoryEnvironment:
		return ActionRetry
	case CategoryRequirement:
		return ActionBlock
	default:
		// TEST, CODE, SECURITY, DEPENDENCY, EXECUTOR, UNKNOWN, and anything else.
		return ActionFail
	}
}

// RetryBackoff returns how long to wait after attemptNumber failed (1-based).
func RetryBackoff(attemptNumber int, base, backoffCap time.Duration) time.Duration {
	if attemptNumber < 1 {
		attemptNumber = 1
	}
	if base <= 0 {
		base = DefaultRetryBackoffBase
	}
	if backoffCap <= 0 {
		backoffCap = DefaultRetryBackoffCap
	}
	d := base
	for i := 1; i < attemptNumber; i++ {
		if d >= backoffCap/2 {
			return backoffCap
		}
		d *= 2
	}
	if d > backoffCap {
		return backoffCap
	}
	return d
}
