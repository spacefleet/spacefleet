package api

import (
	"fmt"

	"github.com/spacefleet/spacefleet/ent/cloudcredential"
	"github.com/spacefleet/spacefleet/lib/cloudcredentials"
)

// ccFields is the union of per-provider credential inputs, flattened from either
// a create or update request so buildCloudCredential has a single shape to work
// with. (Mirrors connFields for clusters.)
type ccFields struct {
	awsAccessKey string
	awsSecret    string
	awsSession   string
	awsRegion    string
	awsRoleARN   string

	gcpKey     string
	gcpProject string

	azureTenant   string
	azureClientID string
	azureSecret   string
	azureSub      string
}

func ccFieldsFromCreate(b *CloudCredentialCreateRequest) ccFields {
	return ccFields{
		awsAccessKey:  deref(b.AwsAccessKeyId),
		awsSecret:     deref(b.AwsSecretAccessKey),
		awsSession:    deref(b.AwsSessionToken),
		awsRegion:     deref(b.AwsRegion),
		awsRoleARN:    deref(b.AwsRoleArn),
		gcpKey:        deref(b.GcpServiceAccountKey),
		gcpProject:    deref(b.GcpProject),
		azureTenant:   deref(b.AzureTenantId),
		azureClientID: deref(b.AzureClientId),
		azureSecret:   deref(b.AzureClientSecret),
		azureSub:      deref(b.AzureSubscriptionId),
	}
}

func ccFieldsFromUpdate(b *CloudCredentialUpdateRequest) ccFields {
	return ccFields{
		awsAccessKey:  deref(b.AwsAccessKeyId),
		awsSecret:     deref(b.AwsSecretAccessKey),
		awsSession:    deref(b.AwsSessionToken),
		awsRegion:     deref(b.AwsRegion),
		awsRoleARN:    deref(b.AwsRoleArn),
		gcpKey:        deref(b.GcpServiceAccountKey),
		gcpProject:    deref(b.GcpProject),
		azureTenant:   deref(b.AzureTenantId),
		azureClientID: deref(b.AzureClientId),
		azureSecret:   deref(b.AzureClientSecret),
		azureSub:      deref(b.AzureSubscriptionId),
	}
}

// credentialSupplied reports whether an update request carries any credential
// field (i.e. the caller wants to rotate the secret), as opposed to a
// metadata-only edit (name/description).
func credentialSupplied(b *CloudCredentialUpdateRequest) bool {
	return b.AwsAccessKeyId != nil || b.AwsSecretAccessKey != nil || b.AwsSessionToken != nil ||
		b.AwsRegion != nil || b.AwsRoleArn != nil ||
		b.GcpServiceAccountKey != nil || b.GcpProject != nil ||
		b.AzureTenantId != nil || b.AzureClientId != nil || b.AzureClientSecret != nil ||
		b.AzureSubscriptionId != nil
}

// buildCloudCredential validates the supplied fields for the chosen provider and
// splits them into the non-secret config map and the raw credential blob the
// service will seal. (Reuses required/jsonCreds/deref from cluster_connection.go.)
func buildCloudCredential(provider cloudcredential.Provider, f ccFields) (config map[string]string, creds []byte, err error) {
	switch provider {
	case cloudcredential.ProviderAWS:
		if err := required(map[string]string{
			"aws_access_key_id":     f.awsAccessKey,
			"aws_secret_access_key": f.awsSecret,
		}); err != nil {
			return nil, nil, err
		}
		cfg := map[string]string{}
		if f.awsRegion != "" {
			cfg[cloudcredentials.ConfigKeyAWSRegion] = f.awsRegion
		}
		if f.awsRoleARN != "" {
			cfg[cloudcredentials.ConfigKeyAWSRoleARN] = f.awsRoleARN
		}
		creds := jsonCreds(map[string]string{
			cloudcredentials.CredKeyAWSAccessKeyID:  f.awsAccessKey,
			cloudcredentials.CredKeyAWSSecretKey:    f.awsSecret,
			cloudcredentials.CredKeyAWSSessionToken: f.awsSession,
		})
		return cfg, creds, nil

	case cloudcredential.ProviderGcp:
		if err := required(map[string]string{"gcp_service_account_key": f.gcpKey}); err != nil {
			return nil, nil, err
		}
		cfg := map[string]string{}
		if f.gcpProject != "" {
			cfg[cloudcredentials.ConfigKeyGCPProject] = f.gcpProject
		}
		creds := jsonCreds(map[string]string{cloudcredentials.CredKeyGCPServiceKey: f.gcpKey})
		return cfg, creds, nil

	case cloudcredential.ProviderAzure:
		if err := required(map[string]string{
			"azure_tenant_id":     f.azureTenant,
			"azure_client_id":     f.azureClientID,
			"azure_client_secret": f.azureSecret,
		}); err != nil {
			return nil, nil, err
		}
		cfg := map[string]string{
			cloudcredentials.ConfigKeyAzureTenantID: f.azureTenant,
			cloudcredentials.ConfigKeyAzureClientID: f.azureClientID,
		}
		if f.azureSub != "" {
			cfg[cloudcredentials.ConfigKeyAzureSubscription] = f.azureSub
		}
		creds := jsonCreds(map[string]string{cloudcredentials.CredKeyAzureClientSecret: f.azureSecret})
		return cfg, creds, nil

	default:
		return nil, nil, fmt.Errorf("unknown provider %q", provider)
	}
}
