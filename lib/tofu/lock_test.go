package tofu

import "testing"

// A failed plan step's captured logs: init chatter, then the boxed lock
// error OpenTofu prints (the gutter survives -no-color).
const sampleLockLogs = `Initializing the backend...

Successfully configured the backend "s3"! OpenTofu will automatically
use this backend unless the backend configuration changes.
╷
│ Error: Error acquiring the state lock
│ 
│ Error message: operation error DynamoDB: PutItem, ConditionalCheckFailedException: The conditional request failed
│ Lock Info:
│   ID:        6ea66d5f-8c3c-4d0d-8ecf-1d0e7a3f1c0f
│   Path:      acme-state/prod/terraform.tfstate
│   Operation: OperationTypeApply
│   Who:       root@tofu-infra-plan-abc12-pod
│   Version:   1.8.5
│   Created:   2026-09-01 12:34:56.789012 +0000 UTC
│   Info:      
│ 
│ 
│ OpenTofu acquires a state lock to protect the state from being written
│ by multiple users at the same time. Please resolve the issue above and try
│ again. For most commands, you can disable locking with the "-lock=false"
│ flag, but this is not recommended.
╵
`

func TestParseLockInfo(t *testing.T) {
	t.Parallel()
	got := ParseLockInfo(sampleLockLogs)
	if got == nil {
		t.Fatal("expected a lock")
	}
	want := LockInfo{
		ID:        "6ea66d5f-8c3c-4d0d-8ecf-1d0e7a3f1c0f",
		Path:      "acme-state/prod/terraform.tfstate",
		Operation: "OperationTypeApply",
		Who:       "root@tofu-infra-plan-abc12-pod",
		Version:   "1.8.5",
		Created:   "2026-09-01 12:34:56.789012 +0000 UTC",
	}
	if *got != want {
		t.Errorf("lock = %+v, want %+v", *got, want)
	}

	// Without the gutter (some backends print the block unboxed) the same
	// fields parse; a Created value keeps its own colons.
	plain := "Error: Error acquiring the state lock\n\nLock Info:\n  ID:        abc\n  Created:   2026-09-01 12:34:56 +0000 UTC\n\nOpenTofu acquires a state lock"
	if got := ParseLockInfo(plain); got == nil || got.ID != "abc" || got.Created != "2026-09-01 12:34:56 +0000 UTC" {
		t.Errorf("unboxed lock = %+v", got)
	}

	// No lock error, or one without an id, yields nothing to release.
	for name, logs := range map[string]string{
		"clean plan": samplePlanLogs,
		"empty":      "",
		"no id":      "Error acquiring the state lock\nLock Info:\n  Path: p\n",
		"no block":   "Error acquiring the state lock\nError message: timeout\n",
	} {
		if got := ParseLockInfo(logs); got != nil {
			t.Errorf("%s: got %+v, want nil", name, got)
		}
	}
}
