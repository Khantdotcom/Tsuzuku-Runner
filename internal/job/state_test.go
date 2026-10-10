package job

import "testing"

func TestTransitionTable(t *testing.T) {
	allowed := map[[2]State]bool{
		{Queued, Scheduled}:    true,
		{Queued, Cancelled}:    true,
		{Scheduled, Preparing}: true,
		{Scheduled, Cancelled}: true,
		{Preparing, Executing}: true,
		{Preparing, Failed}:    true,
		{Preparing, Cancelled}: true,
		{Executing, Verifying}: true,
		{Executing, Failed}:    true,
		{Executing, Cancelled}: true,
		{Verifying, Completed}: true,
		{Verifying, Failed}:    true,
		{Verifying, Cancelled}: true,
	}
	for _, from := range States {
		for _, to := range States {
			if got, want := CanTransition(from, to), allowed[[2]State{from, to}]; got != want {
				t.Errorf("CanTransition(%s, %s) = %v, want %v", from, to, got, want)
			}
		}
	}
}

func TestTerminalStatesHaveNoTransitions(t *testing.T) {
	for _, s := range States {
		if !s.Terminal() {
			continue
		}
		for _, to := range States {
			if CanTransition(s, to) {
				t.Errorf("terminal state %s can move to %s", s, to)
			}
		}
	}
}

func TestEveryTransitionUsesKnownStates(t *testing.T) {
	for from, targets := range transitions {
		if !from.Valid() {
			t.Errorf("unknown source state %q", from)
		}
		for _, to := range targets {
			if !to.Valid() {
				t.Errorf("unknown target state %q from %s", to, from)
			}
		}
	}
}

func TestStateHelpers(t *testing.T) {
	if len(States) != 11 {
		t.Fatalf("States has %d entries, want 11 (one per jobs.state CHECK value)", len(States))
	}
	if State("DONE").Valid() || State("queued").Valid() {
		t.Error("unknown or lower-case states must not be valid")
	}
	terminal := map[State]bool{Completed: true, Failed: true, Cancelled: true}
	for _, s := range States {
		if s.Terminal() != terminal[s] {
			t.Errorf("%s.Terminal() = %v", s, s.Terminal())
		}
	}
	if CanTransition(Queued, Queued) {
		t.Error("a state must not transition to itself")
	}
}
