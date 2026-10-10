package scheduler

import "sync"

// Notifier wakes everyone waiting for new assignments. It only reaches
// waiters in the same process, so claim handlers also re-check on a timer.
type Notifier struct {
	mu sync.Mutex
	ch chan struct{}
}

// NewNotifier returns a Notifier with no pending broadcast.
func NewNotifier() *Notifier {
	return &Notifier{ch: make(chan struct{})}
}

// Wait returns a channel that is closed by the next Broadcast. Call it before
// checking for work, so a broadcast that lands between the check and the
// wait is not missed.
func (n *Notifier) Wait() <-chan struct{} {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.ch
}

// Broadcast wakes every current waiter.
func (n *Notifier) Broadcast() {
	n.mu.Lock()
	defer n.mu.Unlock()
	close(n.ch)
	n.ch = make(chan struct{})
}
