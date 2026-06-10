// Package cloudauth turns a resolved cloud credential into the environment an
// OpenTofu (Terraform) step needs so the s3 backend and the AWS provider both
// authenticate as that credential. When the credential carries a role_arn it
// pre-assumes the role via STS here, so the materialized short-lived session
// keys — not a long-lived static key plus an in-process assume — are what the
// step exports. v1 is AWS-only (the s3 backend), matching the storage backend
// the OpenTofu feature ships.
//
// Security posture: the secret keys returned here are meant to be mounted into
// the step's pod as a credentials file, never placed on the pod's env block —
// only the non-secret region is safe to route to env. AWSEnv keeps that split
// explicit by returning the secret map and the region separately.
package cloudauth

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/spacefleet/spacefleet/ent/cloudcredential"
	"github.com/spacefleet/spacefleet/lib/cloudcredentials"
)

// newSTSClient builds the STS client used to assume a role. It is a package
// variable so tests can swap in a fake AssumeRoleAPIClient and exercise the
// assume-role path without network access; production uses sts.NewFromConfig.
var newSTSClient = func(cfg aws.Config) stscreds.AssumeRoleAPIClient {
	return sts.NewFromConfig(cfg)
}

// AWSEnv returns the AWS environment variables an OpenTofu step exports so the
// s3 backend and the AWS provider authenticate as the given cloud credential.
// When the credential carries a role_arn, it pre-assumes that role via STS and
// returns the resulting session credentials; otherwise it returns the base keys.
// Region (non-secret) is returned separately so the caller can route it to the
// pod env while the keys go into a mounted secret file.
func AWSEnv(ctx context.Context, r cloudcredentials.Resolved) (secret map[string]string, region string, err error) {
	accessKey, secretKey, sessionToken, region, err := awsCredentials(ctx, r)
	if err != nil {
		return nil, "", err
	}
	secret = map[string]string{
		"AWS_ACCESS_KEY_ID":     accessKey,
		"AWS_SECRET_ACCESS_KEY": secretKey,
	}
	if sessionToken != "" {
		secret["AWS_SESSION_TOKEN"] = sessionToken
	}
	return secret, region, nil
}

// awsCredentials resolves the effective AWS credential material for r —
// validating the provider and key presence and pre-assuming any role via STS —
// so every cloudauth entry point (the step env materializer above, the
// lock-table ensure in dynamolock.go) authenticates as exactly the same
// principal the run will.
func awsCredentials(ctx context.Context, r cloudcredentials.Resolved) (accessKey, secretKey, sessionToken, region string, err error) {
	if r.Provider != cloudcredential.ProviderAWS {
		return "", "", "", "", fmt.Errorf("cloudauth: provider %q not supported for terraform backend auth (v1 is aws only)", r.Provider)
	}

	accessKey = r.Secrets[cloudcredentials.CredKeyAWSAccessKeyID]
	secretKey = r.Secrets[cloudcredentials.CredKeyAWSSecretKey]
	sessionToken = r.Secrets[cloudcredentials.CredKeyAWSSessionToken]
	if accessKey == "" || secretKey == "" {
		return "", "", "", "", fmt.Errorf("cloudauth: aws credential is missing access_key_id or secret_access_key")
	}

	region = r.Config[cloudcredentials.ConfigKeyAWSRegion]
	roleARN := r.Config[cloudcredentials.ConfigKeyAWSRoleARN]

	if roleARN != "" {
		cfg, err := config.LoadDefaultConfig(ctx,
			config.WithRegion(region),
			config.WithCredentialsProvider(
				credentials.NewStaticCredentialsProvider(accessKey, secretKey, sessionToken),
			),
		)
		if err != nil {
			return "", "", "", "", fmt.Errorf("cloudauth: load aws config: %w", err)
		}
		assumed := aws.NewCredentialsCache(
			stscreds.NewAssumeRoleProvider(newSTSClient(cfg), roleARN),
		)
		got, err := assumed.Retrieve(ctx)
		if err != nil {
			return "", "", "", "", fmt.Errorf("cloudauth: assume role %q: %w", roleARN, err)
		}
		accessKey = got.AccessKeyID
		secretKey = got.SecretAccessKey
		sessionToken = got.SessionToken
	}

	return accessKey, secretKey, sessionToken, region, nil
}
