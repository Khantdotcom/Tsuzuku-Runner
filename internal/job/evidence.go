package job

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Khantdotcom/tsuzuku-runner/internal/artifact"
	"github.com/Khantdotcom/tsuzuku-runner/internal/store/db"
)

// ErrArtifactNotFound means the job has no such artifact, or its content is
// missing from the store.
var ErrArtifactNotFound = errors.New("artifact not found")

// Evidence is what a job left behind: stored files, verification results,
// and classified failures.
type Evidence struct {
	Artifacts     []db.Artifact
	Verifications []Verification
	Failures      []db.Failure
}

// Verification is a verification run with its checks.
type Verification struct {
	Run    db.VerificationRun
	Checks []db.VerificationCheck
}

// Evidence loads a job's evidence.
func (s *Service) Evidence(ctx context.Context, jobID uuid.UUID) (Evidence, error) {
	var e Evidence
	var err error
	if e.Artifacts, err = s.q.ListArtifacts(ctx, jobID); err != nil {
		return Evidence{}, fmt.Errorf("list artifacts: %w", err)
	}
	if e.Failures, err = s.q.ListFailures(ctx, jobID); err != nil {
		return Evidence{}, fmt.Errorf("list failures: %w", err)
	}
	runs, err := s.q.ListVerificationRuns(ctx, jobID)
	if err != nil {
		return Evidence{}, fmt.Errorf("list verification runs: %w", err)
	}
	checks, err := s.q.ListVerificationChecks(ctx, jobID)
	if err != nil {
		return Evidence{}, fmt.Errorf("list verification checks: %w", err)
	}
	e.Verifications = make([]Verification, 0, len(runs))
	byRun := make(map[uuid.UUID]int, len(runs))
	for _, r := range runs {
		byRun[r.ID] = len(e.Verifications)
		e.Verifications = append(e.Verifications, Verification{Run: r, Checks: []db.VerificationCheck{}})
	}
	for _, c := range checks {
		if i, ok := byRun[c.VerificationRunID]; ok {
			e.Verifications[i].Checks = append(e.Verifications[i].Checks, c)
		}
	}
	return e, nil
}

// Artifacts returns a job's stored files.
func (s *Service) Artifacts(ctx context.Context, jobID uuid.UUID) ([]db.Artifact, error) {
	return s.q.ListArtifacts(ctx, jobID)
}

// OpenArtifact returns one of a job's artifacts and its content. The caller
// closes the content.
func (s *Service) OpenArtifact(ctx context.Context, jobID, artifactID uuid.UUID) (db.Artifact, io.ReadCloser, error) {
	a, err := s.q.GetArtifact(ctx, db.GetArtifactParams{ID: artifactID, JobID: jobID})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && s.artifacts == nil) {
		return db.Artifact{}, nil, ErrArtifactNotFound
	}
	if err != nil {
		return db.Artifact{}, nil, fmt.Errorf("load artifact: %w", err)
	}
	r, err := s.artifacts.Open(ctx, a.StorageKey)
	if errors.Is(err, artifact.ErrNotFound) {
		return db.Artifact{}, nil, ErrArtifactNotFound
	}
	if err != nil {
		return db.Artifact{}, nil, err
	}
	return a, r, nil
}
