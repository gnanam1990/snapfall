// Package logging threads correlation IDs through every log line (G1, NFR-010).
//
// The five correlation dimensions — org, job, task, intent, advance — ride on the
// context.Context. Any code that logs pulls a logger with logging.From(ctx) and every
// line it emits carries whatever IDs are in scope, so a grep for job_id=job_104 shows
// the whole story of that job across Brain, Workers, store, and supervisor.
package logging

import (
	"context"
	"log/slog"
)

type ctxKey struct{}

// Correlation is the set of IDs in scope for a unit of work.
// Zero-valued fields are omitted from log output.
type Correlation struct {
	Org     string
	Job     string
	Task    string
	Intent  string
	Advance string
}

// merge returns c overlaid with any non-empty fields of o.
func (c Correlation) merge(o Correlation) Correlation {
	if o.Org != "" {
		c.Org = o.Org
	}
	if o.Job != "" {
		c.Job = o.Job
	}
	if o.Task != "" {
		c.Task = o.Task
	}
	if o.Intent != "" {
		c.Intent = o.Intent
	}
	if o.Advance != "" {
		c.Advance = o.Advance
	}
	return c
}

// With returns a context carrying the given correlation IDs, merged over any
// already present — setting Job does not erase an Org set upstream.
func With(ctx context.Context, c Correlation) context.Context {
	if cur, ok := ctx.Value(ctxKey{}).(Correlation); ok {
		c = cur.merge(c)
	}
	return context.WithValue(ctx, ctxKey{}, c)
}

// WithJob is the common case: scope a context to one job.
func WithJob(ctx context.Context, jobID string) context.Context {
	return With(ctx, Correlation{Job: jobID})
}

// FromContext extracts the correlation IDs in scope, if any.
func FromContext(ctx context.Context) Correlation {
	c, _ := ctx.Value(ctxKey{}).(Correlation)
	return c
}

// From returns base with the context's correlation IDs attached as attributes.
// This is the one call sites use: logging.From(ctx, log).Info(...).
func From(ctx context.Context, base *slog.Logger) *slog.Logger {
	c := FromContext(ctx)
	attrs := make([]any, 0, 10)
	if c.Org != "" {
		attrs = append(attrs, "org_id", c.Org)
	}
	if c.Job != "" {
		attrs = append(attrs, "job_id", c.Job)
	}
	if c.Task != "" {
		attrs = append(attrs, "task_id", c.Task)
	}
	if c.Intent != "" {
		attrs = append(attrs, "intent_id", c.Intent)
	}
	if c.Advance != "" {
		attrs = append(attrs, "advance_id", c.Advance)
	}
	if len(attrs) == 0 {
		return base
	}
	return base.With(attrs...)
}
