package statemachine

// State represents what action the dispatcher wants taken on an issue.
// These are dispatch signals, not tracker statuses — the tracker only has
// four statuses (To Do, In Progress, In Review, Done).
type State string

const (
	// StateTodo — fresh To Do issue, no planning state row. Action: start planning.
	StateTodo State = "todo"

	// StatePlanning — active planning conversation with new human comments.
	// Action: continue the planning conversation (or detect ready signal).
	StatePlanning State = "planning"

	// StatePlanningReady — human signalled readiness.
	// Action: finalize spec, create worktree, launch agent team, open draft PR.
	StatePlanningReady State = "planning_ready"

	// StateCIFailure — In Progress issue whose PR has a failing CI check.
	// Action: fix CI in worktree and push.
	StateCIFailure State = "ci_failure"

	// StateInReview — PR is ready for review and has unprocessed reviewer comments.
	// Action: classify and respond to review feedback.
	StateInReview State = "in_review"

	// StateDone — PR merged, issue closed. No dispatch action (housekeeping only).
	StateDone State = "done"
)

// Transition represents a valid state transition and the event that triggers it.
type Transition struct {
	From    State
	To      State
	Trigger string
}

// ValidTransitions defines all allowed state transitions per the spec §2.2.
// Note: These use tracker-level concepts (not dispatch states like StateCIFailure).
var ValidTransitions = []Transition{
	{StateTodo, StatePlanning, "issue detected with configured label"},
	{StatePlanning, StatePlanning, "new question posted or human replies"},
	{StatePlanning, StatePlanningReady, "human signals ready for development"},
	{StatePlanningReady, "in_progress", "description updated, implementation begins"},
	{"in_progress", "in_progress", "CI failure fixed or devil's advocate rework"},
	{"in_progress", StateInReview, "CI passes on draft PR"},
	{StateInReview, "in_progress", "reviewer requests code changes"},
	{StateInReview, StateDone, "PR merged"},
}
