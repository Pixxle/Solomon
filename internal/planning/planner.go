package planning

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/pixxle/codehephaestus/internal/config"
	"github.com/pixxle/codehephaestus/internal/db"
	"github.com/pixxle/codehephaestus/internal/figma"
	"github.com/pixxle/codehephaestus/internal/tracker"
	"github.com/pixxle/codehephaestus/internal/worker"
)

type Planner struct {
	cfg       *config.Config
	tracker   tracker.TaskTracker
	stateDB   *db.StateDB
	figma     *figma.Client // may be nil
	botUserID string
}

func NewPlanner(cfg *config.Config, t tracker.TaskTracker, stateDB *db.StateDB, figmaClient *figma.Client, botUserID string) *Planner {
	return &Planner{
		cfg:       cfg,
		tracker:   t,
		stateDB:   stateDB,
		figma:     figmaClient,
		botUserID: botUserID,
	}
}

// StartPlanning begins the planning conversation for a new issue.
func (p *Planner) StartPlanning(ctx context.Context, issue tracker.Issue) error {
	log.Info().Str("issue", issue.Key).Msg("starting planning conversation")

	// Save images from attachments and Figma to disk
	images, err := p.collectImages(ctx, issue)
	if err != nil {
		log.Warn().Err(err).Str("issue", issue.Key).Msg("failed to collect images")
	}

	// Extract Figma URLs
	figmaURLs := figma.ExtractFigmaURLs(issue.Description)
	figmaURLsJSON, _ := json.Marshal(figmaURLs)
	imageRefsJSON, _ := json.Marshal(images)

	// Generate initial planning comment via claude
	prompt, err := worker.RenderPrompt("planning_initial.md.tmpl", map[string]interface{}{
		"IssueKey":       issue.Key,
		"IssueTitle":     issue.Title,
		"Description":    issue.Description,
		"BotDisplayName": p.cfg.BotDisplayName,
		"Images":         images,
	})
	if err != nil {
		return fmt.Errorf("rendering planning prompt: %w", err)
	}

	result, err := worker.RunClaude(ctx, prompt, p.cfg.TargetRepoPath, p.cfg.PlanningModel)
	if err != nil {
		return fmt.Errorf("running planning claude: %w", err)
	}

	// Post the comment
	if !p.cfg.DryRun {
		if err := p.tracker.AddComment(ctx, issue.Key, result.Output); err != nil {
			return fmt.Errorf("posting planning comment: %w", err)
		}
	}

	now := time.Now().UTC()
	// Insert planning state
	ps := &db.PlanningState{
		IssueKey:            issue.Key,
		ConversationJSON:    "[]",
		ParticipantsJSON:    "[]",
		Status:              "active",
		OriginalDescription: issue.Description,
		FigmaURLsJSON:       string(figmaURLsJSON),
		ImageRefsJSON:       string(imageRefsJSON),
		LastSystemCommentAt: &now,
		CreatedAt:           now,
		UpdatedAt:           now,
	}
	if err := p.stateDB.InsertPlanningState(ps); err != nil {
		return fmt.Errorf("inserting planning state: %w", err)
	}

	log.Info().Str("issue", issue.Key).Msg("planning conversation started")
	return nil
}

// ContinuePlanning processes new human comments in a planning conversation.
func (p *Planner) ContinuePlanning(ctx context.Context, issue tracker.Issue, ps *db.PlanningState) error {
	// Get all comments
	comments, err := p.tracker.GetComments(ctx, issue.Key)
	if err != nil {
		return fmt.Errorf("fetching comments: %w", err)
	}

	// Find new human comments (not from our bot)
	var newHumanComments []tracker.Comment
	for _, c := range comments {
		if c.Author == p.botUserID {
			continue
		}
		if ps.LastSystemCommentAt != nil && c.Created.After(*ps.LastSystemCommentAt) {
			newHumanComments = append(newHumanComments, c)
		}
	}

	if len(newHumanComments) == 0 {
		return nil // Nothing to respond to
	}

	// Check cooldown
	newestComment := newHumanComments[len(newHumanComments)-1]
	cooldown := time.Duration(p.cfg.PlanningCommentCooldown) * time.Second
	if time.Since(newestComment.Created) < cooldown {
		log.Debug().Str("issue", issue.Key).Msg("planning cooldown not elapsed, skipping")
		return nil
	}

	// Update participants
	participants := p.updateParticipants(ps, newHumanComments)

	conversationText := tracker.FormatConversation(comments, p.botUserID)

	// Generate follow-up via claude
	prompt, err := worker.RenderPrompt("planning_followup.md.tmpl", map[string]interface{}{
		"IssueKey":       issue.Key,
		"IssueTitle":     issue.Title,
		"Description":    issue.Description,
		"Conversation":   conversationText,
		"BotDisplayName": p.cfg.BotDisplayName,
	})
	if err != nil {
		return fmt.Errorf("rendering follow-up prompt: %w", err)
	}

	result, err := worker.RunClaude(ctx, prompt, p.cfg.TargetRepoPath, p.cfg.PlanningModel)
	if err != nil {
		return fmt.Errorf("running planning follow-up: %w", err)
	}

	if !p.cfg.DryRun {
		if err := p.tracker.AddComment(ctx, issue.Key, result.Output); err != nil {
			return fmt.Errorf("posting follow-up comment: %w", err)
		}
	}

	// Update state
	now := time.Now().UTC()
	lastHuman := newestComment.Created
	participantsJSON, _ := json.Marshal(participants)
	ps.ParticipantsJSON = string(participantsJSON)
	ps.LastHumanResponseAt = &lastHuman
	ps.LastSystemCommentAt = &now
	if err := p.stateDB.UpdatePlanningState(ps); err != nil {
		return fmt.Errorf("updating planning state: %w", err)
	}

	log.Info().Str("issue", issue.Key).Int("new_comments", len(newHumanComments)).Msg("planning conversation continued")
	return nil
}

// CheckReadySignal detects if a human has signalled readiness.
func (p *Planner) CheckReadySignal(ctx context.Context, issue tracker.Issue, ps *db.PlanningState) (bool, error) {
	comments, err := p.tracker.GetComments(ctx, issue.Key)
	if err != nil {
		return false, err
	}

	// Find new human comments since last system comment
	for _, c := range comments {
		if c.Author == p.botUserID {
			continue
		}
		if ps.LastSystemCommentAt != nil && !c.Created.After(*ps.LastSystemCommentAt) {
			continue
		}

		// AI-based intent detection
		isReady, err := p.detectReadyIntent(ctx, c.Body)
		if err != nil {
			log.Warn().Err(err).Msg("failed AI ready detection, falling back to keyword match")
			// Fallback to simple keyword matching
			isReady = containsReadyKeyword(c.Body)
		}

		if isReady {
			return true, nil
		}
	}

	// Also check for thumbs_up reaction on the last system comment
	if ps.LastSystemCommentAt != nil {
		for _, c := range comments {
			if c.Author != p.botUserID {
				continue
			}
			// Check if this is the most recent system comment
			if ps.LastSystemCommentAt != nil && c.Created.Equal(*ps.LastSystemCommentAt) {
				reactions, err := p.tracker.GetCommentReactions(ctx, issue.Key, c.ID)
				if err == nil {
					for _, r := range reactions {
						if r.Type == "thumbs_up" && r.UserID != p.botUserID {
							return true, nil
						}
					}
				}
			}
		}
	}

	return false, nil
}

// CompletePlanning finalizes the planning phase, updates the description, and returns participants.
func (p *Planner) CompletePlanning(ctx context.Context, issue tracker.Issue, ps *db.PlanningState) ([]string, error) {
	log.Info().Str("issue", issue.Key).Msg("completing planning phase")

	comments, err := p.tracker.GetComments(ctx, issue.Key)
	if err != nil {
		return nil, err
	}

	conversationText := tracker.FormatConversation(comments, p.botUserID)

	// Generate final specification via claude
	prompt, err := worker.RenderPrompt("planning_complete.md.tmpl", map[string]interface{}{
		"IssueKey":            issue.Key,
		"IssueTitle":          issue.Title,
		"OriginalDescription": ps.OriginalDescription,
		"Conversation":        conversationText,
		"BotDisplayName":      p.cfg.BotDisplayName,
	})
	if err != nil {
		return nil, fmt.Errorf("rendering completion prompt: %w", err)
	}

	result, err := worker.RunClaude(ctx, prompt, p.cfg.TargetRepoPath, p.cfg.PlanningModel)
	if err != nil {
		return nil, fmt.Errorf("running planning completion: %w", err)
	}

	// Update the issue description
	if !p.cfg.DryRun {
		if err := p.tracker.UpdateDescription(ctx, issue.Key, result.Output, nil); err != nil {
			return nil, fmt.Errorf("updating issue description: %w", err)
		}
	}

	// Update state
	ps.Status = "complete"
	if err := p.stateDB.UpdatePlanningState(ps); err != nil {
		return nil, fmt.Errorf("updating planning state: %w", err)
	}

	// Get participants
	var participants []string
	if err := json.Unmarshal([]byte(ps.ParticipantsJSON), &participants); err != nil {
		participants = nil
	}

	log.Info().Str("issue", issue.Key).Int("participants", len(participants)).Msg("planning phase complete")
	return participants, nil
}

// CheckTimeout checks if a planning conversation has timed out.
func (p *Planner) CheckTimeout(ctx context.Context, issue tracker.Issue, ps *db.PlanningState) error {
	if ps.LastHumanResponseAt == nil {
		return nil
	}

	daysSinceResponse := time.Since(*ps.LastHumanResponseAt).Hours() / 24
	reminderDays := float64(p.cfg.PlanningReminderDays)

	if daysSinceResponse >= reminderDays*2 && p.cfg.PlanningTimeoutAction == "abandon" {
		ps.Status = "timed_out"
		return p.stateDB.UpdatePlanningState(ps)
	}

	if daysSinceResponse >= reminderDays {
		if !p.cfg.DryRun {
			reminder := fmt.Sprintf("## %s — Reminder\n\nThis planning conversation has been waiting for a response for %d days. Please reply to continue or react with 👍 to begin implementation with the current plan.",
				p.cfg.BotDisplayName, int(daysSinceResponse))
			return p.tracker.AddComment(ctx, issue.Key, reminder)
		}
	}

	return nil
}

func (p *Planner) detectReadyIntent(ctx context.Context, commentBody string) (bool, error) {
	prompt := fmt.Sprintf(`Analyze this comment and determine if the human is signaling that they are ready for development to begin. They might say things like "ready", "lgtm", "approved", "go ahead", "looks good", "start building", etc.

Comment: %q

Respond with ONLY a JSON object: {"ready": true} or {"ready": false}`, commentBody)

	output, err := worker.RunClaudeQuick(ctx, prompt, p.cfg.PlanningModel)
	if err != nil {
		return false, err
	}

	var result struct {
		Ready bool `json:"ready"`
	}
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		return false, fmt.Errorf("parsing ready detection response: %w", err)
	}
	return result.Ready, nil
}

func (p *Planner) updateParticipants(ps *db.PlanningState, comments []tracker.Comment) []string {
	var existing []string
	_ = json.Unmarshal([]byte(ps.ParticipantsJSON), &existing)

	seen := make(map[string]bool)
	for _, e := range existing {
		seen[e] = true
	}
	for _, c := range comments {
		if c.Author != p.botUserID && !seen[c.Author] {
			existing = append(existing, c.Author)
			seen[c.Author] = true
		}
	}
	return existing
}

func (p *Planner) collectImages(ctx context.Context, issue tracker.Issue) ([]string, error) {
	var imagePaths []string

	// Download tracker attachments
	attachments, err := p.tracker.GetAttachments(ctx, issue.Key)
	if err != nil {
		return nil, err
	}

	imgDir := filepath.Join(p.cfg.TargetRepoPath, ".codehephaestus", "images", issue.Key)
	if err := os.MkdirAll(imgDir, 0o755); err != nil {
		return nil, err
	}

	for _, att := range attachments {
		if !isImageMime(att.MimeType) {
			continue
		}
		data, _, err := p.tracker.DownloadAttachment(ctx, att.URL)
		if err != nil {
			log.Warn().Err(err).Str("file", att.Filename).Msg("failed to download attachment")
			continue
		}
		path := filepath.Join(imgDir, att.Filename)
		if err := os.WriteFile(path, data, 0o644); err != nil {
			log.Warn().Err(err).Str("file", att.Filename).Msg("failed to save attachment")
			continue
		}
		imagePaths = append(imagePaths, path)
	}

	// Export Figma frames
	if p.figma != nil {
		figmaURLs := figma.ExtractFigmaURLs(issue.Description)
		for _, fu := range figmaURLs {
			exports, err := p.figma.ExportNodes(ctx, fu.FileKey, fu.NodeIDs)
			if err != nil {
				log.Warn().Err(err).Str("fileKey", fu.FileKey).Msg("failed to export Figma frames")
				continue
			}
			for _, exp := range exports {
				filename := fmt.Sprintf("figma_%s_%s.%s", fu.FileKey, exp.NodeID, p.cfg.FigmaExportFormat)
				path := filepath.Join(imgDir, filename)
				if err := os.WriteFile(path, exp.Data, 0o644); err != nil {
					log.Warn().Err(err).Msg("failed to save Figma export")
					continue
				}
				imagePaths = append(imagePaths, path)
			}
		}
	}

	return imagePaths, nil
}

func containsReadyKeyword(text string) bool {
	lower := strings.ToLower(text)
	keywords := []string{"ready", "lgtm", "approved", "go ahead", "looks good", "start building"}
	for _, kw := range keywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

func isImageMime(mime string) bool {
	return mime == "image/png" || mime == "image/jpeg" || mime == "image/gif" ||
		mime == "image/svg+xml" || mime == "image/webp"
}
