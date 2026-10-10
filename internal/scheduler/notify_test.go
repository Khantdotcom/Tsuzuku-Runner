package scheduler

import (
	"testing"
	"time"
)

func closed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func TestNotifierWakesEveryWaiter(t *testing.T) {
	n := NewNotifier()
	a, b := n.Wait(), n.Wait()
	if closed(a) || closed(b) {
		t.Fatal("channels closed before Broadcast")
	}
	n.Broadcast()
	if !closed(a) || !closed(b) {
		t.Fatal("Broadcast did not wake every waiter")
	}
}

func TestNotifierBroadcastBeforeWaitIsSeen(t *testing.T) {
	n := NewNotifier()
	ch := n.Wait()
	n.Broadcast()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("a broadcast between Wait and select was lost")
	}
}

func TestNotifierRearms(t *testing.T) {
	n := NewNotifier()
	n.Broadcast()
	if closed(n.Wait()) {
		t.Fatal("a channel taken after Broadcast is already closed")
	}
	n.Broadcast()
	n.Broadcast()
}
