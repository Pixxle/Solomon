package statemachine

// State represents the internal state of an issue within the system.
// The tracker only has four statuses (To Do, In Progress, In Review, Done).
// Planning is an internal phase tracked in SQLite while the issue remains
// in To Do in the tracker.
type State string

const (
	// StateTodo is a fresh To Do issue with no planning state row.
	StateTodo State = "todo"

	// StatePlanning is a To Do issue with an active planning conversation.
	// The tracker status remains To Do — planning is internal only.
	StatePlanning State = "planning"

	// StatePlanningReady is a planning issue where the human has signalled readiness.
	StatePlanningReady State = "planning_ready"

	// StateInProgress means the issue has been transitioned to In Progress
	// and implementation (agent team) is running or has run.
	StateInProgress State = "in_progress"

	// StateInReview means CI has passed and the PR has been marked ready for review.
	StateInReview State = "in_review"

	// StateDone means the PR has been merged and the issue is closed.
	StateDone State = "done"
)

// Transition represents a valid state transition and the event that triggers it.
type Transition struct {
	From    State
	To      State
	Trigger string
}

// ValidTransitions defines all allowed state transitions per the spec §2.2.
var ValidTransitions = []Transition{
	{StateTodo, StatePlanning, "issue detected with configured label"},
	{StatePlanning, StatePlanning, "new question posted or human replies"},
	{StatePlanning, StatePlanningReady, "human signals ready for development"},
	{StatePlanningReady, StateInProgress, "description updated, implementation begins"},
	{StateInProgress, StateInProgress, "CI failure fixed or devil's advocate rework"},
	{StateInProgress, StateInReview, "CI passes on draft PR"},
	{StateInReview, StateInProgress, "reviewer requests code changes"},
	{StateInReview, StateDone, "PR merged"},
}
