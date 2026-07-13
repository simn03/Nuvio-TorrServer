// Package logging configures the process-wide slog logger and threads a
// per-request id through context so every log line emitted while handling a
// request — across cinemeta, prowlarr, resolver, and torrserver — can be
// correlated back to it, which is what makes the pipeline profileable.
package logging

import (
	"context"
	"log/slog"
	"os"
	"strings"
)

type reqIDKey struct{}

// WithRequestID attaches a request id to ctx. Downstream slog.*Context calls
// made with the returned context automatically carry it as a "req_id" attr.
func WithRequestID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, reqIDKey{}, id)
}

// requestIDFromContext returns the id stashed by WithRequestID, if any.
func requestIDFromContext(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(reqIDKey{}).(string)
	return id, ok && id != ""
}

// ctxHandler wraps a slog.Handler and injects the context's request id (when
// present) as an attribute on every record, so callers never have to thread
// it through manually.
type ctxHandler struct {
	slog.Handler
}

func (h ctxHandler) Handle(ctx context.Context, r slog.Record) error {
	if id, ok := requestIDFromContext(ctx); ok {
		r.AddAttrs(slog.String("req_id", id))
	}
	return h.Handler.Handle(ctx, r)
}

func (h ctxHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return ctxHandler{h.Handler.WithAttrs(attrs)}
}

func (h ctxHandler) WithGroup(name string) slog.Handler {
	return ctxHandler{h.Handler.WithGroup(name)}
}

// Setup builds and installs the process-wide slog logger. level is one of
// "debug"|"info"|"warn"|"error" (default "info" on empty/unknown); format is
// "json" or anything else for human-readable text. Logs go to stderr, per
// container logging convention (§ supervisord routes stderr_logfile).
func Setup(level, format string) *slog.Logger {
	opts := &slog.HandlerOptions{Level: parseLevel(level)}

	var base slog.Handler
	if strings.EqualFold(format, "json") {
		base = slog.NewJSONHandler(os.Stderr, opts)
	} else {
		base = slog.NewTextHandler(os.Stderr, opts)
	}

	logger := slog.New(ctxHandler{base})
	slog.SetDefault(logger)
	return logger
}

func parseLevel(level string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
