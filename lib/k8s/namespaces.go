package k8s

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Namespace is a storage-agnostic view of a Kubernetes namespace: the subset of
// the upstream object the UI renders, flattened into plain Go types so the API
// layer can map it without importing client-go. Like Node, it is a cluster-level
// resource (a namespace is not itself scoped to another namespace).
type Namespace struct {
	Name      string
	Status    string
	Labels    map[string]string
	CreatedAt time.Time
}

// ListNamespaces builds a client from the connection and returns the cluster's
// namespaces. Like ListNodes, it owns the timeout so a slow API server is
// bounded.
func ListNamespaces(ctx context.Context, conn Connection) ([]Namespace, error) {
	cfg, err := RESTConfig(ctx, conn)
	if err != nil {
		return nil, err
	}
	cfg.Timeout = listTimeout
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("k8s: client: %w", err)
	}
	list, err := cs.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("k8s: list namespaces: %w", err)
	}
	out := make([]Namespace, len(list.Items))
	for i := range list.Items {
		out[i] = convertNamespace(&list.Items[i])
	}
	return out, nil
}

// convertNamespace flattens a corev1.Namespace into our plain view. The status
// is the namespace phase (Active while in use, Terminating while being deleted).
func convertNamespace(n *corev1.Namespace) Namespace {
	ns := Namespace{
		Name:      n.Name,
		Status:    string(n.Status.Phase),
		Labels:    n.Labels,
		CreatedAt: n.CreationTimestamp.Time,
	}
	if ns.Labels == nil {
		ns.Labels = map[string]string{}
	}
	return ns
}
