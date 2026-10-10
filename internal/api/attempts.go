package api

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/Khantdotcom/tsuzuku-runner/internal/job"
	"github.com/Khantdotcom/tsuzuku-runner/internal/workerapi"
)

var commitPattern = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

// AttemptReporter records what a worker reports about an attempt it claimed.
// *job.Service implements it.
type AttemptReporter interface {
	Executing(ctx context.Context, workerID, attemptID uuid.UUID, req workerapi.ExecutingRequest) error
	Verifying(ctx context.Context, workerID, attemptID uuid.UUID, req workerapi.VerifyingRequest) (bool, error)
	Finish(ctx context.Context, workerID, attemptID uuid.UUID, req workerapi.FinishRequest) error
	AppendLogs(ctx context.Context, workerID, attemptID uuid.UUID, chunks []workerapi.LogChunk) (bool, error)
}

const (
	// maxLogsBodyBytes fits a full log batch after base64 encoding.
	maxLogsBodyBytes = 256 << 10
	maxOutputTail    = 16 << 10
	maxCommandLen    = 4096
)

var validStages = map[string]bool{
	workerapi.StageWorkspace: true, workerapi.StageImage: true, workerapi.StageCheckout: true,
	workerapi.StageExecute: true, workerapi.StageVerify: true, workerapi.StageShutdown: true,
}

func (s *server) handleAttemptLogs(w http.ResponseWriter, r *http.Request) {
	workerID, attemptID, ok := attemptParams(w, r)
	if !ok {
		return
	}
	var req workerapi.LogsRequest
	if !decodeJSONLimit(w, r, &req, true, maxLogsBodyBytes) {
		return
	}
	if err := validateLogs(req); err != nil {
		writeProblem(w, r, http.StatusUnprocessableEntity, err.Error())
		return
	}
	truncated, err := s.attempts.AppendLogs(r.Context(), workerID, attemptID, req.Chunks)
	if err != nil {
		s.attemptError(w, r, "store logs", err)
		return
	}
	writeJSON(w, http.StatusOK, workerapi.LogsResponse{Truncated: truncated})
}

func validateLogs(req workerapi.LogsRequest) error {
	if len(req.Chunks) == 0 || len(req.Chunks) > workerapi.MaxLogBatchChunks {
		return fmt.Errorf("chunks must hold 1-%d chunks", workerapi.MaxLogBatchChunks)
	}
	total := 0
	for i, c := range req.Chunks {
		switch {
		case c.Seq < 0 || c.Seq > math.MaxInt32:
			return fmt.Errorf("chunks[%d].seq must be a non-negative 32-bit number", i)
		case c.Stream != workerapi.StreamStdout && c.Stream != workerapi.StreamStderr:
			return fmt.Errorf("chunks[%d].stream must be stdout or stderr", i)
		}
		total += len(c.Data)
	}
	if total > workerapi.MaxLogBatchBytes {
		return fmt.Errorf("a batch may carry at most %d bytes of output", workerapi.MaxLogBatchBytes)
	}
	return nil
}

func (s *server) handleAttemptExecuting(w http.ResponseWriter, r *http.Request) {
	workerID, attemptID, ok := attemptParams(w, r)
	if !ok {
		return
	}
	var req workerapi.ExecutingRequest
	if !decodeJSON(w, r, &req, true) {
		return
	}
	if err := validateRuntime(req.Runtime, workerapi.RolePrepare); err != nil {
		writeProblem(w, r, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if !commitPattern.MatchString(req.Commit) {
		writeProblem(w, r, http.StatusUnprocessableEntity, "commit must be a full lowercase commit hash")
		return
	}
	if err := s.attempts.Executing(r.Context(), workerID, attemptID, req); err != nil {
		s.attemptError(w, r, "record executing", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleAttemptVerifying(w http.ResponseWriter, r *http.Request) {
	workerID, attemptID, ok := attemptParams(w, r)
	if !ok {
		return
	}
	var req workerapi.VerifyingRequest
	if !decodeJSON(w, r, &req, true) {
		return
	}
	if err := validateRuntime(req.Runtime, workerapi.RoleExecute); err != nil {
		writeProblem(w, r, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if err := validateStep("execution", req.Execution); err != nil {
		writeProblem(w, r, http.StatusUnprocessableEntity, err.Error())
		return
	}
	verify, err := s.attempts.Verifying(r.Context(), workerID, attemptID, req)
	if err != nil {
		s.attemptError(w, r, "record verifying", err)
		return
	}
	writeJSON(w, http.StatusOK, workerapi.VerifyingResponse{Verify: verify})
}

func (s *server) handleAttemptFinish(w http.ResponseWriter, r *http.Request) {
	workerID, attemptID, ok := attemptParams(w, r)
	if !ok {
		return
	}
	var req workerapi.FinishRequest
	if !decodeJSON(w, r, &req, true) {
		return
	}
	if req.Runtime != nil {
		if err := validateRuntime(*req.Runtime, req.Runtime.Role); err != nil {
			writeProblem(w, r, http.StatusUnprocessableEntity, err.Error())
			return
		}
	}
	if err := validateFinish(req); err != nil {
		writeProblem(w, r, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if err := s.attempts.Finish(r.Context(), workerID, attemptID, req); err != nil {
		s.attemptError(w, r, "finish attempt", err)
		return
	}
	s.logger.InfoContext(r.Context(), "attempt finished",
		"attempt_id", attemptID, "worker_id", workerID, "cancelled", req.Cancelled, "error", req.Error)
	w.WriteHeader(http.StatusNoContent)
}

func attemptParams(w http.ResponseWriter, r *http.Request) (workerID, attemptID uuid.UUID, ok bool) {
	workerID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeProblem(w, r, http.StatusBadRequest, "worker id must be a UUID")
		return uuid.Nil, uuid.Nil, false
	}
	attemptID, err = uuid.Parse(chi.URLParam(r, "attemptID"))
	if err != nil {
		writeProblem(w, r, http.StatusBadRequest, "attempt id must be a UUID")
		return uuid.Nil, uuid.Nil, false
	}
	return workerID, attemptID, true
}

func (s *server) attemptError(w http.ResponseWriter, r *http.Request, op string, err error) {
	switch {
	case errors.Is(err, job.ErrAttemptNotFound):
		writeProblem(w, r, http.StatusNotFound, "attempt not found for this worker")
	case errors.Is(err, job.ErrAttemptFinished), errors.Is(err, job.ErrConflict), errors.Is(err, job.ErrIllegalTransition):
		writeProblem(w, r, http.StatusConflict, err.Error())
	default:
		s.internalError(w, r, op, err)
	}
}

func validateRuntime(rt workerapi.Runtime, role string) error {
	switch {
	case rt.Role != role:
		return fmt.Errorf("runtime.role must be %q", role)
	case role != workerapi.RolePrepare && role != workerapi.RoleExecute && role != workerapi.RoleVerify:
		return errors.New("runtime.role must be prepare, execute, or verify")
	case rt.Image == "" || len(rt.Image) > 255:
		return errors.New("runtime.image must be 1-255 characters")
	case len(rt.ContainerID) > 128 || len(rt.VolumeName) > 255:
		return errors.New("runtime.container_id or runtime.volume_name is too long")
	case rt.CPUMillis < 1 || rt.CPUMillis > math.MaxInt32:
		return errors.New("runtime.cpu_millis must be positive")
	case rt.MemoryMB < 1 || rt.MemoryMB > math.MaxInt32:
		return errors.New("runtime.memory_mb must be positive")
	case rt.StartedAt.IsZero() || rt.FinishedAt.Before(rt.StartedAt):
		return errors.New("runtime.started_at must be set and not after runtime.finished_at")
	}
	return nil
}

func validateFinish(req workerapi.FinishRequest) error {
	switch {
	case req.Cancelled && req.Error != "":
		return errors.New("set either cancelled or error, not both")
	case req.Stage != "" && req.Error == "":
		return errors.New("stage is only allowed with error")
	case req.Stage != "" && !validStages[req.Stage]:
		return errors.New("stage must be workspace, image, checkout, execute, verify, or shutdown")
	}
	v := req.Verification
	if v == nil {
		return nil
	}
	if err := validateRuntime(v.Runtime, workerapi.RoleVerify); err != nil {
		return fmt.Errorf("verification.%w", err)
	}
	if err := validateStep("verification.result", v.Result); err != nil {
		return err
	}
	switch {
	case v.Command == "" || len(v.Command) > maxCommandLen:
		return fmt.Errorf("verification.command must be 1-%d bytes", maxCommandLen)
	case len(v.OutputTail) > maxOutputTail:
		return fmt.Errorf("verification.output_tail must be at most %d bytes", maxOutputTail)
	}
	return nil
}

func validateStep(field string, s workerapi.StepResult) error {
	switch {
	case s.ExitCode < -1 || s.ExitCode > 255:
		return fmt.Errorf("%s.exit_code must be between -1 and 255", field)
	case s.DurationMS < 0:
		return fmt.Errorf("%s.duration_ms must not be negative", field)
	}
	return nil
}
