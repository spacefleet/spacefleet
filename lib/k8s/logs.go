package k8s

import (
	"context"
	"fmt"
	"io"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
)

// LogOptions controls a pod-log stream. Container selects one container of a
// multi-container pod (required when there's more than one); TailLines caps the
// initial backlog (0 means the API server's default); Follow keeps the stream
// open for new lines; Timestamps prefixes each line with an RFC3339 timestamp.
type LogOptions struct {
	Container  string
	TailLines  int64
	Follow     bool
	Timestamps bool
}

// StreamPodLogs opens a log stream for one pod, returning the raw line-delimited
// body for the caller to read and frame. Unlike the List* helpers it sets no
// client timeout: a followed log stream is long-lived and is bounded by the
// caller's context instead. Close the returned reader to release the connection.
func StreamPodLogs(ctx context.Context, conn Connection, namespace, name string, opts LogOptions) (io.ReadCloser, error) {
	cfg, err := RESTConfig(ctx, conn)
	if err != nil {
		return nil, err
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("k8s: client: %w", err)
	}
	logOpts := &corev1.PodLogOptions{
		Container:  opts.Container,
		Follow:     opts.Follow,
		Timestamps: opts.Timestamps,
	}
	if opts.TailLines > 0 {
		tl := opts.TailLines
		logOpts.TailLines = &tl
	}
	stream, err := cs.CoreV1().Pods(namespace).GetLogs(name, logOpts).Stream(ctx)
	if err != nil {
		return nil, fmt.Errorf("k8s: pod logs: %w", err)
	}
	return stream, nil
}
