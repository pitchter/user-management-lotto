package worker_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/pitchter/7-solution/internal/platform/logger"
	"github.com/pitchter/7-solution/internal/worker"
)

// stubCounter counts the calls it receives and returns whatever was configured.
type stubCounter struct {
	calls atomic.Int64
	value atomic.Int64
	// The error lives in a box because atomic.Value.Store(nil) panics, which
	// would make it impossible to go back to "no error".
	err   atomic.Value // errBox
	delay time.Duration
}

// errBox wraps an error so a nil value can be stored in an atomic.Value.
type errBox struct{ err error }

func (s *stubCounter) Count(ctx context.Context) (int64, error) {
	s.calls.Add(1)

	if s.delay > 0 {
		select {
		case <-time.After(s.delay):
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}

	if v, ok := s.err.Load().(errBox); ok && v.err != nil {
		return 0, v.err
	}
	return s.value.Load(), nil
}

func (s *stubCounter) setErr(err error) { s.err.Store(errBox{err}) }

// parseLogs decodes every JSON log line.
func parseLogs(t *testing.T, raw string) []map[string]any {
	t.Helper()

	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		if line == "" {
			continue
		}
		var e map[string]any
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("the log must be JSON: %v (%q)", err, line)
		}
		out = append(out, e)
	}
	return out
}

func countMsg(entries []map[string]any, msg string) int {
	n := 0
	for _, e := range entries {
		if e["msg"] == msg {
			n++
		}
	}
	return n
}

// TestRunReportsOnEveryTick is the main test for the requirement "a
// goroutine that logs the total number of users every 10 seconds".
//
// It uses testing/synctest from the standard library, which gives the
// goroutines in this block virtual time: time.Sleep returns at once while
// the ticker sees real time pass. That makes a 10-second period testable in
// a fraction of a second, with the same result every run. A real time.Sleep
// would need at least 30 seconds and still be flaky.
func TestRunReportsOnEveryTick(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var buf bytes.Buffer
		stub := &stubCounter{}
		stub.value.Store(7)

		w := worker.NewUserCounter(stub, worker.Options{
			Interval: 10 * time.Second,
			Logger:   logger.New(&buf, logger.Options{Level: "info", Format: "json"}),
		})

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		done := make(chan error, 1)
		go func() { done <- w.Run(ctx) }()

		// Wait for the first report, which happens immediately on start.
		synctest.Wait()
		if got := stub.calls.Load(); got != 1 {
			t.Fatalf("it must report once immediately, but Count was called %d times", got)
		}

		// Move through 3 periods; that must add 3 more calls.
		for i := 0; i < 3; i++ {
			time.Sleep(10 * time.Second)
			synctest.Wait()
		}
		if got := stub.calls.Load(); got != 4 {
			t.Fatalf("after 3 periods there must be 4 calls in total, got %d", got)
		}

		// Short of the next period, nothing more may happen.
		time.Sleep(9 * time.Second)
		synctest.Wait()
		if got := stub.calls.Load(); got != 4 {
			t.Errorf("before the period elapses there must be no extra call, got %d", got)
		}

		cancel()
		synctest.Wait()
		if err := <-done; err != nil {
			t.Errorf("Run must return nil when told to stop, got %v", err)
		}

		entries := parseLogs(t, buf.String())
		if n := countMsg(entries, "user_count_report"); n != 4 {
			t.Errorf("user_count_report lines = %d, want 4", n)
		}
		if countMsg(entries, "worker_started") != 1 {
			t.Error("it must log when it starts")
		}
		if countMsg(entries, "worker_stopped") != 1 {
			t.Error("it must log when it stops")
		}

		// Check the log content: the real user count must be there.
		for _, e := range entries {
			if e["msg"] != "user_count_report" {
				continue
			}
			total, ok := e["total"].(float64)
			if !ok || int64(total) != 7 {
				t.Errorf("total = %v, want 7", e["total"])
			}
			if _, found := e[logger.KeyDuration]; !found {
				t.Error("the time taken to count should be recorded too")
			}
		}
	})
}

// TestRunUsesDefaultInterval confirms that an unset interval means the 10
// seconds the challenge asks for.
func TestRunUsesDefaultInterval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		stub := &stubCounter{}
		w := worker.NewUserCounter(stub, worker.Options{})

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() { _ = w.Run(ctx) }()

		synctest.Wait()
		time.Sleep(worker.DefaultInterval)
		synctest.Wait()

		if got := stub.calls.Load(); got != 2 {
			t.Errorf("after %v there must be 2 calls (start + one period), got %d",
				worker.DefaultInterval, got)
		}
		if worker.DefaultInterval != 10*time.Second {
			t.Errorf("DefaultInterval = %v, but the challenge asks for 10 seconds", worker.DefaultInterval)
		}
	})
}

// TestRunSurvivesCountErrors confirms a database hiccup does not kill the worker.
func TestRunSurvivesCountErrors(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var buf bytes.Buffer
		stub := &stubCounter{}
		stub.setErr(errors.New("server selection timeout"))

		w := worker.NewUserCounter(stub, worker.Options{
			Interval: time.Second,
			Logger:   logger.New(&buf, logger.Options{Level: "info", Format: "json"}),
		})

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		done := make(chan error, 1)
		go func() { done <- w.Run(ctx) }()

		synctest.Wait()
		// The first two rounds fail.
		time.Sleep(2 * time.Second)
		synctest.Wait()

		// The database comes back.
		stub.setErr(nil)
		stub.value.Store(42)
		time.Sleep(2 * time.Second)
		synctest.Wait()

		cancel()
		synctest.Wait()
		if err := <-done; err != nil {
			t.Errorf("Run must return nil, got %v", err)
		}

		entries := parseLogs(t, buf.String())
		if n := countMsg(entries, "user_count_failed"); n < 2 {
			t.Errorf("every failure must be logged (got %d)", n)
		}
		if n := countMsg(entries, "user_count_report"); n < 1 {
			t.Error("once the database is back it must report normally again")
		}
		if stub.calls.Load() < 4 {
			t.Errorf("the worker must not stop after an error (Count called %d times)", stub.calls.Load())
		}
	})
}

// TestRunStopsOnContextCancel confirms that cancelling really stops it, with
// no goroutine left behind.
//
// synctest fails the test by itself if a goroutine in this block is still
// running at the end, so this also checks for goroutine leaks.
func TestRunStopsOnContextCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		stub := &stubCounter{}
		w := worker.NewUserCounter(stub, worker.Options{Interval: time.Second})

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- w.Run(ctx) }()

		synctest.Wait()
		cancel()
		synctest.Wait()

		select {
		case err := <-done:
			if err != nil {
				t.Errorf("it must return nil, got %v", err)
			}
		default:
			t.Fatal("Run must finish as soon as it is cancelled")
		}

		// Nothing more may be called once it has stopped.
		before := stub.calls.Load()
		time.Sleep(5 * time.Second)
		synctest.Wait()
		if after := stub.calls.Load(); after != before {
			t.Errorf("after stopping there must be no further call, but it went from %d to %d", before, after)
		}
	})
}

// TestRunCancelsSlowCount confirms a long-stalled count is cancelled by the
// per-round timeout, instead of piling rounds on top of each other.
func TestRunCancelsSlowCount(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var buf bytes.Buffer
		stub := &stubCounter{delay: time.Hour} // far longer than the timeout

		w := worker.NewUserCounter(stub, worker.Options{
			Interval: 10 * time.Second,
			Timeout:  2 * time.Second,
			Logger:   logger.New(&buf, logger.Options{Level: "info", Format: "json"}),
		})

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() { _ = w.Run(ctx) }()

		synctest.Wait()
		time.Sleep(3 * time.Second) // past the first round's timeout
		synctest.Wait()

		entries := parseLogs(t, buf.String())
		if countMsg(entries, "user_count_failed") == 0 {
			t.Errorf("a count over the timeout must be cancelled and logged: %v", entries)
		}

		cancel()
		synctest.Wait()
	})
}

func TestNewUserCounterDefaults(t *testing.T) {
	t.Parallel()

	// Unusable values must be clamped to something safe, never panic.
	if w := worker.NewUserCounter(&stubCounter{}, worker.Options{Interval: -1}); w == nil {
		t.Fatal("it must build even with a negative interval")
	}
	if w := worker.NewUserCounter(&stubCounter{}, worker.Options{Timeout: -1}); w == nil {
		t.Fatal("it must build even with a negative timeout")
	}
}
