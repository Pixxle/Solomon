package securityengineer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
	"github.com/rs/zerolog/log"

	"github.com/pixxle/solomon/internal/db"
	"github.com/pixxle/solomon/internal/git"
	"github.com/pixxle/solomon/internal/plugin"
	"github.com/pixxle/solomon/internal/slack"
)

func init() {
	plugin.Register("securityengineer", func(cfg plugin.PluginConfig, libs *plugin.SharedLibs) (plugin.Plugin, error) {
		return NewSecurityEngineerPlugin(cfg, libs)
	})
}

// SecurityEngineerPlugin runs security audits on configured repositories.
type SecurityEngineerPlugin struct {
	pluginCfg plugin.PluginConfig
	libs      *plugin.SharedLibs
	notifier  slack.Notifier
	cron      *cron.Cron
	mu        sync.Mutex
	cancel    context.CancelFunc
}

// NewSecurityEngineerPlugin creates a new security engineer plugin instance.
func NewSecurityEngineerPlugin(cfg plugin.PluginConfig, libs *plugin.SharedLibs) (*SecurityEngineerPlugin, error) {
	return &SecurityEngineerPlugin{
		pluginCfg: cfg,
		libs:      libs,
		notifier:  plugin.NewNotifier(libs, cfg.Settings),
	}, nil
}

func (p *SecurityEngineerPlugin) Name() string { return "securityengineer" }

func (p *SecurityEngineerPlugin) Start(ctx context.Context) error {
	ctx, p.cancel = context.WithCancel(ctx)

	// --once mode: run all scans synchronously and return
	if p.libs.Config.Once {
		defer p.cancel()
		for _, sched := range p.pluginCfg.Schedules {
			log.Info().Str("schedule", sched.Name).Msg("securityengineer: running scan (once mode)")
			if err := p.runScan(ctx, sched.Name); err != nil {
				log.Error().Err(err).Str("trigger", sched.Name).Msg("security scan failed")
			}
		}
		return nil
	}

	p.cron = cron.New()
	for _, sched := range p.pluginCfg.Schedules {
		s := sched
		_, err := p.cron.AddFunc(s.CronExpr, func() {
			p.mu.Lock()
			defer p.mu.Unlock()
			if ctx.Err() != nil {
				return
			}
			if err := p.runScan(ctx, s.Name); err != nil {
				log.Error().Err(err).Str("trigger", s.Name).Msg("security scan failed")
			}
		})
		if err != nil {
			return fmt.Errorf("add cron schedule %q: %w", s.Name, err)
		}
		log.Info().Str("schedule", s.Name).Str("cron", s.CronExpr).Msg("securityengineer: registered cron schedule")
	}

	p.cron.Start()
	return nil
}

func (p *SecurityEngineerPlugin) Stop(ctx context.Context) error {
	if p.cron != nil {
		p.cron.Stop()
	}
	if p.cancel != nil {
		p.cancel()
	}
	return nil
}

func (p *SecurityEngineerPlugin) runScan(ctx context.Context, scanType string) error {
	for _, repo := range p.pluginCfg.Repos {
		repoPath := repo.Path
		if repoPath == "" || repoPath == "." {
			repoPath = p.libs.Config.TargetRepoPath
		}

		log.Info().Str("repo", repo.Name).Str("scan_type", scanType).Msg("starting security scan")

		// Update main branch
		if err := git.UpdateMain(ctx, repoPath); err != nil {
			log.Warn().Err(err).Str("repo", repo.Name).Msg("failed to update main branch")
		}

		commitHash := getCommitHash(repoPath)

		// Create scan record
		scan := &db.SecurityScan{
			RepoName:   repo.Name,
			ScanType:   scanType,
			Status:     ScanStatusRunning,
			CommitHash: commitHash,
		}
		if err := p.libs.DB.CreateSecurityScan(scan); err != nil {
			return fmt.Errorf("create scan record: %w", err)
		}

		if scanType == ScanTypeQuick {
			if err := p.runQuickScan(ctx, repo.Name, repoPath, scan.ID); err != nil {
				p.libs.DB.UpdateSecurityScanStatus(scan.ID, ScanStatusFailed, err.Error())
				log.Error().Err(err).Str("repo", repo.Name).Msg("quick scan failed")
				continue
			}
		} else {
			if err := p.runFullScan(ctx, repo.Name, repoPath, scan.ID); err != nil {
				p.libs.DB.UpdateSecurityScanStatus(scan.ID, ScanStatusFailed, err.Error())
				log.Error().Err(err).Str("repo", repo.Name).Msg("full scan failed")
				continue
			}
		}
	}

	return nil
}

func (p *SecurityEngineerPlugin) runQuickScan(ctx context.Context, repoName, repoPath string, scanID int64) error {
	openFindings, err := p.libs.DB.GetOpenSecurityFindings(repoName)
	if err != nil {
		return fmt.Errorf("get open findings: %w", err)
	}

	mitigated := 0
	stillOpen := 0
	var mitigatedWithJira []*db.SecurityFinding

	// Cache file contents to avoid reading the same file multiple times
	fileCache := make(map[string][]byte)

	for _, f := range openFindings {
		if f.FilePath == "" {
			stillOpen++
			continue
		}

		content, cached := fileCache[f.FilePath]
		if !cached {
			fullPath := filepath.Join(repoPath, f.FilePath)
			var err error
			content, err = os.ReadFile(fullPath)
			if err != nil {
				// File deleted — mark all findings for this file path as mitigated
				fileCache[f.FilePath] = nil
			} else {
				fileCache[f.FilePath] = content
			}
		}

		isMitigated := false
		if content == nil {
			isMitigated = true
		} else if f.Snippet != "" && !strings.Contains(string(content), f.Snippet) {
			isMitigated = true
		}

		if isMitigated {
			p.libs.DB.MarkSecurityFindingMitigated(f.ID, scanID)
			mitigated++
			if f.JiraIssueKey != "" {
				mitigatedWithJira = append(mitigatedWithJira, f)
			}
			continue
		}

		stillOpen++
	}

	// Sync Jira for mitigated findings
	if len(mitigatedWithJira) > 0 {
		p.syncJiraForMitigated(ctx, mitigatedWithJira, ScanTypeQuick)
	}

	summaryJSON, _ := json.Marshal(map[string]int{
		"checked":   len(openFindings),
		"open":      stillOpen,
		"mitigated": mitigated,
	})
	p.libs.DB.UpdateSecurityScanStatus(scanID, ScanStatusCompleted, string(summaryJSON))

	log.Info().
		Str("repo", repoName).
		Int("checked", len(openFindings)).
		Int("open", stillOpen).
		Int("mitigated", mitigated).
		Msg("quick scan complete")

	if err := p.notifier.NotifySecurityScanComplete(ctx, repoName, 0, stillOpen, mitigated); err != nil {
		log.Warn().Err(err).Msg("failed to send security scan Slack notification")
	}

	return nil
}

func (p *SecurityEngineerPlugin) runFullScan(ctx context.Context, repoName, repoPath string, scanID int64) error {
	parallelAgents := plugin.SettingInt(p.pluginCfg.Settings, "parallel_agents", 4)

	model := p.libs.Config.PlanningModel
	if model == "" {
		model = "sonnet"
	}

	outputDir := filepath.Join(p.libs.Config.DataPath, "security-output", repoName)
	os.MkdirAll(outputDir, 0755)

	pipeline := NewPipeline(outputDir, parallelAgents, model)
	result, err := pipeline.Run(ctx, repoPath)
	if err != nil {
		return fmt.Errorf("pipeline: %w", err)
	}

	persistResult, err := PersistFindings(p.libs.DB, repoName, scanID, result.Consolidated)
	if err != nil {
		return fmt.Errorf("persist findings: %w", err)
	}

	// Sync Jira for mitigated and regressed findings
	if len(persistResult.Mitigated) > 0 {
		p.syncJiraForMitigated(ctx, persistResult.Mitigated, ScanTypeFull)
	}
	if len(persistResult.Regressed) > 0 {
		p.syncJiraForRegressed(ctx, persistResult.Regressed, ScanTypeFull)
	}

	summaryJSON, _ := json.Marshal(map[string]int{
		"new":       persistResult.NewCount,
		"open":      persistResult.OpenCount,
		"mitigated": persistResult.MitigatedCount,
		"total":     len(result.Consolidated),
	})
	p.libs.DB.UpdateSecurityScanStatus(scanID, ScanStatusCompleted, string(summaryJSON))

	log.Info().
		Str("repo", repoName).
		Int("total", len(result.Consolidated)).
		Int("new", persistResult.NewCount).
		Int("open", persistResult.OpenCount).
		Int("mitigated", persistResult.MitigatedCount).
		Msg("full scan complete")

	if err := p.notifier.NotifySecurityScanComplete(ctx, repoName, persistResult.NewCount, persistResult.OpenCount, persistResult.MitigatedCount); err != nil {
		log.Warn().Err(err).Msg("failed to send security scan Slack notification")
	}

	// Create Jira tickets for high+ findings without tickets
	jiraThreshold := plugin.SettingString(p.pluginCfg.Settings, "jira_threshold", SeverityHigh)

	if findingsForJira, jiraErr := p.libs.DB.GetSecurityFindingsWithoutJira(repoName, jiraThreshold); jiraErr != nil {
		log.Warn().Err(jiraErr).Msg("failed to get findings for Jira")
	} else if len(findingsForJira) > 0 {
		epicKey, err := p.resolveSecurityEpic(ctx, repoName)
		if err != nil {
			log.Warn().Err(err).Str("repo", repoName).Msg("failed to resolve security epic, creating tickets without epic")
		}

		for _, f := range findingsForJira {
			title := fmt.Sprintf("[Security] %s: %s", f.Severity, f.Title)
			body := fmt.Sprintf("**Severity:** %s\n**Category:** %s\n**File:** %s (lines %d-%d)\n**CWE:** %s\n**OWASP:** %s\n\n%s\n\n**Remediation:**\n%s",
				f.Severity, f.Category, f.FilePath, f.LineStart, f.LineEnd,
				f.CweID, f.OwaspCategory, f.Description, f.Remediation)

			issueKey, err := p.libs.Tracker.CreateIssue(ctx, title, body, []string{"security", strings.ToLower(f.Severity)})
			if err != nil {
				log.Warn().Err(err).Str("finding", f.Title).Msg("failed to create Jira ticket for security finding")
				continue
			}
			if epicKey != "" {
				if err := p.libs.Tracker.LinkIssueToEpic(ctx, issueKey, epicKey); err != nil {
					log.Warn().Err(err).Str("issue", issueKey).Str("epic", epicKey).Msg("failed to link issue to security epic")
				}
			}
			if err := p.libs.DB.UpdateSecurityFindingJiraKey(f.ID, issueKey); err != nil {
				log.Warn().Err(err).Str("issue", issueKey).Msg("failed to update finding with Jira key")
			}
			log.Info().Str("issue", issueKey).Str("finding", f.Title).Str("epic", epicKey).Msg("created Jira ticket for security finding")
		}
	}

	return nil
}

// resolveSecurityEpic returns the epic key for a repo's security findings.
// If one already exists in the DB it is reused; otherwise a new epic is created.
func (p *SecurityEngineerPlugin) resolveSecurityEpic(ctx context.Context, repoName string) (string, error) {
	epicKey, err := p.libs.DB.GetSecurityEpicKey(repoName)
	if err != nil {
		return "", fmt.Errorf("get security epic key: %w", err)
	}
	if epicKey != "" {
		return epicKey, nil
	}

	title := fmt.Sprintf("Security findings - %s", time.Now().UTC().Format("2006-01-02"))
	description := fmt.Sprintf("Security findings for repository %s, created by %s.", repoName, p.libs.Config.BotDisplayName)
	epicKey, err = p.libs.Tracker.CreateEpic(ctx, title, description, []string{"security"})
	if err != nil {
		return "", fmt.Errorf("create security epic: %w", err)
	}

	if err := p.libs.DB.SetSecurityEpicKey(repoName, epicKey); err != nil {
		return epicKey, fmt.Errorf("persist security epic key: %w", err)
	}

	log.Info().Str("repo", repoName).Str("epic", epicKey).Msg("created security epic")
	return epicKey, nil
}

// syncJiraForMitigated transitions resolved findings' Jira tickets to Done.
func (p *SecurityEngineerPlugin) syncJiraForMitigated(ctx context.Context, findings []*db.SecurityFinding, scanType string) {
	p.syncJiraFindings(ctx, findings, scanType, p.libs.Config.StatusDone(),
		"Security finding verified as resolved during %s scan: %q. Closing ticket.",
		"closed Jira ticket for mitigated finding")
}

// syncJiraForRegressed reopens Jira tickets for findings that have reappeared.
func (p *SecurityEngineerPlugin) syncJiraForRegressed(ctx context.Context, findings []*db.SecurityFinding, scanType string) {
	p.syncJiraFindings(ctx, findings, scanType, p.libs.Config.StatusTodo(),
		"Security regression detected during %s scan: %q has reappeared. Reopening ticket.",
		"reopened Jira ticket for regressed finding")
}

// syncJiraFindings transitions Jira tickets and adds a comment for each finding.
func (p *SecurityEngineerPlugin) syncJiraFindings(ctx context.Context, findings []*db.SecurityFinding, scanType, targetStatus, commentFmt, logMsg string) {
	for _, f := range findings {
		if err := p.libs.Tracker.TransitionIssue(ctx, f.JiraIssueKey, targetStatus); err != nil {
			log.Warn().Err(err).Str("issue", f.JiraIssueKey).Msg("failed to transition Jira issue")
			continue
		}
		comment := fmt.Sprintf(commentFmt, scanType, f.Title)
		if err := p.libs.Tracker.AddComment(ctx, f.JiraIssueKey, comment); err != nil {
			log.Warn().Err(err).Str("issue", f.JiraIssueKey).Msg("failed to add Jira comment")
		}
		log.Info().Str("issue", f.JiraIssueKey).Str("finding", f.Title).Msg(logMsg)
	}
}
