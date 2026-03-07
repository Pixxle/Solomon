package loop

import (
	"time"

	"github.com/pixxle/codehephaestus/internal/db"
)

const (
	maxAttempts   = 5
	attemptWindow = 10 * time.Minute
)

type LoopPrevention struct {
	stateDB *db.StateDB
}

func NewLoopPrevention(stateDB *db.StateDB) *LoopPrevention {
	return &LoopPrevention{stateDB: stateDB}
}

func (lp *LoopPrevention) ShouldSkip(issueKey string) bool {
	count, err := lp.stateDB.CountRecentAttempts(issueKey, attemptWindow)
	if err != nil {
		return false
	}
	return count >= maxAttempts
}

func (lp *LoopPrevention) RecordAttempt(issueKey string) {
	_ = lp.stateDB.RecordAttempt(issueKey)
}

func (lp *LoopPrevention) IsSHAProcessed(sha string) bool {
	processed, _ := lp.stateDB.IsSHAProcessed(sha)
	return processed
}

func (lp *LoopPrevention) MarkSHAProcessed(sha string) {
	_ = lp.stateDB.MarkSHAProcessed(sha)
}

func (lp *LoopPrevention) GetFeedbackCutoff(issueKey string) time.Time {
	cutoff, _ := lp.stateDB.GetFeedbackCutoff(issueKey)
	return cutoff
}

func (lp *LoopPrevention) MarkFeedbackProcessed(issueKey string) {
	_ = lp.stateDB.SetFeedbackCutoff(issueKey, time.Now().UTC())
}

func (lp *LoopPrevention) IsCommentProcessed(commentID string) bool {
	processed, _ := lp.stateDB.IsCommentProcessed(commentID)
	return processed
}

func (lp *LoopPrevention) Prune() {
	_ = lp.stateDB.PruneOldRecords(24 * time.Hour)
}
