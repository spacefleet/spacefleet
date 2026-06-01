package k8s

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// listTimeout bounds a single node-list call so a wedged API server can't hang
// a request handler. It mirrors probeTimeout's intent for read operations.
const listTimeout = 15 * time.Second

// Well-known node labels Kubernetes (and cloud providers) set. We surface a few
// directly so callers don't have to know the label keys.
const (
	labelInstanceType = "node.kubernetes.io/instance-type"
	labelZone         = "topology.kubernetes.io/zone"
	labelRegion       = "topology.kubernetes.io/region"
	nodeRolePrefix    = "node-role.kubernetes.io/"
	labelLegacyRole   = "kubernetes.io/role"
)

// NodeResources is a node's compute capacity or allocatable resources.
type NodeResources struct {
	CPU    string
	Memory string
	Pods   string
}

// NodeTaint mirrors a corev1.Taint in plain strings.
type NodeTaint struct {
	Key    string
	Value  string
	Effect string
}

// NodeCondition is a single status condition reported by the node.
type NodeCondition struct {
	Type               string
	Status             string
	Reason             string
	Message            string
	LastTransitionTime *time.Time
}

// Node is a storage-agnostic view of a Kubernetes node: the subset of the
// upstream object the UI renders, flattened into plain Go types so the API
// layer can map it without importing client-go.
type Node struct {
	Name             string
	Ready            bool
	Unschedulable    bool
	Roles            []string
	KubeletVersion   string
	OSImage          string
	KernelVersion    string
	ContainerRuntime string
	Architecture     string
	OperatingSystem  string
	InternalIP       string
	ExternalIP       string
	Hostname         string
	PodCIDR          string
	ProviderID       string
	InstanceType     string
	Zone             string
	Region           string
	Capacity         NodeResources
	Allocatable      NodeResources
	Labels           map[string]string
	Taints           []NodeTaint
	Conditions       []NodeCondition
	CreatedAt        time.Time
}

// ListNodes builds a client from the connection and returns the cluster's
// nodes. Like Probe, it owns the timeout so a slow API server is bounded.
func ListNodes(ctx context.Context, conn Connection) ([]Node, error) {
	cfg, err := RESTConfig(ctx, conn)
	if err != nil {
		return nil, err
	}
	cfg.Timeout = listTimeout
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("k8s: client: %w", err)
	}
	list, err := cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("k8s: list nodes: %w", err)
	}
	out := make([]Node, len(list.Items))
	for i := range list.Items {
		out[i] = convertNode(&list.Items[i])
	}
	return out, nil
}

// convertNode flattens a corev1.Node into our plain view.
func convertNode(n *corev1.Node) Node {
	node := Node{
		Name:             n.Name,
		Unschedulable:    n.Spec.Unschedulable,
		Roles:            nodeRoles(n),
		KubeletVersion:   n.Status.NodeInfo.KubeletVersion,
		OSImage:          n.Status.NodeInfo.OSImage,
		KernelVersion:    n.Status.NodeInfo.KernelVersion,
		ContainerRuntime: n.Status.NodeInfo.ContainerRuntimeVersion,
		Architecture:     n.Status.NodeInfo.Architecture,
		OperatingSystem:  n.Status.NodeInfo.OperatingSystem,
		PodCIDR:          n.Spec.PodCIDR,
		ProviderID:       n.Spec.ProviderID,
		InstanceType:     n.Labels[labelInstanceType],
		Zone:             n.Labels[labelZone],
		Region:           n.Labels[labelRegion],
		Capacity:         resources(n.Status.Capacity),
		Allocatable:      resources(n.Status.Allocatable),
		Labels:           n.Labels,
		Taints:           taints(n.Spec.Taints),
		Conditions:       conditions(n.Status.Conditions),
		CreatedAt:        n.CreationTimestamp.Time,
	}
	if node.Labels == nil {
		node.Labels = map[string]string{}
	}
	for _, addr := range n.Status.Addresses {
		switch addr.Type {
		case corev1.NodeInternalIP:
			node.InternalIP = addr.Address
		case corev1.NodeExternalIP:
			node.ExternalIP = addr.Address
		case corev1.NodeHostName:
			node.Hostname = addr.Address
		}
	}
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady {
			node.Ready = c.Status == corev1.ConditionTrue
		}
	}
	return node
}

// nodeRoles derives the node's roles from its node-role.kubernetes.io/<role>
// labels (the kubectl convention), falling back to the legacy
// kubernetes.io/role label.
func nodeRoles(n *corev1.Node) []string {
	roles := []string{}
	for k := range n.Labels {
		if r, ok := strings.CutPrefix(k, nodeRolePrefix); ok && r != "" {
			roles = append(roles, r)
		}
	}
	if len(roles) == 0 {
		if r := n.Labels[labelLegacyRole]; r != "" {
			roles = append(roles, r)
		}
	}
	sort.Strings(roles)
	return roles
}

func resources(rl corev1.ResourceList) NodeResources {
	res := NodeResources{}
	if v, ok := rl[corev1.ResourceCPU]; ok {
		res.CPU = v.String()
	}
	if v, ok := rl[corev1.ResourceMemory]; ok {
		res.Memory = v.String()
	}
	if v, ok := rl[corev1.ResourcePods]; ok {
		res.Pods = v.String()
	}
	return res
}

func taints(ts []corev1.Taint) []NodeTaint {
	out := make([]NodeTaint, len(ts))
	for i, t := range ts {
		out[i] = NodeTaint{Key: t.Key, Value: t.Value, Effect: string(t.Effect)}
	}
	return out
}

func conditions(cs []corev1.NodeCondition) []NodeCondition {
	out := make([]NodeCondition, len(cs))
	for i, c := range cs {
		nc := NodeCondition{
			Type:    string(c.Type),
			Status:  string(c.Status),
			Reason:  c.Reason,
			Message: c.Message,
		}
		if !c.LastTransitionTime.IsZero() {
			t := c.LastTransitionTime.Time
			nc.LastTransitionTime = &t
		}
		out[i] = nc
	}
	return out
}
