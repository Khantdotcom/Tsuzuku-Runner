package worker

import "testing"

func TestHostProbe(t *testing.T) {
	var p HostProbe

	c, err := p.Capacity(t.Context())
	if err != nil {
		t.Fatalf("Capacity: %v", err)
	}
	if c.CPUMillis < 1000 || c.MemoryMB < 1 {
		t.Errorf("capacity = %+v, want at least one CPU and some memory", c)
	}

	u, err := p.Usage(t.Context())
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if u.CPUPercent < 0 || u.CPUPercent > 100 {
		t.Errorf("cpu percent = %v, want 0..100", u.CPUPercent)
	}
	if u.MemoryUsedMB < 0 || u.MemoryUsedMB > c.MemoryMB {
		t.Errorf("memory used = %d MB, want 0..%d", u.MemoryUsedMB, c.MemoryMB)
	}
}
