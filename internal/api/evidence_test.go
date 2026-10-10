package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Khantdotcom/tsuzuku-runner/internal/job"
	"github.com/Khantdotcom/tsuzuku-runner/internal/store/db"
)

func TestCancelJob(t *testing.T) {
	tests := []struct {
		name      string
		immediate bool
		err       error
		want      int
	}{
		{"not started", true, nil, http.StatusOK},
		{"running", false, nil, http.StatusAccepted},
		{"finished", false, fmt.Errorf("%w: job 7 is COMPLETED", job.ErrJobFinished), http.StatusConflict},
		{"raced", false, fmt.Errorf("%w: job 7 is no longer running", job.ErrConflict), http.StatusConflict},
		{"broken", false, errors.New("connection reset"), http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jobs := &fakeJobService{cancelImmediate: tt.immediate, cancelErr: tt.err}
			rec := serve(t, newJobsRouter(jobs), http.MethodPost, "/api/v1/jobs/7/cancel", "", "")

			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, tt.want, rec.Body)
			}
			if jobs.cancels != 1 {
				t.Errorf("cancel calls = %d", jobs.cancels)
			}
			if tt.err != nil {
				decodeProblem(t, rec)
				return
			}
			var v jobView
			if err := json.NewDecoder(rec.Body).Decode(&v); err != nil {
				t.Fatal(err)
			}
			if tt.immediate && v.State != "CANCELLED" {
				t.Errorf("state = %s, want CANCELLED", v.State)
			}
			if !tt.immediate && v.CancelRequestedAt == nil {
				t.Error("cancel_requested_at not set on a running job")
			}
		})
	}
}

func TestCancelUnknownJob(t *testing.T) {
	jobs := &fakeJobService{findErr: job.ErrNotFound}
	rec := serve(t, newJobsRouter(jobs), http.MethodPost, "/api/v1/jobs/99/cancel", "", "")
	if rec.Code != http.StatusNotFound || jobs.cancels != 0 {
		t.Fatalf("status = %d, cancels = %d", rec.Code, jobs.cancels)
	}
}

func TestGetEvidence(t *testing.T) {
	jobs := &fakeJobService{}
	h := newJobsRouter(jobs)
	attempt := uuid.New()
	exit := int32(1)
	cmd := "make lint"
	started := time.Now()
	jobs.evidence = job.Evidence{
		Artifacts: []db.Artifact{{
			ID: uuid.New(), JobID: jobs.job.ID, AttemptID: &attempt, Name: "stdout.log",
			ContentType: "text/plain; charset=utf-8", SizeBytes: 6, Sha256: strings.Repeat("a", 64),
		}},
		Verifications: []job.Verification{{
			Run: db.VerificationRun{ID: uuid.New(), AttemptID: attempt, Status: "FAILED", StartedAt: started},
			Checks: []db.VerificationCheck{
				{Name: "exit_code", Kind: "exit_code", Status: "PASSED"},
				{Name: "verification", Kind: "command", Command: &cmd, Status: "FAILED", ExitCode: &exit, Output: "lint failed"},
			},
		}},
		Failures: []db.Failure{{
			ID: uuid.New(), AttemptID: &attempt, Category: "TEST", Message: "verification exited with code 1",
			Details: json.RawMessage(`{"verification_exit_code":1}`),
		}},
	}

	rec := serve(t, h, http.MethodGet, "/api/v1/jobs/7/evidence", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", rec.Code, rec.Body)
	}
	var body struct {
		Artifacts     []artifactView     `json:"artifacts"`
		Verifications []verificationView `json:"verifications"`
		Failures      []failureView      `json:"failures"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	a := jobs.evidence.Artifacts[0]
	if len(body.Artifacts) != 1 || body.Artifacts[0].URL != "/api/v1/jobs/"+jobs.job.ID.String()+"/artifacts/"+a.ID.String() ||
		body.Artifacts[0].SHA256 != a.Sha256 {
		t.Errorf("artifacts = %+v", body.Artifacts)
	}
	if len(body.Verifications) != 1 || len(body.Verifications[0].Checks) != 2 ||
		body.Verifications[0].Checks[1].Output != "lint failed" || *body.Verifications[0].Checks[1].ExitCode != 1 {
		t.Errorf("verifications = %+v", body.Verifications)
	}
	if len(body.Failures) != 1 || body.Failures[0].Category != "TEST" {
		t.Errorf("failures = %+v", body.Failures)
	}
}

func TestEmptyEvidenceUsesEmptyLists(t *testing.T) {
	rec := serve(t, newJobsRouter(&fakeJobService{}), http.MethodGet, "/api/v1/jobs/7/evidence", "", "")
	if got := strings.TrimSpace(rec.Body.String()); got != `{"artifacts":[],"failures":[],"verifications":[]}` {
		t.Errorf("body = %s", got)
	}
}

func TestDownloadArtifact(t *testing.T) {
	jobs := &fakeJobService{content: "hello\n"}
	h := newJobsRouter(jobs)
	jobs.artifact = db.Artifact{
		ID: uuid.New(), JobID: jobs.job.ID, Name: "stdout.log", ContentType: "text/plain; charset=utf-8",
		SizeBytes: 6, Sha256: strings.Repeat("b", 64),
	}

	rec := serve(t, h, http.MethodGet, "/api/v1/jobs/7/artifacts/"+jobs.artifact.ID.String(), "", "")
	if rec.Code != http.StatusOK || rec.Body.String() != "hello\n" {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body)
	}
	h2 := rec.Header()
	if h2.Get("Content-Type") != "text/plain; charset=utf-8" || h2.Get("Content-Length") != "6" ||
		h2.Get("Content-Disposition") != `attachment; filename=stdout.log` || h2.Get("X-Content-Type-Options") != "nosniff" ||
		h2.Get("ETag") != `"`+strings.Repeat("b", 64)+`"` {
		t.Errorf("headers = %v", h2)
	}
}

func TestDownloadArtifactErrors(t *testing.T) {
	tests := []struct {
		name, id string
		openErr  error
		want     int
	}{
		{"bad id", "nope", nil, http.StatusBadRequest},
		{"missing", uuid.NewString(), job.ErrArtifactNotFound, http.StatusNotFound},
		{"broken store", uuid.NewString(), errors.New("disk on fire"), http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jobs := &fakeJobService{openErr: tt.openErr}
			rec := serve(t, newJobsRouter(jobs), http.MethodGet, "/api/v1/jobs/7/artifacts/"+tt.id, "", "")
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d", rec.Code, tt.want)
			}
			p := decodeProblem(t, rec)
			if strings.Contains(p.Detail, "disk on fire") {
				t.Error("internal error leaked")
			}
		})
	}
}
