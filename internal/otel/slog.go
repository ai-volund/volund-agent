package otel

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/otel/trace"
)

// TraceSlogHandler wraps a slog.Handler and adds trace_id and span_id
// attributes from the span context in ctx.
type TraceSlogHandler struct {
	inner slog.Handler
}

// NewTraceSlogHandler wraps an existing handler with trace context enrichment.
func NewTraceSlogHandler(inner slog.Handler) *TraceSlogHandler {
	return &TraceSlogHandler{inner: inner}
}

func (h *TraceSlogHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *TraceSlogHandler) Handle(ctx context.Context, r slog.Record) error {
	sc := trace.SpanContextFromContext(ctx)
	if sc.HasTraceID() {
		r.AddAttrs(slog.String("trace_id", sc.TraceID().String()))
	}
	if sc.HasSpanID() {
		r.AddAttrs(slog.String("span_id", sc.SpanID().String()))
	}
	return h.inner.Handle(ctx, r)
}

func (h *TraceSlogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &TraceSlogHandler{inner: h.inner.WithAttrs(attrs)}
}

func (h *TraceSlogHandler) WithGroup(name string) slog.Handler {
	return &TraceSlogHandler{inner: h.inner.WithGroup(name)}
}
