package k8s

import (
	"context"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	armcontainerservice "github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerservice/armcontainerservice/v4"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// aksAADServerScope is the well-known AKS AAD server application ID, used as the
// token scope when authenticating to an AAD-enabled cluster (the same value
// kubelogin requests).
const aksAADServerScope = "6dae42f8-4368-4678-94ff-3960e28e3630/.default"

// aksRESTConfig connects to an Azure AKS cluster: it authenticates with the
// stored service-principal credentials, fetches the cluster's user kubeconfig,
// and either uses its embedded credentials directly (non-AAD clusters) or mints
// an AAD bearer token (AAD-enabled clusters, whose kubeconfig would otherwise
// require the kubelogin exec plugin).
func aksRESTConfig(ctx context.Context, conn Connection) (*rest.Config, error) {
	sub := conn.Config[ConfigKeyAzureSubscription]
	rg := conn.Config[ConfigKeyAzureResourceGroup]
	clusterName := conn.Config[ConfigKeyAKSClusterName]
	tenant := conn.Config[ConfigKeyAzureTenantID]
	clientID := conn.Config[ConfigKeyAzureClientID]
	if sub == "" || rg == "" || clusterName == "" || tenant == "" || clientID == "" {
		return nil, fmt.Errorf("k8s/aks: subscription, resource group, cluster name, tenant id, and client id are required")
	}
	credMap, err := parseCredMap(conn.Credentials)
	if err != nil {
		return nil, err
	}
	clientSecret := credMap[CredKeyAzureClientSecret]
	if clientSecret == "" {
		return nil, fmt.Errorf("k8s/aks: an Azure client secret is required")
	}

	cred, err := azidentity.NewClientSecretCredential(tenant, clientID, clientSecret, nil)
	if err != nil {
		return nil, fmt.Errorf("k8s/aks: build credential: %w", err)
	}

	client, err := armcontainerservice.NewManagedClustersClient(sub, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("k8s/aks: managed clusters client: %w", err)
	}
	resp, err := client.ListClusterUserCredentials(ctx, rg, clusterName, nil)
	if err != nil {
		return nil, fmt.Errorf("k8s/aks: list cluster credentials: %w", err)
	}
	if len(resp.Kubeconfigs) == 0 || len(resp.Kubeconfigs[0].Value) == 0 {
		return nil, fmt.Errorf("k8s/aks: no kubeconfig returned for cluster")
	}
	kubeconfig := resp.Kubeconfigs[0].Value

	raw, err := clientcmd.Load(kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("k8s/aks: parse kubeconfig: %w", err)
	}
	kctx, ok := raw.Contexts[raw.CurrentContext]
	if !ok {
		return nil, fmt.Errorf("k8s/aks: kubeconfig has no current context")
	}
	clusterEntry, ok := raw.Clusters[kctx.Cluster]
	if !ok {
		return nil, fmt.Errorf("k8s/aks: kubeconfig missing cluster entry")
	}

	// Non-AAD clusters return a self-contained kubeconfig (client cert / token):
	// use it as-is. AAD clusters return an exec-based kubeconfig (kubelogin),
	// which can't run server-side — mint an AAD token instead.
	if authInfo, ok := raw.AuthInfos[kctx.AuthInfo]; !ok || authInfo.Exec == nil {
		return clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	}

	tok, err := cred.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{aksAADServerScope}})
	if err != nil {
		return nil, fmt.Errorf("k8s/aks: mint AAD token: %w", err)
	}
	return &rest.Config{
		Host:            clusterEntry.Server,
		BearerToken:     tok.Token,
		TLSClientConfig: rest.TLSClientConfig{CAData: clusterEntry.CertificateAuthorityData},
	}, nil
}
