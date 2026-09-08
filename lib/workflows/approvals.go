package workflows

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/componentrun"
	"github.com/spacefleet/spacefleet/ent/schema"
	"github.com/spacefleet/spacefleet/ent/workflowrun"
)

// ApprovalPolicy is the per-component approval policy (see the ent schema
// type): who may approve, how many approvals open the gate, whether the run's
// starter may approve, and how long a parked gate waits. The zero value is the
// original behaviour.
type ApprovalPolicy = schema.ApprovalPolicy

// Approval is one recorded approval on a parked gate, as stored in the
// component run's approvals JSON array.
type Approval struct {
	By string    `json:"by"`
	At time.Time `json:"at"`
}

// Approval-policy errors ApproveComponentRun returns; a handler maps the
// first two to 403 and the third to 409.
var (
	ErrNotAnApprover   = errors.New("workflows: you are not one of this step's named approvers")
	ErrSelfApproval    = errors.New("workflows: the person who started the run cannot approve it (the step requires a different approver)")
	ErrAlreadyApproved = errors.New("workflows: you have already approved this step")
)

// maxApprovers bounds a policy's approver list; maxApprovalTimeoutMinutes its
// timeout (a week).
const (
	maxApprovers              = 50
	maxApprovalTimeoutMinutes = 7 * 24 * 60
)

// normalizeApprovalPolicy validates and canonicalises a node's policy at
// write time: approver emails are trimmed, lowercased, non-empty, and unique;
// Required must be at least 1 and, with named approvers, no more than their
// number (0 reads as 1); the timeout is bounded. Failures wrap ErrInvalidConfig.
func normalizeApprovalPolicy(n ComponentInput) (ApprovalPolicy, error) {
	p := n.ApprovalPolicy
	seen := make(map[string]bool, len(p.Approvers))
	approvers := make([]string, 0, len(p.Approvers))
	for _, a := range p.Approvers {
		a = strings.ToLower(strings.TrimSpace(a))
		if a == "" {
			return p, fmt.Errorf("%w: node %q approval_policy.approvers must not contain an empty entry", ErrInvalidConfig, n.Name)
		}
		if strings.ContainsAny(a, " \t\r\n,") {
			return p, fmt.Errorf("%w: node %q approval_policy.approvers entry %q must be a single email address", ErrInvalidConfig, n.Name, a)
		}
		if !seen[a] {
			seen[a] = true
			approvers = append(approvers, a)
		}
	}
	if len(approvers) > maxApprovers {
		return p, fmt.Errorf("%w: node %q approval_policy.approvers lists more than %d approvers", ErrInvalidConfig, n.Name, maxApprovers)
	}
	if p.Required < 0 {
		return p, fmt.Errorf("%w: node %q approval_policy.required must not be negative", ErrInvalidConfig, n.Name)
	}
	if len(approvers) > 0 && p.Required > len(approvers) {
		return p, fmt.Errorf("%w: node %q approval_policy.required (%d) exceeds the number of named approvers (%d)", ErrInvalidConfig, n.Name, p.Required, len(approvers))
	}
	if p.TimeoutMinutes < 0 || p.TimeoutMinutes > maxApprovalTimeoutMinutes {
		return p, fmt.Errorf("%w: node %q approval_policy.timeout_minutes must be between 0 and %d", ErrInvalidConfig, n.Name, maxApprovalTimeoutMinutes)
	}
	if len(approvers) == 0 {
		approvers = nil
	}
	return ApprovalPolicy{
		Approvers:                approvers,
		Required:                 p.Required,
		RequireDifferentApprover: p.RequireDifferentApprover,
		TimeoutMinutes:           p.TimeoutMinutes,
	}, nil
}

// isZeroPolicy reports whether a policy is the default (nothing to snapshot).
func isZeroPolicy(p ApprovalPolicy) bool {
	return len(p.Approvers) == 0 && p.Required <= 1 && !p.RequireDifferentApprover && p.TimeoutMinutes == 0
}

// approvalsRequired is the number of approvals a policy needs (at least one).
func approvalsRequired(p *ApprovalPolicy) int {
	if p == nil || p.Required < 1 {
		return 1
	}
	return p.Required
}

// snapshotPolicy returns the approval policy of the execution unit id in a
// run's graph snapshot, or nil for the default policy (or an unparseable
// snapshot — the run then behaves as before this policy existed).
func snapshotPolicy(graph string, unitID uuid.UUID) *ApprovalPolicy {
	if graph == "" {
		return nil
	}
	var snap GraphSnapshot
	if err := json.Unmarshal([]byte(graph), &snap); err != nil {
		return nil
	}
	for _, n := range snap.Nodes {
		if n.ID == unitID {
			return n.ApprovalPolicy
		}
	}
	return nil
}

// ParseApprovals decodes a component run's approvals JSON; empty or garbled
// input yields nil.
func ParseApprovals(raw string) []Approval {
	if raw == "" {
		return nil
	}
	var out []Approval
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

// SetRunStartedBy records who started a run (the user's email), for the
// no-self-approval rule and the run history. Org-scoped; a run not in the org
// updates zero rows and surfaces as NotFound.
func (s *Service) SetRunStartedBy(ctx context.Context, orgID, runID uuid.UUID, startedBy string) error {
	affected, err := s.ent.WorkflowRun.Update().
		Where(workflowrun.OrganizationID(orgID), workflowrun.ID(runID)).
		SetStartedBy(startedBy).
		Save(ctx)
	if err != nil {
		return err
	}
	if affected == 0 {
		return &ent.NotFoundError{}
	}
	return nil
}

// ReapExpiredApprovals fails every run parked at an approval gate whose
// policy names a timeout that has elapsed since the step last changed (it
// parked, or received a partial approval): the parked step settles failed
// ("approval timed out"), the run settles failed, and its other non-terminal
// steps are skipped — the same shape as a rejection. It scans across
// organizations like ReapStuckRuns. Returns the number of runs failed.
func (s *Service) ReapExpiredApprovals(ctx context.Context) (int, error) {
	parked, err := s.ent.WorkflowRun.Query().
		Where(workflowrun.StatusEQ(workflowrun.StatusAwaitingApproval)).
		All(ctx)
	if err != nil {
		return 0, err
	}
	now := time.Now()
	failed := 0
	for _, run := range parked {
		steps, err := s.ent.ComponentRun.Query().
			Where(
				componentrun.OrganizationID(run.OrganizationID),
				componentrun.WorkflowRunID(run.ID),
				componentrun.StatusEQ(componentrun.StatusAwaitingApproval),
			).
			All(ctx)
		if err != nil {
			log.Printf("worker: approval timeouts: run %s: %v", run.ID, err)
			continue
		}
		expired := false
		for _, cr := range steps {
			p := snapshotPolicy(run.Graph, cr.ComponentID)
			if p == nil || p.TimeoutMinutes <= 0 {
				continue
			}
			if now.Sub(cr.UpdatedAt) < time.Duration(p.TimeoutMinutes)*time.Minute {
				continue
			}
			if _, err := s.ent.ComponentRun.Update().
				Where(componentrun.ID(cr.ID), componentrun.StatusEQ(componentrun.StatusAwaitingApproval)).
				SetStatus(componentrun.StatusFailed).
				SetMessage(fmt.Sprintf("approval timed out after %d minutes", p.TimeoutMinutes)).
				SetFinishedAt(now).
				Save(ctx); err != nil {
				log.Printf("worker: approval timeouts: step %s: %v", cr.ID, err)
				continue
			}
			expired = true
		}
		if !expired {
			continue
		}
		if err := s.MarkRun(ctx, run.OrganizationID, run.ID, "failed", "approval timed out"); err != nil {
			continue
		}
		_, _ = s.SettleStuckComponentRuns(ctx, run.OrganizationID, run.ID, "skipped (approval timed out)")
		s.emitRunEvent(ctx, run.OrganizationID, run.ID, EventRunFailed)
		failed++
		if s.reapHook != nil {
			s.reapHook(ctx, run)
		}
	}
	return failed, nil
}
