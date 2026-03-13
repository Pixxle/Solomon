package securityengineer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/robfig/cron/v3"
	"github.com/rs/zerolog/log"

	"github.com/pixxle/solomon/internal/db"
	"github.com/pixxle/solomon/internal/git"
	"github.com/pixxle/solomon/internal/plugin"
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
	cron      *cron.Cron
	mu        sync.Mutex
	cancel    context.CancelFunc
}

// NewSecurityEngineerPlugin creates a new security engineer plugin instance.
func NewSecurityEngineerPlugin(cfg plugin.PluginConfig, libs *plugin.SharedLibs) (*SecurityEngineerPlugin, error) {
	return &SecurityEngineerPlugin{
		pluginCfg: cfg,
		libs:      libs,
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

		if content == nil {
			p.libs.DB.MarkSecurityFindingMitigated(f.ID, scanID)
			mitigated++
			continue
		}

		if f.Snippet != "" && !strings.Contains(string(content), f.Snippet) {
			p.libs.DB.MarkSecurityFindingMitigated(f.ID, scanID)
			mitigated++
			continue
		}

		stillOpen++
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

	return nil
}

func (p *SecurityEngineerPlugin) runFullScan(ctx context.Context, repoName, repoPath string, scanID int64) error {
	parallelAgents := 4
	if v, ok := p.pluginCfg.Settings["parallel_agents"]; ok {
		if n, ok := v.(float64); ok {
			parallelAgents = int(n)
		}
	}

	model := p.libs.Config.PlanningModel
	if model == "" {
		model = "sonnet"
	}

	outputDir := filepath.Join(".solomon", "security-output", repoName)
	os.MkdirAll(outputDir, 0755)

	pipeline := NewPipeline(outputDir, parallelAgents, model)
	result, err := pipeline.Run(ctx, repoPath)
	if err != nil {
		return fmt.Errorf("pipeline: %w", err)
	}

	newCount, openCount, mitigatedCount, err := PersistFindings(p.libs.DB, repoName, scanID, result.Consolidated)
	if err != nil {
		return fmt.Errorf("persist findings: %w", err)
	}

	summaryJSON, _ := json.Marshal(map[string]int{
		"new":       newCount,
		"open":      openCount,
		"mitigated": mitigatedCount,
		"total":     len(result.Consolidated),
	})
	p.libs.DB.UpdateSecurityScanStatus(scanID, ScanStatusCompleted, string(summaryJSON))

	log.Info().
		Str("repo", repoName).
		Int("total", len(result.Consolidated)).
		Int("new", newCount).
		Int("open", openCount).
		Int("mitigated", mitigatedCount).
		Msg("full scan complete")

	if newCount > 0 || mitigatedCount > 0 {
		if err := p.libs.Notifier.NotifySecurityScanComplete(ctx, repoName, newCount, openCount, mitigatedCount); err != nil {
			log.Warn().Err(err).Msg("failed to send security scan Slack notification")
		}
	}

	// Create Jira tickets for high+ findings without tickets
	jiraThreshold := SeverityHigh
	if v, ok := p.pluginCfg.Settings["jira_threshold"]; ok {
		if s, ok := v.(string); ok {
			jiraThreshold = s
		}
	}

	findingsForJira, err := p.libs.DB.GetSecurityFindingsWithoutJira(repoName, jiraThreshold)
	if err != nil {
		log.Warn().Err(err).Msg("failed to get findings for Jira")
	} else if len(findingsForJira) > 0 {
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
			if err := p.libs.DB.UpdateSecurityFindingJiraKey(f.ID, issueKey); err != nil {
				log.Warn().Err(err).Str("issue", issueKey).Msg("failed to update finding with Jira key")
			}
			log.Info().Str("issue", issueKey).Str("finding", f.Title).Msg("created Jira ticket for security finding")
		}
	}

	return nil
}

