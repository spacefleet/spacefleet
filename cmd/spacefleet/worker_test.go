package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeStopper records the shutdown calls it receives so the tests can pin
// their order and arguments. onStop (optional) runs at the moment Stop is
// invoked, for asserting what has already happened by then.
type fakeStopper struct {
	stopErr error
	onStop  func(ctx context.Context)

	calls []string
}

func (f *fakeStopper) Stop(ctx context.Context) error {
	f.calls = append(f.calls, "Stop")
	if f.onStop != nil {
		f.onStop(ctx)
	}
	return f.stopErr
}

func (f *fakeStopper) StopAndCancel(_ context.Context) error {
	f.calls = append(f.calls, "StopAndCancel")
	return nil
}

// TestShutdownWorkerGraceful pins the H1 fix: client.Stop runs with a live
// drain window (the old code cancelled the jobs' root context before Stop,
// making the graceful drain dead code), the auxiliary loops are stopped
// before the drain, and a clean Stop never escalates to StopAndCancel.
func TestShutdownWorkerGraceful(t *testing.T) {
	loopCtx, loopCancel := context.WithCancel(context.Background())

	var loopsDownAtStop, drainWindowLive bool
	f := &fakeStopper{onStop: func(ctx context.Context) {
		loopsDownAtStop = loopCtx.Err() != nil
		d, ok := ctx.Deadline()
		drainWindowLive = ok && time.Until(d) > 0
	}}

	shutdownWorker(f, loopCancel, time.Second, time.Second)

	if !loopsDownAtStop {
		t.Error("auxiliary loops were not stopped before client.Stop")
	}
	if !drainWindowLive {
		t.Error("client.Stop ran without a live drain window — the graceful stop is dead code again")
	}
	if len(f.calls) != 1 || f.calls[0] != "Stop" {
		t.Errorf("calls = %v, want [Stop] (no StopAndCancel on clean stop)", f.calls)
	}
}

// TestShutdownWorkerEscalates pins the hard-stop fallback: when the graceful
// drain window expires, shutdown escalates to StopAndCancel — bounding the
// shutdown while still letting job recovery defers run — rather than leaving
// jobs running into process exit.
func TestShutdownWorkerEscalates(t *testing.T) {
	f := &fakeStopper{stopErr: context.DeadlineExceeded}
	_, loopCancel := context.WithCancel(context.Background())

	shutdownWorker(f, loopCancel, time.Millisecond, time.Second)

	if len(f.calls) != 2 || f.calls[0] != "Stop" || f.calls[1] != "StopAndCancel" {
		t.Errorf("calls = %v, want [Stop StopAndCancel]", f.calls)
	}
}

// TestShutdownWorkerEscalatesOnAnyStopError covers the non-timeout failure
// path: any Stop error must still hand in-flight jobs a bounded hard stop.
func TestShutdownWorkerEscalatesOnAnyStopError(t *testing.T) {
	f := &fakeStopper{stopErr: errors.New("notifier wedged")}
	_, loopCancel := context.WithCancel(context.Background())

	shutdownWorker(f, loopCancel, time.Second, time.Second)

	if len(f.calls) != 2 || f.calls[1] != "StopAndCancel" {
		t.Errorf("calls = %v, want Stop then StopAndCancel", f.calls)
	}
}
