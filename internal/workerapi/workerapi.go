// Package workerapi defines the JSON messages exchanged between workers and
// the API server. Both sides import it so the protocol has one definition.
package workerapi

import (
	"time"

	"github.com/google/uuid"

	"github.com/Khantdotcom/tsuzuku-runner/internal/workload"
)

// RegisterPath is the endpoint a worker calls on startup.
const RegisterPath = "/api/v1/workers/register"

// MaxClaimWait is the longest a claim request may wait for a job.
const MaxClaimWait = 30 * time.Second

// HeartbeatPath returns the heartbeat endpoint for a registered worker.
func HeartbeatPath(id uuid.UUID) string {
	return "/api/v1/workers/" + id.String() + "/heartbeat"
}

// ClaimPath returns the endpoint a worker calls to start its next assigned
// job. The optional wait query parameter (a duration such as "25s", at most
// MaxClaimWait) holds the request open until a job is assigned. The server
// answers 200 with a ClaimResponse, or 204 when no job arrived in time.
func ClaimPath(id uuid.UUID) string {
	return "/api/v1/workers/" + id.String() + "/claim"
}

// ClaimResponse is a job the worker has started, as attempt AttemptNumber.
type ClaimResponse struct {
	JobID         uuid.UUID     `json:"job_id"`
	JobNumber     int64         `json:"job_number"`
	AttemptID     uuid.UUID     `json:"attempt_id"`
	AttemptNumber int           `json:"attempt_number"`
	Spec          workload.Spec `json:"spec"`
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
