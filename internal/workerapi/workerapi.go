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

// HeartbeatPath returns the heartbeat endpoint for a registered worker. The
// server answers 200 with a HeartbeatResponse.
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

// Attempt report actions. A worker reports each phase of an attempt it
// claimed; the server rejects reports for attempts the worker does not own
// or that have already finished.
const (
	// ActionExecuting reports that the checkout succeeded and the command is
	// starting. Body: ExecutingRequest. Response: 204.
	ActionExecuting = "executing"
	// ActionVerifying reports the command's result. Body: VerifyingRequest.
	// Response: 200 with VerifyingResponse.
	ActionVerifying = "verifying"
	// ActionFinish ends the attempt. Body: FinishRequest. Response: 204.
	ActionFinish = "finish"
	// ActionLogs uploads a batch of command output. Body: LogsRequest.
	// Response: 200 with LogsResponse.
	ActionLogs = "logs"
)

// Output streams of a step.
const (
	StreamStdout = "stdout"
	StreamStderr = "stderr"
)

// Log batch limits. A worker sends a batch at least every second, or sooner
// once it holds MaxLogBatchBytes of output.
const (
	MaxLogBatchBytes  = 64 << 10
	MaxLogBatchChunks = 256
)

// LogChunk is a piece of one stream's output. Seq numbers increase across
// both streams of an attempt, so a re-sent chunk is recognised and ignored.
type LogChunk struct {
	Seq    int    `json:"seq"`
	Stream string `json:"stream"`
	Data   []byte `json:"data"`
}

// LogsRequest uploads output in the order it was produced.
type LogsRequest struct {
	Chunks []LogChunk `json:"chunks"`
}

// LogsResponse reports whether the attempt has reached its output limit.
// Once Truncated is true the server drops further output, so the worker may
// stop sending it.
type LogsResponse struct {
	Truncated bool `json:"truncated"`
}

// Stages a worker reports with FinishRequest.Error, naming what failed.
const (
	StageWorkspace = "workspace"
	StageImage     = "image"
	StageCheckout  = "checkout"
	StageExecute   = "execute"
	StageVerify    = "verify"
	StageShutdown  = "shutdown"
)

// AttemptPath returns the endpoint for reporting action on an attempt.
func AttemptPath(workerID, attemptID uuid.UUID, action string) string {
	return "/api/v1/workers/" + workerID.String() + "/attempts/" + attemptID.String() + "/" + action
}

// Runtime roles: the container that checked out the repository, the one that
// ran the command, and the one that ran verification.
const (
	RolePrepare = "prepare"
	RoleExecute = "execute"
	RoleVerify  = "verify"
)

// Runtime describes one container an attempt used. Times come from the
// worker's clock, so only their difference is meaningful.
type Runtime struct {
	Role        string    `json:"role"`
	Image       string    `json:"image"`
	ContainerID string    `json:"container_id,omitempty"`
	VolumeName  string    `json:"volume_name,omitempty"`
	CPUMillis   int       `json:"cpu_millis"`
	MemoryMB    int       `json:"memory_mb"`
	Network     bool      `json:"network"`
	StartedAt   time.Time `json:"started_at"`
	FinishedAt  time.Time `json:"finished_at"`
}

// StepResult is how a command run in a container ended.
type StepResult struct {
	ExitCode   int   `json:"exit_code"`
	TimedOut   bool  `json:"timed_out"`
	DurationMS int64 `json:"duration_ms"`
}

// ExecutingRequest reports a successful checkout.
type ExecutingRequest struct {
	// Commit is the full commit hash the revision resolved to.
	Commit  string  `json:"commit"`
	Runtime Runtime `json:"runtime"`
}

// VerifyingRequest reports how the workload command ended.
type VerifyingRequest struct {
	Execution StepResult `json:"execution"`
	Runtime   Runtime    `json:"runtime"`
}

// VerifyingResponse tells the worker whether to run verification. It is
// false when the server already ended the job, for example after a timeout.
type VerifyingResponse struct {
	Verify bool `json:"verify"`
}

// FinishRequest ends an attempt. With neither Error nor Cancelled set, the
// server decides the job's outcome from the results reported so far.
type FinishRequest struct {
	// Error describes a failure outside the workload, such as a checkout or
	// container error, that stopped the attempt early.
	Error string `json:"error,omitempty"`
	// Stage names the step that failed when Error is set (Stage* constants).
	Stage string `json:"stage,omitempty"`
	// Cancelled reports that the attempt stopped because it was cancelled.
	Cancelled bool     `json:"cancelled,omitempty"`
	Runtime   *Runtime `json:"runtime,omitempty"`
	// Verification is the result of the workload's verification command. It
	// is absent when the workload has none or the command already failed.
	Verification *VerificationResult `json:"verification,omitempty"`
}

// VerificationResult is how the verification command ended.
type VerificationResult struct {
	Command string     `json:"command"`
	Result  StepResult `json:"result"`
	Runtime Runtime    `json:"runtime"`
	// OutputTail is the end of the command's combined output, for display.
	OutputTail string `json:"output_tail"`
}

// HeartbeatResponse lists the worker's running attempts that were asked to
// stop. The worker kills them and reports them with FinishRequest.Cancelled.
type HeartbeatResponse struct {
	CancelAttempts []uuid.UUID `json:"cancel_attempts"`
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
