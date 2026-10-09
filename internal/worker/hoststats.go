package worker

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/mem"
)

const mib = 1 << 20

// Capacity is the host's total resources.
type Capacity struct {
	CPUMillis int
	MemoryMB  int
}

// Usage is a point-in-time sample of host utilisation.
type Usage struct {
	CPUPercent   float64
	MemoryUsedMB int
}

// Probe samples the host the worker runs on.
type Probe interface {
	Capacity(ctx context.Context) (Capacity, error)
	Usage(ctx context.Context) (Usage, error)
}

// HostProbe reads host statistics from the operating system.
type HostProbe struct{}

// Capacity reports logical CPUs (as millicores) and total memory.
func (HostProbe) Capacity(ctx context.Context) (Capacity, error) {
	cores, err := cpu.CountsWithContext(ctx, true)
	if err != nil {
		return Capacity{}, fmt.Errorf("count cpus: %w", err)
	}
	vm, err := mem.VirtualMemoryWithContext(ctx)
	if err != nil {
		return Capacity{}, fmt.Errorf("read memory: %w", err)
	}
	return Capacity{CPUMillis: cores * 1000, MemoryMB: toMB(vm.Total)}, nil
}

// Usage reports CPU utilisation since the previous call and used memory.
func (HostProbe) Usage(ctx context.Context) (Usage, error) {
	pct, err := cpu.PercentWithContext(ctx, 0, false)
	if err != nil {
		return Usage{}, fmt.Errorf("read cpu usage: %w", err)
	}
	if len(pct) == 0 {
		return Usage{}, errors.New("read cpu usage: no samples")
	}
	vm, err := mem.VirtualMemoryWithContext(ctx)
	if err != nil {
		return Usage{}, fmt.Errorf("read memory: %w", err)
	}
	return Usage{CPUPercent: min(max(pct[0], 0), 100), MemoryUsedMB: toMB(vm.Used)}, nil
}

func toMB(bytes uint64) int {
	return int(min(bytes/mib, math.MaxInt32)) //nolint:gosec // clamped to int32 range
}
