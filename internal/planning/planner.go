package planning

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/pixxle/codehephaestus/internal/config"
	"github.com/pixxle/codehephaestus/internal/db"
	"github.com/pixxle/codehephaestus/internal/figma"
	"github.com/pixxle/codehephaestus/internal/tracker"
	"github.com/pixxle/codehephaestus/internal/worker"
)

// DescriptionChanged reports whether the issue description differs from the last analyzed version.
func DescriptionChanged(current, lastSeen string) bool {
	return strings.TrimSpace(current) != strings.TrimSpace(lastSeen)
}

// Planning state status values.
const (
	StatusActive   = "active"
	StatusComplete = "complete"
	StatusTimedOut = "timed_out"
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
// Posts a single analysis comment and stores its ID for in-place updates.
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

	// Strip any preamble text that leaks from tool use in --print mode
	cleanOutput := stripPreamble(result.Output, p.cfg.BotDisplayName)

	// Parse questions from AI output
	questions := parseQuestions(cleanOutput)
	questionsJSON, _ := json.Marshal(questions)

	// Ensure heading reflects question state — AI may not always comply with template instructions
	output := ensureCorrectHeading(cleanOutput, len(questions) == 0, p.cfg.BotDisplayName)

	// Post the comment and capture its ID
	var botCommentID string
	if !p.cfg.DryRun {
		commentID, err := p.tracker.AddCommentReturningID(ctx, issue.Key, output)
		if err != nil {
			return fmt.Errorf("posting planning comment: %w", err)
		}
		botCommentID = commentID
	}

	now := time.Now().UTC()
	ps := &db.PlanningState{
		IssueKey:            issue.Key,
		ConversationJSON:    db.EmptyJSONArray,
		ParticipantsJSON:    db.EmptyJSONArray,
		Status:              StatusActive,
		OriginalDescription: issue.Description,
		FigmaURLsJSON:       string(figmaURLsJSON),
		ImageRefsJSON:       string(imageRefsJSON),
		LastSystemCommentAt: &now,
		CreatedAt:           now,
		UpdatedAt:           now,
		BotCommentID:        botCommentID,
		LastSeenDescription: issue.Description,
		QuestionsJSON:       string(questionsJSON),
	}
	if err := p.stateDB.InsertPlanningState(ps); err != nil {
		return fmt.Errorf("inserting planning state: %w", err)
	}

	log.Info().Str("issue", issue.Key).Int("questions", len(questions)).Msg("planning conversation started")
	return nil
}

// ContinuePlanning re-analyzes when the issue description has changed.
// Updates the bot's comment in-place instead of posting new comments.
func (p *Planner) ContinuePlanning(ctx context.Context, issue tracker.Issue, ps *db.PlanningState) error {
	// Only act if the description changed
	if !DescriptionChanged(issue.Description, ps.LastSeenDescription) {
		return nil
	}

	// Load open questions
	var openQuestions []string
	_ = json.Unmarshal([]byte(ps.QuestionsJSON), &openQuestions)

	// Generate follow-up via claude
	prompt, err := worker.RenderPrompt("planning_followup.md.tmpl", map[string]interface{}{
		"IssueKey":            issue.Key,
		"IssueTitle":          issue.Title,
		"PreviousDescription": ps.LastSeenDescription,
		"CurrentDescription":  issue.Description,
		"OpenQuestions":       openQuestions,
		"BotDisplayName":      p.cfg.BotDisplayName,
	})
	if err != nil {
		return fmt.Errorf("rendering follow-up prompt: %w", err)
	}

	result, err := worker.RunClaude(ctx, prompt, p.cfg.TargetRepoPath, p.cfg.PlanningModel)
	if err != nil {
		return fmt.Errorf("running planning follow-up: %w", err)
	}

	// Strip any preamble text that leaks from tool use in --print mode
	cleanOutput := stripPreamble(result.Output, p.cfg.BotDisplayName)

	// Parse remaining questions from output
	remainingQuestions := parseQuestions(cleanOutput)
	questionsJSON, _ := json.Marshal(remainingQuestions)

	// Ensure heading reflects question state
	output := ensureCorrectHeading(cleanOutput, len(remainingQuestions) == 0, p.cfg.BotDisplayName)

	// Update comment in-place; fallback to new comment if update fails
	if !p.cfg.DryRun {
		if ps.BotCommentID != "" {
			if err := p.tracker.UpdateComment(ctx, issue.Key, ps.BotCommentID, output); err != nil {
				log.Warn().Err(err).Str("issue", issue.Key).Msg("failed to update comment, posting new one")
				newID, postErr := p.tracker.AddCommentReturningID(ctx, issue.Key, output)
				if postErr != nil {
					return fmt.Errorf("posting fallback comment: %w", postErr)
				}
				ps.BotCommentID = newID
			}
		} else {
			newID, err := p.tracker.AddCommentReturningID(ctx, issue.Key, output)
			if err != nil {
				return fmt.Errorf("posting planning comment: %w", err)
			}
			ps.BotCommentID = newID
		}
	}

	// Update state
	now := time.Now().UTC()
	ps.LastSeenDescription = issue.Description
	ps.QuestionsJSON = string(questionsJSON)
	ps.LastSystemCommentAt = &now
	if err := p.stateDB.UpdatePlanningState(ps); err != nil {
		return fmt.Errorf("updating planning state: %w", err)
	}

	log.Info().Str("issue", issue.Key).Int("remaining_questions", len(remainingQuestions)).Msg("planning comment updated")
	return nil
}

// CheckReadySignal detects if a human has signalled readiness via thumbs-up reaction on the bot's comment.
func (p *Planner) CheckReadySignal(ctx context.Context, issue tracker.Issue, ps *db.PlanningState) (bool, error) {
	if ps.BotCommentID != "" {
		reactions, err := p.tracker.GetCommentReactions(ctx, issue.Key, ps.BotCommentID)
		if err == nil {
			for _, r := range reactions {
				if r.Type == "thumbs_up" {
					return true, nil
				}
			}
		}
	}

	return false, nil
}

// CompletePlanning finalizes the planning phase. The description IS the spec,
// so we update the bot's comment to indicate implementation has begun.
func (p *Planner) CompletePlanning(ctx context.Context, issue tracker.Issue, ps *db.PlanningState) error {
	log.Info().Str("issue", issue.Key).Msg("completing planning phase")

	// Update the bot's comment to indicate implementation is starting
	if !p.cfg.DryRun && ps.BotCommentID != "" {
		finalComment := fmt.Sprintf("## %s — Implementation Started\n\nAll planning questions have been resolved. Implementation has begun based on the current issue description.",
			p.cfg.BotDisplayName)
		if err := p.tracker.UpdateComment(ctx, issue.Key, ps.BotCommentID, finalComment); err != nil {
			log.Warn().Err(err).Msg("failed to update bot comment for completion")
		}
	}

	ps.Status = StatusComplete
	if err := p.stateDB.UpdatePlanningState(ps); err != nil {
		return fmt.Errorf("updating planning state: %w", err)
	}

	log.Info().Str("issue", issue.Key).Msg("planning phase complete")
	return nil
}

// CheckTimeout checks if a planning conversation has timed out.
// Uses LastSystemCommentAt (when the bot last analyzed) since the
// description-centric flow doesn't track human comment timestamps.
func (p *Planner) CheckTimeout(ctx context.Context, issue tracker.Issue, ps *db.PlanningState) error {
	if ps.LastSystemCommentAt == nil {
		return nil
	}

	daysSinceActivity := time.Since(*ps.LastSystemCommentAt).Hours() / 24
	reminderDays := float64(p.cfg.PlanningReminderDays)

	if daysSinceActivity >= reminderDays*2 && p.cfg.PlanningTimeoutAction == "abandon" {
		ps.Status = StatusTimedOut
		return p.stateDB.UpdatePlanningState(ps)
	}

	if daysSinceActivity >= reminderDays {
		if !p.cfg.DryRun {
			reminder := fmt.Sprintf("## %s — Reminder\n\nThis planning conversation has been waiting for a response for %d days. Please update the issue description to continue or react with :+1: to begin implementation with the current plan.",
				p.cfg.BotDisplayName, int(daysSinceActivity))
			return p.tracker.AddComment(ctx, issue.Key, reminder)
		}
	}

	return nil
}

// parseQuestions extracts numbered items from the ### Open Questions section.
var questionsRe = regexp.MustCompile(`(?m)^\d+\.\s+(.+)`)

func parseQuestions(output string) []string {
	// Find the Open Questions section
	sectionStart := strings.Index(output, "### Open Questions")
	if sectionStart == -1 {
		return nil
	}

	// Extract text until the next ### heading or end of string
	rest := output[sectionStart+len("### Open Questions"):]
	nextSection := strings.Index(rest, "\n### ")
	if nextSection != -1 {
		rest = rest[:nextSection]
	}

	matches := questionsRe.FindAllStringSubmatch(rest, -1)
	var questions []string
	for _, m := range matches {
		q := strings.TrimSpace(m[1])
		if q != "" {
			questions = append(questions, q)
		}
	}
	return questions
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

// stripPreamble removes any text before the expected heading.
// When claude --print is used with tool use, intermediate text output
// (e.g. "Let me read the images...") leaks into stdout before the actual comment.
func stripPreamble(output, botName string) string {
	marker := fmt.Sprintf("## %s — ", botName)
	idx := strings.Index(output, marker)
	if idx > 0 {
		return output[idx:]
	}
	return output
}

// ensureCorrectHeading fixes the comment heading to match the question state.
// When noQuestions is true, ensures "Planning Complete"; otherwise ensures "Planning".
func ensureCorrectHeading(output string, noQuestions bool, botName string) string {
	planning := fmt.Sprintf("## %s — Planning\n", botName)
	planningComplete := fmt.Sprintf("## %s — Planning Complete\n", botName)

	if noQuestions {
		return strings.Replace(output, planning, planningComplete, 1)
	}
	return strings.Replace(output, planningComplete, planning, 1)
}

func isImageMime(mime string) bool {
	return mime == "image/png" || mime == "image/jpeg" || mime == "image/gif" ||
		mime == "image/svg+xml" || mime == "image/webp"
}
