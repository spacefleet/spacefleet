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
	"net"
	"net/url"
	"os"
	"sync/atomic"
	"syscall"
	"time"

	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// inClusterDNS is the stable in-cluster API server address. A kubeconfig built
// for an in_cluster connection uses this (not the resolved KUBERNETES_SERVICE_HOST
// IP) so it resolves from any pod in that cluster — e.g. the TaskRun pod on a
// runner that is the same cluster.
const inClusterDNS = "https://kubernetes.default.svc"

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

// EndpointPolicy restricts which network destinations a user-supplied cluster
// API endpoint is allowed to resolve to. It governs only the methods whose
// endpoint is chosen by the caller — token and kubeconfig. The cloud methods
// (eks/gke/aks) learn their endpoint from the provider after proving ownership
// of cloud credentials, and in_cluster targets the pod's own fixed API server,
// so neither is an attacker-controlled SSRF vector and neither is subject to
// this policy.
type EndpointPolicy struct {
	// AllowPrivate permits loopback (127.0.0.0/8, ::1) and RFC1918/ULA private
	// addresses (10/8, 172.16/12, 192.168/16, fc00::/7). Default false so a
	// malicious org member cannot use the server as an SSRF proxy to localhost
	// services (the bundled Postgres/Dex, debug endpoints) or to sweep the
	// internal pod network. Self-hosters whose clusters live on a private
	// network opt in via ALLOW_PRIVATE_CLUSTER_ENDPOINTS=true.
	//
	// Cloud-metadata / link-local (169.254.0.0/16, fe80::/10), the unspecified
	// address, and multicast are rejected unconditionally — none is ever a
	// legitimate Kubernetes API server, and the metadata endpoint is the
	// highest-value SSRF target (it hands out the node's IAM credentials).
	AllowPrivate bool
}

// endpointPolicy is the process-wide policy, configured once at startup via
// SetEndpointPolicy. The zero value (AllowPrivate=false) is the secure default,
// so it is safe before SetEndpointPolicy is called (e.g. in tests).
var endpointPolicy atomic.Pointer[EndpointPolicy]

func init() {
	endpointPolicy.Store(&EndpointPolicy{})
}

// SetEndpointPolicy installs the process-wide endpoint policy from operator
// configuration. Call once at startup, before serving requests.
func SetEndpointPolicy(p EndpointPolicy) { endpointPolicy.Store(&p) }

func currentEndpointPolicy() EndpointPolicy { return *endpointPolicy.Load() }

// unconditionallyDeniedIP reports the addresses that are never a legitimate
// Kubernetes API server regardless of policy: cloud-metadata / link-local
// (169.254.0.0/16 incl. 169.254.169.254, fe80::/10), the unspecified address,
// and multicast. These are rejected up front (a literal endpoint IP fails at
// registration with a clear error) as well as at dial time.
func unconditionallyDeniedIP(ip net.IP) error {
	switch {
	case ip.IsUnspecified():
		return fmt.Errorf("k8s: endpoint resolves to the unspecified address %s, which is not allowed", ip)
	case ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast():
		return fmt.Errorf("k8s: endpoint resolves to a link-local/metadata address %s, which is not allowed", ip)
	case ip.IsMulticast():
		return fmt.Errorf("k8s: endpoint resolves to a multicast address %s, which is not allowed", ip)
	}
	return nil
}

// allowsIP reports whether the policy permits connecting to ip. It is the
// dial-time enforcement (run by the Control hook on the resolved IP of every
// connection, which is what defeats DNS rebinding) and the full classifier the
// up-front check builds on.
func (p EndpointPolicy) allowsIP(ip net.IP) error {
	if err := unconditionallyDeniedIP(ip); err != nil {
		return err
	}
	if p.AllowPrivate {
		return nil
	}
	if ip.IsLoopback() || ip.IsPrivate() {
		return fmt.Errorf("k8s: endpoint resolves to a private address %s, which is not allowed; set ALLOW_PRIVATE_CLUSTER_ENDPOINTS=true if this deployment must reach clusters on a private network", ip)
	}
	return nil
}

// guardEndpoint applies the SSRF endpoint policy to a *rest.Config built from a
// user-supplied endpoint. It requires https and installs a dial-time Control
// hook that re-checks the resolved IP on every connection. That hook is the
// actual security boundary: a hostname can resolve to a public IP at validation
// time and a private one when dialed (DNS rebinding), and it covers the probe
// and every live reader uniformly.
//
// A literal endpoint IP is additionally rejected up front, but only for the
// unconditional set (metadata/link-local/…). Loopback/private are left to the
// dial hook on purpose: a same-cluster kubeconfig whose loopback endpoint is
// rewritten to in-cluster DNS (see Kubeconfig) is never actually dialed at the
// loopback, so rejecting it at build time would be a false positive.
func guardEndpoint(cfg *rest.Config) error {
	policy := currentEndpointPolicy()
	u, err := url.Parse(cfg.Host)
	if err != nil {
		return fmt.Errorf("k8s: invalid endpoint %q: %w", cfg.Host, err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("k8s: endpoint must use https, got %q", cfg.Host)
	}
	if u.Hostname() == "" {
		return fmt.Errorf("k8s: endpoint %q has no host", cfg.Host)
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil {
		if err := unconditionallyDeniedIP(ip); err != nil {
			return err
		}
	}
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return fmt.Errorf("k8s: parse dial address %q: %w", address, err)
			}
			ip := net.ParseIP(host)
			if ip == nil {
				return fmt.Errorf("k8s: dial address %q is not an IP", host)
			}
			return policy.allowsIP(ip)
		},
	}
	cfg.Dial = dialer.DialContext
	return nil
}

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
		return kubeconfigRESTConfig(conn.Credentials)
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

// kubeconfigRESTConfig builds a config from a user-supplied kubeconfig, after
// rejecting any auth method that would run a local binary or external helper on
// the Spacefleet host.
//
// A kubeconfig `exec` block names an arbitrary command that client-go runs (the
// default policy is PluginPolicyAllowAll) the first time it authenticates — for
// us, synchronously during the registration probe. That is remote code
// execution on the serve/worker host from any caller who can register a
// cluster, so we refuse exec entirely. The legacy `auth-provider` block is
// rejected for the same reason (some providers shell out / make their own
// network calls). Clusters that legitimately need such a helper — the cloud
// CLIs' `aws eks get-token` / `gke-gcloud-auth-plugin` / `kubelogin` — must use
// the native eks/gke/aks methods, which mint tokens directly. Only static
// credentials (token, client cert, basic auth) are allowed here, mirroring the
// existing AKS guard (see aks.go).
func kubeconfigRESTConfig(kubeconfig []byte) (*rest.Config, error) {
	raw, err := clientcmd.Load(kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("k8s: parse kubeconfig: %w", err)
	}
	for name, authInfo := range raw.AuthInfos {
		if authInfo.Exec != nil {
			return nil, fmt.Errorf("k8s: kubeconfig user %q uses an exec credential plugin, which is not allowed; use the eks/gke/aks connection method or a static token/client-certificate kubeconfig", name)
		}
		if authInfo.AuthProvider != nil {
			return nil, fmt.Errorf("k8s: kubeconfig user %q uses an auth-provider plugin, which is not allowed; use the eks/gke/aks connection method or a static token/client-certificate kubeconfig", name)
		}
	}
	cfg, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		return nil, err
	}
	// Belt-and-suspenders: even if a future client-go change resolved a plugin
	// we didn't catch above, never hand a config that would exec a binary.
	if cfg.ExecProvider != nil || cfg.AuthProvider != nil {
		return nil, fmt.Errorf("k8s: kubeconfig resolves to an exec/auth-provider credential, which is not allowed")
	}
	// The kubeconfig's server URL is fully caller-controlled, so enforce the
	// SSRF endpoint policy on it just like the token method.
	if err := guardEndpoint(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
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
	if err := guardEndpoint(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Kubeconfig builds a portable, self-contained kubeconfig for the connection, so
// it can be injected into a workload running elsewhere (e.g. a Helm rollout
// TaskRun on a runner cluster, targeting this cluster). It resolves the
// connection through RESTConfig — minting a cloud token late, per call, for the
// eks/gke/aks methods, and reading the ServiceAccount config for in_cluster —
// then serializes the host, CA (or insecure flag), and credentials (a bearer
// token, or a client cert/key for a cert-based kubeconfig) into a single-context
// kubeconfig via clientcmd.Write.
//
// The host is rewritten to the stable in-cluster DNS when the consumer runs in
// this same cluster — always for in_cluster (whose resolved KUBERNETES_SERVICE_HOST
// IP is never portable), and whenever inSameCluster is set (runner == target).
// The latter matters for clusters whose registered endpoint is unreachable from
// inside — e.g. a kind cluster's host-side loopback (127.0.0.1:<port>), which
// points at the pod's own loopback once injected into a TaskRun in that cluster.
func Kubeconfig(ctx context.Context, conn Connection, inSameCluster bool) ([]byte, error) {
	cfg, err := RESTConfig(ctx, conn)
	if err != nil {
		return nil, err
	}

	host := cfg.Host
	if conn.Method == MethodInCluster || inSameCluster {
		host = inClusterDNS
	}

	cluster := clientcmdapi.NewCluster()
	cluster.Server = host
	if cfg.Insecure {
		cluster.InsecureSkipTLSVerify = true
	} else {
		ca := cfg.CAData
		// rest.InClusterConfig (and some kubeconfigs) reference the CA by file
		// path rather than inlining it; read it so the result is self-contained.
		if len(ca) == 0 && cfg.CAFile != "" {
			ca, err = os.ReadFile(cfg.CAFile)
			if err != nil {
				return nil, fmt.Errorf("k8s: read CA file %q: %w", cfg.CAFile, err)
			}
		}
		cluster.CertificateAuthorityData = ca
	}

	authInfo := clientcmdapi.NewAuthInfo()
	switch {
	case cfg.BearerToken != "":
		authInfo.Token = cfg.BearerToken
	case len(cfg.CertData) > 0 && len(cfg.KeyData) > 0:
		authInfo.ClientCertificateData = cfg.CertData
		authInfo.ClientKeyData = cfg.KeyData
	default:
		return nil, fmt.Errorf("k8s: connection has no serializable credentials (a bearer token or client certificate is required)")
	}

	const name = "spacefleet"
	out := clientcmdapi.NewConfig()
	out.Clusters[name] = cluster
	out.AuthInfos[name] = authInfo
	out.Contexts[name] = &clientcmdapi.Context{Cluster: name, AuthInfo: name}
	out.CurrentContext = name

	b, err := clientcmd.Write(*out)
	if err != nil {
		return nil, fmt.Errorf("k8s: serialize kubeconfig: %w", err)
	}
	return b, nil
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
