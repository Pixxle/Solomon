package developer

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/pixxle/solomon/internal/db"
)

func openTestDB(t *testing.T) *db.StateDB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open() error: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestShouldSkip(t *testing.T) {
	stateDB := openTestDB(t)
	lp := NewLoopPrevention(stateDB)

	if lp.ShouldSkip("TEST-1") {
		t.Error("ShouldSkip() = true initially, want false")
	}

	for i := 0; i < maxAttempts; i++ {
		lp.RecordAttempt("TEST-1")
	}

	if !lp.ShouldSkip("TEST-1") {
		t.Errorf("ShouldSkip() = false after %d attempts, want true", maxAttempts)
	}
}

func TestShouldSkipDifferentIssues(t *testing.T) {
	stateDB := openTestDB(t)
	lp := NewLoopPrevention(stateDB)

	for i := 0; i < maxAttempts; i++ {
		lp.RecordAttempt("TEST-1")
	}

	if lp.ShouldSkip("TEST-2") {
		t.Error("ShouldSkip(TEST-2) should be false when only TEST-1 has attempts")
	}
}

func TestIsSHAProcessedAndMark(t *testing.T) {
	stateDB := openTestDB(t)
	lp := NewLoopPrevention(stateDB)

	if lp.IsSHAProcessed("abc123") {
		t.Error("IsSHAProcessed() = true initially, want false")
	}

	lp.MarkSHAProcessed("abc123")

	if !lp.IsSHAProcessed("abc123") {
		t.Error("IsSHAProcessed() = false after marking, want true")
	}

	if lp.IsSHAProcessed("def456") {
		t.Error("IsSHAProcessed() = true for different SHA, want false")
	}
}

func TestGetFeedbackCutoffAndMark(t *testing.T) {
	stateDB := openTestDB(t)
	lp := NewLoopPrevention(stateDB)

	cutoff := lp.GetFeedbackCutoff("TEST-1")
	if !cutoff.IsZero() {
		t.Errorf("GetFeedbackCutoff() = %v initially, want zero", cutoff)
	}

	lp.MarkFeedbackProcessed("TEST-1")

	cutoff = lp.GetFeedbackCutoff("TEST-1")
	if cutoff.IsZero() {
		t.Error("GetFeedbackCutoff() = zero after marking, want non-zero")
	}

	if time.Since(cutoff) > 5*time.Second {
		t.Errorf("GetFeedbackCutoff() = %v, expected recent time", cutoff)
	}
}

func TestRecordAttempt(t *testing.T) {
	stateDB := openTestDB(t)
	lp := NewLoopPrevention(stateDB)

	lp.RecordAttempt("TEST-1")
	lp.RecordAttempt("TEST-1")
	lp.RecordAttempt("TEST-1")

	// Should not skip yet (3 < maxAttempts=5)
	if lp.ShouldSkip("TEST-1") {
		t.Error("ShouldSkip() = true after 3 attempts, want false")
	}
}

func TestIsCommentProcessed(t *testing.T) {
	stateDB := openTestDB(t)
	lp := NewLoopPrevention(stateDB)

	// Before any feedback is recorded
	if lp.IsCommentProcessed("comment-1") {
		t.Error("IsCommentProcessed() = true initially, want false")
	}

	// Record feedback via DB directly to set up state
	rec := &db.PRFeedbackRecord{
		IssueKey:    "TEST-1",
		PRNumber:    1,
		CommentID:   "comment-1",
		CommentType: "review",
		ActionTaken: "addressed",
		CreatedAt:   time.Now().UTC(),
	}
	if err := stateDB.InsertPRFeedback(rec); err != nil {
		t.Fatalf("InsertPRFeedback() error: %v", err)
	}

	if !lp.IsCommentProcessed("comment-1") {
		t.Error("IsCommentProcessed() = false after insert, want true")
	}
}

func TestPrune(t *testing.T) {
	stateDB := openTestDB(t)
	lp := NewLoopPrevention(stateDB)

	lp.RecordAttempt("TEST-1")
	lp.MarkSHAProcessed("sha1")

	// Prune should not panic or error
	lp.Prune()
}
