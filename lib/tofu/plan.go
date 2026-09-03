package tofu

import (
	"regexp"
	"strconv"
	"strings"
)

// Plan is the structured reading of a `tofu plan` step's captured logs: the
// resource-level change list, the add/change/destroy totals, and the plan body
// itself. It is parsed from the human-readable plan text rather than
// `tofu show -json` deliberately — the text is what the step already logs, it
// masks values the module marks sensitive ("(sensitive value)"), and it exists
// for a preview too (a preview writes no planfile, so there is nothing to
// `show`). The text layout has been stable across Terraform and OpenTofu for
// years; a line the parser does not recognize is simply left inside the body.
type Plan struct {
	// Found reports whether the logs contained a plan at all (either a change
	// listing or the "No changes." verdict). False for a step that failed before
	// planning, or for a non-plan step.
	Found bool
	// HasChanges is the plan verdict: whether applying it would change
	// infrastructure or outputs.
	HasChanges bool
	// Add, Change, and Destroy are tofu's own totals from the
	// "Plan: N to add, M to change, K to destroy." line. A replacement counts
	// once in Add and once in Destroy, as tofu counts it.
	Add, Change, Destroy int
	// Replace is the number of resources that will be destroyed and recreated
	// (a subset of Add/Destroy above), which tofu does not total separately.
	Replace int
	// Resources lists every resource action in the plan, in plan order.
	Resources []ResourceChange
	// OutputsChanged reports whether the plan changes any output values.
	OutputsChanged bool
	// Body is the plan text: from the "will perform the following actions:"
	// heading (or the "No changes." verdict) through the totals and any output
	// changes — the review material, without the init/clone chatter around it.
	Body string
	// Drift lists the resources tofu found changed outside of OpenTofu since
	// the last apply (the "Objects have changed outside of OpenTofu" section a
	// plan prints before its actions, and the whole content of a refresh-only
	// plan). Each entry's Action is ActionDriftUpdate or ActionDriftDelete.
	Drift []ResourceChange
	// HasDrift reports whether any drift was detected.
	HasDrift bool
	// RefreshOnly reports whether this was a refresh-only plan (a drift check):
	// it proposes no configuration changes, so Found/HasChanges describe only
	// the drift verdict.
	RefreshOnly bool
}

// ResourceChange is one resource's planned action.
type ResourceChange struct {
	// Address is the resource address (e.g. module.vpc.aws_subnet.private[0]).
	Address string
	// Action is one of the ResourceAction constants.
	Action string
	// Detail is tofu's own phrasing of the action from the resource heading
	// (e.g. "must be replaced", "is tainted, so must be replaced"), so a UI can
	// show the nuance behind a coarse Action.
	Detail string
	// Diff is the resource's block of the plan text (heading included): the
	// attribute-level +/-/~ lines for that one resource.
	Diff string
}

// Resource actions a plan heading maps to.
const (
	ActionCreate  = "create"
	ActionUpdate  = "update"
	ActionReplace = "replace"
	ActionDelete  = "delete"
	ActionRead    = "read"
	ActionMove    = "move"
	ActionImport  = "import"
	ActionForget  = "forget"
	// ActionOther is a heading the parser did not recognize; Detail carries the
	// raw phrasing so nothing is lost.
	ActionOther = "other"
	// ActionDriftUpdate and ActionDriftDelete are the drift-section actions: a
	// resource whose real object was changed, or deleted, outside of OpenTofu.
	ActionDriftUpdate = "drift_update"
	ActionDriftDelete = "drift_delete"
)

var (
	// resourceHeading matches "  # <address> <what will happen>" — the line tofu
	// prints above every resource block in the actions section. The address is
	// the first token; the rest of the line is the action phrasing. A heading is
	// sometimes followed by a parenthesised explanation on its own "# (...)"
	// line ("# (because ... is not in configuration)"), which the leading-"("
	// exclusion keeps from reading as a second heading.
	resourceHeading = regexp.MustCompile(`^\s*# ([^\s(]\S*) (.+)$`)
	// planTotals matches the "Plan: N to add, M to change, K to destroy." line.
	planTotals = regexp.MustCompile(`^Plan: (\d+) to add, (\d+) to change, (\d+) to destroy\.`)
)

// ParsePlan extracts the structured plan from a plan step's captured logs. A
// step that failed before planning (or is not a plan step) yields Found=false
// and an otherwise zero Plan.
func ParsePlan(logs string) Plan {
	var p Plan
	lines := strings.Split(logs, "\n")

	// The drift section — "Objects have changed outside of OpenTofu" — comes
	// before the actions (it is the whole content of a refresh-only plan). Its
	// headings use the same "# <address> <what happened>" shape as the actions.
	driftStart, driftEnd := -1, -1
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if driftStart < 0 {
			if strings.Contains(t, "detected the following changes made outside of") {
				driftStart = i
			}
			continue
		}
		if isDriftTerminator(t) {
			driftEnd = i
			break
		}
	}
	if driftStart >= 0 {
		if driftEnd < 0 {
			driftEnd = len(lines)
		}
		p.Drift = parseResourceBlocks(lines[driftStart+1:driftEnd], classifyDriftHeading)
		p.HasDrift = len(p.Drift) > 0
	}
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "This is a refresh-only plan") ||
			strings.HasPrefix(t, "No changes. Your infrastructure still matches the configuration") {
			p.RefreshOnly = true
			break
		}
	}

	// Locate the plan: either the actions heading or the no-changes verdict,
	// after any drift section. The actions heading is preferred when both
	// appear (a "No changes" that is really part of a warning is not a verdict).
	start := -1
	for i := max(driftEnd, 0); i < len(lines); i++ {
		t := strings.TrimSpace(lines[i])
		if strings.HasSuffix(t, "will perform the following actions:") {
			start = i
			p.HasChanges = true
			break
		}
		if strings.HasPrefix(t, "No changes.") {
			start = i
			break
		}
	}
	if start < 0 {
		// A refresh-only plan that found drift prints no actions and no
		// verdict — the drift section is the whole plan.
		if p.HasDrift {
			p.Found = true
			p.HasChanges = true
			p.Body = strings.TrimRight(strings.Join(lines[driftStart:driftEnd], "\n"), "\n ")
		}
		return p
	}
	p.Found = true
	if p.RefreshOnly && p.HasDrift {
		p.HasChanges = true
	}

	// The plan body runs from the heading to the first line that is clearly not
	// part of it: the section separator, a diagnostics box, the saved-planfile
	// notice, the preview's -out hint, or the handover kubectl output.
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if isPlanTerminator(lines[i]) {
			end = i
			break
		}
	}
	body := lines[start:end]

	// Walk the body: resource headings open a block that runs until the next
	// heading, the totals line, or the outputs section.
	inActions := p.HasChanges
	var cur *ResourceChange
	var curLines []string
	flush := func() {
		if cur == nil {
			return
		}
		cur.Diff = strings.TrimRight(strings.Join(curLines, "\n"), "\n ")
		p.Resources = append(p.Resources, *cur)
		cur, curLines = nil, nil
	}
	for _, line := range body {
		t := strings.TrimSpace(line)
		if m := planTotals.FindStringSubmatch(t); m != nil {
			flush()
			inActions = false
			p.Add, _ = strconv.Atoi(m[1])
			p.Change, _ = strconv.Atoi(m[2])
			p.Destroy, _ = strconv.Atoi(m[3])
			continue
		}
		if t == "Changes to Outputs:" {
			flush()
			inActions = false
			p.OutputsChanged = true
			p.HasChanges = true
			continue
		}
		if inActions {
			if m := resourceHeading.FindStringSubmatch(line); m != nil {
				flush()
				action, detail := classifyHeading(m[2])
				cur = &ResourceChange{Address: m[1], Action: action, Detail: detail}
				if action == ActionReplace {
					p.Replace++
				}
			}
			if cur != nil {
				curLines = append(curLines, line)
			}
		}
	}
	flush()

	p.Body = strings.TrimRight(strings.Join(body, "\n"), "\n ")
	return p
}

// isPlanTerminator reports whether a line marks the end of the plan text.
func isPlanTerminator(line string) bool {
	t := strings.TrimSpace(line)
	switch {
	case strings.HasPrefix(t, "─"), strings.HasPrefix(t, "╷"):
		return true // section separator / diagnostics box
	case strings.HasPrefix(t, "Saved the plan to:"):
		return true
	case strings.HasPrefix(t, "Note: You didn't use the -out option"):
		return true
	case strings.HasPrefix(t, "You can apply this plan to save these new output values"):
		return true
	case strings.HasPrefix(t, "secret/"), strings.HasPrefix(t, "SPACEFLEET_"):
		return true // planfile-handover kubectl output / worker markers
	case strings.HasPrefix(t, "apk add"), strings.HasPrefix(t, "fetch https://"):
		return true // kubectl install chatter, if it lands after the plan
	}
	return false
}

// classifyHeading maps the action phrasing of a resource heading to a coarse
// action, keeping the phrasing as detail.
func classifyHeading(phrase string) (action, detail string) {
	detail = strings.TrimSpace(phrase)
	switch {
	case strings.HasPrefix(detail, "will be created"):
		return ActionCreate, detail
	case strings.HasPrefix(detail, "will be updated in-place"):
		return ActionUpdate, detail
	case strings.Contains(detail, "must be replaced"),
		strings.HasPrefix(detail, "will be replaced"):
		return ActionReplace, detail
	case strings.HasPrefix(detail, "will be destroyed"):
		return ActionDelete, detail
	case strings.HasPrefix(detail, "will be read"):
		return ActionRead, detail
	case strings.HasPrefix(detail, "has moved to"):
		return ActionMove, detail
	case strings.HasPrefix(detail, "will be imported"):
		return ActionImport, detail
	case strings.HasPrefix(detail, "will no longer be managed"),
		strings.HasPrefix(detail, "will be forgotten"):
		return ActionForget, detail
	}
	return ActionOther, detail
}

// classifyDriftHeading maps a drift-section heading ("has been changed",
// "has been deleted") to its drift action.
func classifyDriftHeading(phrase string) (action, detail string) {
	detail = strings.TrimSpace(phrase)
	switch {
	case strings.HasPrefix(detail, "has been deleted"):
		return ActionDriftDelete, detail
	case strings.HasPrefix(detail, "has been changed"), strings.HasPrefix(detail, "has changed"):
		return ActionDriftUpdate, detail
	}
	return ActionOther, detail
}

// isDriftTerminator reports whether a (trimmed) line ends the drift section:
// the separator before the actions, the refresh-only explanation, or the
// actions heading itself.
func isDriftTerminator(t string) bool {
	switch {
	case strings.HasPrefix(t, "─"), strings.HasPrefix(t, "╷"):
		return true
	case strings.HasPrefix(t, "This is a refresh-only plan"),
		strings.HasPrefix(t, "Unless you have made equivalent changes"):
		return true
	case strings.HasSuffix(t, "will perform the following actions:"),
		strings.HasPrefix(t, "No changes."):
		return true
	}
	return false
}

// parseResourceBlocks walks a section of plan text and returns one
// ResourceChange per "# <address> <phrase>" heading, each carrying its block
// (the heading through the line before the next heading). classify maps the
// heading phrase to an action.
func parseResourceBlocks(lines []string, classify func(string) (string, string)) []ResourceChange {
	var out []ResourceChange
	var cur *ResourceChange
	var curLines []string
	flush := func() {
		if cur == nil {
			return
		}
		cur.Diff = strings.TrimRight(strings.Join(curLines, "\n"), "\n ")
		out = append(out, *cur)
		cur, curLines = nil, nil
	}
	for _, line := range lines {
		if m := resourceHeading.FindStringSubmatch(line); m != nil {
			flush()
			action, detail := classify(m[2])
			cur = &ResourceChange{Address: m[1], Action: action, Detail: detail}
		}
		if cur != nil {
			curLines = append(curLines, line)
		}
	}
	flush()
	return out
}
