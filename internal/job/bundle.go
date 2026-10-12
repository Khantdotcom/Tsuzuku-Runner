package job

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/google/uuid"

	"github.com/Khantdotcom/tsuzuku-runner/internal/store/db"
	"github.com/Khantdotcom/tsuzuku-runner/internal/workload"
)

// Evidence bundle artifact names.
const (
	ArtifactJob          = "job.json"
	ArtifactEnvironment  = "environment.json"
	ArtifactCommands     = "commands.json"
	ArtifactVerification = "verification.json"
	ArtifactResources    = "resources.json"
	ArtifactFailure      = "failure.json"
	ArtifactDiff         = "diff.patch"
)

const jsonContentType = "application/json"

// archiveEvidence stores log files and JSON sidecars for a finished attempt.
// It is best effort: the job's outcome is already committed.
func (s *Service) archiveEvidence(ctx context.Context, a db.JobAttempt) {
	if s.artifacts == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), archiveTimeout)
	defer cancel()
	if err := s.storeLogs(ctx, a); err != nil {
		s.logger.WarnContext(ctx, "store log artifacts", "job_id", a.JobID, "attempt_id", a.ID, "err", err)
	}
	if err := s.storeBundle(ctx, a); err != nil {
		s.logger.WarnContext(ctx, "store evidence bundle", "job_id", a.JobID, "attempt_id", a.ID, "err", err)
	}
}

func (s *Service) storeBundle(ctx context.Context, a db.JobAttempt) error {
	j, err := s.q.GetJob(ctx, a.JobID)
	if err != nil {
		return fmt.Errorf("load job: %w", err)
	}
	w, err := s.q.GetWorkload(ctx, j.WorkloadID)
	if err != nil {
		return fmt.Errorf("load workload: %w", err)
	}
	var spec workload.Spec
	_ = json.Unmarshal(w.Spec, &spec)

	allRuntimes, err := s.q.ListRuntimes(ctx, a.JobID)
	if err != nil {
		return fmt.Errorf("list runtimes: %w", err)
	}
	var runtimes []db.Runtime
	for _, r := range allRuntimes {
		if r.AttemptID == a.ID {
			runtimes = append(runtimes, r)
		}
	}
	failures, err := s.q.ListFailures(ctx, a.JobID)
	if err != nil {
		return fmt.Errorf("list failures: %w", err)
	}
	var attemptFailures []db.Failure
	for _, f := range failures {
		if f.AttemptID != nil && *f.AttemptID == a.ID {
			attemptFailures = append(attemptFailures, f)
		}
	}
	evidence, err := s.Evidence(ctx, a.JobID)
	if err != nil {
		return err
	}
	var verifs []Verification
	for _, v := range evidence.Verifications {
		if v.Run.AttemptID == a.ID {
			verifs = append(verifs, v)
		}
	}

	files := map[string]any{
		ArtifactJob: map[string]any{
			"job_id": j.ID, "number": j.Number, "state": j.State,
			"attempt_id": a.ID, "attempt_number": a.AttemptNumber, "attempt_status": a.Status,
			"repository": w.RepositoryURL, "revision": w.Revision, "command": w.Command,
			"started_at": j.StartedAt, "finished_at": j.FinishedAt,
		},
		ArtifactEnvironment: runtimes,
		ArtifactCommands: map[string]any{
			"command": w.Command, "exit_code": a.ExitCode, "error": a.Error,
			"verification_command": w.VerificationCommand,
			"verification_steps":   spec.VerificationSteps(),
		},
		ArtifactVerification: verifs,
		ArtifactResources: map[string]any{
			"cpu_millis": w.CpuMillis, "memory_mb": w.MemoryMB,
			"timeout_seconds": w.TimeoutSeconds, "network_enabled": w.NetworkEnabled,
			"image": w.Image,
		},
		ArtifactFailure: attemptFailures,
	}
	for name, value := range files {
		data, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			return err
		}
		if err := s.putArtifact(ctx, a, name, jsonContentType, data); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) putArtifact(ctx context.Context, a db.JobAttempt, name, contentType string, data []byte) error {
	key := fmt.Sprintf("jobs/%s/attempts/%d/%s", a.JobID, a.AttemptNumber, name)
	obj, err := s.artifacts.Put(ctx, key, data)
	if err != nil {
		return err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return err
	}
	if _, err := s.q.CreateArtifact(ctx, db.CreateArtifactParams{
		ID: id, JobID: a.JobID, AttemptID: &a.ID, Name: name,
		ContentType: contentType, StorageKey: obj.Key, SizeBytes: obj.Size, Sha256: obj.SHA256,
	}); err != nil {
		return fmt.Errorf("record %s artifact: %w", name, err)
	}
	return nil
}

// storeDiffArtifact writes integrity.diff as diff.patch when non-empty.
func (s *Service) storeDiffArtifact(ctx context.Context, a db.JobAttempt, diff string) {
	if s.artifacts == nil || diff == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), archiveTimeout)
	defer cancel()
	if err := s.putArtifact(ctx, a, ArtifactDiff, "text/x-diff", []byte(diff)); err != nil {
		s.logger.WarnContext(ctx, "store diff artifact", "job_id", a.JobID, "attempt_id", a.ID, "err", err)
	}
}

// BundleZip writes a zip of the latest attempt's artifacts for jobID to w.
func (s *Service) BundleZip(ctx context.Context, jobID uuid.UUID, w io.Writer) (filename string, err error) {
	if s.artifacts == nil {
		return "", ErrArtifactNotFound
	}
	j, err := s.q.GetJob(ctx, jobID)
	if err != nil {
		return "", fmt.Errorf("load job: %w", err)
	}
	attempts, err := s.q.ListAttempts(ctx, jobID)
	if err != nil {
		return "", fmt.Errorf("list attempts: %w", err)
	}
	if len(attempts) == 0 {
		return "", ErrArtifactNotFound
	}
	latest := attempts[len(attempts)-1]
	arts, err := s.q.ListArtifacts(ctx, jobID)
	if err != nil {
		return "", fmt.Errorf("list artifacts: %w", err)
	}
	zw := zip.NewWriter(w)
	defer func() { _ = zw.Close() }()
	count := 0
	for _, a := range arts {
		if a.AttemptID == nil || *a.AttemptID != latest.ID {
			continue
		}
		r, err := s.artifacts.Open(ctx, a.StorageKey)
		if err != nil {
			return "", err
		}
		fw, err := zw.Create(a.Name)
		if err != nil {
			_ = r.Close()
			return "", err
		}
		_, copyErr := io.Copy(fw, r)
		_ = r.Close()
		if copyErr != nil {
			return "", copyErr
		}
		count++
	}
	if count == 0 {
		return "", ErrArtifactNotFound
	}
	return fmt.Sprintf("job-%d-attempt-%d-evidence.zip", j.Number, latest.AttemptNumber), nil
}
