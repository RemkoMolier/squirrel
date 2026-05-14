package certs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/go-logr/logr"
)

// DefaultRotationInterval is the design's recommended period between
// rotation checks. Rotation only fires when a certificate is within
// the 30-day threshold of expiry (or fails the bundle-vs-opts
// validation), so checking every hour is overkill but cheap - each
// check is an in-memory CA verify plus an in-cache Secret read when
// the bundle is current.
const DefaultRotationInterval = time.Hour

// AuthoritativeRunnable is a controller-runtime manager.Runnable that
// drives CertSource.Authoritative on a periodic ticker. It opts INTO
// leader election so only one replica writes to the apiserver at a
// time; concurrent rotations across replicas cannot race the
// Secret's resourceVersion check or the MWC patch.
//
// Bootstrap is handled separately in internal/manager/run.go: every
// replica runs an initial Authoritative there (race-tolerant via
// apiserver-serialised Create + AlreadyExists short circuit), then
// the AuthoritativeRunnable's first leader-elected tick performs the
// next idempotent rotation check.
type AuthoritativeRunnable struct {
	source   CertSource
	interval time.Duration
	log      logr.Logger
}

// NewAuthoritativeRunnable validates inputs and returns a configured
// runnable. source is the CertSource the loop drives; interval is
// the cadence between rotation checks; log is the logger used to
// report tick failures.
func NewAuthoritativeRunnable(source CertSource, interval time.Duration, log logr.Logger) (*AuthoritativeRunnable, error) {
	if source == nil {
		return nil, errors.New("AuthoritativeRunnable: source must not be nil")
	}
	if interval <= 0 {
		return nil, fmt.Errorf("AuthoritativeRunnable: interval (%s) must be positive", interval)
	}
	return &AuthoritativeRunnable{source: source, interval: interval, log: log}, nil
}

// NeedLeaderElection implements controller-runtime's
// LeaderElectionRunnable interface. Returns true because only the
// elected leader should write to the apiserver-side Secret + MWC.
func (r *AuthoritativeRunnable) NeedLeaderElection() bool { return true }

// Start implements controller-runtime's manager.Runnable. Loops on a
// time.Ticker firing every interval, calling source.Authoritative on
// each tick. Errors are logged at the Error level and the loop
// continues; only context cancellation exits the loop.
//
// There is no synchronous initial Authoritative call here: bootstrap
// already did one before mgr.Start, so the leader's first tick is
// purely a rotation-check pass.
func (r *AuthoritativeRunnable) Start(ctx context.Context) error {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := r.source.Authoritative(ctx); err != nil {
				// A tick failure is operationally noisy but not fatal:
				// the existing Secret + MWC bundle is still in place,
				// so the webhook keeps serving while the next tick
				// retries. A transient resourceVersion Conflict
				// (e.g. an admin edited the Secret) bubbles up here;
				// the next tick resolves it.
				r.log.Error(err, "cert authoritative pass failed; will retry on next tick")
			}
		}
	}
}

// LocalSyncRunnable is a controller-runtime manager.Runnable that
// drives CertSource.Localize on a periodic ticker. Every replica
// must run this so its on-disk CertDir reflects the latest Secret
// the leader wrote; the runnable opts OUT of leader election.
//
// Read-only against the apiserver, so no race against concurrent
// replicas.
type LocalSyncRunnable struct {
	source   CertSource
	interval time.Duration
	log      logr.Logger
}

// NewLocalSyncRunnable validates inputs and returns a configured
// runnable. source is the CertSource the loop drives; interval is
// the cadence between local-sync checks; log is the logger used to
// report tick failures.
func NewLocalSyncRunnable(source CertSource, interval time.Duration, log logr.Logger) (*LocalSyncRunnable, error) {
	if source == nil {
		return nil, errors.New("LocalSyncRunnable: source must not be nil")
	}
	if interval <= 0 {
		return nil, fmt.Errorf("LocalSyncRunnable: interval (%s) must be positive", interval)
	}
	return &LocalSyncRunnable{source: source, interval: interval, log: log}, nil
}

// NeedLeaderElection implements controller-runtime's
// LeaderElectionRunnable interface. Returns false: every replica
// needs its own CertDir current so the local webhook server can
// load TLS.
func (r *LocalSyncRunnable) NeedLeaderElection() bool { return false }

// Start implements controller-runtime's manager.Runnable. Drives
// source.Localize on a self-adjusting timer: the steady-state cadence
// is interval, but consecutive failures back off from
// localSyncErrorRetryFloor towards interval so a transient apiserver
// blip recovers in tens of seconds rather than waiting out the full
// hour-long rotation cadence. This bounds the staleness window after
// a CA rotation on the leader: a follower that observed the leader's
// MWC caBundle change but failed to read the new Secret will retry
// soon enough to keep its on-disk TLS in step.
//
// There is no synchronous initial Localize call here: bootstrap
// already did one before mgr.Start so the webhook server has TLS
// material on disk. The runnable's job is to keep CertDir in sync
// with subsequent leader rotations.
func (r *LocalSyncRunnable) Start(ctx context.Context) error {
	consecutiveErrors := 0
	for {
		delay := nextLocalSyncDelay(consecutiveErrors, r.interval)
		t := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil
		case <-t.C:
		}
		if err := r.source.Localize(ctx); err != nil {
			consecutiveErrors++
			next := nextLocalSyncDelay(consecutiveErrors, r.interval)
			r.log.Error(err, "cert local-sync pass failed; will retry", "next-attempt-in", next.String(), "consecutive-errors", consecutiveErrors)
			continue
		}
		consecutiveErrors = 0
	}
}

// localSyncErrorRetryFloor is the shortest delay used after a failed
// Localize. Doubles on each subsequent consecutive failure until it
// reaches the normal rotation interval, at which point the loop is
// back to steady-state cadence.
const localSyncErrorRetryFloor = 30 * time.Second

// nextLocalSyncDelay computes how long to wait before the next
// Localize attempt. With zero consecutive errors the normal interval
// is used; on a streak of N errors the delay is
// localSyncErrorRetryFloor * 2^(N-1), capped at interval so a
// permanent failure does not become more eager than steady state.
func nextLocalSyncDelay(consecutiveErrors int, interval time.Duration) time.Duration {
	if consecutiveErrors == 0 {
		return interval
	}
	if consecutiveErrors > 30 {
		// Defence against pathological streaks: cap the shift count
		// before the left-shift could overflow int64.
		return interval
	}
	d := localSyncErrorRetryFloor << (consecutiveErrors - 1)
	if d <= 0 || d > interval {
		return interval
	}
	return d
}
