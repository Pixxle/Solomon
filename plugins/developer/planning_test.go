package developer

import (
	"testing"

	"github.com/pixxle/solomon/internal/db"
	"github.com/pixxle/solomon/internal/tracker"
)

func TestDescriptionChanged(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		current string
		last    string
		want    bool
	}{
		{"identical", "hello", "hello", false},
		{"different", "hello", "world", true},
		{"whitespace only diff", "  hello  ", "hello", false},
		{"empty both", "", "", false},
		{"empty vs content", "", "something", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DescriptionChanged(tt.current, tt.last); got != tt.want {
				t.Errorf("DescriptionChanged(%q, %q) = %v, want %v", tt.current, tt.last, got, tt.want)
			}
		})
	}
}

func TestResolvePhase(t *testing.T) {
	t.Parallel()
	if got := resolvePhase(""); got != PhaseProduct {
		t.Errorf("resolvePhase(\"\") = %q, want %q", got, PhaseProduct)
	}
	if got := resolvePhase(PhaseTechnical); got != PhaseTechnical {
		t.Errorf("resolvePhase(%q) = %q, want %q", PhaseTechnical, got, PhaseTechnical)
	}
	if got := resolvePhase(PhaseProduct); got != PhaseProduct {
		t.Errorf("resolvePhase(%q) = %q, want %q", PhaseProduct, got, PhaseProduct)
	}
}

func TestParseQuestions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  int
	}{
		{"no section", "some output without questions", 0},
		{"empty section", "### Open Questions\n\nNo questions.\n### Next", 0},
		{
			"two questions",
			"### Open Questions\n1. What is X?\n2. How does Y work?\n### Summary\n",
			2,
		},
		{
			"questions at end",
			"### Open Questions\n1. Only question?\n",
			1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseQuestions(tt.input)
			if len(got) != tt.want {
				t.Errorf("parseQuestions() returned %d questions, want %d: %v", len(got), tt.want, got)
			}
		})
	}
}

func TestParseProductGaps(t *testing.T) {
	t.Parallel()
	input := "### Product Requirements Gaps\n1. Missing auth flow\n2. No error states\n### Technical\n"
	got := parseProductGaps(input)
	if len(got) != 2 {
		t.Fatalf("parseProductGaps() returned %d, want 2", len(got))
	}
	if got[0] != "Missing auth flow" {
		t.Errorf("gap[0] = %q", got[0])
	}
}

func TestParseSection(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		output  string
		heading string
		want    int
	}{
		{"missing heading", "no match here", "### Foo", 0},
		{"heading with items", "### Foo\n1. A\n2. B\n", "### Foo", 2},
		{"heading bounded by next section", "### Foo\n1. A\n### Bar\n1. B\n", "### Foo", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseSection(tt.output, tt.heading)
			if len(got) != tt.want {
				t.Errorf("parseSection() returned %d, want %d", len(got), tt.want)
			}
		})
	}
}

func TestStripPreamble(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		output  string
		botName string
		want    string
	}{
		{
			"has preamble",
			"Here is my analysis:\n\n## Solomon — Product Requirements Refinement\nContent here",
			"Solomon",
			"## Solomon — Product Requirements Refinement\nContent here",
		},
		{
			"no preamble",
			"## Solomon — Technical Refinement\nContent",
			"Solomon",
			"## Solomon — Technical Refinement\nContent",
		},
		{
			"no marker at all",
			"Just plain text",
			"Solomon",
			"Just plain text",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripPreamble(tt.output, tt.botName)
			if got != tt.want {
				t.Errorf("stripPreamble() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEnsureCorrectProductHeading(t *testing.T) {
	t.Parallel()
	bot := "Solomon"
	active := "## Solomon — Product Requirements Refinement\nContent"
	complete := "## Solomon — Product Requirements Complete\nContent"

	// No questions -> should use complete heading
	got := ensureCorrectProductHeading(active, true, bot)
	if got != complete {
		t.Errorf("noQuestions=true: got %q, want %q", got, complete)
	}

	// Has questions -> should use active heading
	got = ensureCorrectProductHeading(complete, false, bot)
	if got != active {
		t.Errorf("noQuestions=false: got %q, want %q", got, active)
	}
}

func TestEnsureCorrectTechnicalHeading(t *testing.T) {
	t.Parallel()
	bot := "Solomon"
	active := "## Solomon — Technical Refinement\nContent"
	complete := "## Solomon — Technical Refinement Complete\nContent"

	got := ensureCorrectTechnicalHeading(active, true, bot)
	if got != complete {
		t.Errorf("noQuestions=true: got %q, want %q", got, complete)
	}

	got = ensureCorrectTechnicalHeading(complete, false, bot)
	if got != active {
		t.Errorf("noQuestions=false: got %q, want %q", got, active)
	}
}

func TestIsImageMime(t *testing.T) {
	t.Parallel()
	tests := []struct {
		mime string
		want bool
	}{
		{"image/png", true},
		{"image/jpeg", true},
		{"image/gif", true},
		{"image/svg+xml", true},
		{"image/webp", true},
		{"application/pdf", false},
		{"text/plain", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.mime, func(t *testing.T) {
			if got := isImageMime(tt.mime); got != tt.want {
				t.Errorf("isImageMime(%q) = %v, want %v", tt.mime, got, tt.want)
			}
		})
	}
}

func TestAutoLaunchReady(t *testing.T) {
	t.Parallel()
	botID := "bot-123"
	completePlanState := &db.PlanningState{
		PlanningPhase: PhaseTechnical,
		QuestionsJSON: "[]",
	}
	activePlanState := &db.PlanningState{
		PlanningPhase: PhaseProduct,
		QuestionsJSON: `["Q1?"]`,
	}
	assignedIssue := tracker.Issue{Key: "TEST-1", Assignees: []string{botID}}
	unassignedIssue := tracker.Issue{Key: "TEST-2", Assignees: []string{"other"}}

	tests := []struct {
		name        string
		autoLaunch  bool
		issue       tracker.Issue
		ps          *db.PlanningState
		want        bool
	}{
		{"all conditions met", true, assignedIssue, completePlanState, true},
		{"auto launch disabled", false, assignedIssue, completePlanState, false},
		{"not assigned to bot", true, unassignedIssue, completePlanState, false},
		{"technical incomplete", true, assignedIssue, activePlanState, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AutoLaunchReady(tt.autoLaunch, botID, tt.issue, tt.ps)
			if got != tt.want {
				t.Errorf("AutoLaunchReady() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsProductPhaseComplete(t *testing.T) {
	t.Parallel()
	if IsProductPhaseComplete(&db.PlanningState{PlanningPhase: PhaseProduct}) {
		t.Error("product phase should not be complete when phase is product")
	}
	if !IsProductPhaseComplete(&db.PlanningState{PlanningPhase: PhaseTechnical}) {
		t.Error("product phase should be complete when phase is technical")
	}
}

func TestIsTechnicalPhaseComplete(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		ps    *db.PlanningState
		want  bool
	}{
		{"product phase", &db.PlanningState{PlanningPhase: PhaseProduct, QuestionsJSON: "[]"}, false},
		{"technical with questions", &db.PlanningState{PlanningPhase: PhaseTechnical, QuestionsJSON: `["Q?"]`}, false},
		{"technical no questions", &db.PlanningState{PlanningPhase: PhaseTechnical, QuestionsJSON: "[]"}, true},
		{"technical empty questions", &db.PlanningState{PlanningPhase: PhaseTechnical, QuestionsJSON: ""}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsTechnicalPhaseComplete(tt.ps); got != tt.want {
				t.Errorf("IsTechnicalPhaseComplete() = %v, want %v", got, tt.want)
			}
		})
	}
}
