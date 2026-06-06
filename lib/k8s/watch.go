package k8s

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	toolswatch "k8s.io/client-go/tools/watch"
)

// This file is the live-watch counterpart to the one-shot List* helpers: it
// turns a cluster connection into a stream of resource changes for the
// SSE-backed UI. The transport (lib/api) is resource-agnostic; the per-resource
// glue is small and lives here. WatchNodes is the first instance — copy its
// shape for pods/workloads/etc.

// EventType classifies a change to a watched resource. EventSnapshot carries the
// full current set (sent once at start, and again after a re-list resync); the
// others are single-object deltas.
type EventType string

const (
	EventSnapshot EventType = "snapshot"
	EventAdded    EventType = "added"
	EventModified EventType = "modified"
	EventDeleted  EventType = "deleted"
)

// Event is a single change on a watch stream. For EventSnapshot, Snapshot holds
// the full set and Object is the zero value; for the delta types it's reversed.
type Event[T any] struct {
	Type     EventType
	Object   T
	Snapshot []T
}

// NodeStream is an open watch on a cluster's nodes: the initial Snapshot plus a
// channel of subsequent changes. The channel is closed when the watch ends
// (context cancelled, or an unrecoverable error). Drain it from a single
// goroutine; cancel the context passed to WatchNodes to stop the watch.
type NodeStream struct {
	Snapshot []Node
	Events   <-chan Event[Node]
}

// WatchNodes lists the cluster's nodes (the snapshot), then streams changes
// until ctx is cancelled. The initial List doubles as a reachability check: a
// failure to build the client or reach the API server is returned synchronously
// (so the caller can surface an HTTP error before committing to a stream),
// while errors that occur once the stream is live simply end it.
//
// Resilience: deltas come from a RetryWatcher, which transparently reconnects
// the underlying watch across transient drops. If the watch's resourceVersion
// ages out ("410 Gone") the RetryWatcher stops; we then re-list to resync,
// emit a fresh EventSnapshot (so a client that missed deletes self-heals), and
// resume from the new resourceVersion.
func WatchNodes(ctx context.Context, conn Connection) (*NodeStream, error) {
	cfg, err := RESTConfig(ctx, conn)
	if err != nil {
		return nil, err
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("k8s: client: %w", err)
	}

	initial, rv, err := listNodesRV(ctx, cs)
	if err != nil {
		return nil, err
	}

	events := make(chan Event[Node])
	go func() {
		defer close(events)
		currentRV := rv
		lw := &cache.ListWatch{
			WatchFuncWithContext: func(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
				return cs.CoreV1().Nodes().Watch(ctx, opts)
			},
		}
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
			// The RetryWatcher gave up (typically an expired resourceVersion).
			// Re-list to resync and continue from a fresh resourceVersion.
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
	}()

	return &NodeStream{Snapshot: initial, Events: events}, nil
}

// drainWatch forwards delta events from a watch result channel until either the
// channel closes (returns false — caller should resync and restart) or the
// context is cancelled (returns true — caller should stop).
func drainWatch(ctx context.Context, in <-chan watch.Event, out chan<- Event[Node]) bool {
	for {
		select {
		case <-ctx.Done():
			return true
		case ev, ok := <-in:
			if !ok {
				// The watch channel closed. On context cancel the RetryWatcher
				// closes this channel *and* fires ctx.Done() — racing the case
				// above — so check ctx here too: a cancel is terminal (return
				// true), only a genuine RetryWatcher give-up means resync.
				return ctx.Err() != nil
			}
			var t EventType
			switch ev.Type {
			case watch.Added:
				t = EventAdded
			case watch.Modified:
				t = EventModified
			case watch.Deleted:
				t = EventDeleted
			default:
				// Bookmark/Error events carry no node delta for the UI; the
				// RetryWatcher acts on Error itself.
				continue
			}
			n, ok := ev.Object.(*corev1.Node)
			if !ok {
				continue
			}
			select {
			case out <- Event[Node]{Type: t, Object: convertNode(n)}:
			case <-ctx.Done():
				return true
			}
		}
	}
}

// listNodesRV lists nodes and returns them alongside the list's resourceVersion
// — the point a watch should start from to see every change after this read.
func listNodesRV(ctx context.Context, cs kubernetes.Interface) ([]Node, string, error) {
	list, err := cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, "", fmt.Errorf("k8s: list nodes: %w", err)
	}
	out := make([]Node, len(list.Items))
	for i := range list.Items {
		out[i] = convertNode(&list.Items[i])
	}
	return out, list.ResourceVersion, nil
}

// PodStream is an open watch on a cluster's pods (across all namespaces): the
// initial Snapshot plus a channel of subsequent changes. It is the pod
// counterpart of NodeStream — same lifecycle rules (drain from one goroutine;
// cancel the context to stop).
type PodStream struct {
	Snapshot []Pod
	Events   <-chan Event[Pod]
}

// WatchPods is the pod counterpart of WatchNodes: it lists the cluster's pods
// across all namespaces (the snapshot), then streams changes until ctx is
// cancelled, with the same RetryWatcher resilience and re-list resync.
func WatchPods(ctx context.Context, conn Connection) (*PodStream, error) {
	cfg, err := RESTConfig(ctx, conn)
	if err != nil {
		return nil, err
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("k8s: client: %w", err)
	}

	initial, rv, err := listPodsRV(ctx, cs)
	if err != nil {
		return nil, err
	}

	events := make(chan Event[Pod])
	go func() {
		defer close(events)
		currentRV := rv
		lw := &cache.ListWatch{
			WatchFuncWithContext: func(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
				return cs.CoreV1().Pods(metav1.NamespaceAll).Watch(ctx, opts)
			},
		}
		for {
			rw, err := toolswatch.NewRetryWatcherWithContext(ctx, currentRV, lw)
			if err != nil {
				return
			}
			ctxDone := drainPodWatch(ctx, rw.ResultChan(), events)
			rw.Stop()
			if ctxDone {
				return
			}
			snap, newRV, err := listPodsRV(ctx, cs)
			if err != nil {
				return
			}
			currentRV = newRV
			select {
			case events <- Event[Pod]{Type: EventSnapshot, Snapshot: snap}:
			case <-ctx.Done():
				return
			}
		}
	}()

	return &PodStream{Snapshot: initial, Events: events}, nil
}

// drainPodWatch is the pod counterpart of drainWatch: it forwards delta events
// until the channel closes (returns false — resync and restart) or the context
// is cancelled (returns true — stop).
func drainPodWatch(ctx context.Context, in <-chan watch.Event, out chan<- Event[Pod]) bool {
	for {
		select {
		case <-ctx.Done():
			return true
		case ev, ok := <-in:
			if !ok {
				// See drainWatch: a closed channel on context cancel races
				// ctx.Done(), so a cancel is terminal here too.
				return ctx.Err() != nil
			}
			var t EventType
			switch ev.Type {
			case watch.Added:
				t = EventAdded
			case watch.Modified:
				t = EventModified
			case watch.Deleted:
				t = EventDeleted
			default:
				continue
			}
			p, ok := ev.Object.(*corev1.Pod)
			if !ok {
				continue
			}
			select {
			case out <- Event[Pod]{Type: t, Object: convertPod(p)}:
			case <-ctx.Done():
				return true
			}
		}
	}
}

// listPodsRV lists pods across all namespaces and returns them alongside the
// list's resourceVersion — the point a watch should start from.
func listPodsRV(ctx context.Context, cs kubernetes.Interface) ([]Pod, string, error) {
	list, err := cs.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, "", fmt.Errorf("k8s: list pods: %w", err)
	}
	out := make([]Pod, len(list.Items))
	for i := range list.Items {
		out[i] = convertPod(&list.Items[i])
	}
	return out, list.ResourceVersion, nil
}

// NamespaceStream is an open watch on a cluster's namespaces: the initial
// Snapshot plus a channel of subsequent changes. It is the namespace
// counterpart of NodeStream — same lifecycle rules (drain from one goroutine;
// cancel the context to stop). Namespaces are cluster-level, so the watch is
// not scoped to a namespace.
type NamespaceStream struct {
	Snapshot []Namespace
	Events   <-chan Event[Namespace]
}

// WatchNamespaces is the namespace counterpart of WatchNodes: it lists the
// cluster's namespaces (the snapshot), then streams changes until ctx is
// cancelled, with the same RetryWatcher resilience and re-list resync.
func WatchNamespaces(ctx context.Context, conn Connection) (*NamespaceStream, error) {
	cfg, err := RESTConfig(ctx, conn)
	if err != nil {
		return nil, err
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("k8s: client: %w", err)
	}

	initial, rv, err := listNamespacesRV(ctx, cs)
	if err != nil {
		return nil, err
	}

	events := make(chan Event[Namespace])
	go func() {
		defer close(events)
		currentRV := rv
		lw := &cache.ListWatch{
			WatchFuncWithContext: func(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
				return cs.CoreV1().Namespaces().Watch(ctx, opts)
			},
		}
		for {
			rw, err := toolswatch.NewRetryWatcherWithContext(ctx, currentRV, lw)
			if err != nil {
				return
			}
			ctxDone := drainNamespaceWatch(ctx, rw.ResultChan(), events)
			rw.Stop()
			if ctxDone {
				return
			}
			snap, newRV, err := listNamespacesRV(ctx, cs)
			if err != nil {
				return
			}
			currentRV = newRV
			select {
			case events <- Event[Namespace]{Type: EventSnapshot, Snapshot: snap}:
			case <-ctx.Done():
				return
			}
		}
	}()

	return &NamespaceStream{Snapshot: initial, Events: events}, nil
}

// drainNamespaceWatch is the namespace counterpart of drainWatch: it forwards
// delta events until the channel closes (returns false — resync and restart) or
// the context is cancelled (returns true — stop).
func drainNamespaceWatch(ctx context.Context, in <-chan watch.Event, out chan<- Event[Namespace]) bool {
	for {
		select {
		case <-ctx.Done():
			return true
		case ev, ok := <-in:
			if !ok {
				// See drainWatch: a closed channel on context cancel races
				// ctx.Done(), so a cancel is terminal here too.
				return ctx.Err() != nil
			}
			var t EventType
			switch ev.Type {
			case watch.Added:
				t = EventAdded
			case watch.Modified:
				t = EventModified
			case watch.Deleted:
				t = EventDeleted
			default:
				continue
			}
			n, ok := ev.Object.(*corev1.Namespace)
			if !ok {
				continue
			}
			select {
			case out <- Event[Namespace]{Type: t, Object: convertNamespace(n)}:
			case <-ctx.Done():
				return true
			}
		}
	}
}

// listNamespacesRV lists namespaces and returns them alongside the list's
// resourceVersion — the point a watch should start from.
func listNamespacesRV(ctx context.Context, cs kubernetes.Interface) ([]Namespace, string, error) {
	list, err := cs.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, "", fmt.Errorf("k8s: list namespaces: %w", err)
	}
	out := make([]Namespace, len(list.Items))
	for i := range list.Items {
		out[i] = convertNamespace(&list.Items[i])
	}
	return out, list.ResourceVersion, nil
}
