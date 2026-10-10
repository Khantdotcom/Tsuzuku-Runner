package worker

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/Khantdotcom/tsuzuku-runner/internal/workerapi"
)

// fakeLogSink records uploaded batches. errs are returned by successive
// calls before uploads succeed; truncateAfter makes the server report
// truncation once that many batches were accepted.
type fakeLogSink struct {
	mu            sync.Mutex
	errs          []error
	calls         int
	batches       [][]workerapi.LogChunk
	truncateAfter int
}

func (f *fakeLogSink) send(_ context.Context, chunks []workerapi.LogChunk) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.calls <= len(f.errs) {
		return false, f.errs[f.calls-1]
	}
	f.batches = append(f.batches, append([]workerapi.LogChunk(nil), chunks...))
	return f.truncateAfter > 0 && len(f.batches) >= f.truncateAfter, nil
}

func (f *fakeLogSink) chunks() []workerapi.LogChunk {
	f.mu.Lock()
	defer f.mu.Unlock()
	var all []workerapi.LogChunk
	for _, b := range f.batches {
		all = append(all, b...)
	}
	return all
}

func newTestShipper(sink *fakeLogSink, interval time.Duration) *logShipper {
	return newLogShipper(sink.send, slog.New(slog.DiscardHandler), interval, time.Millisecond)
}

func TestLogShipperUploadsOnClose(t *testing.T) {
	sink := &fakeLogSink{}
	s := newTestShipper(sink, time.Hour)
	s.start(t.Context())
	_, _ = s.writer(workerapi.StreamStdout).Write([]byte("one\n"))
	_, _ = s.writer(workerapi.StreamStderr).Write([]byte("two\n"))
	_, _ = s.writer(workerapi.StreamStdout).Write([]byte("three\n"))
	s.close(t.Context(), time.Second)

	got := sink.chunks()
	want := []workerapi.LogChunk{
		{Seq: 0, Stream: workerapi.StreamStdout, Data: []byte("one\n")},
		{Seq: 1, Stream: workerapi.StreamStderr, Data: []byte("two\n")},
		{Seq: 2, Stream: workerapi.StreamStdout, Data: []byte("three\n")},
	}
	if len(got) != len(want) {
		t.Fatalf("chunks = %+v", got)
	}
	for i := range want {
		if got[i].Seq != want[i].Seq || got[i].Stream != want[i].Stream || !bytes.Equal(got[i].Data, want[i].Data) {
			t.Errorf("chunk %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestLogShipperUploadsOnInterval(t *testing.T) {
	sink := &fakeLogSink{}
	s := newTestShipper(sink, 5*time.Millisecond)
	s.start(t.Context())
	defer s.close(t.Context(), time.Second)

	_, _ = s.writer(workerapi.StreamStdout).Write([]byte("tick\n"))
	waitFor(t, "an upload before close", func() bool { return len(sink.chunks()) == 1 })
}

func TestLogShipperSplitsAndBatchesLargeOutput(t *testing.T) {
	sink := &fakeLogSink{}
	s := newTestShipper(sink, time.Hour)
	s.start(t.Context())
	big := bytes.Repeat([]byte("x"), 3*workerapi.MaxLogBatchBytes+10)
	_, _ = s.writer(workerapi.StreamStdout).Write(big)
	s.close(t.Context(), time.Second)

	var joined []byte
	for i, c := range sink.chunks() {
		if c.Seq != i || len(c.Data) > maxChunkBytes {
			t.Errorf("chunk %d: seq %d, %d bytes", i, c.Seq, len(c.Data))
		}
		joined = append(joined, c.Data...)
	}
	if !bytes.Equal(joined, big) {
		t.Errorf("uploaded %d bytes, want %d", len(joined), len(big))
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	for i, b := range sink.batches {
		size := 0
		for _, c := range b {
			size += len(c.Data)
		}
		if size > workerapi.MaxLogBatchBytes || len(b) > workerapi.MaxLogBatchChunks {
			t.Errorf("batch %d holds %d chunks, %d bytes", i, len(b), size)
		}
	}
	if len(sink.batches) < 4 {
		t.Errorf("batches = %d, want at least 4 for %d bytes", len(sink.batches), len(big))
	}
}

func TestLogShipperResendsTheSameSeqAfterAnError(t *testing.T) {
	sink := &fakeLogSink{errs: []error{errors.New("connection reset"), &APIError{Status: 503}}}
	s := newTestShipper(sink, time.Hour)
	s.start(t.Context())
	_, _ = s.writer(workerapi.StreamStdout).Write([]byte("hello"))
	s.close(t.Context(), time.Second)

	got := sink.chunks()
	if sink.calls != 3 || len(got) != 1 || got[0].Seq != 0 || string(got[0].Data) != "hello" {
		t.Errorf("calls = %d, chunks = %+v", sink.calls, got)
	}
}

func TestLogShipperStopsWhenTruncated(t *testing.T) {
	sink := &fakeLogSink{truncateAfter: 1}
	s := newTestShipper(sink, 5*time.Millisecond)
	s.start(t.Context())
	_, _ = s.writer(workerapi.StreamStdout).Write([]byte("first"))
	waitFor(t, "the first upload", func() bool { return len(sink.chunks()) == 1 })
	waitFor(t, "the shipper to stop", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.stopped
	})
	_, _ = s.writer(workerapi.StreamStdout).Write([]byte("second"))
	s.close(t.Context(), time.Second)

	if got := sink.chunks(); len(got) != 1 {
		t.Errorf("chunks = %+v, want nothing after truncation", got)
	}
}

func TestLogShipperStopsWhenRejected(t *testing.T) {
	sink := &fakeLogSink{errs: []error{&APIError{Status: 409}}}
	s := newTestShipper(sink, time.Hour)
	s.start(t.Context())
	_, _ = s.writer(workerapi.StreamStdout).Write([]byte("late"))
	start := time.Now()
	s.close(t.Context(), 5*time.Second)

	if sink.calls != 1 || len(sink.chunks()) != 0 {
		t.Errorf("calls = %d, want one rejected upload and no retry", sink.calls)
	}
	if time.Since(start) > time.Second {
		t.Error("close kept retrying after the server rejected the output")
	}
}

func TestLogShipperGivesUpAfterTimeout(t *testing.T) {
	errs := make([]error, 10000)
	for i := range errs {
		errs[i] = errors.New("connection refused")
	}
	sink := &fakeLogSink{errs: errs}
	s := newTestShipper(sink, time.Hour)
	s.start(t.Context())
	_, _ = s.writer(workerapi.StreamStdout).Write([]byte("lost"))
	start := time.Now()
	s.close(t.Context(), 50*time.Millisecond)

	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("close took %s, want about the 50ms timeout", elapsed)
	}
}

func TestLogShipperUploadsAfterContextCancelled(t *testing.T) {
	sink := &fakeLogSink{}
	ctx, cancel := context.WithCancel(t.Context())
	s := newTestShipper(sink, time.Hour)
	s.start(ctx)
	_, _ = s.writer(workerapi.StreamStderr).Write([]byte("killed\n"))
	cancel()
	s.close(ctx, time.Second)

	if got := sink.chunks(); len(got) != 1 || string(got[0].Data) != "killed\n" {
		t.Errorf("chunks = %+v, want the output of the stopped step", got)
	}
}

func TestLogShipperBoundsPendingOutput(t *testing.T) {
	s := newTestShipper(&fakeLogSink{}, time.Hour)
	chunk := bytes.Repeat([]byte("y"), maxChunkBytes)
	for range maxPendingLogBytes/maxChunkBytes + 10 {
		_, _ = s.writer(workerapi.StreamStdout).Write(chunk)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pendingBytes > maxPendingLogBytes || s.dropped != 10*maxChunkBytes {
		t.Errorf("pending %d bytes, dropped %d", s.pendingBytes, s.dropped)
	}
}
