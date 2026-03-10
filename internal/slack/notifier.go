package slack

import (
	"context"

	slackapi "github.com/slack-go/slack"

	"github.com/pixxle/codehephaestus/internal/config"
	"github.com/pixxle/codehephaestus/internal/db"
)

// Notifier sends lifecycle notifications for ticket processing.
type Notifier interface {
	NotifyStartedScoping(ctx context.Context, issueKey, issueTitle string) error
	NotifyPlanningPhase(ctx context.Context, issueKey, phase string) error
	NotifyQuestions(ctx context.Context, issueKey, phase string, count int) error
	NotifyQuestionsResolved(ctx context.Context, issueKey, phase string) error
	NotifyPhaseRevert(ctx context.Context, issueKey, fromPhase, toPhase string) error
	NotifyImplementationStarted(ctx context.Context, issueKey string) error
	NotifyPRCreated(ctx context.Context, issueKey string, prNumber int) error
	NotifyCIFailure(ctx context.Context, issueKey string, prNumber int) error
	NotifyReviewFeedback(ctx context.Context, issueKey string, prNumber int) error
	NotifyPRMerged(ctx context.Context, issueKey string, prNumber int) error
	NotifyJailbreakDetected(ctx context.Context, issueKey, source, reason string) error
}

// NoopNotifier is a no-op implementation used when Slack is disabled or in dry-run mode.
type NoopNotifier struct{}

func (n *NoopNotifier) NotifyStartedScoping(context.Context, string, string) error    { return nil }
func (n *NoopNotifier) NotifyPlanningPhase(context.Context, string, string) error     { return nil }
func (n *NoopNotifier) NotifyQuestions(context.Context, string, string, int) error    { return nil }
func (n *NoopNotifier) NotifyQuestionsResolved(context.Context, string, string) error { return nil }
func (n *NoopNotifier) NotifyPhaseRevert(context.Context, string, string, string) error {
	return nil
}
func (n *NoopNotifier) NotifyImplementationStarted(context.Context, string) error { return nil }
func (n *NoopNotifier) NotifyPRCreated(context.Context, string, int) error        { return nil }
func (n *NoopNotifier) NotifyCIFailure(context.Context, string, int) error        { return nil }
func (n *NoopNotifier) NotifyReviewFeedback(context.Context, string, int) error   { return nil }
func (n *NoopNotifier) NotifyPRMerged(context.Context, string, int) error         { return nil }
func (n *NoopNotifier) NotifyJailbreakDetected(context.Context, string, string, string) error {
	return nil
}

// NewNotifier returns a SlackNotifier when Slack is configured and not in dry-run mode,
// or a NoopNotifier otherwise. The returned slackClient can be passed to NewStandupRunner
// to share the same HTTP connection pool; it is nil when Slack is disabled.
func NewNotifier(cfg *config.Config, stateDB *db.StateDB) (Notifier, *slackapi.Client) {
	if cfg.SlackEnabled() && !cfg.DryRun {
		client := slackapi.New(cfg.SlackBotToken)
		return newSlackNotifier(client, cfg.SlackChannelID, stateDB), client
	}
	return &NoopNotifier{}, nil
}
