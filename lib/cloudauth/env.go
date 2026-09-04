package cloudauth

import (
	"context"
	"fmt"

	"github.com/spacefleet/spacefleet/ent/cloudcredential"
	"github.com/spacefleet/spacefleet/lib/cloudcredentials"
)

// Env materializes the environment an OpenTofu step exports so its state
// backend and the module's providers authenticate as the given cloud
// credential, for every supported provider:
//
//   - aws:   AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY (/ AWS_SESSION_TOKEN),
//     after pre-assuming any role_arn via STS (see AWSEnv); AWS_REGION when
//     the credential names a default region.
//   - gcp:   GOOGLE_CREDENTIALS carrying the service-account JSON key — the
//     variable both the gcs backend and the google provider read the key's
//     contents from; GOOGLE_PROJECT (and CLOUDSDK_CORE_PROJECT) when a
//     project is set, the provider's default project.
//   - azure: ARM_CLIENT_SECRET, with ARM_CLIENT_ID / ARM_TENANT_ID /
//     ARM_SUBSCRIPTION_ID — the client-secret (service principal)
//     authentication both the azurerm backend and the azurerm provider read.
//
// The split is the security posture: secret carries the keys and is meant to
// be mounted into the step's pod as a sourced credentials file, never placed
// on the pod's env block; plain carries only non-secret identifiers that are
// safe on the env block.
func Env(ctx context.Context, r cloudcredentials.Resolved) (secret, plain map[string]string, err error) {
	switch r.Provider {
	case cloudcredential.ProviderAWS:
		secret, region, err := AWSEnv(ctx, r)
		if err != nil {
			return nil, nil, err
		}
		plain = map[string]string{}
		if region != "" {
			plain["AWS_REGION"] = region
		}
		return secret, plain, nil
	case cloudcredential.ProviderGcp:
		key := r.Secrets[cloudcredentials.CredKeyGCPServiceKey]
		if key == "" {
			return nil, nil, fmt.Errorf("cloudauth: gcp credential is missing its service account key")
		}
		secret = map[string]string{"GOOGLE_CREDENTIALS": key}
		plain = map[string]string{}
		if project := r.Config[cloudcredentials.ConfigKeyGCPProject]; project != "" {
			plain["GOOGLE_PROJECT"] = project
			plain["CLOUDSDK_CORE_PROJECT"] = project
		}
		return secret, plain, nil
	case cloudcredential.ProviderAzure:
		clientID := r.Config[cloudcredentials.ConfigKeyAzureClientID]
		tenantID := r.Config[cloudcredentials.ConfigKeyAzureTenantID]
		clientSecret := r.Secrets[cloudcredentials.CredKeyAzureClientSecret]
		if clientID == "" || tenantID == "" || clientSecret == "" {
			return nil, nil, fmt.Errorf("cloudauth: azure credential is missing tenant_id, client_id, or client_secret")
		}
		secret = map[string]string{"ARM_CLIENT_SECRET": clientSecret}
		plain = map[string]string{
			"ARM_CLIENT_ID": clientID,
			"ARM_TENANT_ID": tenantID,
		}
		if sub := r.Config[cloudcredentials.ConfigKeyAzureSubscription]; sub != "" {
			plain["ARM_SUBSCRIPTION_ID"] = sub
		}
		return secret, plain, nil
	default:
		return nil, nil, fmt.Errorf("cloudauth: provider %q is not supported for OpenTofu authentication", r.Provider)
	}
}
