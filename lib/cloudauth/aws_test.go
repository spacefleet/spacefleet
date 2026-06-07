package cloudauth

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	ststypes "github.com/aws/aws-sdk-go-v2/service/sts/types"

	"github.com/spacefleet/spacefleet/ent/cloudcredential"
	"github.com/spacefleet/spacefleet/lib/cloudcredentials"
)

func TestAWSEnv_NoRole(t *testing.T) {
	r := cloudcredentials.Resolved{
		Provider: cloudcredential.ProviderAWS,
		Config:   map[string]string{cloudcredentials.ConfigKeyAWSRegion: "us-east-1"},
		Secrets: map[string]string{
			cloudcredentials.CredKeyAWSAccessKeyID: "AKIABASE",
			cloudcredentials.CredKeyAWSSecretKey:   "basesecret",
		},
	}

	secret, region, err := AWSEnv(context.Background(), r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if region != "us-east-1" {
		t.Errorf("region = %q, want us-east-1", region)
	}
	if secret["AWS_ACCESS_KEY_ID"] != "AKIABASE" {
		t.Errorf("AWS_ACCESS_KEY_ID = %q, want AKIABASE", secret["AWS_ACCESS_KEY_ID"])
	}
	if secret["AWS_SECRET_ACCESS_KEY"] != "basesecret" {
		t.Errorf("AWS_SECRET_ACCESS_KEY = %q, want basesecret", secret["AWS_SECRET_ACCESS_KEY"])
	}
	if _, ok := secret["AWS_SESSION_TOKEN"]; ok {
		t.Errorf("AWS_SESSION_TOKEN should not be set when absent, got %q", secret["AWS_SESSION_TOKEN"])
	}
}

func TestAWSEnv_NoRole_WithSessionToken(t *testing.T) {
	r := cloudcredentials.Resolved{
		Provider: cloudcredential.ProviderAWS,
		Config:   map[string]string{cloudcredentials.ConfigKeyAWSRegion: "eu-west-1"},
		Secrets: map[string]string{
			cloudcredentials.CredKeyAWSAccessKeyID:  "AKIABASE",
			cloudcredentials.CredKeyAWSSecretKey:    "basesecret",
			cloudcredentials.CredKeyAWSSessionToken: "basetoken",
		},
	}

	secret, region, err := AWSEnv(context.Background(), r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if region != "eu-west-1" {
		t.Errorf("region = %q, want eu-west-1", region)
	}
	if secret["AWS_SESSION_TOKEN"] != "basetoken" {
		t.Errorf("AWS_SESSION_TOKEN = %q, want basetoken", secret["AWS_SESSION_TOKEN"])
	}
}

// fakeSTS is an injected stscreds.AssumeRoleAPIClient that returns canned
// session credentials without any network call.
type fakeSTS struct {
	out *sts.AssumeRoleOutput
	err error
}

func (f *fakeSTS) AssumeRole(_ context.Context, _ *sts.AssumeRoleInput, _ ...func(*sts.Options)) (*sts.AssumeRoleOutput, error) {
	return f.out, f.err
}

func TestAWSEnv_AssumeRole(t *testing.T) {
	fake := &fakeSTS{
		out: &sts.AssumeRoleOutput{
			Credentials: &ststypes.Credentials{
				AccessKeyId:     aws.String("AKIASESSION"),
				SecretAccessKey: aws.String("sessionsecret"),
				SessionToken:    aws.String("sessiontoken"),
				Expiration:      aws.Time(time.Now().Add(time.Hour)),
			},
		},
	}

	orig := newSTSClient
	newSTSClient = func(aws.Config) stscreds.AssumeRoleAPIClient { return fake }
	t.Cleanup(func() { newSTSClient = orig })

	r := cloudcredentials.Resolved{
		Provider: cloudcredential.ProviderAWS,
		Config: map[string]string{
			cloudcredentials.ConfigKeyAWSRegion:  "us-east-1",
			cloudcredentials.ConfigKeyAWSRoleARN: "arn:aws:iam::123456789012:role/deployer",
		},
		Secrets: map[string]string{
			cloudcredentials.CredKeyAWSAccessKeyID: "AKIABASE",
			cloudcredentials.CredKeyAWSSecretKey:   "basesecret",
		},
	}

	secret, region, err := AWSEnv(context.Background(), r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if region != "us-east-1" {
		t.Errorf("region = %q, want us-east-1 (base config region)", region)
	}
	if secret["AWS_ACCESS_KEY_ID"] != "AKIASESSION" {
		t.Errorf("AWS_ACCESS_KEY_ID = %q, want AKIASESSION", secret["AWS_ACCESS_KEY_ID"])
	}
	if secret["AWS_SECRET_ACCESS_KEY"] != "sessionsecret" {
		t.Errorf("AWS_SECRET_ACCESS_KEY = %q, want sessionsecret", secret["AWS_SECRET_ACCESS_KEY"])
	}
	if secret["AWS_SESSION_TOKEN"] != "sessiontoken" {
		t.Errorf("AWS_SESSION_TOKEN = %q, want sessiontoken", secret["AWS_SESSION_TOKEN"])
	}
}

func TestAWSEnv_NonAWSProvider(t *testing.T) {
	r := cloudcredentials.Resolved{
		Provider: cloudcredential.ProviderGcp,
		Secrets: map[string]string{
			cloudcredentials.CredKeyAWSAccessKeyID: "AKIABASE",
			cloudcredentials.CredKeyAWSSecretKey:   "basesecret",
		},
	}
	if _, _, err := AWSEnv(context.Background(), r); err == nil {
		t.Fatal("expected error for non-aws provider, got nil")
	}
}

func TestAWSEnv_MissingKeys(t *testing.T) {
	cases := map[string]map[string]string{
		"missing access key": {cloudcredentials.CredKeyAWSSecretKey: "basesecret"},
		"missing secret key": {cloudcredentials.CredKeyAWSAccessKeyID: "AKIABASE"},
		"both missing":       {},
	}
	for name, secrets := range cases {
		t.Run(name, func(t *testing.T) {
			r := cloudcredentials.Resolved{
				Provider: cloudcredential.ProviderAWS,
				Config:   map[string]string{},
				Secrets:  secrets,
			}
			if _, _, err := AWSEnv(context.Background(), r); err == nil {
				t.Fatal("expected error for missing keys, got nil")
			}
		})
	}
}
