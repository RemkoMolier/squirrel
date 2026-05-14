package certs_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr/testr"

	"github.com/RemkoMolier/squirrel/internal/certs"
)

// fakeCertSource is the test double for CertSource. Every
// Authoritative / Localize call increments its own counter under a
// mutex; tick-error injection is per-method so the tests can
// exercise the two runnables independently.
type fakeCertSource struct {
	mu            sync.Mutex
	authCalls     int
	localizeCalls int
	authErr       error
	localizeErr   error
}

func (f *fakeCertSource) Authoritative(_ context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.authCalls++
	return f.authErr
}

func (f *fakeCertSource) Localize(_ context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.localizeCalls++
	return f.localizeErr
}

func (f *fakeCertSource) CertDir() string { return "/dev/null" }

func (f *fakeCertSource) AuthCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.authCalls
}

func (f *fakeCertSource) LocalizeCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.localizeCalls
}

func TestNewAuthoritativeRunnableRejectsBadArguments(t *testing.T) {
	t.Parallel()

	src := &fakeCertSource{}
	tests := []struct {
		name     string
		source   certs.CertSource
		interval time.Duration
	}{
		{name: "nil source", source: nil, interval: time.Second},
		{name: "zero interval", source: src, interval: 0},
		{name: "negative interval", source: src, interval: -time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, err := certs.NewAuthoritativeRunnable(tt.source, tt.interval, testr.New(t)); err == nil {
				t.Errorf("expected error, got nil")
			}
		})
	}
}

func TestNewLocalSyncRunnableRejectsBadArguments(t *testing.T) {
	t.Parallel()

	src := &fakeCertSource{}
	tests := []struct {
		name     string
		source   certs.CertSource
		interval time.Duration
	}{
		{name: "nil source", source: nil, interval: time.Second},
		{name: "zero interval", source: src, interval: 0},
		{name: "negative interval", source: src, interval: -time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, err := certs.NewLocalSyncRunnable(tt.source, tt.interval, testr.New(t)); err == nil {
				t.Errorf("expected error, got nil")
			}
		})
	}
}

// TestAuthoritativeRunnableNeedLeaderElectionTrue pins the design
// contract that the apiserver-writing runnable runs only on the
// elected leader. Flipping this to false would re-introduce the
// concurrent-replica race the split was designed to eliminate.
func TestAuthoritativeRunnableNeedLeaderElectionTrue(t *testing.T) {
	t.Parallel()

	r, err := certs.NewAuthoritativeRunnable(&fakeCertSource{}, time.Second, testr.New(t))
	if err != nil {
		t.Fatalf("NewAuthoritativeRunnable: %v", err)
	}
	if !r.NeedLeaderElection() {
		t.Errorf("NeedLeaderElection: got false, want true (only the leader writes apiserver state)")
	}
}

// TestLocalSyncRunnableNeedLeaderElectionFalse pins the design
// contract that every replica keeps its own CertDir current. If
// this returned true a follower's webhook server would never see
// the leader's rotation reflected to disk.
func TestLocalSyncRunnableNeedLeaderElectionFalse(t *testing.T) {
	t.Parallel()

	r, err := certs.NewLocalSyncRunnable(&fakeCertSource{}, time.Second, testr.New(t))
	if err != nil {
		t.Fatalf("NewLocalSyncRunnable: %v", err)
	}
	if r.NeedLeaderElection() {
		t.Errorf("NeedLeaderElection: got true, want false (every replica must keep its local cert dir current)")
	}
}

// TestAuthoritativeRunnableStartCallsAuthoritativeOnTicks waits
// long enough for a handful of ticks, cancels the context to drain
// Start, and confirms the call count is in the expected range.
func TestAuthoritativeRunnableStartCallsAuthoritativeOnTicks(t *testing.T) {
	t.Parallel()

	src := &fakeCertSource{}
	r, err := certs.NewAuthoritativeRunnable(src, 20*time.Millisecond, testr.New(t))
	if err != nil {
		t.Fatalf("NewAuthoritativeRunnable: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Start(ctx) }()

	time.Sleep(150 * time.Millisecond)
	cancel()

	select {
	case startErr := <-done:
		if startErr != nil {
			t.Errorf("Start: got %v, want nil on graceful cancel", startErr)
		}
	case <-time.After(time.Second):
		t.Fatalf("Start did not return after context cancel")
	}

	if got := src.AuthCalls(); got < 2 {
		t.Errorf("Authoritative call count: got %d, want >= 2 (at least two ticks)", got)
	}
}

// TestLocalSyncRunnableStartCallsLocalizeOnTicks mirrors the
// authoritative test for the every-replica runnable.
func TestLocalSyncRunnableStartCallsLocalizeOnTicks(t *testing.T) {
	t.Parallel()

	src := &fakeCertSource{}
	r, err := certs.NewLocalSyncRunnable(src, 20*time.Millisecond, testr.New(t))
	if err != nil {
		t.Fatalf("NewLocalSyncRunnable: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Start(ctx) }()

	time.Sleep(150 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("Start did not return after context cancel")
	}

	if got := src.LocalizeCalls(); got < 2 {
		t.Errorf("Localize call count: got %d, want >= 2 (at least two ticks)", got)
	}
}

// TestAuthoritativeRunnableContinuesAfterTickError pins the contract
// that a transient apiserver failure on a single tick does not
// crash the loop. Returning an error on every tick simulates a
// persistent failure; the loop must keep firing.
func TestAuthoritativeRunnableContinuesAfterTickError(t *testing.T) {
	t.Parallel()

	src := &fakeCertSource{authErr: errors.New("simulated tick failure")}
	r, err := certs.NewAuthoritativeRunnable(src, 20*time.Millisecond, testr.New(t))
	if err != nil {
		t.Fatalf("NewAuthoritativeRunnable: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Start(ctx) }()

	time.Sleep(150 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("Start did not return after context cancel")
	}

	if got := src.AuthCalls(); got < 2 {
		t.Errorf("Authoritative call count: got %d, want >= 2 (loop must keep ticking through errors)", got)
	}
}

// TestLocalSyncRunnableContinuesAfterTickError mirrors the
// authoritative test for the every-replica runnable.
func TestLocalSyncRunnableContinuesAfterTickError(t *testing.T) {
	t.Parallel()

	src := &fakeCertSource{localizeErr: errors.New("simulated tick failure")}
	r, err := certs.NewLocalSyncRunnable(src, 20*time.Millisecond, testr.New(t))
	if err != nil {
		t.Fatalf("NewLocalSyncRunnable: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Start(ctx) }()

	time.Sleep(150 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("Start did not return after context cancel")
	}

	if got := src.LocalizeCalls(); got < 2 {
		t.Errorf("Localize call count: got %d, want >= 2 (loop must keep ticking through errors)", got)
	}
}

// TestAuthoritativeRunnableStartReturnsOnContextCancel covers the
// graceful-shutdown contract: when the manager cancels the context,
// Start returns nil within a reasonable timeout.
func TestAuthoritativeRunnableStartReturnsOnContextCancel(t *testing.T) {
	t.Parallel()

	src := &fakeCertSource{}
	r, err := certs.NewAuthoritativeRunnable(src, time.Hour, testr.New(t))
	if err != nil {
		t.Fatalf("NewAuthoritativeRunnable: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Start(ctx) }()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case startErr := <-done:
		if startErr != nil {
			t.Errorf("Start: got %v, want nil on graceful cancel", startErr)
		}
	case <-time.After(time.Second):
		t.Fatalf("Start did not return after context cancel")
	}
}

// TestLocalSyncRunnableStartReturnsOnContextCancel mirrors the
// authoritative test for the every-replica runnable.
func TestLocalSyncRunnableStartReturnsOnContextCancel(t *testing.T) {
	t.Parallel()

	src := &fakeCertSource{}
	r, err := certs.NewLocalSyncRunnable(src, time.Hour, testr.New(t))
	if err != nil {
		t.Fatalf("NewLocalSyncRunnable: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Start(ctx) }()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case startErr := <-done:
		if startErr != nil {
			t.Errorf("Start: got %v, want nil on graceful cancel", startErr)
		}
	case <-time.After(time.Second):
		t.Fatalf("Start did not return after context cancel")
	}
}
