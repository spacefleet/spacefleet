package k8s

import (
	"encoding/json"
	"fmt"
)

// Cloud-native connection methods (eks/gke/aks). Each discovers the API
// endpoint + CA from the provider and mints a short-lived Kubernetes token from
// the stored cloud credentials. The implementations live in eks.go, gke.go, and
// aks.go; this file holds the shared parameter keys and credential decoding.

// parseCredMap decodes the JSON credential blob (a flat string map) the API
// layer encodes for cloud methods. An empty blob yields an empty map.
func parseCredMap(b []byte) (map[string]string, error) {
	m := map[string]string{}
	if len(b) == 0 {
		return m, nil
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("k8s: parse credentials: %w", err)
	}
	return m, nil
}

// Cloud connection parameters. Non-secret values (ConfigKey*) live in
// Connection.Config; secret values (CredKey*) are JSON-encoded into
// Connection.Credentials as a map[string]string. The API layer writes them
// during registration; the per-provider implementations (Wave 2) read them.
const (
	// EKS
	ConfigKeyAWSRegion      = "aws_region"
	ConfigKeyEKSClusterName = "eks_cluster_name"
	ConfigKeyAWSRoleARN     = "aws_role_arn"
	CredKeyAWSAccessKeyID   = "aws_access_key_id"
	CredKeyAWSSecretKey     = "aws_secret_access_key"
	CredKeyAWSSessionToken  = "aws_session_token"

	// GKE
	ConfigKeyGCPProject     = "gcp_project"
	ConfigKeyGCPLocation    = "gcp_location"
	ConfigKeyGKEClusterName = "gke_cluster_name"
	CredKeyGCPServiceKey    = "gcp_service_account_key"

	// AKS
	ConfigKeyAzureSubscription  = "azure_subscription_id"
	ConfigKeyAzureResourceGroup = "azure_resource_group"
	ConfigKeyAKSClusterName     = "aks_cluster_name"
	ConfigKeyAzureTenantID      = "azure_tenant_id"
	ConfigKeyAzureClientID      = "azure_client_id"
	CredKeyAzureClientSecret    = "azure_client_secret"
)
