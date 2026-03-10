package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
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
