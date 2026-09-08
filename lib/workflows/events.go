package workflows

import (
	"context"
	"log"
	"time"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/componentrun"
	"github.com/spacefleet/spacefleet/ent/workflowrun"
	"github.com/spacefleet/spacefleet/lib/tofu"
)

// Run events: the moments a person may want to hear about without watching
// the run page. The worker process emits them from every settle path and
// hands them to the hook registered with OnEvent (lib/notifications in
// production), which fans them out to the organization's channels.

// Event kinds.
const (
	// EventAwaitingApproval fires when a run parks at an approval gate.
	EventAwaitingApproval = "awaiting_approval"
	// EventRunFailed fires when a run settles failed or partial — by the
	// worker, the reaper, or an approval timeout.
	EventRunFailed = "run_failed"
	// EventDriftDetected fires when a drift check settles with drift on at
	// least one component.
	EventDriftDetected = "drift_detected"
)

// EventKinds is every event kind a notification channel may subscribe to.
var EventKinds = []string{EventAwaitingApproval, EventRunFailed, EventDriftDetected}

// ValidEventKind reports whether k is one of EventKinds.
func ValidEventKind(k string) bool {
	for _, e := range EventKinds {
		if e == k {
			return true
		}
	}
	return false
}

// Event is one run event with what a notification needs to say: the
// application and run, the run's state, the steps that are not simply
// succeeded (a failed or parked step names the problem), the drifted
// resource addresses for a drift event, and the run page's public URL (empty
// without an external URL).
type Event struct {
	Kind      string      `json:"kind"`
	At        time.Time   `json:"at"`
	OrgID     uuid.UUID   `json:"organization_id"`
	AppID     uuid.UUID   `json:"application_id"`
	AppName   string      `json:"application_name"`
	RunID     uuid.UUID   `json:"run_id"`
	Action    string      `json:"action"`
	Status    string      `json:"status"`
	Message   string      `json:"message,omitempty"`
	StartedBy string      `json:"started_by,omitempty"`
	RunURL    string      `json:"run_url,omitempty"`
	Steps     []EventStep `json:"steps,omitempty"`
	Drift     []string    `json:"drift,omitempty"`
}

// EventStep is a step worth naming in a notification: one that failed, was
// skipped, or is parked awaiting approval.
type EventStep struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

// OnEvent registers the hook run events are handed to. One hook; a later
// call replaces it.
func (s *Service) OnEvent(fn func(context.Context, Event)) {
	s.eventHook = fn
}

// SetExternalURL records the deployment's public base URL, used to link
// notifications to the run page.
func (s *Service) SetExternalURL(externalURL string) {
	s.externalURL = trimSlash(externalURL)
}

// emitRunEvent builds the event of the given kind for a run and hands it to
// the hook. A no-op without a hook; a drift event is emitted only when the
// run actually found drift. Best-effort: a load failure is logged, never
// returned — the run has already been settled.
func (s *Service) emitRunEvent(ctx context.Context, orgID, runID uuid.UUID, kind string) {
	if s.eventHook == nil {
		return
	}
	run, err := s.ent.WorkflowRun.Query().Where(workflowrun.OrganizationID(orgID), workflowrun.ID(runID)).Only(ctx)
	if err != nil {
		log.Printf("workflows: event %s for run %s: %v", kind, runID, err)
		return
	}
	app, err := s.getApp(ctx, orgID, run.ApplicationID)
	if err != nil {
		log.Printf("workflows: event %s for run %s: %v", kind, runID, err)
		return
	}
	steps, err := s.ent.ComponentRun.Query().
		Where(componentrun.OrganizationID(orgID), componentrun.WorkflowRunID(runID)).
		Order(ent.Asc(componentrun.FieldCreatedAt)).
		All(ctx)
	if err != nil {
		log.Printf("workflows: event %s for run %s: load steps: %v", kind, runID, err)
		return
	}
	ev := buildEvent(kind, run, app, steps, s.runURL(app.ID, run.ID))
	if kind == EventDriftDetected && len(ev.Drift) == 0 {
		return
	}
	s.eventHook(ctx, ev)
}

// buildEvent assembles an Event from the run rows. A pure function
// (unit-testable).
func buildEvent(kind string, run *ent.WorkflowRun, app *ent.Application, steps []*ent.ComponentRun, runURL string) Event {
	ev := Event{
		Kind:      kind,
		At:        time.Now(),
		OrgID:     run.OrganizationID,
		AppID:     app.ID,
		AppName:   app.Name,
		RunID:     run.ID,
		Action:    string(run.Action),
		Status:    string(run.Status),
		Message:   run.Message,
		StartedBy: run.StartedBy,
		RunURL:    runURL,
	}
	for _, cr := range steps {
		switch cr.Status {
		case componentrun.StatusFailed, componentrun.StatusSkipped, componentrun.StatusAwaitingApproval:
			ev.Steps = append(ev.Steps, EventStep{Name: cr.Name, Status: string(cr.Status)})
		}
		if kind == EventDriftDetected && cr.Type == TypeTerraform && cr.Logs != "" {
			if p := tofu.ParsePlan(cr.Logs); p.Found && p.HasDrift {
				for _, d := range p.Drift {
					ev.Drift = append(ev.Drift, d.Address)
				}
			}
		}
	}
	return ev
}

// trimSlash drops a trailing slash from a base URL.
func trimSlash(u string) string {
	for len(u) > 0 && u[len(u)-1] == '/' {
		u = u[:len(u)-1]
	}
	return u
}
