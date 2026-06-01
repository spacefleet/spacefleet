package api

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spacefleet/spacefleet/lib/clusters"
	"github.com/spacefleet/spacefleet/lib/k8s"
)

// connFields is the union of per-method connection inputs, flattened from
// either a create or update request so buildConnection has a single shape to
// work with.
type connFields struct {
	endpoint        string
	ca              string
	token           string
	kubeconfig      string
	insecureSkipTLS bool

	awsRegion    string
	eksCluster   string
	awsAccessKey string
	awsSecret    string
	awsSession   string
	awsRoleARN   string

	gcpProject  string
	gcpLocation string
	gkeCluster  string
	gcpKey      string

	azureSub      string
	azureRG       string
	aksCluster    string
	azureTenant   string
	azureClientID string
	azureSecret   string
}

func fieldsFromCreate(b *ClusterCreateRequest) connFields {
	return connFields{
		endpoint:        deref(b.Endpoint),
		ca:              deref(b.Ca),
		token:           deref(b.Token),
		kubeconfig:      deref(b.Kubeconfig),
		insecureSkipTLS: b.InsecureSkipTls != nil && *b.InsecureSkipTls,
		awsRegion:       deref(b.AwsRegion),
		eksCluster:      deref(b.EksClusterName),
		awsAccessKey:    deref(b.AwsAccessKeyId),
		awsSecret:       deref(b.AwsSecretAccessKey),
		awsSession:      deref(b.AwsSessionToken),
		awsRoleARN:      deref(b.AwsRoleArn),
		gcpProject:      deref(b.GcpProject),
		gcpLocation:     deref(b.GcpLocation),
		gkeCluster:      deref(b.GkeClusterName),
		gcpKey:          deref(b.GcpServiceAccountKey),
		azureSub:        deref(b.AzureSubscriptionId),
		azureRG:         deref(b.AzureResourceGroup),
		aksCluster:      deref(b.AksClusterName),
		azureTenant:     deref(b.AzureTenantId),
		azureClientID:   deref(b.AzureClientId),
		azureSecret:     deref(b.AzureClientSecret),
	}
}

func fieldsFromUpdate(b *ClusterUpdateRequest) connFields {
	return connFields{
		endpoint:        deref(b.Endpoint),
		ca:              deref(b.Ca),
		token:           deref(b.Token),
		kubeconfig:      deref(b.Kubeconfig),
		insecureSkipTLS: b.InsecureSkipTls != nil && *b.InsecureSkipTls,
		awsRegion:       deref(b.AwsRegion),
		eksCluster:      deref(b.EksClusterName),
		awsAccessKey:    deref(b.AwsAccessKeyId),
		awsSecret:       deref(b.AwsSecretAccessKey),
		awsSession:      deref(b.AwsSessionToken),
		awsRoleARN:      deref(b.AwsRoleArn),
		gcpProject:      deref(b.GcpProject),
		gcpLocation:     deref(b.GcpLocation),
		gkeCluster:      deref(b.GkeClusterName),
		gcpKey:          deref(b.GcpServiceAccountKey),
		azureSub:        deref(b.AzureSubscriptionId),
		azureRG:         deref(b.AzureResourceGroup),
		aksCluster:      deref(b.AksClusterName),
		azureTenant:     deref(b.AzureTenantId),
		azureClientID:   deref(b.AzureClientId),
		azureSecret:     deref(b.AzureClientSecret),
	}
}

// connectionSupplied reports whether an update request carries any connection
// field (i.e. the caller wants to re-supply credentials and re-probe), as
// opposed to a name-only rename.
func connectionSupplied(b *ClusterUpdateRequest) bool {
	return b.Endpoint != nil || b.Ca != nil || b.Token != nil || b.Kubeconfig != nil ||
		b.InsecureSkipTls != nil ||
		b.AwsRegion != nil || b.EksClusterName != nil || b.AwsAccessKeyId != nil ||
		b.AwsSecretAccessKey != nil || b.AwsSessionToken != nil || b.AwsRoleArn != nil ||
		b.GcpProject != nil || b.GcpLocation != nil || b.GkeClusterName != nil || b.GcpServiceAccountKey != nil ||
		b.AzureSubscriptionId != nil || b.AzureResourceGroup != nil || b.AksClusterName != nil ||
		b.AzureTenantId != nil || b.AzureClientId != nil || b.AzureClientSecret != nil
}

// buildConnection validates the supplied fields for the chosen method and
// splits them into the non-secret config map and the raw credential blob the
// service will seal.
func buildConnection(method ConnectionMethod, f connFields) (clusters.ConnectionInput, error) {
	switch method {
	case InCluster:
		return clusters.ConnectionInput{}, nil

	case Token:
		if err := required(map[string]string{"endpoint": f.endpoint, "token": f.token}); err != nil {
			return clusters.ConnectionInput{}, err
		}
		cfg := map[string]string{}
		if f.insecureSkipTLS {
			cfg[k8s.ConfigKeyInsecureSkipTLS] = "true"
		} else if f.ca != "" {
			cfg[k8s.ConfigKeyCA] = f.ca
		} else {
			return clusters.ConnectionInput{}, fmt.Errorf("a CA certificate is required unless insecure_skip_tls is set")
		}
		return clusters.ConnectionInput{Endpoint: f.endpoint, Config: cfg, Credentials: []byte(f.token)}, nil

	case Kubeconfig:
		if err := required(map[string]string{"kubeconfig": f.kubeconfig}); err != nil {
			return clusters.ConnectionInput{}, err
		}
		return clusters.ConnectionInput{Credentials: []byte(f.kubeconfig)}, nil

	case Eks:
		if err := required(map[string]string{
			"aws_region": f.awsRegion, "eks_cluster_name": f.eksCluster,
			"aws_access_key_id": f.awsAccessKey, "aws_secret_access_key": f.awsSecret,
		}); err != nil {
			return clusters.ConnectionInput{}, err
		}
		cfg := map[string]string{
			k8s.ConfigKeyAWSRegion:      f.awsRegion,
			k8s.ConfigKeyEKSClusterName: f.eksCluster,
		}
		if f.awsRoleARN != "" {
			cfg[k8s.ConfigKeyAWSRoleARN] = f.awsRoleARN
		}
		creds := jsonCreds(map[string]string{
			k8s.CredKeyAWSAccessKeyID:  f.awsAccessKey,
			k8s.CredKeyAWSSecretKey:    f.awsSecret,
			k8s.CredKeyAWSSessionToken: f.awsSession,
		})
		return clusters.ConnectionInput{Config: cfg, Credentials: creds}, nil

	case Gke:
		if err := required(map[string]string{
			"gcp_project": f.gcpProject, "gcp_location": f.gcpLocation,
			"gke_cluster_name": f.gkeCluster, "gcp_service_account_key": f.gcpKey,
		}); err != nil {
			return clusters.ConnectionInput{}, err
		}
		cfg := map[string]string{
			k8s.ConfigKeyGCPProject:     f.gcpProject,
			k8s.ConfigKeyGCPLocation:    f.gcpLocation,
			k8s.ConfigKeyGKEClusterName: f.gkeCluster,
		}
		creds := jsonCreds(map[string]string{k8s.CredKeyGCPServiceKey: f.gcpKey})
		return clusters.ConnectionInput{Config: cfg, Credentials: creds}, nil

	case Aks:
		if err := required(map[string]string{
			"azure_subscription_id": f.azureSub, "azure_resource_group": f.azureRG,
			"aks_cluster_name": f.aksCluster, "azure_tenant_id": f.azureTenant,
			"azure_client_id": f.azureClientID, "azure_client_secret": f.azureSecret,
		}); err != nil {
			return clusters.ConnectionInput{}, err
		}
		cfg := map[string]string{
			k8s.ConfigKeyAzureSubscription:  f.azureSub,
			k8s.ConfigKeyAzureResourceGroup: f.azureRG,
			k8s.ConfigKeyAKSClusterName:     f.aksCluster,
			k8s.ConfigKeyAzureTenantID:      f.azureTenant,
			k8s.ConfigKeyAzureClientID:      f.azureClientID,
		}
		creds := jsonCreds(map[string]string{k8s.CredKeyAzureClientSecret: f.azureSecret})
		return clusters.ConnectionInput{Config: cfg, Credentials: creds}, nil

	default:
		return clusters.ConnectionInput{}, fmt.Errorf("unknown connection method %q", method)
	}
}

// required returns an error naming the first blank field, so the caller gets a
// clear 400 instead of a deep failure.
func required(fields map[string]string) error {
	missing := make([]string, 0)
	for name, val := range fields {
		if strings.TrimSpace(val) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required field(s): %s", strings.Join(missing, ", "))
	}
	return nil
}

// jsonCreds encodes a credential map, dropping empty values (e.g. an optional
// AWS session token). A marshal error is impossible for a string map, so it's
// ignored.
func jsonCreds(m map[string]string) []byte {
	for k, v := range m {
		if v == "" {
			delete(m, k)
		}
	}
	b, _ := json.Marshal(m)
	return b
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
