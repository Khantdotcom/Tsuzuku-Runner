package job

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/Khantdotcom/tsuzuku-runner/internal/store"
	"github.com/Khantdotcom/tsuzuku-runner/internal/store/db"
	"github.com/Khantdotcom/tsuzuku-runner/internal/workerapi"
)

// EventLogsTruncated is written once, when an attempt's output reaches the
// log limit and later output starts being dropped.
const EventLogsTruncated = "attempt.logs_truncated"

// Log artifact names.
const (
	ArtifactStdout = "stdout.log"
	ArtifactStderr = "stderr.log"
)

const (
	logContentType = "text/plain; charset=utf-8"
	// archiveTimeout bounds storing an attempt's logs after it ends.
	archiveTimeout = 30 * time.Second
)

// AppendLogs stores a batch of an attempt's output. A chunk whose seq was
// already stored is ignored, so a worker can safely resend a batch. Output
// beyond the log limit is dropped; truncated reports that the limit is
// reached.
func (s *Service) AppendLogs(ctx context.Context, workerID, attemptID uuid.UUID, chunks []workerapi.LogChunk) (truncated bool, err error) {
	err = store.WithTx(ctx, s.pool, func(q *db.Queries) error {
		a, err := lockAttempt(ctx, q, workerID, attemptID)
		if err != nil {
			return err
		}
		used, err := q.AttemptLogBytes(ctx, a.ID)
		if err != nil {
			return fmt.Errorf("measure logs: %w", err)
		}
		before := used
		for _, c := range chunks {
			data := c.Data
			if room := s.maxLogBytes - used; int64(len(data)) > room {
				data = data[:max(room, 0)]
			}
			if len(data) == 0 {
				continue
			}
			n, err := q.InsertLogChunk(ctx, db.InsertLogChunkParams{
				JobID:     a.JobID,
				AttemptID: a.ID,
				Seq:       int32(c.Seq), //nolint:gosec // validated by the API
				Stream:    c.Stream,
				Data:      data,
			})
			if err != nil {
				return fmt.Errorf("store log chunk %d: %w", c.Seq, err)
			}
			if n > 0 {
				used += int64(len(data))
			}
		}

		truncated = used >= s.maxLogBytes
		if before < s.maxLogBytes && truncated {
			payload, err := json.Marshal(map[string]any{"limit_bytes": s.maxLogBytes})
			if err != nil {
				return err
			}
			if _, err := q.CreateJobEvent(ctx, db.CreateJobEventParams{
				JobID: a.JobID, AttemptID: &a.ID, Type: EventLogsTruncated, Payload: payload,
			}); err != nil {
				return fmt.Errorf("record truncation event: %w", err)
			}
		}
		return nil
	})
	return truncated, err
}

// archiveLogs stores a finished attempt's stdout and stderr as artifacts.
// It is best effort: the job's outcome is already committed, and the output
// remains readable from the log endpoint if storing fails.
func (s *Service) archiveLogs(ctx context.Context, a db.JobAttempt) {
	if s.artifacts == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), archiveTimeout)
	defer cancel()
	if err := s.storeLogs(ctx, a); err != nil {
		s.logger.WarnContext(ctx, "store log artifacts", "job_id", a.JobID, "attempt_id", a.ID, "err", err)
	}
}

func (s *Service) storeLogs(ctx context.Context, a db.JobAttempt) error {
	chunks, err := s.q.ListAttemptLogChunks(ctx, a.ID)
	if err != nil {
		return fmt.Errorf("list log chunks: %w", err)
	}
	var stdout, stderr bytes.Buffer
	for _, c := range chunks {
		if c.Stream == workerapi.StreamStderr {
			stderr.Write(c.Data)
		} else {
			stdout.Write(c.Data)
		}
	}
	for _, f := range []struct {
		name string
		data []byte
	}{{ArtifactStdout, stdout.Bytes()}, {ArtifactStderr, stderr.Bytes()}} {
		key := fmt.Sprintf("jobs/%s/attempts/%d/%s", a.JobID, a.AttemptNumber, f.name)
		obj, err := s.artifacts.Put(ctx, key, f.data)
		if err != nil {
			return err
		}
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		if _, err := s.q.CreateArtifact(ctx, db.CreateArtifactParams{
			ID:          id,
			JobID:       a.JobID,
			AttemptID:   &a.ID,
			Name:        f.name,
			ContentType: logContentType,
			StorageKey:  obj.Key,
			SizeBytes:   obj.Size,
			Sha256:      obj.SHA256,
		}); err != nil {
			return fmt.Errorf("record %s artifact: %w", f.name, err)
		}
	}
	return nil
}
