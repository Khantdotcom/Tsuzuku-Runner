package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/Khantdotcom/tsuzuku-runner/internal/job"
)

const (
	streamPollInterval = time.Second
	streamBatchLimit   = int32(100)
)

func (s *server) handleStreamEvents(w http.ResponseWriter, r *http.Request) {
	j, ok := s.findJob(w, r)
	if !ok {
		return
	}
	after, ok := parseStreamAfter(w, r)
	if !ok {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeProblem(w, r, http.StatusInternalServerError, "streaming is not supported")
		return
	}

	setSSEHeaders(w)
	flusher.Flush()

	ticker := time.NewTicker(streamPollInterval)
	defer ticker.Stop()
	ctx := r.Context()
	for {
		events, err := s.jobs.Events(ctx, j.ID, after, streamBatchLimit)
		if err != nil {
			writeSSEComment(w, "error")
			flusher.Flush()
			return
		}
		for _, e := range events {
			view := eventView{ID: e.ID, Type: e.Type, AttemptID: e.AttemptID, Payload: e.Payload, CreatedAt: e.CreatedAt}
			if err := writeSSE(w, "event", strconv.FormatInt(e.ID, 10), view); err != nil {
				return
			}
			after = e.ID
		}
		if len(events) > 0 {
			flusher.Flush()
		}

		finished, err := s.jobIsTerminal(ctx, j.ID)
		if err != nil {
			return
		}
		if finished {
			if len(events) == 0 {
				_ = writeSSE(w, "done", "", map[string]string{"state": "finished"})
				flusher.Flush()
				return
			}
			continue
		}

		select {
		case <-ctx.Done():
			return
		case <-s.stopping:
			return
		case <-ticker.C:
		}
	}
}

func (s *server) handleStreamLogs(w http.ResponseWriter, r *http.Request) {
	j, ok := s.findJob(w, r)
	if !ok {
		return
	}
	after, ok := parseStreamAfter(w, r)
	if !ok {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeProblem(w, r, http.StatusInternalServerError, "streaming is not supported")
		return
	}

	setSSEHeaders(w)
	flusher.Flush()

	ticker := time.NewTicker(streamPollInterval)
	defer ticker.Stop()
	ctx := r.Context()
	for {
		chunks, err := s.jobs.Logs(ctx, j.ID, after, streamBatchLimit)
		if err != nil {
			writeSSEComment(w, "error")
			flusher.Flush()
			return
		}
		for _, c := range chunks {
			view := logChunkView{
				ID: c.ID, AttemptID: c.AttemptID, Seq: c.Seq, Stream: c.Stream, Data: c.Data, CreatedAt: c.CreatedAt,
			}
			if err := writeSSE(w, "chunk", strconv.FormatInt(c.ID, 10), view); err != nil {
				return
			}
			after = c.ID
		}
		if len(chunks) > 0 {
			flusher.Flush()
		}

		finished, err := s.jobIsTerminal(ctx, j.ID)
		if err != nil {
			return
		}
		if finished {
			if len(chunks) == 0 {
				_ = writeSSE(w, "done", "", map[string]string{"state": "finished"})
				flusher.Flush()
				return
			}
			continue
		}

		select {
		case <-ctx.Done():
			return
		case <-s.stopping:
			return
		case <-ticker.C:
		}
	}
}

func (s *server) jobIsTerminal(ctx context.Context, jobID uuid.UUID) (bool, error) {
	current, err := s.jobs.Find(ctx, job.Ref{ID: jobID})
	if err != nil {
		return false, err
	}
	return job.State(current.State).Terminal(), nil
}

func setSSEHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
}

func writeSSE(w http.ResponseWriter, event, id string, data any) error {
	payload, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if event != "" {
		if _, err := fmt.Fprintf(w, "event: %s\n", event); err != nil {
			return err
		}
	}
	if id != "" {
		if _, err := fmt.Fprintf(w, "id: %s\n", id); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
		return err
	}
	return nil
}

func writeSSEComment(w http.ResponseWriter, text string) {
	_, _ = fmt.Fprintf(w, ": %s\n\n", text)
}

// parseStreamAfter reads the after cursor from ?after= or Last-Event-ID.
func parseStreamAfter(w http.ResponseWriter, r *http.Request) (int64, bool) {
	if v := r.URL.Query().Get("after"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			writeProblem(w, r, http.StatusBadRequest, "after must be a non-negative id")
			return 0, false
		}
		return n, true
	}
	if v := r.Header.Get("Last-Event-ID"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			writeProblem(w, r, http.StatusBadRequest, "Last-Event-ID must be a non-negative id")
			return 0, false
		}
		return n, true
	}
	return 0, true
}
