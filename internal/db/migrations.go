package db

const schemaVersion = 2

var migrations = [][]string{
	// v1: Initial schema - each statement separate for SQLite compatibility
	{
		`CREATE TABLE IF NOT EXISTS planning_state (
			issue_key TEXT PRIMARY KEY,
			conversation_json TEXT NOT NULL DEFAULT '[]',
			participants_json TEXT NOT NULL DEFAULT '[]',
			status TEXT NOT NULL DEFAULT 'active',
			original_description TEXT NOT NULL DEFAULT '',
			figma_urls_json TEXT DEFAULT '[]',
			image_refs_json TEXT DEFAULT '[]',
			last_human_response_at TEXT,
			last_system_comment_at TEXT,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS pr_feedback_state (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			issue_key TEXT NOT NULL,
			pr_number INTEGER NOT NULL,
			comment_id TEXT NOT NULL UNIQUE,
			comment_type TEXT NOT NULL,
			action_taken TEXT NOT NULL,
			commit_sha TEXT,
			processed_at TEXT NOT NULL,
			created_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_pr_feedback_pr ON pr_feedback_state(pr_number)`,
		`CREATE INDEX IF NOT EXISTS idx_pr_feedback_issue ON pr_feedback_state(issue_key)`,
		`CREATE TABLE IF NOT EXISTS feedback_cutoffs (
			issue_key TEXT PRIMARY KEY,
			cutoff_utc TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS processed_shas (
			sha TEXT PRIMARY KEY,
			processed_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS attempt_records (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			issue_key TEXT NOT NULL,
			attempted_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_attempt_issue ON attempt_records(issue_key)`,
		`CREATE TABLE IF NOT EXISTS schema_version (
			version INTEGER NOT NULL
		)`,
		`INSERT INTO schema_version (version) VALUES (1)`,
	},
	// v2: Description-centric planning flow
	{
		`ALTER TABLE planning_state ADD COLUMN bot_comment_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE planning_state ADD COLUMN last_seen_description TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE planning_state ADD COLUMN questions_json TEXT NOT NULL DEFAULT '[]'`,
	},
}
