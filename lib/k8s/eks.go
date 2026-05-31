package k8s

import (
	"context"
	"encoding/base64"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"k8s.io/client-go/rest"
	awstoken "sigs.k8s.io/aws-iam-authenticator/pkg/token"
)

// eksRESTConfig connects to an Amazon EKS cluster: it authenticates with the
// stored AWS credentials (optionally assuming a role), discovers the API
// endpoint and CA via eks:DescribeCluster, and mints a short-lived bearer token
// the same way `aws eks get-token` does (a presigned STS GetCallerIdentity
// request, via aws-iam-authenticator).
func eksRESTConfig(ctx context.Context, conn Connection) (*rest.Config, error) {
	region := conn.Config[ConfigKeyAWSRegion]
	clusterName := conn.Config[ConfigKeyEKSClusterName]
	if region == "" || clusterName == "" {
		return nil, fmt.Errorf("k8s/eks: region and cluster name are required")
	}
	creds, err := parseCredMap(conn.Credentials)
	if err != nil {
		return nil, err
	}
	accessKey, secret := creds[CredKeyAWSAccessKeyID], creds[CredKeyAWSSecretKey]
	if accessKey == "" || secret == "" {
		return nil, fmt.Errorf("k8s/eks: AWS access key id and secret access key are required")
	}

	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(region),
		config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(accessKey, secret, creds[CredKeyAWSSessionToken]),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("k8s/eks: load aws config: %w", err)
	}
	if roleARN := conn.Config[ConfigKeyAWSRoleARN]; roleARN != "" {
		cfg.Credentials = aws.NewCredentialsCache(
			stscreds.NewAssumeRoleProvider(sts.NewFromConfig(cfg), roleARN),
		)
	}

	// Discover the API endpoint and CA from the cluster description.
	desc, err := eks.NewFromConfig(cfg).DescribeCluster(ctx, &eks.DescribeClusterInput{
		Name: aws.String(clusterName),
	})
	if err != nil {
		return nil, fmt.Errorf("k8s/eks: describe cluster: %w", err)
	}
	c := desc.Cluster
	if c == nil || c.Endpoint == nil || c.CertificateAuthority == nil || c.CertificateAuthority.Data == nil {
		return nil, fmt.Errorf("k8s/eks: cluster description missing endpoint or CA")
	}
	caData, err := base64.StdEncoding.DecodeString(*c.CertificateAuthority.Data)
	if err != nil {
		return nil, fmt.Errorf("k8s/eks: decode CA: %w", err)
	}

	gen, err := awstoken.NewGenerator(false, false)
	if err != nil {
		return nil, fmt.Errorf("k8s/eks: token generator: %w", err)
	}
	tok, err := gen.GetWithSTS(clusterName, sts.NewFromConfig(cfg))
	if err != nil {
		return nil, fmt.Errorf("k8s/eks: get token: %w", err)
	}

	return &rest.Config{
		Host:            *c.Endpoint,
		BearerToken:     tok.Token,
		TLSClientConfig: rest.TLSClientConfig{CAData: caData},
	}, nil
}
