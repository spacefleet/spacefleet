package k8s

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"
	toolswatch "k8s.io/client-go/tools/watch"
)

// These tests drive a FAKE watch (client-go fake clientset + watch.FakeWatcher)
// through the exact RetryWatcher + drainWatch loop that WatchNodes runs inline,
// without standing up a real API server. WatchNodes builds its own clientset
// from a Connection, so we re-create its goroutine body here against an injected
// fake clientset — the loop under test (NewRetryWatcherWithContext, drainWatch,
// re-list resync on a 410 Gone, terminal close) is the same code path.

// node returns a corev1.Node carrying a resourceVersion so the RetryWatcher can
// track it across reconnects.
func node(name, rv string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, ResourceVersion: rv},
	}
}

// runNodeWatchLoop mirrors the goroutine inside WatchNodes: it drives a
// RetryWatcher built from lw, draining deltas into events, re-listing and
// emitting a fresh EventSnapshot whenever the RetryWatcher gives up (e.g. a 410
// Gone), and stops when ctx is cancelled. Splitting it out lets a test inject a
// fake clientset; the body is a copy of WatchNodes's loop.
func runNodeWatchLoop(ctx context.Context, cs kubernetes.Interface, startRV string, lw *cache.ListWatch, events chan<- Event[Node]) {
	defer close(events)
	currentRV := startRV
	for {
		rw, err := toolswatch.NewRetryWatcherWithContext(ctx, currentRV, lw)
		if err != nil {
			return
		}
		ctxDone := drainWatch(ctx, rw.ResultChan(), events)
		rw.Stop()
		if ctxDone {
			return
		}
		snap, newRV, err := listNodesRV(ctx, cs)
		if err != nil {
			return
		}
		currentRV = newRV
		select {
		case events <- Event[Node]{Type: EventSnapshot, Snapshot: snap}:
		case <-ctx.Done():
			return
		}
	}
}

// fakeWithWatcher returns a fake clientset whose node Watch calls all return the
// supplied FakeWatcher, so a test fully controls the watch stream (deltas and
// injected errors). The clientset's object tracker still backs List, so the
// re-list resync after a 410 reads whatever objects were loaded into the fake.
func fakeWithWatcher(fw *watch.FakeWatcher, objs ...*corev1.Node) *fake.Clientset {
	runtimeObjs := make([]runtime.Object, 0, len(objs))
	for _, o := range objs {
		runtimeObjs = append(runtimeObjs, o)
	}
	cs := fake.NewSimpleClientset(runtimeObjs...)
	cs.PrependWatchReactor("nodes", func(clienttesting.Action) (bool, watch.Interface, error) {
		return true, fw, nil
	})
	return cs
}

// drainEvents reads up to want events from ch (or until ctx/timeout), returning what
// it got. Used to assert the sequence of events a watch produces.
func drainEvents(t *testing.T, ch <-chan Event[Node], want int) []Event[Node] {
	t.Helper()
	var got []Event[Node]
	timeout := time.After(2 * time.Second)
	for len(got) < want {
		select {
		case ev, ok := <-ch:
			if !ok {
				return got
			}
			got = append(got, ev)
		case <-timeout:
			t.Fatalf("timed out waiting for events: got %d, want %d", len(got), want)
		}
	}
	return got
}

func newListWatch(cs kubernetes.Interface) *cache.ListWatch {
	return &cache.ListWatch{
		WatchFuncWithContext: func(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
			return cs.CoreV1().Nodes().Watch(ctx, opts)
		},
	}
}

// TestWatchResyncOn410Gone is the core case: an established watch streams a
// delta, then the server expires the resourceVersion (410 Gone). The
// RetryWatcher must NOT retry that — it stops, and our loop re-lists and emits a
// fresh EventSnapshot so a client that missed deletes self-heals.
func TestWatchResyncOn410Gone(t *testing.T) {
	fw := watch.NewFake()
	// After the 410, the re-list should see node "b" (added to the tracker
	// before the resync runs).
	cs := fakeWithWatcher(fw, node("b", "200"))

	events := make(chan Event[Node])
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runNodeWatchLoop(ctx, cs, "100", newListWatch(cs), events)

	// First a normal delta on the live watch.
	fw.Add(node("a", "101"))
	// Then the server expires our resourceVersion. RetryWatcher forwards this
	// single Error event and then stops (closes its result chan).
	fw.Error(&apierrors.NewResourceExpired("too old resource version").ErrStatus)

	got := drainEvents(t, events, 2)
	if len(got) != 2 {
		t.Fatalf("got %d events, want 2: %+v", len(got), got)
	}
	if got[0].Type != EventAdded || got[0].Object.Name != "a" {
		t.Errorf("first event = %+v, want Added node a", got[0])
	}
	// The 410 must trigger a re-list + fresh snapshot, not a retry.
	if got[1].Type != EventSnapshot {
		t.Fatalf("second event = %s, want %s (resync snapshot)", got[1].Type, EventSnapshot)
	}
	if len(got[1].Snapshot) != 1 || got[1].Snapshot[0].Name != "b" {
		t.Errorf("resync snapshot = %+v, want [b]", got[1].Snapshot)
	}

	cancel()
}

// TestWatchStopsOnContextCancel asserts terminal behavior: cancelling the
// context ends the loop and closes the events channel, with no resync attempted.
func TestWatchStopsOnContextCancel(t *testing.T) {
	fw := watch.NewFake()
	cs := fakeWithWatcher(fw)

	events := make(chan Event[Node])
	ctx, cancel := context.WithCancel(context.Background())
	go runNodeWatchLoop(ctx, cs, "100", newListWatch(cs), events)

	// Deliver one delta so we know the watch is live, then cancel.
	fw.Add(node("a", "101"))
	got := drainEvents(t, events, 1)
	if len(got) != 1 || got[0].Type != EventAdded {
		t.Fatalf("expected one Added event before cancel, got %+v", got)
	}

	cancel()

	// The events channel must close (no further events, no resync snapshot).
	select {
	case ev, ok := <-events:
		if ok {
			t.Fatalf("expected events channel to close after cancel, got %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("events channel did not close after context cancel")
	}
}

// TestDrainWatchForwardsDeltas exercises drainWatch directly: Added/Modified/
// Deleted map to their EventType, while non-delta events (Bookmark) are skipped,
// and a closed input channel returns false (signal to resync) without marking
// the context as done.
func TestDrainWatchForwardsDeltas(t *testing.T) {
	in := make(chan watch.Event, 8)
	out := make(chan Event[Node], 8)
	ctx := context.Background()

	in <- watch.Event{Type: watch.Added, Object: node("a", "1")}
	in <- watch.Event{Type: watch.Bookmark, Object: node("a", "2")} // skipped
	in <- watch.Event{Type: watch.Modified, Object: node("a", "3")}
	in <- watch.Event{Type: watch.Deleted, Object: node("a", "4")}
	close(in)

	ctxDone := drainWatch(ctx, in, out)
	if ctxDone {
		t.Error("drainWatch returned ctxDone=true on a closed input; want false (resync)")
	}
	close(out)

	var types []EventType
	for ev := range out {
		types = append(types, ev.Type)
	}
	want := []EventType{EventAdded, EventModified, EventDeleted}
	if len(types) != len(want) {
		t.Fatalf("forwarded %v, want %v", types, want)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Errorf("event[%d] = %s, want %s", i, types[i], want[i])
		}
	}
}

// TestDrainWatchSkipsErrorEvent confirms drainWatch treats a watch.Error event
// as a non-delta (skipped, not forwarded as garbage) — the RetryWatcher is the
// one that acts on errors — and still reports the subsequent close as a resync
// signal rather than a context cancellation.
func TestDrainWatchSkipsErrorEvent(t *testing.T) {
	in := make(chan watch.Event, 4)
	out := make(chan Event[Node], 4)

	in <- watch.Event{Type: watch.Error, Object: &apierrors.NewResourceExpired("gone").ErrStatus}
	close(in)

	if drainWatch(context.Background(), in, out) {
		t.Error("drainWatch returned ctxDone=true; want false")
	}
	close(out)
	if ev, ok := <-out; ok {
		t.Errorf("expected no forwarded events for a watch.Error, got %+v", ev)
	}
}

// TestDrainWatchStopsOnContextCancel checks the other terminal: a cancelled
// context makes drainWatch return true (stop) so the caller does not resync.
func TestDrainWatchStopsOnContextCancel(t *testing.T) {
	in := make(chan watch.Event)
	out := make(chan Event[Node])
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan bool, 1)
	go func() { done <- drainWatch(ctx, in, out) }()

	select {
	case ctxDone := <-done:
		if !ctxDone {
			t.Error("drainWatch returned false on a cancelled context; want true (stop)")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("drainWatch did not return on a cancelled context")
	}
}
