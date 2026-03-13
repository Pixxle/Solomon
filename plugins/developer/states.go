package developer

import "github.com/pixxle/solomon/internal/tracker"

// State represents what action the dispatcher wants taken on an issue.
// These are dispatch signals, not tracker statuses — the tracker only has
// four statuses (To Do, In Progress, In Review, Done).
type State string

const (
	// StateTodo — fresh To Do issue, no planning state row. Action: start planning.
	StateTodo State = "todo"

	// StatePlanning — active planning conversation (product or technical phase)
	// with new human comments.
	StatePlanning State = "planning"

	// StatePlanningReady — human signalled readiness or auto-launch triggered.
	StatePlanningReady State = "planning_ready"

	// StateCIFailure — In Progress issue whose PR has a failing CI check.
	StateCIFailure State = "ci_failure"

	// StateInReview — PR is ready for review and has unprocessed reviewer comments.
	StateInReview State = "in_review"

	// StateInProgress — issue is being implemented.
	StateInProgress State = "in_progress"

	// StateDone — PR merged, issue closed.
	StateDone State = "done"
)

// WorkItem is a unit of work identified by the priority dispatcher,
// tagged with the internal state the issue is in.
type WorkItem struct {
	State   State
	Issue   tracker.Issue
	Context map[string]interface{}
}
