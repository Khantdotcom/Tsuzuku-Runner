// Package workerapi defines the JSON messages exchanged between workers and
// the API server. Both sides import it so the protocol has one definition.
package workerapi

import "github.com/google/uuid"

// RegisterPath is the endpoint a worker calls on startup.
const RegisterPath = "/api/v1/workers/register"

// HeartbeatPath returns the heartbeat endpoint for a registered worker.
func HeartbeatPath(id uuid.UUID) string {
	return "/api/v1/workers/" + id.String() + "/heartbeat"
}

// RegisterRequest announces a worker and its capacity. Registering again with
// the same name updates the existing worker and keeps its ID.
type RegisterRequest struct {
	Name      string   `json:"name"`
	Slots     int      `json:"slots"`
	CPUMillis int      `json:"cpu_millis"`
	MemoryMB  int      `json:"memory_mb"`
	Metadata  Metadata `json:"metadata"`
}

// Metadata describes the host a worker runs on.
type Metadata struct {
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	Hostname string `json:"hostname"`
	Version  string `json:"version"`
}

// RegisterResponse carries the ID the worker uses for later calls.
type RegisterResponse struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

// HeartbeatRequest reports current host usage.
type HeartbeatRequest struct {
	CPUUsedPercent float64 `json:"cpu_used_percent"`
	MemoryUsedMB   int     `json:"memory_used_mb"`
}
