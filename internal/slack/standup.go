package slack

import (
	"context"
	"encoding/json"
	"time"

	"github.com/rs/zerolog/log"
	slackapi "github.com/slack-go/slack"

	"github.com/pixxle/codehephaestus/internal/config"
	"github.com/pixxle/codehephaestus/internal/db"
	"github.com/pixxle/codehephaestus/internal/worker"
)

// standupItem is the JSON shape passed to the standup prompt template.
type standupItem struct {
	IssueKey  string `json:"issue_key"`
	Status    string `json:"status"`
	Phase     string `json:"phase"`
	Questions string `json:"questions"`
	Updated   string `json:"updated"`
}

// StandupRunner posts a daily LLM-generated standup message.
type StandupRunner struct {
	client    *slackapi.Client
	channelID string
	stateDB   *db.StateDB
	cfg       *config.Config
	lastDate  string // tracks the date of the last standup to avoid duplicates
}

// NewStandupRunner creates a StandupRunner that posts to the configured standup channel.
// Accepts a shared Slack client to avoid duplicate HTTP connection pools.
func NewStandupRunner(cfg *config.Config, stateDB *db.StateDB, client *slackapi.Client) *StandupRunner {
	channelID := cfg.SlackStandupChannelID
	if channelID == "" {
		channelID = cfg.SlackChannelID
	}
	return &StandupRunner{
		client:    client,
		channelID: channelID,
		stateDB:   stateDB,
		cfg:       cfg,
	}
}

// Run starts the standup ticker. Blocks until ctx is cancelled.
func (s *StandupRunner) Run(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.check(ctx)
		}
	}
}

func (s *StandupRunner) check(ctx context.Context) {
	now := time.Now().UTC()
	today := now.Format("2006-01-02")

	if now.Hour() != s.cfg.SlackStandupHour {
		return
	}
	if s.lastDate == today {
		return
	}

	s.lastDate = today
	if err := s.postStandup(ctx, now); err != nil {
		log.Error().Err(err).Msg("failed to post standup")
	}
}

func (s *StandupRunner) postStandup(ctx context.Context, now time.Time) error {
	states, err := s.stateDB.GetAllPlanningStates()
	if err != nil {
		return err
	}

	items := make([]standupItem, len(states))
	for i, ps := range states {
		items[i] = standupItem{
			IssueKey:  ps.IssueKey,
			Status:    ps.Status,
			Phase:     ps.PlanningPhase,
			Questions: ps.QuestionsJSON,
			Updated:   ps.UpdatedAt.Format(time.RFC3339),
		}
	}
	itemsJSON, _ := json.Marshal(items)

	prompt, err := worker.RenderPrompt("standup.md.tmpl", map[string]interface{}{
		"Date":           now.Format("2006-01-02"),
		"BotDisplayName": s.cfg.BotDisplayName,
		"TicketData":     string(itemsJSON),
	})
	if err != nil {
		return err
	}

	result, err := worker.RunClaudeText(ctx, prompt, s.cfg.TargetRepoPath, s.cfg.PlanningModel)
	if err != nil {
		return err
	}

	_, _, err = s.client.PostMessageContext(ctx, s.channelID,
		slackapi.MsgOptionText(result.Output, false),
	)
	if err != nil {
		log.Error().Err(err).Msg("failed to post standup to Slack")
		return err
	}

	log.Info().Str("channel", s.channelID).Msg("standup posted")
	return nil
}
