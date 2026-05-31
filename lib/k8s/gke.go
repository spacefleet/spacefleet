package k8s

import (
	"context"
	"encoding/base64"
	"fmt"

	"golang.org/x/oauth2/google"
	container "google.golang.org/api/container/v1"
	"google.golang.org/api/option"
	"k8s.io/client-go/rest"
)

// gkeRESTConfig connects to a Google GKE cluster: it authenticates with the
// stored GCP service-account key, discovers the API endpoint and CA via the
// Container API, and uses a Google OAuth2 access token as the bearer token.
func gkeRESTConfig(ctx context.Context, conn Connection) (*rest.Config, error) {
	project := conn.Config[ConfigKeyGCPProject]
	location := conn.Config[ConfigKeyGCPLocation]
	clusterName := conn.Config[ConfigKeyGKEClusterName]
	if project == "" || location == "" || clusterName == "" {
		return nil, fmt.Errorf("k8s/gke: project, location, and cluster name are required")
	}
	credMap, err := parseCredMap(conn.Credentials)
	if err != nil {
		return nil, err
	}
	saKey := credMap[CredKeyGCPServiceKey]
	if saKey == "" {
		return nil, fmt.Errorf("k8s/gke: a GCP service-account key is required")
	}

	// The key is the org's own service-account credential, supplied to reach
	// their own cluster — the trusted-input case the deprecation note warns
	// about does not apply.
	creds, err := google.CredentialsFromJSON(ctx, []byte(saKey), container.CloudPlatformScope)
	if err != nil {
		return nil, fmt.Errorf("k8s/gke: parse service-account key: %w", err)
	}

	svc, err := container.NewService(ctx, option.WithCredentials(creds))
	if err != nil {
		return nil, fmt.Errorf("k8s/gke: container client: %w", err)
	}
	name := fmt.Sprintf("projects/%s/locations/%s/clusters/%s", project, location, clusterName)
	cl, err := svc.Projects.Locations.Clusters.Get(name).Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("k8s/gke: get cluster: %w", err)
	}
	if cl.Endpoint == "" || cl.MasterAuth == nil || cl.MasterAuth.ClusterCaCertificate == "" {
		return nil, fmt.Errorf("k8s/gke: cluster missing endpoint or CA")
	}
	caData, err := base64.StdEncoding.DecodeString(cl.MasterAuth.ClusterCaCertificate)
	if err != nil {
		return nil, fmt.Errorf("k8s/gke: decode CA: %w", err)
	}

	tok, err := creds.TokenSource.Token()
	if err != nil {
		return nil, fmt.Errorf("k8s/gke: mint access token: %w", err)
	}

	return &rest.Config{
		Host:            "https://" + cl.Endpoint,
		BearerToken:     tok.AccessToken,
		TLSClientConfig: rest.TLSClientConfig{CAData: caData},
	}, nil
}
