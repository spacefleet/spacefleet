// Package k8s turns a registered cluster's connection details into a usable
// Kubernetes client config and probes the cluster for reachability. It is
// deliberately decoupled from the storage layer: callers translate their stored
// representation (e.g. an ent.Cluster plus decrypted credentials) into a
// Connection, and this package handles the Kubernetes specifics.
//
// The portable methods (in_cluster, kubeconfig, token) are implemented here.
// The cloud-native methods (eks, gke, aks) mint short-lived tokens from cloud
// credentials and live in sibling files (eks.go, gke.go, aks.go).
package k8s

import (
	"context"
	"fmt"
	"time"

	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// Method identifies how Spacefleet connects to a cluster. The string values
// match the Cluster.connection_method enum in the ent schema.
type Method string

const (
	MethodInCluster  Method = "in_cluster"
	MethodKubeconfig Method = "kubeconfig"
	MethodToken      Method = "token"
	MethodEKS        Method = "eks"
	MethodGKE        Method = "gke"
	MethodAKS        Method = "aks"
)

// Config keys shared by the portable methods. Cloud methods define their own
// keys in their respective files.
const (
	// ConfigKeyCA is a PEM-encoded CA certificate bundle (non-secret).
	ConfigKeyCA = "ca"
	// ConfigKeyInsecureSkipTLS, when "true", disables TLS verification. Intended
	// only for clusters with self-signed certs the operator can't supply.
	ConfigKeyInsecureSkipTLS = "insecure_skip_tls"
)

// probeTimeout bounds a single connectivity probe so a wedged or unreachable
// API server can't hang a request handler.
const probeTimeout = 10 * time.Second

// Connection is the fully-resolved, decrypted view of a cluster needed to build
// a client. Credentials carries already-decrypted secret material (a kubeconfig
// or bearer token for the portable methods; cloud credentials for the rest).
type Connection struct {
	Method      Method
	Endpoint    string
	Config      map[string]string
	Credentials []byte
}

// RESTConfig builds a *rest.Config for the connection. The context is used by
// the cloud-native methods that mint tokens over the network; the portable
// methods ignore it.
func RESTConfig(ctx context.Context, conn Connection) (*rest.Config, error) {
	switch conn.Method {
	case MethodInCluster:
		return rest.InClusterConfig()
	case MethodKubeconfig:
		// NOTE: a kubeconfig that authenticates via an exec plugin (the
		// `aws eks get-token` / `gke-gcloud-auth-plugin` / `kubelogin` helpers
		// that cloud CLIs write) will not resolve server-side — those binaries
		// aren't present. Such clusters should use the native eks/gke/aks
		// methods, which mint tokens directly.
		return clientcmd.RESTConfigFromKubeConfig(conn.Credentials)
	case MethodToken:
		return tokenRESTConfig(conn)
	case MethodEKS:
		return eksRESTConfig(ctx, conn)
	case MethodGKE:
		return gkeRESTConfig(ctx, conn)
	case MethodAKS:
		return aksRESTConfig(ctx, conn)
	default:
		return nil, fmt.Errorf("k8s: unknown connection method %q", conn.Method)
	}
}

// tokenRESTConfig builds a config from an explicit API URL, CA, and bearer
// token (a long-lived ServiceAccount token is the typical source).
func tokenRESTConfig(conn Connection) (*rest.Config, error) {
	if conn.Endpoint == "" {
		return nil, fmt.Errorf("k8s: token method requires an endpoint")
	}
	if len(conn.Credentials) == 0 {
		return nil, fmt.Errorf("k8s: token method requires a bearer token")
	}
	cfg := &rest.Config{
		Host:        conn.Endpoint,
		BearerToken: string(conn.Credentials),
	}
	if conn.Config[ConfigKeyInsecureSkipTLS] == "true" {
		cfg.TLSClientConfig = rest.TLSClientConfig{Insecure: true}
	} else if ca := conn.Config[ConfigKeyCA]; ca != "" {
		cfg.TLSClientConfig = rest.TLSClientConfig{CAData: []byte(ca)}
	}
	return cfg, nil
}

// Verify probes the API server and returns its reported version. It mutates a
// copy of cfg to apply the probe timeout, leaving the caller's config untouched.
func Verify(cfg *rest.Config) (string, error) {
	probe := rest.CopyConfig(cfg)
	probe.Timeout = probeTimeout
	dc, err := discovery.NewDiscoveryClientForConfig(probe)
	if err != nil {
		return "", fmt.Errorf("k8s: discovery client: %w", err)
	}
	v, err := dc.ServerVersion()
	if err != nil {
		return "", fmt.Errorf("k8s: server version: %w", err)
	}
	return v.GitVersion, nil
}

// Probe is the common case: build a config and check reachability in one call,
// returning the cluster's reported version.
func Probe(ctx context.Context, conn Connection) (string, error) {
	cfg, err := RESTConfig(ctx, conn)
	if err != nil {
		return "", err
	}
	return Verify(cfg)
}
