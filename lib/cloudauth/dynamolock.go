package cloudauth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// DynamoLockClient is the slice of the DynamoDB API EnsureLockTable needs. A
// package seam (newDynamoClient) injects it so tests run the ensure logic
// against a fake without network access, mirroring newSTSClient.
type DynamoLockClient interface {
	DescribeTable(ctx context.Context, in *dynamodb.DescribeTableInput, opts ...func(*dynamodb.Options)) (*dynamodb.DescribeTableOutput, error)
	CreateTable(ctx context.Context, in *dynamodb.CreateTableInput, opts ...func(*dynamodb.Options)) (*dynamodb.CreateTableOutput, error)
}

var newDynamoClient = func(cfg aws.Config) DynamoLockClient {
	return dynamodb.NewFromConfig(cfg)
}

// lockTableWaitTimeout bounds the wait for a freshly created lock table to go
// ACTIVE. On-demand tables typically activate in seconds; the bound only
// matters when AWS is degraded, where failing the run beats hanging the
// worker.
const lockTableWaitTimeout = 2 * time.Minute

// EnsureLockTable makes sure the DynamoDB state-lock table an OpenTofu
// component's s3 backend names actually exists, creating it when it doesn't —
// the first-party half of DynamoDB locking: the user names a table and
// Spacefleet provisions it, instead of asking them to learn the LockID schema
// and create it by hand. It is called per run; the common case is a single
// DescribeTable confirming the table exists.
//
// env is the materialized AWS environment AWSEnv returned for the run's cloud
// credential (post any assume-role), so ensure authenticates as exactly the
// principal the run will without a second STS round trip. region is where the
// table must live — the s3 backend's region (OpenTofu looks the lock table up
// in the backend region, not the credential's default).
//
// Failure posture, deliberately asymmetric:
//   - DescribeTable says the table is missing → CreateTable (LockID string
//     hash key — the schema OpenTofu requires — on-demand billing so an idle
//     lock table costs nothing) and wait until it's ACTIVE. A create that
//     races a concurrent run's create (ResourceInUse) counts as success; any
//     other create failure is returned and fails the run loudly — the table
//     is definitely absent, so every subsequent lock attempt would fail
//     anyway, and at this point the user still gets a clear, actionable
//     error.
//   - DescribeTable fails any other way (e.g. the credential may lock but not
//     describe) → soft-pass with nil. The table may well exist; if it truly
//     doesn't, the run surfaces OpenTofu's own lock error. Failing here would
//     break working setups that simply haven't granted DescribeTable.
func EnsureLockTable(ctx context.Context, env map[string]string, region, table string) error {
	if region == "" {
		return fmt.Errorf("cloudauth: dynamodb lock table %q: no region to look it up in", table)
	}

	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(region),
		config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(
				env["AWS_ACCESS_KEY_ID"], env["AWS_SECRET_ACCESS_KEY"], env["AWS_SESSION_TOKEN"],
			),
		),
	)
	if err != nil {
		return fmt.Errorf("cloudauth: load aws config: %w", err)
	}
	client := newDynamoClient(cfg)

	_, err = client.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(table)})
	if err == nil {
		return nil
	}
	var notFound *ddbtypes.ResourceNotFoundException
	if !errors.As(err, &notFound) {
		return nil
	}

	_, err = client.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName: aws.String(table),
		AttributeDefinitions: []ddbtypes.AttributeDefinition{
			{AttributeName: aws.String("LockID"), AttributeType: ddbtypes.ScalarAttributeTypeS},
		},
		KeySchema: []ddbtypes.KeySchemaElement{
			{AttributeName: aws.String("LockID"), KeyType: ddbtypes.KeyTypeHash},
		},
		BillingMode: ddbtypes.BillingModePayPerRequest,
		Tags: []ddbtypes.Tag{
			{Key: aws.String("managed-by"), Value: aws.String("spacefleet")},
		},
	})
	if err != nil {
		var inUse *ddbtypes.ResourceInUseException
		if !errors.As(err, &inUse) {
			return fmt.Errorf("cloudauth: create dynamodb lock table %q in %s: %w", table, region, err)
		}
	}

	// Lock acquisition against a CREATING table fails, so wait for ACTIVE —
	// the standard TableExists waiter polls DescribeTable for exactly that.
	waiter := dynamodb.NewTableExistsWaiter(client)
	if err := waiter.Wait(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(table)}, lockTableWaitTimeout); err != nil {
		return fmt.Errorf("cloudauth: dynamodb lock table %q did not become active: %w", table, err)
	}
	return nil
}
