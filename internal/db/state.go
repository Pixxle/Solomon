package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type StateDB struct {
	db *sql.DB
}

// EmptyJSONArray is the default value for JSON array columns with no entries.
const EmptyJSONArray = "[]"

type PlanningState struct {
	IssueKey            string
	ConversationJSON    string
	ParticipantsJSON    string
	Status              string
	OriginalDescription string
	FigmaURLsJSON       string
	ImageRefsJSON       string
	LastHumanResponseAt *time.Time
	LastSystemCommentAt *time.Time
	CreatedAt           time.Time
	UpdatedAt           time.Time
	BotCommentID        string
	LastSeenDescription string
	QuestionsJSON       string
	PlanningPhase       string
	ProductSummary      string
}

type PRFeedbackRecord struct {
	ID          int64
	IssueKey    string
	PRNumber    int
	CommentID   string
	CommentType string
	ActionTaken string
	CommitSHA   *string
	ProcessedAt time.Time
	CreatedAt   time.Time
}

func Open(dbPath string) (*StateDB, error) {
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("creating state db directory: %w", err)
	}

	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_synchronous=NORMAL")
	if err != nil {
		return nil, fmt.Errorf("opening state db: %w", err)
	}

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("connecting to state db: %w", err)
	}

	s := &StateDB{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("running migrations: %w", err)
	}

	return s, nil
}

func (s *StateDB) Close() error {
	return s.db.Close()
}

func (s *StateDB) migrate() error {
	var version int
	err := s.db.QueryRow("SELECT version FROM schema_version LIMIT 1").Scan(&version)
	if err != nil {
		// Table doesn't exist yet, run all migrations
		for _, stmts := range migrations {
			for _, stmt := range stmts {
				if _, err := s.db.Exec(stmt); err != nil {
					return fmt.Errorf("running migration: %w", err)
				}
			}
		}
		// Ensure version reflects the final schema (v1 INSERT sets 1,
		// but we may have run further migrations beyond v1)
		if schemaVersion > 1 {
			if _, err := s.db.Exec("UPDATE schema_version SET version = ?", schemaVersion); err != nil {
				return fmt.Errorf("setting schema version: %w", err)
			}
		}
		return nil
	}

	// Run any migrations newer than current version
	for i := version; i < len(migrations); i++ {
		for _, stmt := range migrations[i] {
			if _, err := s.db.Exec(stmt); err != nil {
				return fmt.Errorf("running migration %d: %w", i+1, err)
			}
		}
		if _, err := s.db.Exec("UPDATE schema_version SET version = ?", i+1); err != nil {
			return err
		}
	}
	return nil
}

// Planning State operations

func (s *StateDB) GetPlanningState(issueKey string) (*PlanningState, error) {
	row := s.db.QueryRow(`SELECT issue_key, conversation_json, participants_json, status,
		original_description, figma_urls_json, image_refs_json,
		last_human_response_at, last_system_comment_at, created_at, updated_at,
		bot_comment_id, last_seen_description, questions_json, planning_phase, product_summary
		FROM planning_state WHERE issue_key = ?`, issueKey)

	ps := &PlanningState{}
	var lastHuman, lastSystem, created, updated sql.NullString
	err := row.Scan(&ps.IssueKey, &ps.ConversationJSON, &ps.ParticipantsJSON, &ps.Status,
		&ps.OriginalDescription, &ps.FigmaURLsJSON, &ps.ImageRefsJSON,
		&lastHuman, &lastSystem, &created, &updated,
		&ps.BotCommentID, &ps.LastSeenDescription, &ps.QuestionsJSON, &ps.PlanningPhase,
		&ps.ProductSummary)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	ps.CreatedAt = parseTime(created.String)
	ps.UpdatedAt = parseTime(updated.String)
	if lastHuman.Valid {
		t := parseTime(lastHuman.String)
		ps.LastHumanResponseAt = &t
	}
	if lastSystem.Valid {
		t := parseTime(lastSystem.String)
		ps.LastSystemCommentAt = &t
	}

	return ps, nil
}

func (s *StateDB) InsertPlanningState(ps *PlanningState) error {
	now := timeStr(time.Now().UTC())
	_, err := s.db.Exec(`INSERT INTO planning_state
		(issue_key, conversation_json, participants_json, status, original_description,
		figma_urls_json, image_refs_json, last_human_response_at, last_system_comment_at,
		created_at, updated_at, bot_comment_id, last_seen_description, questions_json,
		planning_phase, product_summary)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ps.IssueKey, ps.ConversationJSON, ps.ParticipantsJSON, ps.Status,
		ps.OriginalDescription, ps.FigmaURLsJSON, ps.ImageRefsJSON,
		nullTimeStr(ps.LastHumanResponseAt), nullTimeStr(ps.LastSystemCommentAt),
		now, now, ps.BotCommentID, ps.LastSeenDescription, ps.QuestionsJSON,
		ps.PlanningPhase, ps.ProductSummary)
	return err
}

func (s *StateDB) UpdatePlanningState(ps *PlanningState) error {
	now := timeStr(time.Now().UTC())
	_, err := s.db.Exec(`UPDATE planning_state SET
		conversation_json = ?, participants_json = ?, status = ?,
		figma_urls_json = ?, image_refs_json = ?,
		last_human_response_at = ?, last_system_comment_at = ?,
		updated_at = ?,
		bot_comment_id = ?, last_seen_description = ?, questions_json = ?,
		planning_phase = ?, product_summary = ?
		WHERE issue_key = ?`,
		ps.ConversationJSON, ps.ParticipantsJSON, ps.Status,
		ps.FigmaURLsJSON, ps.ImageRefsJSON,
		nullTimeStr(ps.LastHumanResponseAt), nullTimeStr(ps.LastSystemCommentAt),
		now,
		ps.BotCommentID, ps.LastSeenDescription, ps.QuestionsJSON,
		ps.PlanningPhase, ps.ProductSummary,
		ps.IssueKey)
	return err
}

func (s *StateDB) GetActivePlanningStates() ([]*PlanningState, error) {
	return s.queryPlanningStates("WHERE status = 'active'")
}

func (s *StateDB) DeletePlanningState(issueKey string) error {
	_, err := s.db.Exec("DELETE FROM planning_state WHERE issue_key = ?", issueKey)
	return err
}

func (s *StateDB) GetAllPlanningStates() ([]*PlanningState, error) {
	return s.queryPlanningStates("")
}

func (s *StateDB) queryPlanningStates(whereClause string) ([]*PlanningState, error) {
	query := `SELECT issue_key, conversation_json, participants_json, status,
		original_description, figma_urls_json, image_refs_json,
		last_human_response_at, last_system_comment_at, created_at, updated_at,
		bot_comment_id, last_seen_description, questions_json, planning_phase, product_summary
		FROM planning_state ` + whereClause
	rows, err := s.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []*PlanningState
	for rows.Next() {
		ps := &PlanningState{}
		var lastHuman, lastSystem, created, updated sql.NullString
		if err := rows.Scan(&ps.IssueKey, &ps.ConversationJSON, &ps.ParticipantsJSON, &ps.Status,
			&ps.OriginalDescription, &ps.FigmaURLsJSON, &ps.ImageRefsJSON,
			&lastHuman, &lastSystem, &created, &updated,
			&ps.BotCommentID, &ps.LastSeenDescription, &ps.QuestionsJSON, &ps.PlanningPhase,
			&ps.ProductSummary); err != nil {
			return nil, err
		}
		ps.CreatedAt = parseTime(created.String)
		ps.UpdatedAt = parseTime(updated.String)
		if lastHuman.Valid {
			t := parseTime(lastHuman.String)
			ps.LastHumanResponseAt = &t
		}
		if lastSystem.Valid {
			t := parseTime(lastSystem.String)
			ps.LastSystemCommentAt = &t
		}
		result = append(result, ps)
	}
	return result, rows.Err()
}

// PR Feedback operations

func (s *StateDB) IsCommentProcessed(commentID string) (bool, error) {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM pr_feedback_state WHERE comment_id = ?", commentID).Scan(&count)
	return count > 0, err
}

func (s *StateDB) InsertPRFeedback(rec *PRFeedbackRecord) error {
	now := timeStr(time.Now().UTC())
	_, err := s.db.Exec(`INSERT OR IGNORE INTO pr_feedback_state
		(issue_key, pr_number, comment_id, comment_type, action_taken, commit_sha, processed_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.IssueKey, rec.PRNumber, rec.CommentID, rec.CommentType,
		rec.ActionTaken, rec.CommitSHA, now, rec.CreatedAt.Format(time.RFC3339))
	return err
}

// SHA tracking

func (s *StateDB) IsSHAProcessed(sha string) (bool, error) {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM processed_shas WHERE sha = ?", sha).Scan(&count)
	return count > 0, err
}

func (s *StateDB) MarkSHAProcessed(sha string) error {
	_, err := s.db.Exec("INSERT OR IGNORE INTO processed_shas (sha, processed_at) VALUES (?, ?)",
		sha, timeStr(time.Now().UTC()))
	return err
}

// Feedback cutoffs

func (s *StateDB) GetFeedbackCutoff(issueKey string) (time.Time, error) {
	var cutoff string
	err := s.db.QueryRow("SELECT cutoff_utc FROM feedback_cutoffs WHERE issue_key = ?", issueKey).Scan(&cutoff)
	if err == sql.ErrNoRows {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	return parseTime(cutoff), nil
}

func (s *StateDB) SetFeedbackCutoff(issueKey string, cutoff time.Time) error {
	_, err := s.db.Exec(`INSERT INTO feedback_cutoffs (issue_key, cutoff_utc) VALUES (?, ?)
		ON CONFLICT(issue_key) DO UPDATE SET cutoff_utc = ?`,
		issueKey, timeStr(cutoff), timeStr(cutoff))
	return err
}

// Attempt tracking

func (s *StateDB) RecordAttempt(issueKey string) error {
	_, err := s.db.Exec("INSERT INTO attempt_records (issue_key, attempted_at) VALUES (?, ?)",
		issueKey, timeStr(time.Now().UTC()))
	return err
}

func (s *StateDB) CountRecentAttempts(issueKey string, window time.Duration) (int, error) {
	cutoff := timeStr(time.Now().UTC().Add(-window))
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM attempt_records WHERE issue_key = ? AND attempted_at > ?",
		issueKey, cutoff).Scan(&count)
	return count, err
}

func (s *StateDB) PruneOldRecords(olderThan time.Duration) error {
	cutoff := timeStr(time.Now().UTC().Add(-olderThan))
	_, err := s.db.Exec("DELETE FROM attempt_records WHERE attempted_at < ?", cutoff)
	if err != nil {
		return err
	}
	_, err = s.db.Exec("DELETE FROM processed_shas WHERE processed_at < ?", cutoff)
	return err
}

// Slack thread operations

func (s *StateDB) GetSlackThread(issueKey string) (string, error) {
	var threadTS string
	err := s.db.QueryRow("SELECT thread_ts FROM slack_threads WHERE issue_key = ?", issueKey).Scan(&threadTS)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return threadTS, err
}

func (s *StateDB) UpsertSlackThread(issueKey, threadTS string) error {
	_, err := s.db.Exec(`INSERT INTO slack_threads (issue_key, thread_ts, created_at) VALUES (?, ?, ?)
		ON CONFLICT(issue_key) DO UPDATE SET thread_ts = ?`,
		issueKey, threadTS, timeStr(time.Now().UTC()), threadTS)
	return err
}

func timeStr(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

func nullTimeStr(t *time.Time) interface{} {
	if t == nil {
		return nil
	}
	return timeStr(*t)
}

// Security scan and finding types

type SecurityScan struct {
	ID            int64
	RepoName      string
	ScanType      string
	Status        string
	CommitHash    string
	FindingsCount int
	Summary       string
	StartedAt     *time.Time
	CompletedAt   *time.Time
	CreatedAt     time.Time
}

type SecurityFinding struct {
	ID                int64
	RepoName          string
	ScanID            int64
	Agent             string
	FindingID         string
	Title             string
	Description       string
	Severity          string
	Confidence        string
	Priority          string
	Category          string
	CweID             string
	OwaspCategory     string
	FilePath          string
	LineStart         int
	LineEnd           int
	Snippet           string
	Evidence          string
	Source            string
	SourceTool        string
	Remediation       string
	RemediationEffort string
	CodeSuggestion    string
	FalsePositiveRisk string
	Status            string
	Fingerprint       string
	FirstSeenScanID   int64
	LastSeenScanID    int64
	JiraIssueKey      string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// Security scan operations

// CreateSecurityScan inserts a new scan record and sets the ID on the passed struct.
func (s *StateDB) CreateSecurityScan(scan *SecurityScan) error {
	now := timeStr(time.Now().UTC())
	res, err := s.db.Exec(`INSERT INTO security_scans
		(repo_name, scan_type, status, commit_hash, findings_count, summary, started_at, completed_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		scan.RepoName, scan.ScanType, scan.Status, scan.CommitHash,
		scan.FindingsCount, scan.Summary,
		nullTimeStr(scan.StartedAt), nullTimeStr(scan.CompletedAt), now)
	if err != nil {
		return err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	scan.ID = id
	scan.CreatedAt = parseTime(now)
	return nil
}

// UpdateSecurityScanStatus updates scan status, summary, and completed_at timestamp.
func (s *StateDB) UpdateSecurityScanStatus(id int64, status string, summary string) error {
	now := timeStr(time.Now().UTC())
	_, err := s.db.Exec(`UPDATE security_scans SET status = ?, summary = ?, completed_at = ? WHERE id = ?`,
		status, summary, now, id)
	return err
}

// UpsertSecurityFinding creates or updates a finding by repo_name+fingerprint.
func (s *StateDB) UpsertSecurityFinding(f *SecurityFinding) error {
	now := timeStr(time.Now().UTC())
	res, err := s.db.Exec(`INSERT INTO security_findings
		(repo_name, scan_id, agent, finding_id, title, description, severity, confidence,
		priority, category, cwe_id, owasp_category, file_path, line_start, line_end,
		snippet, evidence, source, source_tool, remediation, remediation_effort,
		code_suggestion, false_positive_risk, status, fingerprint,
		first_seen_scan_id, last_seen_scan_id, jira_issue_key, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(repo_name, fingerprint) DO UPDATE SET
			scan_id = excluded.scan_id,
			agent = excluded.agent,
			finding_id = excluded.finding_id,
			title = excluded.title,
			description = excluded.description,
			severity = excluded.severity,
			confidence = excluded.confidence,
			priority = excluded.priority,
			category = excluded.category,
			cwe_id = excluded.cwe_id,
			owasp_category = excluded.owasp_category,
			file_path = excluded.file_path,
			line_start = excluded.line_start,
			line_end = excluded.line_end,
			snippet = excluded.snippet,
			evidence = excluded.evidence,
			source = excluded.source,
			source_tool = excluded.source_tool,
			remediation = excluded.remediation,
			remediation_effort = excluded.remediation_effort,
			code_suggestion = excluded.code_suggestion,
			false_positive_risk = excluded.false_positive_risk,
			status = excluded.status,
			last_seen_scan_id = excluded.last_seen_scan_id,
			updated_at = excluded.updated_at`,
		f.RepoName, f.ScanID, f.Agent, f.FindingID, f.Title, f.Description,
		f.Severity, f.Confidence, f.Priority, f.Category, f.CweID, f.OwaspCategory,
		f.FilePath, f.LineStart, f.LineEnd, f.Snippet, f.Evidence, f.Source,
		f.SourceTool, f.Remediation, f.RemediationEffort, f.CodeSuggestion,
		f.FalsePositiveRisk, f.Status, f.Fingerprint,
		f.FirstSeenScanID, f.LastSeenScanID, f.JiraIssueKey, now, now)
	if err != nil {
		return err
	}
	if f.ID == 0 {
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		f.ID = id
	}
	return nil
}

// GetOpenSecurityFindings returns all open findings for a repo.
func (s *StateDB) GetOpenSecurityFindings(repoName string) ([]*SecurityFinding, error) {
	rows, err := s.db.Query(`SELECT id, repo_name, scan_id, agent, finding_id, title, description,
		severity, confidence, priority, category, cwe_id, owasp_category,
		file_path, line_start, line_end, snippet, evidence, source, source_tool,
		remediation, remediation_effort, code_suggestion, false_positive_risk,
		status, fingerprint, first_seen_scan_id, last_seen_scan_id, jira_issue_key,
		created_at, updated_at
		FROM security_findings WHERE repo_name = ? AND status = 'open'`, repoName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSecurityFindings(rows)
}

// MarkSecurityFindingMitigated marks a finding as mitigated.
func (s *StateDB) MarkSecurityFindingMitigated(id int64, scanID int64) error {
	now := timeStr(time.Now().UTC())
	_, err := s.db.Exec(`UPDATE security_findings SET status = 'mitigated', last_seen_scan_id = ?, updated_at = ? WHERE id = ?`,
		scanID, now, id)
	return err
}

// UpdateSecurityFindingJiraKey sets the Jira issue key on a finding.
func (s *StateDB) UpdateSecurityFindingJiraKey(id int64, jiraKey string) error {
	now := timeStr(time.Now().UTC())
	_, err := s.db.Exec(`UPDATE security_findings SET jira_issue_key = ?, updated_at = ? WHERE id = ?`,
		jiraKey, now, id)
	return err
}

// GetSecurityFindingsWithoutJira returns open findings above a severity threshold that lack Jira tickets.
// minSeverity should be one of: critical, high, medium, low, info.
func (s *StateDB) GetSecurityFindingsWithoutJira(repoName string, minSeverity string) ([]*SecurityFinding, error) {
	severityOrder := map[string]int{
		"critical": 5,
		"high":     4,
		"medium":   3,
		"low":      2,
		"info":     1,
	}
	minLevel := severityOrder[strings.ToLower(minSeverity)]

	var allowed []string
	for sev, level := range severityOrder {
		if level >= minLevel {
			allowed = append(allowed, sev)
		}
	}
	if len(allowed) == 0 {
		return nil, nil
	}

	// Build placeholders
	query := `SELECT id, repo_name, scan_id, agent, finding_id, title, description,
		severity, confidence, priority, category, cwe_id, owasp_category,
		file_path, line_start, line_end, snippet, evidence, source, source_tool,
		remediation, remediation_effort, code_suggestion, false_positive_risk,
		status, fingerprint, first_seen_scan_id, last_seen_scan_id, jira_issue_key,
		created_at, updated_at
		FROM security_findings
		WHERE repo_name = ? AND status = 'open' AND (jira_issue_key IS NULL OR jira_issue_key = '') AND severity IN (`
	args := []interface{}{repoName}
	for i, sev := range allowed {
		if i > 0 {
			query += ", "
		}
		query += "?"
		args = append(args, sev)
	}
	query += ")"

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSecurityFindings(rows)
}

func scanSecurityFindings(rows *sql.Rows) ([]*SecurityFinding, error) {
	var result []*SecurityFinding
	for rows.Next() {
		f := &SecurityFinding{}
		var findingID, description, confidence, priority, category sql.NullString
		var cweID, owaspCategory, filePath, snippet, evidence sql.NullString
		var source, sourceTool, remediation, remediationEffort sql.NullString
		var codeSuggestion, falsePositiveRisk, jiraIssueKey sql.NullString
		var lineStart, lineEnd sql.NullInt64
		var firstSeenScanID, lastSeenScanID sql.NullInt64
		var createdAt, updatedAt sql.NullString

		if err := rows.Scan(&f.ID, &f.RepoName, &f.ScanID, &f.Agent, &findingID,
			&f.Title, &description, &f.Severity, &confidence, &priority,
			&category, &cweID, &owaspCategory, &filePath, &lineStart, &lineEnd,
			&snippet, &evidence, &source, &sourceTool, &remediation,
			&remediationEffort, &codeSuggestion, &falsePositiveRisk,
			&f.Status, &f.Fingerprint, &firstSeenScanID, &lastSeenScanID,
			&jiraIssueKey, &createdAt, &updatedAt); err != nil {
			return nil, err
		}

		f.FindingID = findingID.String
		f.Description = description.String
		f.Confidence = confidence.String
		f.Priority = priority.String
		f.Category = category.String
		f.CweID = cweID.String
		f.OwaspCategory = owaspCategory.String
		f.FilePath = filePath.String
		f.LineStart = int(lineStart.Int64)
		f.LineEnd = int(lineEnd.Int64)
		f.Snippet = snippet.String
		f.Evidence = evidence.String
		f.Source = source.String
		f.SourceTool = sourceTool.String
		f.Remediation = remediation.String
		f.RemediationEffort = remediationEffort.String
		f.CodeSuggestion = codeSuggestion.String
		f.FalsePositiveRisk = falsePositiveRisk.String
		f.FirstSeenScanID = firstSeenScanID.Int64
		f.LastSeenScanID = lastSeenScanID.Int64
		f.JiraIssueKey = jiraIssueKey.String
		if createdAt.Valid {
			f.CreatedAt = parseTime(createdAt.String)
		}
		if updatedAt.Valid {
			f.UpdatedAt = parseTime(updatedAt.String)
		}

		result = append(result, f)
	}
	return result, rows.Err()
}
