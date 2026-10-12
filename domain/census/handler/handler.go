package handler

import (
	"context"
	"errors"
	"log/slog"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// Handler processes a single queue job's payload and returns the jobs to publish
// next (downstream chaining). A non-nil error signals a transient failure; the
// worker maps it to Queue.Retry.
type Handler interface {
	Handle(ctx context.Context, payload []byte) ([]contract.QueueJob, error)
}

// ErrNoWriter is returned by a census handler built without a result writer.
// Handlers never store anything themselves: every character, job and
// achievement goes through contract.ResultWriter so the worker acks a job only
// after its batch has committed. The job fails and is retried rather than
// writing outside that path.
var ErrNoWriter = errors.New("result writer unconfigured on this worker")

// discardLogger is a no-op logger whose Enabled always returns false,
// allowing callers to skip expensive attribute construction.
type discardLogger struct{}

func (discardLogger) DebugContext(context.Context, string, ...any) {}
func (discardLogger) InfoContext(context.Context, string, ...any)  {}
func (discardLogger) WarnContext(context.Context, string, ...any)  {}
func (discardLogger) ErrorContext(context.Context, string, ...any) {}
func (discardLogger) Enabled(context.Context, slog.Level) bool     { return false }

// loggerOrDiscard returns l, or a discard logger when l is nil, so handlers
// never require a non-nil logger.
func loggerOrDiscard(l contract.Logger) contract.Logger {
	if l == nil {
		return discardLogger{}
	}
	return l
}

// Registry maps event types to their handlers.
type Registry struct {
	handlers map[string]Handler
}

func NewRegistry() *Registry {
	return &Registry{handlers: map[string]Handler{}}
}

func (r *Registry) Register(eventType string, h Handler) {
	r.handlers[eventType] = h
}

func (r *Registry) Get(eventType string) (Handler, bool) {
	h, ok := r.handlers[eventType]
	return h, ok
}
