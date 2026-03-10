package planning

import (
	"testing"

	"github.com/pixxle/codehephaestus/internal/db"
)

func TestParseQuestions(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name:  "no questions section",
			input: "## Analysis\nLooks good.",
			want:  nil,
		},
		{
			name: "single question",
			input: `### Open Questions
1. What database should we use?
`,
			want: []string{"What database should we use?"},
		},
		{
			name: "multiple questions",
			input: `### Open Questions
1. What database should we use?
2. Should we add caching?
3. What is the deployment target?
`,
			want: []string{
				"What database should we use?",
				"Should we add caching?",
				"What is the deployment target?",
			},
		},
		{
			name: "questions section followed by another section",
			input: `### Open Questions
1. First question?

### Next Steps
Do something.
`,
			want: []string{"First question?"},
		},
		{
			name:  "empty questions section",
			input: "### Open Questions\n\n### Next Steps\n",
			want:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseQuestions(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("parseQuestions() returned %d questions, want %d\ngot: %v", len(got), len(tt.want), got)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("question[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestDescriptionChanged(t *testing.T) {
	tests := []struct {
		name     string
		current  string
		lastSeen string
		want     bool
	}{
		{"same content", "hello", "hello", false},
		{"different content", "hello", "world", true},
		{"whitespace only diff", "  hello  ", "hello", false},
		{"both empty", "", "", false},
		{"empty vs content", "", "hello", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DescriptionChanged(tt.current, tt.lastSeen); got != tt.want {
				t.Errorf("DescriptionChanged(%q, %q) = %v, want %v", tt.current, tt.lastSeen, got, tt.want)
			}
		})
	}
}

func TestEnsureCorrectProductHeading(t *testing.T) {
	botName := "TestBot"
	tests := []struct {
		name        string
		input       string
		noQuestions bool
		want        string
	}{
		{
			name:        "upgrade to complete when no questions",
			input:       "## TestBot — Product Requirements Refinement\nSome content",
			noQuestions: true,
			want:        "## TestBot — Product Requirements Complete\nSome content",
		},
		{
			name:        "downgrade to refinement when has questions",
			input:       "## TestBot — Product Requirements Complete\nSome content",
			noQuestions: false,
			want:        "## TestBot — Product Requirements Refinement\nSome content",
		},
		{
			name:        "already correct complete heading",
			input:       "## TestBot — Product Requirements Complete\nSome content",
			noQuestions: true,
			want:        "## TestBot — Product Requirements Complete\nSome content",
		},
		{
			name:        "already correct refinement heading",
			input:       "## TestBot — Product Requirements Refinement\nSome content",
			noQuestions: false,
			want:        "## TestBot — Product Requirements Refinement\nSome content",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ensureCorrectProductHeading(tt.input, tt.noQuestions, botName)
			if got != tt.want {
				t.Errorf("ensureCorrectProductHeading() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEnsureCorrectTechnicalHeading(t *testing.T) {
	botName := "TestBot"
	tests := []struct {
		name        string
		input       string
		noQuestions bool
		want        string
	}{
		{
			name:        "upgrade to complete when no questions",
			input:       "## TestBot — Technical Refinement\nSome content",
			noQuestions: true,
			want:        "## TestBot — Technical Refinement Complete\nSome content",
		},
		{
			name:        "downgrade to refinement when has questions",
			input:       "## TestBot — Technical Refinement Complete\nSome content",
			noQuestions: false,
			want:        "## TestBot — Technical Refinement\nSome content",
		},
		{
			name:        "already correct complete heading",
			input:       "## TestBot — Technical Refinement Complete\nSome content",
			noQuestions: true,
			want:        "## TestBot — Technical Refinement Complete\nSome content",
		},
		{
			name:        "already correct refinement heading",
			input:       "## TestBot — Technical Refinement\nSome content",
			noQuestions: false,
			want:        "## TestBot — Technical Refinement\nSome content",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ensureCorrectTechnicalHeading(tt.input, tt.noQuestions, botName)
			if got != tt.want {
				t.Errorf("ensureCorrectTechnicalHeading() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseProductGaps(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name:  "no gaps section",
			input: "### Open Questions\n1. Something?\n",
			want:  nil,
		},
		{
			name: "single gap",
			input: `### Product Requirements Gaps
1. The acceptance criteria contradicts the user flow diagram
`,
			want: []string{"The acceptance criteria contradicts the user flow diagram"},
		},
		{
			name: "multiple gaps",
			input: `### Product Requirements Gaps
1. User flow for edge case X is undefined
2. Acceptance criteria conflict with mobile requirements

### Implementation Outline
- [ ] Step 1
`,
			want: []string{
				"User flow for edge case X is undefined",
				"Acceptance criteria conflict with mobile requirements",
			},
		},
		{
			name:  "empty gaps section",
			input: "### Product Requirements Gaps\n\n### Implementation Outline\n",
			want:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseProductGaps(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("parseProductGaps() returned %d gaps, want %d\ngot: %v", len(got), len(tt.want), got)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("gap[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestIsProductPhaseComplete(t *testing.T) {
	tests := []struct {
		name  string
		phase string
		want  bool
	}{
		{"product phase", PhaseProduct, false},
		{"technical phase", PhaseTechnical, true},
		{"empty phase", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ps := &db.PlanningState{PlanningPhase: tt.phase}
			if got := IsProductPhaseComplete(ps); got != tt.want {
				t.Errorf("IsProductPhaseComplete() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsTechnicalPhaseComplete(t *testing.T) {
	tests := []struct {
		name          string
		phase         string
		questionsJSON string
		want          bool
	}{
		{"product phase", PhaseProduct, "[]", false},
		{"technical with no questions", PhaseTechnical, "[]", true},
		{"technical with questions", PhaseTechnical, `["Q1?"]`, false},
		{"technical with empty json", PhaseTechnical, "", true},
		{"technical with null json", PhaseTechnical, "null", true},
		{"empty phase", "", "[]", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ps := &db.PlanningState{
				PlanningPhase: tt.phase,
				QuestionsJSON: tt.questionsJSON,
			}
			if got := IsTechnicalPhaseComplete(ps); got != tt.want {
				t.Errorf("IsTechnicalPhaseComplete() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestStripPreamble(t *testing.T) {
	botName := "claes"
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "no preamble",
			input: "## claes — Planning\n### Understanding\nSome content",
			want:  "## claes — Planning\n### Understanding\nSome content",
		},
		{
			name:  "with preamble from tool use",
			input: "Now I have a thorough understanding of the codebase and the visual requirements. Let me compose the planning comment.\n## claes — Planning\n### Understanding\nSome content",
			want:  "## claes — Planning\n### Understanding\nSome content",
		},
		{
			name:  "multi-line preamble",
			input: "Let me read these images first.\n\nOk, I can see the mockups.\n\n## claes — Planning Complete\n### Understanding\nDone",
			want:  "## claes — Planning Complete\n### Understanding\nDone",
		},
		{
			name:  "no heading at all",
			input: "Some random output with no heading",
			want:  "Some random output with no heading",
		},
		{
			name:  "heading at very start",
			input: "## claes — Planning\nContent",
			want:  "## claes — Planning\nContent",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripPreamble(tt.input, botName)
			if got != tt.want {
				t.Errorf("stripPreamble() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIsImageMime(t *testing.T) {
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
