package applications

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// TestCreateRejectsNonSlugName proves the name slug check short-circuits before
// any database access: Create returns a validation error for a non-slug name on
// a service with a nil ent client (the name check runs first in validate). This
// keeps the rule covered by `make test` (the rest of the service is
// integration-tagged).
func TestCreateRejectsNonSlugName(t *testing.T) {
	t.Parallel()
	svc := NewService(nil)
	_, err := svc.Create(context.Background(), uuid.New(), CreateParams{
		Name:            "My App",
		RunnerClusterID: uuid.New(),
	})
	if !IsValidation(err) {
		t.Fatalf("non-slug name: want a validation error, got %v", err)
	}
}

// TestUpdateRejectsNonSlugName proves the same gate on the update path. The Get
// it would issue never runs because the name is validated first.
func TestUpdateRejectsNonSlugName(t *testing.T) {
	t.Parallel()
	svc := NewService(nil)
	bad := "Not A Slug"
	_, err := svc.Update(context.Background(), uuid.New(), uuid.New(), UpdateParams{Name: &bad})
	if !IsValidation(err) {
		t.Fatalf("non-slug name: want a validation error, got %v", err)
	}
}
