package tracker

import (
	"context"
	"fmt"

	"github.com/pixxle/solomon/internal/config"
)

type TaskTracker interface {
	ValidateConnection(ctx context.Context) error
	ResolveCurrentUser(ctx context.Context) (string, error)
	FetchIssuesByStatus(ctx context.Context, status string) ([]Issue, error)
	TransitionIssue(ctx context.Context, issueKey string, toStatus string) error
	GetIssueBranchName(issue Issue, botSlug string) string
	GetComments(ctx context.Context, issueKey string) ([]Comment, error)
	AddComment(ctx context.Context, issueKey string, body string) error
	AddCommentReturningID(ctx context.Context, issueKey, body string) (string, error)
	UpdateComment(ctx context.Context, issueKey, commentID, body string) error
	DeleteComment(ctx context.Context, issueKey, commentID string) error
	AttachFile(ctx context.Context, issueKey string, filePath string) error
	GetCommentReactions(ctx context.Context, issueKey string, commentID string) ([]Reaction, error)
	UpdateDescription(ctx context.Context, issueKey string, description string, attachments []Attachment) error
	GetAttachments(ctx context.Context, issueKey string) ([]Attachment, error)
	DownloadAttachment(ctx context.Context, url string) ([]byte, string, error)
	IsReadySignal(ctx context.Context, issue Issue, botCommentID string) (bool, error)
	ClearReadySignal(ctx context.Context, issueKey string) error
	ReadySignalInstruction() string
}

func NewTracker(cfg *config.Config) (TaskTracker, error) {
	switch cfg.TaskTracker {
	case config.TrackerJira:
		return NewJiraTracker(cfg)
	case config.TrackerLinear:
		return nil, fmt.Errorf("linear tracker not yet implemented (deferred)")
	default:
		return nil, fmt.Errorf("unsupported tracker type: %s", cfg.TaskTracker)
	}
}
