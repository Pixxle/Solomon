package securityengineer

import (
	"path/filepath"
	"testing"

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

func TestPersistFindings_NewFindings(t *testing.T) {
	stateDB := openTestDB(t)

	findings := []*RawFinding{
		{FindingID: "f1", Title: "SQL Injection", Severity: SeverityHigh, Confidence: ConfidenceHigh, Fingerprint: "fp1"},
		{FindingID: "f2", Title: "XSS", Severity: SeverityMedium, Confidence: ConfidenceMedium, Fingerprint: "fp2"},
	}

	result, err := PersistFindings(stateDB, "test-repo", 1, findings)
	if err != nil {
		t.Fatalf("PersistFindings() error: %v", err)
	}
	if result.NewCount != 2 {
		t.Errorf("NewCount = %d, want 2", result.NewCount)
	}
	if result.MitigatedCount != 0 {
		t.Errorf("MitigatedCount = %d, want 0", result.MitigatedCount)
	}

	// Priority should be auto-computed
	open, err := stateDB.GetOpenSecurityFindings("test-repo")
	if err != nil {
		t.Fatalf("GetOpenSecurityFindings() error: %v", err)
	}
	if len(open) != 2 {
		t.Fatalf("open findings = %d, want 2", len(open))
	}
}

func TestPersistFindings_Deduplication(t *testing.T) {
	stateDB := openTestDB(t)

	findings := []*RawFinding{
		{FindingID: "f1", Title: "Bug", Severity: SeverityHigh, Confidence: ConfidenceHigh, Fingerprint: "fp1"},
	}

	// First persist
	_, err := PersistFindings(stateDB, "test-repo", 1, findings)
	if err != nil {
		t.Fatalf("first PersistFindings() error: %v", err)
	}

	// Second persist with same fingerprint
	result, err := PersistFindings(stateDB, "test-repo", 2, findings)
	if err != nil {
		t.Fatalf("second PersistFindings() error: %v", err)
	}
	if result.NewCount != 0 {
		t.Errorf("NewCount on re-persist = %d, want 0", result.NewCount)
	}
}

func TestPersistFindings_Mitigation(t *testing.T) {
	stateDB := openTestDB(t)

	// First scan: two findings
	findings := []*RawFinding{
		{FindingID: "f1", Title: "Bug A", Severity: SeverityHigh, Confidence: ConfidenceHigh, Fingerprint: "fp1"},
		{FindingID: "f2", Title: "Bug B", Severity: SeverityMedium, Confidence: ConfidenceMedium, Fingerprint: "fp2"},
	}
	_, err := PersistFindings(stateDB, "test-repo", 1, findings)
	if err != nil {
		t.Fatalf("first PersistFindings() error: %v", err)
	}

	// Second scan: only one finding (fp2 is gone -> mitigated)
	findings2 := []*RawFinding{
		{FindingID: "f1", Title: "Bug A", Severity: SeverityHigh, Confidence: ConfidenceHigh, Fingerprint: "fp1"},
	}
	result, err := PersistFindings(stateDB, "test-repo", 2, findings2)
	if err != nil {
		t.Fatalf("second PersistFindings() error: %v", err)
	}
	if result.MitigatedCount != 1 {
		t.Errorf("MitigatedCount = %d, want 1", result.MitigatedCount)
	}
	if result.NewCount != 0 {
		t.Errorf("NewCount = %d, want 0", result.NewCount)
	}
}

func TestPersistFindings_MitigatedWithJira(t *testing.T) {
	stateDB := openTestDB(t)

	findings := []*RawFinding{
		{FindingID: "f1", Title: "Bug", Severity: SeverityHigh, Confidence: ConfidenceHigh, Fingerprint: "fp1"},
	}
	_, err := PersistFindings(stateDB, "test-repo", 1, findings)
	if err != nil {
		t.Fatalf("PersistFindings() error: %v", err)
	}

	// Attach a Jira key to the finding
	open, _ := stateDB.GetOpenSecurityFindings("test-repo")
	if len(open) != 1 {
		t.Fatalf("expected 1 open finding, got %d", len(open))
	}
	if err := stateDB.UpdateSecurityFindingJiraKey(open[0].ID, "SEC-123"); err != nil {
		t.Fatalf("UpdateSecurityFindingJiraKey() error: %v", err)
	}

	// Second scan: finding gone -> mitigated with Jira ticket
	result, err := PersistFindings(stateDB, "test-repo", 2, []*RawFinding{})
	if err != nil {
		t.Fatalf("second PersistFindings() error: %v", err)
	}
	if result.MitigatedCount != 1 {
		t.Errorf("MitigatedCount = %d, want 1", result.MitigatedCount)
	}
	if len(result.Mitigated) != 1 {
		t.Fatalf("Mitigated list = %d, want 1", len(result.Mitigated))
	}
	if result.Mitigated[0].JiraIssueKey != "SEC-123" {
		t.Errorf("Mitigated[0].JiraIssueKey = %q, want %q", result.Mitigated[0].JiraIssueKey, "SEC-123")
	}
}

func TestPersistFindings_PriorityAutoComputed(t *testing.T) {
	stateDB := openTestDB(t)

	findings := []*RawFinding{
		{FindingID: "f1", Severity: SeverityCritical, Confidence: ConfidenceHigh, Fingerprint: "fp1"},
	}

	_, err := PersistFindings(stateDB, "test-repo", 1, findings)
	if err != nil {
		t.Fatalf("PersistFindings() error: %v", err)
	}

	open, _ := stateDB.GetOpenSecurityFindings("test-repo")
	if len(open) != 1 {
		t.Fatalf("expected 1 open finding, got %d", len(open))
	}
	if open[0].Priority != "P0" {
		t.Errorf("Priority = %q, want P0", open[0].Priority)
	}
}

func TestPersistFindings_EmptyFindings(t *testing.T) {
	stateDB := openTestDB(t)

	result, err := PersistFindings(stateDB, "test-repo", 1, []*RawFinding{})
	if err != nil {
		t.Fatalf("PersistFindings() error: %v", err)
	}
	if result.NewCount != 0 {
		t.Errorf("NewCount = %d, want 0", result.NewCount)
	}
	if result.OpenCount != 0 {
		t.Errorf("OpenCount = %d, want 0", result.OpenCount)
	}
}
