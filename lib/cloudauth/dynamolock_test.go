package cloudauth

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// fakeDynamo scripts DescribeTable responses in order (the ensure flow
// describes once up front, then the ACTIVE waiter describes again after a
// create) and records whether/what CreateTable was called with.
type fakeDynamo struct {
	describeErrs []error // per-call; nil entry = table exists and is ACTIVE
	describes    int
	created      *dynamodb.CreateTableInput
	createErr    error
}

func (f *fakeDynamo) DescribeTable(_ context.Context, in *dynamodb.DescribeTableInput, _ ...func(*dynamodb.Options)) (*dynamodb.DescribeTableOutput, error) {
	i := f.describes
	f.describes++
	if i < len(f.describeErrs) && f.describeErrs[i] != nil {
		return nil, f.describeErrs[i]
	}
	return &dynamodb.DescribeTableOutput{Table: &ddbtypes.TableDescription{
		TableName:   in.TableName,
		TableStatus: ddbtypes.TableStatusActive,
	}}, nil
}

func (f *fakeDynamo) CreateTable(_ context.Context, in *dynamodb.CreateTableInput, _ ...func(*dynamodb.Options)) (*dynamodb.CreateTableOutput, error) {
	f.created = in
	if f.createErr != nil {
		return nil, f.createErr
	}
	return &dynamodb.CreateTableOutput{}, nil
}

func injectDynamo(t *testing.T, f *fakeDynamo) {
	t.Helper()
	orig := newDynamoClient
	newDynamoClient = func(aws.Config) DynamoLockClient { return f }
	t.Cleanup(func() { newDynamoClient = orig })
}

// awsEnv is the materialized credential env the resolver hands EnsureLockTable
// (the AWSEnv output shape).
func awsEnv() map[string]string {
	return map[string]string{
		"AWS_ACCESS_KEY_ID":     "AKIABASE",
		"AWS_SECRET_ACCESS_KEY": "basesecret",
	}
}

func TestEnsureLockTable_TableExists(t *testing.T) {
	f := &fakeDynamo{}
	injectDynamo(t, f)

	if err := EnsureLockTable(context.Background(), awsEnv(), "us-east-1", "tf-locks"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.created != nil {
		t.Fatal("CreateTable must not be called when the table exists")
	}
	if f.describes != 1 {
		t.Fatalf("describes = %d, want exactly 1 for the exists fast path", f.describes)
	}
}

func TestEnsureLockTable_CreatesMissingTable(t *testing.T) {
	f := &fakeDynamo{describeErrs: []error{&ddbtypes.ResourceNotFoundException{}}}
	injectDynamo(t, f)

	if err := EnsureLockTable(context.Background(), awsEnv(), "us-east-1", "tf-locks"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.created == nil {
		t.Fatal("expected CreateTable for a missing table")
	}
	if got := aws.ToString(f.created.TableName); got != "tf-locks" {
		t.Errorf("created table %q, want tf-locks", got)
	}
	// The exact schema OpenTofu's s3 backend requires: a LockID string hash key.
	if len(f.created.KeySchema) != 1 ||
		aws.ToString(f.created.KeySchema[0].AttributeName) != "LockID" ||
		f.created.KeySchema[0].KeyType != ddbtypes.KeyTypeHash {
		t.Errorf("key schema = %+v, want a single LockID HASH key", f.created.KeySchema)
	}
	if len(f.created.AttributeDefinitions) != 1 ||
		f.created.AttributeDefinitions[0].AttributeType != ddbtypes.ScalarAttributeTypeS {
		t.Errorf("attribute definitions = %+v, want a single LockID string", f.created.AttributeDefinitions)
	}
	if f.created.BillingMode != ddbtypes.BillingModePayPerRequest {
		t.Errorf("billing mode = %v, want on-demand", f.created.BillingMode)
	}
}

func TestEnsureLockTable_CreateRaceCountsAsSuccess(t *testing.T) {
	f := &fakeDynamo{
		describeErrs: []error{&ddbtypes.ResourceNotFoundException{}},
		createErr:    &ddbtypes.ResourceInUseException{},
	}
	injectDynamo(t, f)

	if err := EnsureLockTable(context.Background(), awsEnv(), "us-east-1", "tf-locks"); err != nil {
		t.Fatalf("a create losing the race to a concurrent run must not fail the run: %v", err)
	}
}

func TestEnsureLockTable_CreateFailureIsLoud(t *testing.T) {
	f := &fakeDynamo{
		describeErrs: []error{&ddbtypes.ResourceNotFoundException{}},
		createErr:    errors.New("AccessDeniedException: not authorized to CreateTable"),
	}
	injectDynamo(t, f)

	err := EnsureLockTable(context.Background(), awsEnv(), "us-east-1", "tf-locks")
	if err == nil {
		t.Fatal("a table that is definitely missing and cannot be created must fail the run")
	}
}

func TestEnsureLockTable_DescribeDeniedSoftPasses(t *testing.T) {
	// A credential that can lock but not describe must not break a working
	// setup: ensure soft-passes and lets the run surface any genuine failure.
	f := &fakeDynamo{describeErrs: []error{errors.New("AccessDeniedException: no DescribeTable")}}
	injectDynamo(t, f)

	if err := EnsureLockTable(context.Background(), awsEnv(), "us-east-1", "tf-locks"); err != nil {
		t.Fatalf("non-NotFound describe failures must soft-pass, got: %v", err)
	}
	if f.created != nil {
		t.Fatal("must not attempt create when the table's existence is unknown")
	}
}

func TestEnsureLockTable_NoRegion(t *testing.T) {
	f := &fakeDynamo{}
	injectDynamo(t, f)

	// The s3 backend's region is validated at write time, so a blank one here
	// is a programming error — fail loudly rather than guessing a region.
	if err := EnsureLockTable(context.Background(), awsEnv(), "", "tf-locks"); err == nil {
		t.Fatal("expected an error when no region is known")
	}
	if f.describes != 0 {
		t.Fatal("must not call DynamoDB without a region")
	}
}
