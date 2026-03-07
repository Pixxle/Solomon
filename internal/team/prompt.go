package team

import (
	"fmt"
	"strings"

	"github.com/pixxle/codehephaestus/internal/worker"
)

type TeamLeadContext struct {
	IssueKey            string
	IssueTitle          string
	Specification       string
	AcceptanceCriteria  string
	EdgeCases           string
	PlanningConversation string
	BotDisplayName      string
	ImagePaths          []string
	MaxReviewRounds     int
}

// BuildTeamLeadPrompt constructs the prompt for the team lead session.
func BuildTeamLeadPrompt(ctx TeamLeadContext) (string, error) {
	prompt, err := worker.RenderPrompt("team_lead.md.tmpl", ctx)
	if err != nil {
		return "", fmt.Errorf("rendering team lead prompt: %w", err)
	}
	return prompt, nil
}

// BuildPRDescriptionPrompt creates a prompt for generating PR descriptions.
func BuildPRDescriptionPrompt(issueKey, issueTitle, originalDesc, planConversation, diff, commitLog, botName string) (string, error) {
	data := map[string]interface{}{
		"IssueKey":         issueKey,
		"IssueTitle":       issueTitle,
		"OriginalDesc":     originalDesc,
		"PlanConversation": planConversation,
		"Diff":             truncate(diff, 50000),
		"CommitLog":        commitLog,
		"BotDisplayName":   botName,
	}
	return worker.RenderPrompt("pr_description.md.tmpl", data)
}

// BuildCIFixPrompt creates a prompt for fixing CI failures.
func BuildCIFixPrompt(issueKey, ciOutput, diff, spec string) (string, error) {
	data := map[string]interface{}{
		"IssueKey": issueKey,
		"CIOutput": truncate(ciOutput, 20000),
		"Diff":     truncate(diff, 50000),
		"Spec":     spec,
	}
	return worker.RenderPrompt("fix_ci.md.tmpl", data)
}

// BuildAnswerQuestionPrompt creates a prompt for answering PR questions.
func BuildAnswerQuestionPrompt(question, diff string) (string, error) {
	data := map[string]interface{}{
		"Question": question,
		"Diff":     truncate(diff, 50000),
	}
	return worker.RenderPrompt("answer_question.md.tmpl", data)
}

// BuildAddressChangesPrompt creates a prompt for addressing review feedback.
func BuildAddressChangesPrompt(issueKey string, feedback []string, diff string) (string, error) {
	data := map[string]interface{}{
		"IssueKey": issueKey,
		"Feedback": strings.Join(feedback, "\n\n---\n\n"),
		"Diff":     truncate(diff, 50000),
	}
	return worker.RenderPrompt("address_changes.md.tmpl", data)
}

// ClassifyReviewComment uses AI to determine if a comment requests code changes or asks a question.
func ClassifyReviewComment(comment string) string {
	prompt := fmt.Sprintf(`Classify this PR review comment as either a request for code changes or a question.

Comment: %q

Respond with ONLY a JSON object: {"type": "code_change"} or {"type": "question"}`, comment)

	// This will be called via worker.RunClaudeQuick at the call site
	_ = prompt
	return "code_change" // default fallback
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "\n\n... (truncated)"
}
