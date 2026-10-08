package workflows

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/componentrun"
)

// Component-run statuses, as RunStages combines them.
const (
	stepPending          = "pending"
	stepRunning          = "running"
	stepSucceeded        = "succeeded"
	stepFailed           = "failed"
	stepSkipped          = "skipped"
	stepAwaitingApproval = "awaiting_approval"
)

// RunStage is one stage of a run as it is shown: its name, the combined status
// of its components, and the components themselves in display order.
type RunStage struct {
	Name       string
	Status     string
	Components []RunStageComponent
}

// RunStageComponent is one authored component within a run's stage. An
// OpenTofu component runs as two steps (plan, then apply), so it lists both
// step ids; every other component has one. Status combines its steps.
type RunStageComponent struct {
	// ComponentID is the authored component's id (an OpenTofu apply step's own
	// id is derived from it; this is the component's).
	ComponentID uuid.UUID
	// Name is the component's name as it ran, without a step suffix.
	Name string
	Type string
	// Status is the steps' statuses combined (see combineStatus).
	Status string
	// ComponentRunIDs are the component runs (steps) of this component, in
	// run order.
	ComponentRunIDs []uuid.UUID
}

// RunStages summarizes a run stage by stage from its graph snapshot and its
// component runs (steps): each execution node is grouped under the authored
// component it came from, each component under the stage it ran in, and the
// statuses are combined up the way. A step with no component run reads as
// pending.
//
// A run started before workflows had stages records no stages; its stages are
// derived instead from the dependencies it recorded — each component is placed
// by its longest chain of dependencies, the same rule the move to stages used
// to place existing workflows — and named "Stage 1", "Stage 2", …. A snapshot
// that cannot be read yields a single stage listing each step on its own.
//
// It is pure (no ent queries, no I/O) so it is unit tested without a database.
func RunStages(graph string, steps []*ent.ComponentRun) []RunStage {
	stepByNode := make(map[uuid.UUID]*ent.ComponentRun, len(steps))
	for _, st := range steps {
		if st.ComponentID != uuid.Nil {
			stepByNode[st.ComponentID] = st
		}
	}

	var snap GraphSnapshot
	if graph == "" || json.Unmarshal([]byte(graph), &snap) != nil || len(snap.Nodes) == 0 {
		return stepsOnlyStages(steps)
	}

	// Group the execution nodes into authored components, in first-seen order.
	type job struct {
		RunStageComponent
		stageID *uuid.UUID
		nodes   []GraphNode
	}
	var jobs []*job
	jobOf := make(map[uuid.UUID]*job) // execution node id → its component
	byComponent := make(map[uuid.UUID]*job)
	for _, n := range snap.Nodes {
		cid := n.ComponentID
		if cid == uuid.Nil {
			cid = n.ID
		}
		j, ok := byComponent[cid]
		if !ok {
			j = &job{
				RunStageComponent: RunStageComponent{
					ComponentID: cid,
					Name:        componentNameOf(n),
					Type:        n.Type,
				},
				stageID: n.StageID,
			}
			byComponent[cid] = j
			jobs = append(jobs, j)
		}
		j.nodes = append(j.nodes, n)
		jobOf[n.ID] = j
	}
	for _, j := range jobs {
		statuses := make([]string, 0, len(j.nodes))
		j.ComponentRunIDs = make([]uuid.UUID, 0, len(j.nodes))
		for _, n := range j.nodes {
			if st, ok := stepByNode[n.ID]; ok {
				j.ComponentRunIDs = append(j.ComponentRunIDs, st.ID)
				statuses = append(statuses, string(st.Status))
			} else {
				statuses = append(statuses, stepPending)
			}
		}
		j.Status = combineStatus(statuses)
	}

	// Bucket the components into stages: the recorded ones when the snapshot
	// has them (and every component names one of them), else derived ones.
	var (
		names   []string
		buckets [][]*job
	)
	if index, ok := recordedStageIndex(snap.Stages, jobs, func(j *job) *uuid.UUID { return j.stageID }); ok {
		names = make([]string, len(snap.Stages))
		buckets = make([][]*job, len(snap.Stages))
		for i, st := range snap.Stages {
			names[i] = st.Name
		}
		for _, j := range jobs {
			i := index[*j.stageID]
			buckets[i] = append(buckets[i], j)
		}
	} else {
		deps := make(map[*job][]*job, len(jobs))
		for _, j := range jobs {
			seen := map[*job]bool{j: true}
			for _, n := range j.nodes {
				for _, d := range n.DependsOn {
					if dj, ok := jobOf[d]; ok && !seen[dj] {
						seen[dj] = true
						deps[j] = append(deps[j], dj)
					}
				}
			}
		}
		depth := longestPathDepths(jobs, deps)
		for _, j := range jobs {
			d := depth[j]
			for len(buckets) <= d {
				buckets = append(buckets, nil)
				names = append(names, fmt.Sprintf("Stage %d", len(names)+1))
			}
			buckets[d] = append(buckets[d], j)
		}
	}

	out := make([]RunStage, 0, len(buckets))
	for i, b := range buckets {
		if len(b) == 0 {
			continue
		}
		st := RunStage{Name: names[i], Components: make([]RunStageComponent, len(b))}
		statuses := make([]string, len(b))
		for k, j := range b {
			st.Components[k] = j.RunStageComponent
			statuses[k] = j.Status
		}
		st.Status = combineStatus(statuses)
		out = append(out, st)
	}
	return out
}

// recordedStageIndex maps each recorded stage id to its position, and reports
// whether the recorded stages can place every component — false when the
// snapshot predates stages or any component names no (or an unknown) stage.
func recordedStageIndex[J any](stages []GraphStage, jobs []J, stageOf func(J) *uuid.UUID) (map[uuid.UUID]int, bool) {
	if len(stages) == 0 {
		return nil, false
	}
	index := make(map[uuid.UUID]int, len(stages))
	for i, st := range stages {
		index[st.ID] = i
	}
	for _, j := range jobs {
		id := stageOf(j)
		if id == nil {
			return nil, false
		}
		if _, ok := index[*id]; !ok {
			return nil, false
		}
	}
	return index, true
}

// longestPathDepths places each item at the length of its longest chain of
// dependencies (no dependencies = 0). A dependency cycle can't occur in a
// recorded run, but the in-progress guard keeps a corrupt one from recursing
// forever (the cycle is cut where it is found).
func longestPathDepths[T comparable](items []T, deps map[T][]T) map[T]int {
	depth := make(map[T]int, len(items))
	visiting := make(map[T]bool)
	var resolve func(T) int
	resolve = func(it T) int {
		if d, ok := depth[it]; ok {
			return d
		}
		if visiting[it] {
			return 0
		}
		visiting[it] = true
		d := 0
		for _, dep := range deps[it] {
			if dd := resolve(dep) + 1; dd > d {
				d = dd
			}
		}
		visiting[it] = false
		depth[it] = d
		return d
	}
	for _, it := range items {
		resolve(it)
	}
	return depth
}

// componentNameOf recovers the authored component name from an execution
// node: an OpenTofu component's steps carry a " · plan" / " · apply" (or state
// operation) suffix that expandExecutionNodes added.
func componentNameOf(n GraphNode) string {
	if i := strings.Index(n.Name, " · "); i > 0 && n.Type == TypeTerraform {
		return n.Name[:i]
	}
	return n.Name
}

// stepsOnlyStages is the fallback summary for a run whose snapshot cannot be
// read: one stage, with each step as its own component.
func stepsOnlyStages(steps []*ent.ComponentRun) []RunStage {
	if len(steps) == 0 {
		return []RunStage{}
	}
	st := RunStage{Name: "Stage 1", Components: make([]RunStageComponent, len(steps))}
	statuses := make([]string, len(steps))
	for i, s := range steps {
		st.Components[i] = RunStageComponent{
			ComponentID:     s.ComponentID,
			Name:            s.Name,
			Type:            s.Type,
			Status:          string(s.Status),
			ComponentRunIDs: []uuid.UUID{s.ID},
		}
		statuses[i] = string(s.Status)
	}
	st.Status = combineStatus(statuses)
	return []RunStage{st}
}

// combineStatus folds several step (or component) statuses into one: failed
// if any failed, else awaiting approval if any is parked at a gate, else
// running if any is running — or if some have settled while others are still
// pending, which is a stage mid-flight. Otherwise everything has settled the
// same way (succeeded, skipped, or still all pending); a mix of succeeded and
// skipped reads as skipped, since the work did not all happen.
func combineStatus(statuses []string) string {
	if len(statuses) == 0 {
		return stepPending
	}
	has := make(map[string]bool, len(statuses))
	for _, s := range statuses {
		has[s] = true
	}
	switch {
	case has[stepFailed]:
		return stepFailed
	case has[stepAwaitingApproval]:
		return stepAwaitingApproval
	case has[stepRunning]:
		return stepRunning
	case has[stepPending] && len(has) > 1:
		return stepRunning
	case has[stepPending]:
		return stepPending
	case has[stepSkipped]:
		return stepSkipped
	default:
		return stepSucceeded
	}
}

// ListRunSteps returns the component runs (steps) of the given runs, keyed by
// run id, in created order — only the columns RunStages reads (never the
// logs), so summarizing a whole run list stays cheap. Strictly org-scoped.
func (s *Service) ListRunSteps(ctx context.Context, orgID uuid.UUID, runIDs []uuid.UUID) (map[uuid.UUID][]*ent.ComponentRun, error) {
	out := make(map[uuid.UUID][]*ent.ComponentRun, len(runIDs))
	if len(runIDs) == 0 {
		return out, nil
	}
	steps, err := s.ent.ComponentRun.Query().
		Where(componentrun.OrganizationID(orgID), componentrun.WorkflowRunIDIn(runIDs...)).
		Order(ent.Asc(componentrun.FieldCreatedAt)).
		Select(
			componentrun.FieldID,
			componentrun.FieldWorkflowRunID,
			componentrun.FieldComponentID,
			componentrun.FieldName,
			componentrun.FieldType,
			componentrun.FieldStatus,
		).
		All(ctx)
	if err != nil {
		return nil, err
	}
	for _, st := range steps {
		out[st.WorkflowRunID] = append(out[st.WorkflowRunID], st)
	}
	return out, nil
}
