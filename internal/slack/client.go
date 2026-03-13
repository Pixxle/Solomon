package slack

import (
	"context"
	"fmt"
	"sync"

	"github.com/rs/zerolog/log"
	slackapi "github.com/slack-go/slack"

	"github.com/pixxle/solomon/internal/db"
)

// SlackNotifier posts threaded messages to Slack, one thread per ticket.
type SlackNotifier struct {
	client    *slackapi.Client
	channelID string
	stateDB   *db.StateDB

	mu      sync.Mutex
	threads map[string]string // in-memory cache: issueKey → threadTS
}

// newSlackNotifier creates a SlackNotifier with a shared Slack client.
func newSlackNotifier(client *slackapi.Client, channelID string, stateDB *db.StateDB) *SlackNotifier {
	return &SlackNotifier{
		client:    client,
		channelID: channelID,
		stateDB:   stateDB,
		threads:   make(map[string]string),
	}
}

// getThreadTS returns the cached threadTS, falling back to the DB.
func (s *SlackNotifier) getThreadTS(issueKey string) (string, error) {
	s.mu.Lock()
	if ts, ok := s.threads[issueKey]; ok {
		s.mu.Unlock()
		return ts, nil
	}
	s.mu.Unlock()

	ts, err := s.stateDB.GetSlackThread(issueKey)
	if err != nil {
		return "", err
	}
	if ts != "" {
		s.mu.Lock()
		s.threads[issueKey] = ts
		s.mu.Unlock()
	}
	return ts, nil
}

// postOrReply posts a message to the ticket's thread, creating a new thread if needed.
func (s *SlackNotifier) postOrReply(ctx context.Context, issueKey, text string) error {
	threadTS, err := s.getThreadTS(issueKey)
	if err != nil {
		return fmt.Errorf("getting slack thread: %w", err)
	}

	if threadTS != "" {
		_, _, err := s.client.PostMessageContext(ctx, s.channelID,
			slackapi.MsgOptionText(text, false),
			slackapi.MsgOptionTS(threadTS),
		)
		if err != nil {
			return fmt.Errorf("posting slack reply: %w", err)
		}
		return nil
	}

	// Create new thread
	_, ts, err := s.client.PostMessageContext(ctx, s.channelID,
		slackapi.MsgOptionText(text, false),
	)
	if err != nil {
		return fmt.Errorf("posting slack message: %w", err)
	}

	if err := s.stateDB.UpsertSlackThread(issueKey, ts); err != nil {
		return fmt.Errorf("saving slack thread ts: %w", err)
	}

	s.mu.Lock()
	s.threads[issueKey] = ts
	s.mu.Unlock()
	return nil
}

// notify wraps postOrReply, logging errors instead of propagating them.
func (s *SlackNotifier) notify(ctx context.Context, issueKey, text string) {
	if err := s.postOrReply(ctx, issueKey, text); err != nil {
		log.Warn().Err(err).Str("issue", issueKey).Msg("slack notification failed")
	}
}

func (s *SlackNotifier) NotifyStartedScoping(ctx context.Context, issueKey, issueTitle string) error {
	s.notify(ctx, issueKey, fmt.Sprintf(":mag: *%s* — %s\nStarted scoping", issueKey, issueTitle))
	return nil
}

func (s *SlackNotifier) NotifyPlanningPhase(ctx context.Context, issueKey, phase string) error {
	s.notify(ctx, issueKey, fmt.Sprintf(":clipboard: %s planning started", phase))
	return nil
}

func (s *SlackNotifier) NotifyQuestions(ctx context.Context, issueKey, phase string, count int) error {
	s.notify(ctx, issueKey, fmt.Sprintf(":question: %d %s question(s) waiting for answers", count, phase))
	return nil
}

func (s *SlackNotifier) NotifyQuestionsResolved(ctx context.Context, issueKey, phase string) error {
	s.notify(ctx, issueKey, fmt.Sprintf(":white_check_mark: All %s questions resolved", phase))
	return nil
}

func (s *SlackNotifier) NotifyPhaseRevert(ctx context.Context, issueKey, fromPhase, toPhase string) error {
	s.notify(ctx, issueKey, fmt.Sprintf(":rewind: Reverted from %s to %s (gaps found)", fromPhase, toPhase))
	return nil
}

func (s *SlackNotifier) NotifyImplementationStarted(ctx context.Context, issueKey string) error {
	s.notify(ctx, issueKey, ":hammer_and_wrench: Implementation started")
	return nil
}

func (s *SlackNotifier) NotifyPRCreated(ctx context.Context, issueKey string, prNumber int) error {
	s.notify(ctx, issueKey, fmt.Sprintf(":rocket: PR #%d created", prNumber))
	return nil
}

func (s *SlackNotifier) NotifyCIFailure(ctx context.Context, issueKey string, prNumber int) error {
	s.notify(ctx, issueKey, fmt.Sprintf(":x: CI failed on PR #%d — fixing", prNumber))
	return nil
}

func (s *SlackNotifier) NotifyReviewFeedback(ctx context.Context, issueKey string, prNumber int) error {
	s.notify(ctx, issueKey, fmt.Sprintf(":speech_balloon: Review feedback on PR #%d — addressing", prNumber))
	return nil
}

func (s *SlackNotifier) NotifyPRMerged(ctx context.Context, issueKey string, prNumber int) error {
	s.notify(ctx, issueKey, fmt.Sprintf(":tada: PR #%d merged!", prNumber))
	return nil
}

func (s *SlackNotifier) NotifyJailbreakDetected(ctx context.Context, issueKey, source, reason string) error {
	s.notify(ctx, issueKey, fmt.Sprintf(":rotating_light: *Jailbreak attempt detected*\nSource: %s\nReason: %s", source, reason))
	return nil
}

func (s *SlackNotifier) NotifySecurityScanComplete(ctx context.Context, repoName string, newFindings, openFindings, mitigatedFindings int) error {
	msg := fmt.Sprintf(":shield: *Security scan complete — %s*\nNew: %d | Open: %d | Mitigated: %d", repoName, newFindings, openFindings, mitigatedFindings)
	// Post as a top-level message using the repo name as the thread key
	s.notify(ctx, "security:"+repoName, msg)
	return nil
}
