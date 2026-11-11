// Package worker holds the background jobs that run alongside the API.
package worker

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"user-management-api/internal/platform/logger"
)

// DefaultInterval is the period the challenge asks for.
const DefaultInterval = 10 * time.Second

// Counter is the only thing this worker needs: a way to count users.
//
// Declaring a narrow interface on the consumer side, instead of taking the
// whole UserRepository, makes it obvious that the worker can only read a
// number and has no way to change any data.
type Counter interface {
	Count(ctx context.Context) (int64, error)
}

// UserCounter logs the total number of users at a fixed interval.
type UserCounter struct {
	counter  Counter
	interval time.Duration
	// timeout is the ceiling for a single count.
	timeout time.Duration
	log     *slog.Logger
}

// Options tweaks a UserCounter.
type Options struct {
	// Interval is the period between reports (zero means DefaultInterval).
	Interval time.Duration
	// Timeout is the ceiling per round (zero means half of Interval).
	Timeout time.Duration
	Logger  *slog.Logger
}

// NewUserCounter builds the worker.
func NewUserCounter(c Counter, opts Options) *UserCounter {
	if opts.Interval <= 0 {
		opts.Interval = DefaultInterval
	}
	if opts.Timeout <= 0 {
		// Keep the timeout shorter than the period so a stalled count is
		// cancelled before the next tick, instead of piling work up.
		opts.Timeout = opts.Interval / 2
	}
	if opts.Logger == nil {
		opts.Logger = logger.Discard()
	}

	return &UserCounter{
		counter:  c,
		interval: opts.Interval,
		timeout:  opts.Timeout,
		log:      opts.Logger.With(slog.String(logger.KeyComponent, "user_counter")),
	}
}

// Run works until ctx is cancelled.
//
// The behaviour is deliberate:
//   - it reports once immediately on start, then every interval, so there is
//     no 10-second wait before you can tell the worker is alive;
//   - a failed count is logged and the loop moves on rather than dying: a
//     brief database hiccup should not stop a background job for good;
//   - it returns nil when told to stop, because an ordered stop is not a
//     failure. That matters when it runs inside an errgroup during shutdown.
func (w *UserCounter) Run(ctx context.Context) error {
	w.log.InfoContext(ctx, "worker_started", slog.String("interval", w.interval.String()))

	w.reportOnce(ctx)

	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			w.log.InfoContext(ctx, "worker_stopped")
			return nil
		case <-ticker.C:
			w.reportOnce(ctx)
		}
	}
}

// reportOnce performs one count and logs the outcome.
func (w *UserCounter) reportOnce(ctx context.Context) {
	// If we were stopped meanwhile, do not bother issuing the query.
	if ctx.Err() != nil {
		return
	}

	opCtx, cancel := context.WithTimeout(ctx, w.timeout)
	defer cancel()

	start := time.Now()
	total, err := w.counter.Count(opCtx)
	if err != nil {
		// Cancellation during shutdown is not a failure worth reporting.
		if errors.Is(err, context.Canceled) {
			return
		}
		w.log.ErrorContext(ctx, "user_count_failed",
			slog.String(logger.KeyError, err.Error()),
			slog.Float64(logger.KeyDuration, float64(time.Since(start).Microseconds())/1000.0),
		)
		return
	}

	w.log.InfoContext(ctx, "user_count_report",
		slog.Int64("total", total),
		slog.Float64(logger.KeyDuration, float64(time.Since(start).Microseconds())/1000.0),
	)
}
