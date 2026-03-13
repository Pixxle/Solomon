package securityengineer

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/rs/zerolog/log"

	"github.com/pixxle/solomon/internal/db"
)

// PersistFindings stores pipeline findings in the database, diffing against existing.
func PersistFindings(stateDB *db.StateDB, repoName string, scanID int64, findings []*RawFinding) (newCount, openCount, mitigatedCount int, err error) {
	existing, err := stateDB.GetOpenSecurityFindings(repoName)
	if err != nil {
		return 0, 0, 0, err
	}

	existingFPs := make(map[string]*db.SecurityFinding)
	for _, f := range existing {
		existingFPs[f.Fingerprint] = f
	}

	seenFPs := make(map[string]bool)

	for _, raw := range findings {
		seenFPs[raw.Fingerprint] = true

		if raw.Priority == "" {
			raw.Priority = Priority(raw.Severity, raw.Confidence)
		}

		f := &db.SecurityFinding{
			RepoName:          repoName,
			ScanID:            scanID,
			Agent:             raw.Agent,
			FindingID:         raw.FindingID,
			Title:             raw.Title,
			Description:       raw.Description,
			Severity:          raw.Severity,
			Confidence:        raw.Confidence,
			Priority:          raw.Priority,
			Category:          raw.Category,
			CweID:             raw.CweID,
			OwaspCategory:     raw.OwaspCategory,
			FilePath:          raw.FilePath,
			LineStart:         raw.LineStart,
			LineEnd:           raw.LineEnd,
			Snippet:           raw.Snippet,
			Evidence:          raw.Evidence,
			Source:            raw.Source,
			SourceTool:        raw.SourceTool,
			Remediation:       raw.Remediation,
			RemediationEffort: raw.RemediationEffort,
			CodeSuggestion:    raw.CodeSuggestion,
			FalsePositiveRisk: raw.FalsePositiveRisk,
			Status:            StatusOpen,
			Fingerprint:       raw.Fingerprint,
			FirstSeenScanID:   scanID,
			LastSeenScanID:    scanID,
		}

		if err := stateDB.UpsertSecurityFinding(f); err != nil {
			return 0, 0, 0, fmt.Errorf("upsert finding: %w", err)
		}

		if _, existed := existingFPs[raw.Fingerprint]; !existed {
			newCount++
		}
	}

	for fp, ef := range existingFPs {
		if !seenFPs[fp] {
			if err := stateDB.MarkSecurityFindingMitigated(ef.ID, scanID); err != nil {
				log.Warn().Err(err).Int64("finding_id", ef.ID).Msg("failed to mark finding mitigated")
			}
			mitigatedCount++
		}
	}

	openCount = len(findings) - newCount + (len(existing) - mitigatedCount)
	if openCount < 0 {
		openCount = 0
	}

	return newCount, openCount + newCount, mitigatedCount, nil
}

// getCommitHash returns the HEAD commit hash for a repo path.
func getCommitHash(path string) string {
	cmd := exec.Command("git", "-C", path, "rev-parse", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}
