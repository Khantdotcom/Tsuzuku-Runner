package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/Khantdotcom/tsuzuku-runner/internal/workerapi"
)

const (
	defaultLogInterval = time.Second
	// maxChunkBytes splits large writes so several chunks fit in one batch.
	maxChunkBytes = 16 << 10
	// maxPendingLogBytes bounds output held while the server is unreachable.
	// Output beyond it is dropped rather than growing memory without limit.
	maxPendingLogBytes = 4 << 20
)

// sendLogs uploads one batch and reports whether the server stopped storing
// output for the attempt.
type sendLogs func(ctx context.Context, chunks []workerapi.LogChunk) (truncated bool, err error)

// logShipper collects a step's output and uploads it in batches: every
// interval, or sooner once a full batch is waiting. Every chunk gets the next
// sequence number, so a batch resent after an error is stored only once.
type logShipper struct {
	send       sendLogs
	log        *slog.Logger
	interval   time.Duration
	retryDelay time.Duration

	mu           sync.Mutex
	pending      []workerapi.LogChunk
	pendingBytes int
	seq          int
	// stopped is set once the server will no longer store output.
	stopped bool
	dropped int

	full chan struct{}
	quit chan struct{}
	done chan struct{}
}

func newLogShipper(send sendLogs, log *slog.Logger, interval, retryDelay time.Duration) *logShipper {
	if interval <= 0 {
		interval = defaultLogInterval
	}
	if retryDelay <= 0 {
		retryDelay = time.Second
	}
	return &logShipper{
		send:       send,
		log:        log,
		interval:   interval,
		retryDelay: retryDelay,
		full:       make(chan struct{}, 1),
		quit:       make(chan struct{}),
		done:       make(chan struct{}),
	}
}

// writer returns an io.Writer that ships what is written to it as stream.
func (s *logShipper) writer(stream string) io.Writer {
	return streamWriter{s: s, stream: stream}
}

type streamWriter struct {
	s      *logShipper
	stream string
}

func (w streamWriter) Write(p []byte) (int, error) {
	w.s.add(w.stream, p)
	return len(p), nil
}

func (s *logShipper) add(stream string, p []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return
	}
	for len(p) > 0 {
		n := min(len(p), maxChunkBytes)
		if s.pendingBytes+n > maxPendingLogBytes {
			s.dropped += len(p)
			break
		}
		s.pending = append(s.pending, workerapi.LogChunk{Seq: s.seq, Stream: stream, Data: append([]byte(nil), p[:n]...)})
		s.seq++
		s.pendingBytes += n
		p = p[n:]
	}
	if s.pendingBytes >= workerapi.MaxLogBatchBytes {
		select {
		case s.full <- struct{}{}:
		default:
		}
	}
}

// start uploads batches in the background until close is called.
func (s *logShipper) start(ctx context.Context) {
	go func() {
		defer close(s.done)
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()
		for {
			select {
			case <-s.quit:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
			case <-s.full:
			}
			s.flush(ctx)
		}
	}()
}

// flush uploads batches until none is waiting or an upload fails.
func (s *logShipper) flush(ctx context.Context) {
	for s.flushOnce(ctx) {
		if ctx.Err() != nil {
			return
		}
	}
}

// close stops background uploads and sends the remaining output, retrying
// until timeout passes. It uploads even if ctx is already cancelled, so the
// output of a stopped step is kept.
func (s *logShipper) close(ctx context.Context, timeout time.Duration) {
	close(s.quit)
	<-s.done

	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	delay := s.retryDelay
	for {
		s.flush(ctx)
		if s.empty() {
			break
		}
		if !sleep(ctx, delay) {
			s.log.WarnContext(ctx, "gave up uploading output", "unsent_bytes", s.unsent())
			break
		}
		delay = min(delay*2, maxReportRetryDelay)
	}
	s.mu.Lock()
	dropped := s.dropped
	s.mu.Unlock()
	if dropped > 0 {
		s.log.WarnContext(ctx, "output dropped while the server was unreachable", "dropped_bytes", dropped)
	}
}

// flushOnce uploads the oldest batch. It reports true when the batch was
// accepted and more output is waiting.
func (s *logShipper) flushOnce(ctx context.Context) bool {
	batch := s.nextBatch()
	if len(batch) == 0 {
		return false
	}
	truncated, err := s.send(ctx, batch)
	if apiErr, ok := errors.AsType[*APIError](err); ok && apiErr.Status < http.StatusInternalServerError {
		s.log.WarnContext(ctx, "server rejected output; no longer uploading it", "err", err)
		s.stop()
		return false
	}
	if err != nil {
		s.log.WarnContext(ctx, "upload output failed; retrying", "err", err)
		return false
	}
	if truncated {
		s.log.InfoContext(ctx, "output limit reached; no longer uploading output")
		s.stop()
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range batch {
		s.pendingBytes -= len(c.Data)
	}
	s.pending = s.pending[len(batch):]
	return len(s.pending) > 0
}

func (s *logShipper) nextBatch() []workerapi.LogChunk {
	s.mu.Lock()
	defer s.mu.Unlock()
	size, n := 0, 0
	for n < len(s.pending) && n < workerapi.MaxLogBatchChunks {
		if size+len(s.pending[n].Data) > workerapi.MaxLogBatchBytes {
			break
		}
		size += len(s.pending[n].Data)
		n++
	}
	return s.pending[:n:n]
}

func (s *logShipper) stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopped = true
	s.pending, s.pendingBytes = nil, 0
}

func (s *logShipper) empty() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.pending) == 0
}

func (s *logShipper) unsent() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pendingBytes
}
