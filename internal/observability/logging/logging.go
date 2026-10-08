// Package logging builds the structured slog loggers used by Tsuzuku processes.
package logging

import (
	"io"
	"log/slog"

	"github.com/Khantdotcom/tsuzuku-runner/internal/config"
)

// New returns a logger that writes human-readable text in development and
// JSON everywhere else, tagged with the emitting service name.
func New(w io.Writer, env config.Environment, level slog.Level, service string) *slog.Logger {
	opts := &slog.HandlerOptions{Level: level}

	var handler slog.Handler
	if env == config.Development {
		handler = slog.NewTextHandler(w, opts)
	} else {
		handler = slog.NewJSONHandler(w, opts)
	}

	return slog.New(handler).With(slog.String("service", service))
}
