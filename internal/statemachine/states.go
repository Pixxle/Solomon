package statemachine

// State represents what action the dispatcher wants taken on an issue.
// These are dispatch signals, not tracker statuses — the tracker only has
// four statuses (To Do, In Progress, In Review, Done).
type State string

const (
	// StateTodo — fresh To Do issue, no planning state row. Action: start planning.
	StateTodo State = "todo"

	// StatePlanning — active planning conversation (product or technical phase)
	// with new human comments. The planning_phase field in the DB determines
	// which phase is active: "product" for product requirements refinement,
	// "technical" for technical refinement. Phase transitions are bidirectional:
	// product → technical when product questions are resolved, and
	// technical → product when the AI detects product requirement gaps.
	// Action: continue the planning conversation (or detect ready signal).
	StatePlanning State = "planning"

	// StatePlanningReady — human signalled readiness or auto-launch triggered.
	// Action: finalize spec, create worktree, launch agent team, open draft PR.
	StatePlanningReady State = "planning_ready"

	// StateCIFailure — In Progress issue whose PR has a failing CI check.
	// Action: fix CI in worktree and push.
	StateCIFailure State = "ci_failure"

	// StateInReview — PR is ready for review and has unprocessed reviewer comments.
	// Action: classify and respond to review feedback.
	StateInReview State = "in_review"

	// StateInProgress — issue is being implemented. Used in transitions table only;
	// the dispatcher does not emit this state directly (it uses StateCIFailure instead).
	StateInProgress State = "in_progress"

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
	{StateTodo, StatePlanning, "issue detected with planning label or assignment"},
	{StatePlanning, StatePlanning, "description updated, phase auto-transition (product↔technical), or product gaps revert"},
	{StatePlanning, StatePlanningReady, "human signals ready or auto-launch after both phases complete"},
	{StatePlanningReady, StateInProgress, "description updated, implementation begins"},
	{StateInProgress, StateInProgress, "CI failure fixed or devil's advocate rework"},
	{StateInProgress, StateInReview, "CI passes on draft PR"},
	{StateInReview, StateInProgress, "reviewer requests code changes"},
	{StateInReview, StateDone, "PR merged"},
}
