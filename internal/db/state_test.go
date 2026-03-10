package db

import (
	"path/filepath"
	"testing"
	"time"
)

func openTestDB(t *testing.T) *StateDB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open(%q) error: %v", dbPath, err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestOpenAndMigrate(t *testing.T) {
	db := openTestDB(t)
	if db == nil {
		t.Fatal("expected non-nil StateDB")
	}
}

func TestOpenIdempotent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")

	db1, err := Open(dbPath)
	if err != nil {
		t.Fatalf("first Open() error: %v", err)
	}
	db1.Close()

	db2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("second Open() error: %v", err)
	}
	db2.Close()
}

func TestPlanningStateCRUD(t *testing.T) {
	db := openTestDB(t)
	now := time.Now().UTC()

	ps := &PlanningState{
		IssueKey:            "TEST-1",
		ConversationJSON:    "[]",
		ParticipantsJSON:    "[]",
		Status:              "active",
		OriginalDescription: "Original desc",
		FigmaURLsJSON:       "[]",
		ImageRefsJSON:       "[]",
		LastSystemCommentAt: &now,
		CreatedAt:           now,
		UpdatedAt:           now,
		BotCommentID:        "comment-123",
		LastSeenDescription: "Original desc",
		QuestionsJSON:       `["Q1?"]`,
		PlanningPhase:       "product",
	}

	// Insert
	if err := db.InsertPlanningState(ps); err != nil {
		t.Fatalf("InsertPlanningState() error: %v", err)
	}

	// Get
	got, err := db.GetPlanningState("TEST-1")
	if err != nil {
		t.Fatalf("GetPlanningState() error: %v", err)
	}
	if got == nil {
		t.Fatal("GetPlanningState() returned nil")
	}
	if got.IssueKey != "TEST-1" {
		t.Errorf("IssueKey = %q, want %q", got.IssueKey, "TEST-1")
	}
	if got.Status != "active" {
		t.Errorf("Status = %q, want %q", got.Status, "active")
	}
	if got.BotCommentID != "comment-123" {
		t.Errorf("BotCommentID = %q, want %q", got.BotCommentID, "comment-123")
	}
	if got.QuestionsJSON != `["Q1?"]` {
		t.Errorf("QuestionsJSON = %q, want %q", got.QuestionsJSON, `["Q1?"]`)
	}

	// Verify default values for new fields
	if got.PlanningPhase != "product" {
		t.Errorf("PlanningPhase = %q, want %q", got.PlanningPhase, "product")
	}
	if got.ProductSummary != "" {
		t.Errorf("ProductSummary = %q, want empty", got.ProductSummary)
	}

	// Update including new fields
	ps.Status = "complete"
	ps.QuestionsJSON = "[]"
	ps.PlanningPhase = "technical"
	ps.ProductSummary = "Product requirements are settled."
	if err := db.UpdatePlanningState(ps); err != nil {
		t.Fatalf("UpdatePlanningState() error: %v", err)
	}

	got, err = db.GetPlanningState("TEST-1")
	if err != nil {
		t.Fatalf("GetPlanningState() after update error: %v", err)
	}
	if got.Status != "complete" {
		t.Errorf("Status after update = %q, want %q", got.Status, "complete")
	}
	if got.PlanningPhase != "technical" {
		t.Errorf("PlanningPhase after update = %q, want %q", got.PlanningPhase, "technical")
	}
	if got.ProductSummary != "Product requirements are settled." {
		t.Errorf("ProductSummary after update = %q, want %q", got.ProductSummary, "Product requirements are settled.")
	}

	// Get non-existent
	missing, err := db.GetPlanningState("NOPE-1")
	if err != nil {
		t.Fatalf("GetPlanningState(missing) error: %v", err)
	}
	if missing != nil {
		t.Error("expected nil for missing issue key")
	}
}

func TestGetActivePlanningStates(t *testing.T) {
	db := openTestDB(t)
	now := time.Now().UTC()

	for _, key := range []string{"A-1", "A-2", "A-3"} {
		status := "active"
		if key == "A-3" {
			status = "complete"
		}
		if err := db.InsertPlanningState(&PlanningState{
			IssueKey:            key,
			ConversationJSON:    "[]",
			ParticipantsJSON:    "[]",
			Status:              status,
			OriginalDescription: "",
			FigmaURLsJSON:       "[]",
			ImageRefsJSON:       "[]",
			CreatedAt:           now,
			UpdatedAt:           now,
			LastSeenDescription: "",
			QuestionsJSON:       "[]",
		}); err != nil {
			t.Fatalf("InsertPlanningState(%s) error: %v", key, err)
		}
	}

	active, err := db.GetActivePlanningStates()
	if err != nil {
		t.Fatalf("GetActivePlanningStates() error: %v", err)
	}
	if len(active) != 2 {
		t.Errorf("GetActivePlanningStates() returned %d, want 2", len(active))
	}
}

func TestGetAllPlanningStates(t *testing.T) {
	db := openTestDB(t)
	now := time.Now().UTC()

	statuses := map[string]string{"ALL-1": "active", "ALL-2": "complete", "ALL-3": "timed_out"}
	for key, status := range statuses {
		if err := db.InsertPlanningState(&PlanningState{
			IssueKey:            key,
			ConversationJSON:    "[]",
			ParticipantsJSON:    "[]",
			Status:              status,
			OriginalDescription: "",
			FigmaURLsJSON:       "[]",
			ImageRefsJSON:       "[]",
			CreatedAt:           now,
			UpdatedAt:           now,
			LastSeenDescription: "",
			QuestionsJSON:       "[]",
		}); err != nil {
			t.Fatalf("InsertPlanningState(%s) error: %v", key, err)
		}
	}

	all, err := db.GetAllPlanningStates()
	if err != nil {
		t.Fatalf("GetAllPlanningStates() error: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("GetAllPlanningStates() returned %d, want 3", len(all))
	}

	// Verify each status is present
	found := make(map[string]string)
	for _, ps := range all {
		found[ps.IssueKey] = ps.Status
	}
	for key, want := range statuses {
		if got, ok := found[key]; !ok {
			t.Errorf("missing issue %s", key)
		} else if got != want {
			t.Errorf("issue %s status = %q, want %q", key, got, want)
		}
	}
}

func TestDeletePlanningState(t *testing.T) {
	db := openTestDB(t)
	now := time.Now().UTC()

	if err := db.InsertPlanningState(&PlanningState{
		IssueKey:            "DEL-1",
		ConversationJSON:    "[]",
		ParticipantsJSON:    "[]",
		Status:              "active",
		OriginalDescription: "",
		FigmaURLsJSON:       "[]",
		ImageRefsJSON:       "[]",
		CreatedAt:           now,
		UpdatedAt:           now,
		LastSeenDescription: "",
		QuestionsJSON:       "[]",
	}); err != nil {
		t.Fatalf("InsertPlanningState() error: %v", err)
	}

	if err := db.DeletePlanningState("DEL-1"); err != nil {
		t.Fatalf("DeletePlanningState() error: %v", err)
	}

	got, err := db.GetPlanningState("DEL-1")
	if err != nil {
		t.Fatalf("GetPlanningState() after delete error: %v", err)
	}
	if got != nil {
		t.Error("expected nil after delete")
	}

	// Deleting non-existent key is a no-op
	if err := db.DeletePlanningState("NOPE-99"); err != nil {
		t.Fatalf("DeletePlanningState(non-existent) error: %v", err)
	}
}

func TestSHAProcessed(t *testing.T) {
	db := openTestDB(t)

	processed, err := db.IsSHAProcessed("abc123")
	if err != nil {
		t.Fatalf("IsSHAProcessed() error: %v", err)
	}
	if processed {
		t.Error("expected SHA to not be processed initially")
	}

	if err := db.MarkSHAProcessed("abc123"); err != nil {
		t.Fatalf("MarkSHAProcessed() error: %v", err)
	}

	processed, err = db.IsSHAProcessed("abc123")
	if err != nil {
		t.Fatalf("IsSHAProcessed() after mark error: %v", err)
	}
	if !processed {
		t.Error("expected SHA to be processed after marking")
	}

	// Idempotent
	if err := db.MarkSHAProcessed("abc123"); err != nil {
		t.Fatalf("MarkSHAProcessed() duplicate error: %v", err)
	}
}

func TestAttemptTracking(t *testing.T) {
	db := openTestDB(t)

	count, err := db.CountRecentAttempts("TEST-1", time.Hour)
	if err != nil {
		t.Fatalf("CountRecentAttempts() error: %v", err)
	}
	if count != 0 {
		t.Errorf("initial count = %d, want 0", count)
	}

	if err := db.RecordAttempt("TEST-1"); err != nil {
		t.Fatalf("RecordAttempt() error: %v", err)
	}
	if err := db.RecordAttempt("TEST-1"); err != nil {
		t.Fatalf("RecordAttempt() second error: %v", err)
	}

	count, err = db.CountRecentAttempts("TEST-1", time.Hour)
	if err != nil {
		t.Fatalf("CountRecentAttempts() after records error: %v", err)
	}
	if count != 2 {
		t.Errorf("count after 2 attempts = %d, want 2", count)
	}

	// Different issue key
	count, err = db.CountRecentAttempts("OTHER-1", time.Hour)
	if err != nil {
		t.Fatalf("CountRecentAttempts(OTHER) error: %v", err)
	}
	if count != 0 {
		t.Errorf("count for other issue = %d, want 0", count)
	}
}

func TestPRFeedback(t *testing.T) {
	db := openTestDB(t)

	processed, err := db.IsCommentProcessed("comment-1")
	if err != nil {
		t.Fatalf("IsCommentProcessed() error: %v", err)
	}
	if processed {
		t.Error("expected comment to not be processed initially")
	}

	rec := &PRFeedbackRecord{
		IssueKey:    "TEST-1",
		PRNumber:    42,
		CommentID:   "comment-1",
		CommentType: "review",
		ActionTaken: "addressed",
		CreatedAt:   time.Now().UTC(),
	}
	if err := db.InsertPRFeedback(rec); err != nil {
		t.Fatalf("InsertPRFeedback() error: %v", err)
	}

	processed, err = db.IsCommentProcessed("comment-1")
	if err != nil {
		t.Fatalf("IsCommentProcessed() after insert error: %v", err)
	}
	if !processed {
		t.Error("expected comment to be processed after insert")
	}
}

func TestFeedbackCutoff(t *testing.T) {
	db := openTestDB(t)

	cutoff, err := db.GetFeedbackCutoff("TEST-1")
	if err != nil {
		t.Fatalf("GetFeedbackCutoff() error: %v", err)
	}
	if !cutoff.IsZero() {
		t.Error("expected zero time for missing cutoff")
	}

	now := time.Now().UTC().Truncate(time.Second)
	if err := db.SetFeedbackCutoff("TEST-1", now); err != nil {
		t.Fatalf("SetFeedbackCutoff() error: %v", err)
	}

	cutoff, err = db.GetFeedbackCutoff("TEST-1")
	if err != nil {
		t.Fatalf("GetFeedbackCutoff() after set error: %v", err)
	}
	if !cutoff.Equal(now) {
		t.Errorf("cutoff = %v, want %v", cutoff, now)
	}

	// Upsert
	later := now.Add(time.Hour)
	if err := db.SetFeedbackCutoff("TEST-1", later); err != nil {
		t.Fatalf("SetFeedbackCutoff() upsert error: %v", err)
	}
	cutoff, err = db.GetFeedbackCutoff("TEST-1")
	if err != nil {
		t.Fatalf("GetFeedbackCutoff() after upsert error: %v", err)
	}
	if !cutoff.Equal(later) {
		t.Errorf("cutoff after upsert = %v, want %v", cutoff, later)
	}
}

func TestPruneOldRecords(t *testing.T) {
	db := openTestDB(t)

	if err := db.RecordAttempt("TEST-1"); err != nil {
		t.Fatalf("RecordAttempt() error: %v", err)
	}
	if err := db.MarkSHAProcessed("sha1"); err != nil {
		t.Fatalf("MarkSHAProcessed() error: %v", err)
	}

	// Prune with a very large window — nothing should be removed
	if err := db.PruneOldRecords(24 * time.Hour); err != nil {
		t.Fatalf("PruneOldRecords(24h) error: %v", err)
	}
	count, err := db.CountRecentAttempts("TEST-1", time.Hour)
	if err != nil {
		t.Fatalf("CountRecentAttempts() after no-op prune error: %v", err)
	}
	if count != 1 {
		t.Errorf("count after no-op prune = %d, want 1", count)
	}

	// Prune with zero window — should not error
	if err := db.PruneOldRecords(0); err != nil {
		t.Fatalf("PruneOldRecords(0) error: %v", err)
	}
}
