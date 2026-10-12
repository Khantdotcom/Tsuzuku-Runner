// Package delivery records outbound results (patch download, GitHub PR) after a
// job completes. Each delivery is keyed so retries do not create duplicates.
package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Khantdotcom/tsuzuku-runner/internal/artifact"
	"github.com/Khantdotcom/tsuzuku-runner/internal/job"
	"github.com/Khantdotcom/tsuzuku-runner/internal/store"
	"github.com/Khantdotcom/tsuzuku-runner/internal/store/db"
	"github.com/Khantdotcom/tsuzuku-runner/internal/workload"
)

// Delivery kinds stored in deliveries.kind.
const (
	KindPatch    = "PATCH"
	KindGitHubPR = "GITHUB_PR"
)

// Status values stored in deliveries.status.
const (
	StatusPending    = "PENDING"
	StatusInProgress = "IN_PROGRESS"
	StatusDelivered  = "DELIVERED"
	StatusFailed     = "FAILED"
)

const deliverTimeout = 2 * time.Minute

// ErrNotConfigured means GitHub delivery was requested but no token is set.
var ErrNotConfigured = errors.New("GitHub delivery is not configured")

// ErrNoPatch means the job has no diff.patch artifact to deliver.
var ErrNoPatch = errors.New("no diff.patch artifact")

// Options configures a Service.
type Options struct {
	// GitHubToken authenticates the GitHub API. Empty disables GITHUB_PR delivery.
	GitHubToken string
	// GitHubAPIBase is the API root (default https://api.github.com).
	GitHubAPIBase string
	// HTTPClient is used for GitHub calls; nil uses a client with a 60s timeout.
	HTTPClient *http.Client
	Logger     *slog.Logger
}

// Service runs idempotent deliveries for completed jobs.
type Service struct {
	pool      *pgxpool.Pool
	q         *db.Queries
	artifacts artifact.Store
	opts      Options
	log       *slog.Logger
	gh        *gitHubClient
}

// New returns a delivery Service. artifacts may be nil (patch delivery then no-ops).
func New(pool *pgxpool.Pool, artifacts artifact.Store, opts Options) *Service {
	log := opts.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	s := &Service{pool: pool, q: db.New(pool), artifacts: artifacts, opts: opts, log: log}
	if opts.GitHubToken != "" {
		s.gh = newGitHubClient(opts.GitHubToken, opts.GitHubAPIBase, opts.HTTPClient)
	}
	return s
}

// Deliver runs best-effort PATCH (when a diff exists) and optional GITHUB_PR
// for a completed job. Safe to call more than once.
func (s *Service) Deliver(ctx context.Context, jobID uuid.UUID) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), deliverTimeout)
	defer cancel()
	if _, err := s.EnsurePatch(ctx, jobID); err != nil && !errors.Is(err, ErrNoPatch) {
		s.log.WarnContext(ctx, "patch delivery", "job_id", jobID, "err", err)
	}
	j, err := s.q.GetJob(ctx, jobID)
	if err != nil {
		return
	}
	w, err := s.q.GetWorkload(ctx, j.WorkloadID)
	if err != nil {
		return
	}
	var spec workload.Spec
	if err := json.Unmarshal(w.Spec, &spec); err != nil {
		return
	}
	if !spec.Delivery.GitHubPR {
		return
	}
	if _, err := s.EnsureGitHubPR(ctx, jobID); err != nil {
		s.log.WarnContext(ctx, "GitHub PR delivery", "job_id", jobID, "err", err)
	}
}

// List returns deliveries for a job.
func (s *Service) List(ctx context.Context, jobID uuid.UUID) ([]db.Delivery, error) {
	return s.q.ListDeliveriesByJob(ctx, jobID)
}

// EnsurePatch records a PATCH delivery pointing at the diff.patch artifact.
func (s *Service) EnsurePatch(ctx context.Context, jobID uuid.UUID) (db.Delivery, error) {
	key := idemKey(jobID, KindPatch)
	if d, ok, err := s.existing(ctx, key); err != nil {
		return db.Delivery{}, err
	} else if ok && d.Status == StatusDelivered {
		return d, nil
	}

	art, err := s.findDiffArtifact(ctx, jobID)
	if err != nil {
		return db.Delivery{}, err
	}
	target := art.StorageKey
	d, err := s.begin(ctx, jobID, KindPatch, key, target)
	if err != nil {
		return db.Delivery{}, err
	}
	if d.Status == StatusDelivered {
		return d, nil
	}
	ref := "/api/v1/jobs/" + jobID.String() + "/artifacts/" + art.ID.String()
	return s.finishOK(ctx, d.ID, ref, target, true)
}

// EnsureGitHubPR opens (or reuses) a pull request that adds the job's patch file.
func (s *Service) EnsureGitHubPR(ctx context.Context, jobID uuid.UUID) (db.Delivery, error) {
	if s.gh == nil {
		return db.Delivery{}, ErrNotConfigured
	}
	key := idemKey(jobID, KindGitHubPR)
	if d, ok, err := s.existing(ctx, key); err != nil {
		return db.Delivery{}, err
	} else if ok && d.Status == StatusDelivered {
		return d, nil
	}

	j, err := s.q.GetJob(ctx, jobID)
	if err != nil {
		return db.Delivery{}, fmt.Errorf("load job: %w", err)
	}
	w, err := s.q.GetWorkload(ctx, j.WorkloadID)
	if err != nil {
		return db.Delivery{}, fmt.Errorf("load workload: %w", err)
	}
	var spec workload.Spec
	if err := json.Unmarshal(w.Spec, &spec); err != nil {
		return db.Delivery{}, fmt.Errorf("decode spec: %w", err)
	}
	owner, repo, err := parseGitHubRepo(spec.Repository)
	if err != nil {
		return db.Delivery{}, err
	}
	patch, _, err := s.readPatch(ctx, jobID)
	if err != nil {
		return db.Delivery{}, err
	}

	target := owner + "/" + repo
	d, err := s.begin(ctx, jobID, KindGitHubPR, key, target)
	if err != nil {
		return db.Delivery{}, err
	}
	if d.Status == StatusDelivered {
		return d, nil
	}

	branch := fmt.Sprintf("tsuzuku/job-%d", j.Number)
	path := fmt.Sprintf("tsuzuku/job-%d.patch", j.Number)
	prURL, err := s.gh.openPatchPR(ctx, owner, repo, spec.Revision, branch, path, patch, j.Number, spec)
	if err != nil {
		_, _ = s.finishFail(ctx, d.ID, err.Error())
		return db.Delivery{}, err
	}
	return s.finishOK(ctx, d.ID, prURL, target, false)
}

// Retry runs deliveries again for a COMPLETED job (idempotent).
func (s *Service) Retry(ctx context.Context, jobID uuid.UUID) ([]db.Delivery, error) {
	j, err := s.q.GetJob(ctx, jobID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, job.ErrNotFound
		}
		return nil, err
	}
	if j.State != string(job.Completed) {
		return nil, fmt.Errorf("%w: deliveries run only for COMPLETED jobs", job.ErrConflict)
	}
	s.Deliver(ctx, jobID)
	return s.List(ctx, jobID)
}

func (s *Service) existing(ctx context.Context, key string) (db.Delivery, bool, error) {
	d, err := s.q.GetDeliveryByKey(ctx, key)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Delivery{}, false, nil
	}
	if err != nil {
		return db.Delivery{}, false, err
	}
	return d, true, nil
}

func (s *Service) begin(ctx context.Context, jobID uuid.UUID, kind, key, target string) (db.Delivery, error) {
	var out db.Delivery
	err := store.WithTx(ctx, s.pool, func(q *db.Queries) error {
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		d, err := q.InsertDelivery(ctx, db.InsertDeliveryParams{
			ID: id, JobID: jobID, Kind: kind, IdempotencyKey: key, Target: target,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			d, err = q.GetDeliveryByKey(ctx, key)
			if err != nil {
				return err
			}
		} else if err != nil {
			return fmt.Errorf("insert delivery: %w", err)
		}
		if d.Status == StatusDelivered {
			out = d
			return nil
		}
		claimed, err := q.ClaimDelivery(ctx, d.ID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				out, err = q.GetDeliveryByKey(ctx, key)
				return err
			}
			return err
		}
		out = claimed
		return nil
	})
	return out, err
}

func (s *Service) finishOK(ctx context.Context, id uuid.UUID, ref, target string, setTarget bool) (db.Delivery, error) {
	d, err := s.q.CompleteDelivery(ctx, db.CompleteDeliveryParams{
		ID: id, ExternalRef: &ref, SetTarget: setTarget, Target: target,
	})
	if err != nil {
		return db.Delivery{}, fmt.Errorf("complete delivery: %w", err)
	}
	return d, nil
}

func (s *Service) finishFail(ctx context.Context, id uuid.UUID, msg string) (db.Delivery, error) {
	d, err := s.q.FailDelivery(ctx, db.FailDeliveryParams{ID: id, ExternalRef: &msg})
	if err != nil {
		return db.Delivery{}, err
	}
	return d, nil
}

func (s *Service) findDiffArtifact(ctx context.Context, jobID uuid.UUID) (db.Artifact, error) {
	arts, err := s.q.ListArtifacts(ctx, jobID)
	if err != nil {
		return db.Artifact{}, err
	}
	for i := len(arts) - 1; i >= 0; i-- {
		if arts[i].Name == job.ArtifactDiff {
			return arts[i], nil
		}
	}
	return db.Artifact{}, ErrNoPatch
}

func (s *Service) readPatch(ctx context.Context, jobID uuid.UUID) ([]byte, db.Artifact, error) {
	if s.artifacts == nil {
		return nil, db.Artifact{}, ErrNoPatch
	}
	art, err := s.findDiffArtifact(ctx, jobID)
	if err != nil {
		return nil, db.Artifact{}, err
	}
	r, err := s.artifacts.Open(ctx, art.StorageKey)
	if err != nil {
		return nil, db.Artifact{}, err
	}
	defer func() { _ = r.Close() }()
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, db.Artifact{}, err
	}
	if len(data) == 0 {
		return nil, db.Artifact{}, ErrNoPatch
	}
	return data, art, nil
}

func idemKey(jobID uuid.UUID, kind string) string {
	return "job:" + jobID.String() + ":" + kind
}

func parseGitHubRepo(repoURL string) (owner, repo string, err error) {
	u := strings.TrimSuffix(strings.TrimSpace(repoURL), ".git")
	const prefix = "https://github.com/"
	if !strings.HasPrefix(u, prefix) {
		return "", "", fmt.Errorf("repository must be an https://github.com/… URL for GitHub delivery")
	}
	rest := strings.TrimPrefix(u, prefix)
	parts := strings.Split(rest, "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("cannot parse GitHub owner/repo from %q", repoURL)
	}
	return parts[0], parts[1], nil
}
