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

// Planning phase values.
const (
	PhaseProduct   = "product"
	PhaseTechnical = "technical"
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
// Starts with the product requirements refinement phase.
func (p *Planner) StartPlanning(ctx context.Context, issue tracker.Issue) error {
	log.Info().Str("issue", issue.Key).Msg("starting planning conversation (product requirements phase)")

	// Save images from attachments and Figma to disk
	images, err := p.collectImages(ctx, issue)
	if err != nil {
		log.Warn().Err(err).Str("issue", issue.Key).Msg("failed to collect images")
	}

	// Extract Figma URLs
	figmaURLs := figma.ExtractFigmaURLs(issue.Description)
	figmaURLsJSON, _ := json.Marshal(figmaURLs)
	imageRefsJSON, _ := json.Marshal(images)

	// Generate initial product requirements comment via claude
	prompt, err := worker.RenderPrompt("planning_initial.md.tmpl", map[string]interface{}{
		"IssueKey":         issue.Key,
		"IssueTitle":       issue.Title,
		"Description":      issue.Description,
		"BotDisplayName":   p.cfg.BotDisplayName,
		"Images":           images,
		"ReadyInstruction": p.tracker.ReadySignalInstruction(),
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

	// Ensure heading reflects question state
	output := ensureCorrectProductHeading(cleanOutput, len(questions) == 0, p.cfg.BotDisplayName)

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
		PlanningPhase:       PhaseProduct,
	}
	if err := p.stateDB.InsertPlanningState(ps); err != nil {
		return fmt.Errorf("inserting planning state: %w", err)
	}

	log.Info().Str("issue", issue.Key).Int("questions", len(questions)).Str("phase", PhaseProduct).Msg("planning conversation started")
	return nil
}

// ContinuePlanning re-analyzes when the issue description has changed.
// Uses phase-appropriate prompts and handles automatic phase transitions.
func (p *Planner) ContinuePlanning(ctx context.Context, issue tracker.Issue, ps *db.PlanningState) error {
	// Only act if the description changed
	if !DescriptionChanged(issue.Description, ps.LastSeenDescription) {
		return nil
	}

	phase := ps.PlanningPhase
	if phase == "" {
		phase = PhaseProduct
	}

	// Load open questions
	var openQuestions []string
	_ = json.Unmarshal([]byte(ps.QuestionsJSON), &openQuestions)

	// Select the appropriate follow-up template based on phase
	templateName := "planning_followup.md.tmpl"
	if phase == PhaseTechnical {
		templateName = "planning_technical_followup.md.tmpl"
	}

	// Generate follow-up via claude
	templateData := map[string]interface{}{
		"IssueKey":            issue.Key,
		"IssueTitle":          issue.Title,
		"PreviousDescription": ps.LastSeenDescription,
		"CurrentDescription":  issue.Description,
		"OpenQuestions":       openQuestions,
		"BotDisplayName":      p.cfg.BotDisplayName,
		"ReadyInstruction":    p.tracker.ReadySignalInstruction(),
	}
	if phase == PhaseTechnical {
		templateData["ProductSummary"] = ps.ProductSummary
	}
	prompt, err := worker.RenderPrompt(templateName, templateData)
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

	// During technical phase, check if the AI flagged product requirements gaps
	if phase == PhaseTechnical {
		productGaps := parseProductGaps(cleanOutput)
		if len(productGaps) > 0 {
			log.Info().Str("issue", issue.Key).Int("product_gaps", len(productGaps)).
				Msg("product requirements gaps detected during technical refinement, reverting to product phase")
			// Transition back to product phase with the gaps as questions
			questionsJSON, _ := json.Marshal(productGaps)
			now := time.Now().UTC()
			ps.LastSeenDescription = issue.Description
			ps.QuestionsJSON = string(questionsJSON)
			ps.PlanningPhase = PhaseProduct
			ps.LastSystemCommentAt = &now

			// Rewrite heading to reflect product phase
			output := ensureCorrectProductHeading(cleanOutput, false, p.cfg.BotDisplayName)
			// Replace "Technical Refinement" heading with product heading if present
			output = strings.Replace(output,
				fmt.Sprintf("## %s — Technical Refinement\n", p.cfg.BotDisplayName),
				fmt.Sprintf("## %s — Product Requirements Refinement\n", p.cfg.BotDisplayName), 1)

			p.updateBotComment(ctx, issue.Key, ps, output)

			if err := p.stateDB.UpdatePlanningState(ps); err != nil {
				return fmt.Errorf("updating planning state for product revert: %w", err)
			}
			return nil
		}
	}

	questionsJSON, _ := json.Marshal(remainingQuestions)

	// Ensure heading reflects question state based on phase
	var output string
	if phase == PhaseTechnical {
		output = ensureCorrectTechnicalHeading(cleanOutput, len(remainingQuestions) == 0, p.cfg.BotDisplayName)
	} else {
		output = ensureCorrectProductHeading(cleanOutput, len(remainingQuestions) == 0, p.cfg.BotDisplayName)
	}

	p.updateBotComment(ctx, issue.Key, ps, output)

	// Update state
	now := time.Now().UTC()
	ps.LastSeenDescription = issue.Description
	ps.QuestionsJSON = string(questionsJSON)
	ps.LastSystemCommentAt = &now

	// Check for automatic phase transition: product → technical
	if phase == PhaseProduct && len(remainingQuestions) == 0 {
		log.Info().Str("issue", issue.Key).Msg("product requirements complete, transitioning to technical refinement")
		// Save the product refinement output as the product summary
		ps.ProductSummary = output
		ps.PlanningPhase = PhaseTechnical
		// Reset questions for the new phase — they'll be populated by StartTechnicalRefinement
		ps.QuestionsJSON = db.EmptyJSONArray
		if err := p.stateDB.UpdatePlanningState(ps); err != nil {
			return fmt.Errorf("updating planning state for phase transition: %w", err)
		}
		// Immediately start technical refinement
		return p.StartTechnicalRefinement(ctx, issue, ps)
	}

	if err := p.stateDB.UpdatePlanningState(ps); err != nil {
		return fmt.Errorf("updating planning state: %w", err)
	}

	log.Info().Str("issue", issue.Key).Int("remaining_questions", len(remainingQuestions)).Str("phase", phase).Msg("planning comment updated")
	return nil
}

// StartTechnicalRefinement begins the technical refinement phase.
// Called automatically when product requirements are complete.
func (p *Planner) StartTechnicalRefinement(ctx context.Context, issue tracker.Issue, ps *db.PlanningState) error {
	log.Info().Str("issue", issue.Key).Msg("starting technical refinement phase")

	var images []string
	_ = json.Unmarshal([]byte(ps.ImageRefsJSON), &images)

	// Generate technical refinement comment via claude
	prompt, err := worker.RenderPrompt("planning_technical_initial.md.tmpl", map[string]interface{}{
		"IssueKey":         issue.Key,
		"IssueTitle":       issue.Title,
		"Description":      issue.Description,
		"BotDisplayName":   p.cfg.BotDisplayName,
		"Images":           images,
		"ReadyInstruction": p.tracker.ReadySignalInstruction(),
		"ProductSummary":   ps.ProductSummary,
	})
	if err != nil {
		return fmt.Errorf("rendering technical planning prompt: %w", err)
	}

	result, err := worker.RunClaude(ctx, prompt, p.cfg.TargetRepoPath, p.cfg.PlanningModel)
	if err != nil {
		return fmt.Errorf("running technical planning claude: %w", err)
	}

	cleanOutput := stripPreamble(result.Output, p.cfg.BotDisplayName)

	// Check if the initial technical analysis immediately found product gaps
	productGaps := parseProductGaps(cleanOutput)
	if len(productGaps) > 0 {
		log.Info().Str("issue", issue.Key).Int("product_gaps", len(productGaps)).
			Msg("product requirements gaps detected at start of technical refinement, reverting to product phase")
		questionsJSON, _ := json.Marshal(productGaps)
		now := time.Now().UTC()
		ps.QuestionsJSON = string(questionsJSON)
		ps.PlanningPhase = PhaseProduct
		ps.LastSystemCommentAt = &now

		output := strings.Replace(cleanOutput,
			fmt.Sprintf("## %s — Technical Refinement\n", p.cfg.BotDisplayName),
			fmt.Sprintf("## %s — Product Requirements Refinement\n", p.cfg.BotDisplayName), 1)
		p.updateBotComment(ctx, issue.Key, ps, output)

		if err := p.stateDB.UpdatePlanningState(ps); err != nil {
			return fmt.Errorf("updating planning state for product revert: %w", err)
		}
		return nil
	}

	questions := parseQuestions(cleanOutput)
	questionsJSON, _ := json.Marshal(questions)

	output := ensureCorrectTechnicalHeading(cleanOutput, len(questions) == 0, p.cfg.BotDisplayName)
	p.updateBotComment(ctx, issue.Key, ps, output)

	now := time.Now().UTC()
	ps.QuestionsJSON = string(questionsJSON)
	ps.PlanningPhase = PhaseTechnical
	ps.LastSystemCommentAt = &now
	if err := p.stateDB.UpdatePlanningState(ps); err != nil {
		return fmt.Errorf("updating planning state for technical phase: %w", err)
	}

	log.Info().Str("issue", issue.Key).Int("questions", len(questions)).Str("phase", PhaseTechnical).Msg("technical refinement started")
	return nil
}

// IsProductPhaseComplete returns true if the planning is in the technical phase
// (meaning product refinement has already completed).
func IsProductPhaseComplete(ps *db.PlanningState) bool {
	return ps.PlanningPhase == PhaseTechnical
}

// IsTechnicalPhaseComplete returns true if the planning is in the technical phase
// and there are no remaining open questions.
func IsTechnicalPhaseComplete(ps *db.PlanningState) bool {
	if ps.PlanningPhase != PhaseTechnical {
		return false
	}
	var questions []string
	_ = json.Unmarshal([]byte(ps.QuestionsJSON), &questions)
	return len(questions) == 0
}

// CheckReadySignal detects if a human has signalled readiness for implementation.
func (p *Planner) CheckReadySignal(ctx context.Context, issue tracker.Issue, ps *db.PlanningState) (bool, error) {
	return p.tracker.IsReadySignal(ctx, issue, ps.BotCommentID)
}

// ShouldAutoLaunch returns true if auto-launch is configured and conditions are met:
// the ticket is assigned to the bot, product refinement is done, and technical refinement
// has no open questions.
func (p *Planner) ShouldAutoLaunch(issue tracker.Issue, ps *db.PlanningState) bool {
	if !p.cfg.AutoLaunchImplementation {
		return false
	}
	if !issue.IsAssignedTo(p.botUserID) {
		return false
	}
	return IsTechnicalPhaseComplete(ps)
}

// CompletePlanning finalizes the planning phase. The description IS the spec,
// so we update the bot's comment to indicate implementation has begun.
func (p *Planner) CompletePlanning(ctx context.Context, issue tracker.Issue, ps *db.PlanningState) error {
	log.Info().Str("issue", issue.Key).Msg("completing planning phase")

	// Update the bot's comment to indicate implementation is starting
	if !p.cfg.DryRun && ps.BotCommentID != "" {
		finalComment := fmt.Sprintf("## %s — Implementation Started\n\nAll product and technical refinement questions have been resolved. Implementation has begun based on the current issue description.",
			p.cfg.BotDisplayName)
		if err := p.tracker.UpdateComment(ctx, issue.Key, ps.BotCommentID, finalComment); err != nil {
			log.Warn().Err(err).Msg("failed to update bot comment for completion")
		}
	}

	// Clear the ready signal if present (best-effort)
	if ready, _ := p.tracker.IsReadySignal(ctx, issue, ps.BotCommentID); ready {
		if err := p.tracker.ClearReadySignal(ctx, issue.Key); err != nil {
			log.Warn().Err(err).Str("issue", issue.Key).Msg("failed to clear ready signal after completing planning")
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

	phase := ps.PlanningPhase
	if phase == "" {
		phase = PhaseProduct
	}

	if daysSinceActivity >= reminderDays {
		if !p.cfg.DryRun {
			phaseLabel := "product requirements refinement"
			if phase == PhaseTechnical {
				phaseLabel = "technical refinement"
			}
			reminder := fmt.Sprintf("## %s — Reminder\n\nThis %s conversation has been waiting for a response for %d days. Please update the issue description to continue or %s.",
				p.cfg.BotDisplayName, phaseLabel, int(daysSinceActivity), p.tracker.ReadySignalInstruction())
			return p.tracker.AddComment(ctx, issue.Key, reminder)
		}
	}

	return nil
}

// parseQuestions extracts numbered items from the ### Open Questions section.
var questionsRe = regexp.MustCompile(`(?m)^\d+\.\s+(.+)`)

func parseQuestions(output string) []string {
	return parseSection(output, "### Open Questions")
}

// parseProductGaps extracts numbered items from the ### Product Requirements Gaps section.
// When present during technical refinement, this triggers a transition back to product phase.
func parseProductGaps(output string) []string {
	return parseSection(output, "### Product Requirements Gaps")
}

func parseSection(output, heading string) []string {
	sectionStart := strings.Index(output, heading)
	if sectionStart == -1 {
		return nil
	}

	// Extract text until the next ### heading or end of string
	rest := output[sectionStart+len(heading):]
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

// updateBotComment updates the bot's comment in-place, falling back to a new comment if update fails.
func (p *Planner) updateBotComment(ctx context.Context, issueKey string, ps *db.PlanningState, content string) {
	if p.cfg.DryRun {
		return
	}
	if ps.BotCommentID != "" {
		if err := p.tracker.UpdateComment(ctx, issueKey, ps.BotCommentID, content); err != nil {
			log.Warn().Err(err).Str("issue", issueKey).Msg("failed to update comment, posting new one")
			newID, postErr := p.tracker.AddCommentReturningID(ctx, issueKey, content)
			if postErr != nil {
				log.Error().Err(postErr).Str("issue", issueKey).Msg("failed to post fallback comment")
				return
			}
			ps.BotCommentID = newID
		}
	} else {
		newID, err := p.tracker.AddCommentReturningID(ctx, issueKey, content)
		if err != nil {
			log.Error().Err(err).Str("issue", issueKey).Msg("failed to post planning comment")
			return
		}
		ps.BotCommentID = newID
	}
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

// ensureCorrectProductHeading fixes the comment heading to match the question state
// during the product requirements refinement phase.
func ensureCorrectProductHeading(output string, noQuestions bool, botName string) string {
	active := fmt.Sprintf("## %s — Product Requirements Refinement\n", botName)
	complete := fmt.Sprintf("## %s — Product Requirements Complete\n", botName)

	if noQuestions {
		return strings.Replace(output, active, complete, 1)
	}
	return strings.Replace(output, complete, active, 1)
}

// ensureCorrectTechnicalHeading fixes the comment heading to match the question state
// during the technical refinement phase.
func ensureCorrectTechnicalHeading(output string, noQuestions bool, botName string) string {
	active := fmt.Sprintf("## %s — Technical Refinement\n", botName)
	complete := fmt.Sprintf("## %s — Technical Refinement Complete\n", botName)

	if noQuestions {
		return strings.Replace(output, active, complete, 1)
	}
	return strings.Replace(output, complete, active, 1)
}

func isImageMime(mime string) bool {
	return mime == "image/png" || mime == "image/jpeg" || mime == "image/gif" ||
		mime == "image/svg+xml" || mime == "image/webp"
}
