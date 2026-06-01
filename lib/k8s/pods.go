package k8s

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// PodCondition is a single status condition reported by a pod.
type PodCondition struct {
	Type               string
	Status             string
	Reason             string
	Message            string
	LastTransitionTime *time.Time
}

// ContainerStatus is the live state of one container in a pod, flattened from
// the upstream corev1.ContainerStatus.
type ContainerStatus struct {
	Name         string
	Image        string
	Ready        bool
	Started      bool
	RestartCount int
	State        string // Running, Waiting, or Terminated
	StateReason  string
	StateMessage string
}

// Pod is a storage-agnostic view of a Kubernetes pod: the subset of the
// upstream object the UI renders, flattened into plain Go types. Status is the
// derived display status kubectl shows (which folds container state into the
// raw phase, e.g. CrashLoopBackOff or Completed); Phase is the raw
// pod.Status.Phase.
type Pod struct {
	Name            string
	Namespace       string
	Phase           string
	Status          string
	Ready           string // "2/3"
	ReadyContainers int
	TotalContainers int
	Restarts        int
	NodeName        string
	PodIP           string
	HostIP          string
	QOSClass        string
	ServiceAccount  string
	Labels          map[string]string
	Conditions      []PodCondition
	Containers      []ContainerStatus
	CreatedAt       time.Time
}

// ListPods builds a client from the connection and returns the cluster's pods
// across all namespaces. Like ListNodes, it owns the timeout so a slow API
// server is bounded.
func ListPods(ctx context.Context, conn Connection) ([]Pod, error) {
	cfg, err := RESTConfig(ctx, conn)
	if err != nil {
		return nil, err
	}
	cfg.Timeout = listTimeout
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("k8s: client: %w", err)
	}
	list, err := cs.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("k8s: list pods: %w", err)
	}
	out := make([]Pod, len(list.Items))
	for i := range list.Items {
		out[i] = convertPod(&list.Items[i])
	}
	return out, nil
}

// convertPod flattens a corev1.Pod into our plain view, deriving the same
// display status and ready/restart counts kubectl's `get pods` shows.
func convertPod(p *corev1.Pod) Pod {
	pod := Pod{
		Name:            p.Name,
		Namespace:       p.Namespace,
		Phase:           string(p.Status.Phase),
		NodeName:        p.Spec.NodeName,
		PodIP:           p.Status.PodIP,
		HostIP:          p.Status.HostIP,
		QOSClass:        string(p.Status.QOSClass),
		ServiceAccount:  p.Spec.ServiceAccountName,
		Labels:          p.Labels,
		Conditions:      podConditions(p.Status.Conditions),
		Containers:      containerStatuses(p.Status.ContainerStatuses),
		TotalContainers: len(p.Spec.Containers),
		CreatedAt:       p.CreationTimestamp.Time,
	}
	if pod.Labels == nil {
		pod.Labels = map[string]string{}
	}
	for _, cs := range p.Status.ContainerStatuses {
		if cs.Ready {
			pod.ReadyContainers++
		}
	}
	pod.Ready = fmt.Sprintf("%d/%d", pod.ReadyContainers, pod.TotalContainers)
	pod.Status, pod.Restarts = podDisplayStatus(p)
	return pod
}

// podDisplayStatus reproduces kubectl's pod status column: it starts from the
// phase (or the pod's terminal reason) and overlays init/app container waiting
// and terminated reasons, then accounts for a pending deletion. It also returns
// the total restart count across the app containers. This mirrors the logic in
// kubectl's printers so our table reads the same as `kubectl get pods`.
func podDisplayStatus(p *corev1.Pod) (status string, restarts int) {
	reason := string(p.Status.Phase)
	if p.Status.Reason != "" {
		reason = p.Status.Reason
	}

	initializing := false
	for i := range p.Status.InitContainerStatuses {
		c := p.Status.InitContainerStatuses[i]
		switch {
		case c.State.Terminated != nil && c.State.Terminated.ExitCode == 0:
			continue
		case c.State.Terminated != nil:
			if c.State.Terminated.Reason != "" {
				reason = "Init:" + c.State.Terminated.Reason
			} else if c.State.Terminated.Signal != 0 {
				reason = fmt.Sprintf("Init:Signal:%d", c.State.Terminated.Signal)
			} else {
				reason = fmt.Sprintf("Init:ExitCode:%d", c.State.Terminated.ExitCode)
			}
			initializing = true
		case c.State.Waiting != nil && c.State.Waiting.Reason != "" && c.State.Waiting.Reason != "PodInitializing":
			reason = "Init:" + c.State.Waiting.Reason
			initializing = true
		default:
			reason = fmt.Sprintf("Init:%d/%d", i, len(p.Spec.InitContainers))
			initializing = true
		}
		break
	}

	if !initializing {
		hasRunning := false
		// Walk back-to-front so the lowest-indexed container's reason wins, as
		// kubectl does.
		for i := len(p.Status.ContainerStatuses) - 1; i >= 0; i-- {
			c := p.Status.ContainerStatuses[i]
			restarts += int(c.RestartCount)
			switch {
			case c.State.Waiting != nil && c.State.Waiting.Reason != "":
				reason = c.State.Waiting.Reason
			case c.State.Terminated != nil && c.State.Terminated.Reason != "":
				reason = c.State.Terminated.Reason
			case c.State.Terminated != nil:
				if c.State.Terminated.Signal != 0 {
					reason = fmt.Sprintf("Signal:%d", c.State.Terminated.Signal)
				} else {
					reason = fmt.Sprintf("ExitCode:%d", c.State.Terminated.ExitCode)
				}
			case c.Ready && c.State.Running != nil:
				hasRunning = true
			}
		}
		// A completed pod with a container still running is reported by its
		// readiness, matching kubectl.
		if reason == "Completed" && hasRunning {
			if podReady(p.Status.Conditions) {
				reason = "Running"
			} else {
				reason = "NotReady"
			}
		}
	}

	switch {
	case p.DeletionTimestamp != nil && p.Status.Reason == "NodeLost":
		reason = "Unknown"
	case p.DeletionTimestamp != nil:
		reason = "Terminating"
	}
	return reason, restarts
}

// podReady reports whether the pod's Ready condition is True.
func podReady(cs []corev1.PodCondition) bool {
	for _, c := range cs {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

func podConditions(cs []corev1.PodCondition) []PodCondition {
	out := make([]PodCondition, len(cs))
	for i, c := range cs {
		pc := PodCondition{
			Type:    string(c.Type),
			Status:  string(c.Status),
			Reason:  c.Reason,
			Message: c.Message,
		}
		if !c.LastTransitionTime.IsZero() {
			t := c.LastTransitionTime.Time
			pc.LastTransitionTime = &t
		}
		out[i] = pc
	}
	return out
}

func containerStatuses(cs []corev1.ContainerStatus) []ContainerStatus {
	out := make([]ContainerStatus, len(cs))
	for i, c := range cs {
		sc := ContainerStatus{
			Name:         c.Name,
			Image:        c.Image,
			Ready:        c.Ready,
			RestartCount: int(c.RestartCount),
		}
		if c.Started != nil {
			sc.Started = *c.Started
		}
		switch {
		case c.State.Running != nil:
			sc.State = "Running"
		case c.State.Waiting != nil:
			sc.State = "Waiting"
			sc.StateReason = c.State.Waiting.Reason
			sc.StateMessage = c.State.Waiting.Message
		case c.State.Terminated != nil:
			sc.State = "Terminated"
			sc.StateReason = c.State.Terminated.Reason
			sc.StateMessage = c.State.Terminated.Message
		}
		out[i] = sc
	}
	return out
}
