package securityengineer

import (
	"encoding/json"
	"testing"
)

func TestPriority(t *testing.T) {
	t.Parallel()
	tests := []struct {
		severity   string
		confidence string
		want       string
	}{
		{SeverityCritical, ConfidenceHigh, "P0"},
		{SeverityCritical, ConfidenceMedium, "P0"},
		{SeverityCritical, ConfidenceLow, "P1"},
		{SeverityHigh, ConfidenceHigh, "P1"},
		{SeverityHigh, ConfidenceMedium, "P1"},
		{SeverityHigh, ConfidenceLow, "P2"},
		{SeverityMedium, ConfidenceHigh, "P2"},
		{SeverityMedium, ConfidenceMedium, "P2"},
		{SeverityMedium, ConfidenceLow, "P3"},
		{SeverityLow, ConfidenceHigh, "P3"},
		{SeverityLow, ConfidenceMedium, "P3"},
		{SeverityLow, ConfidenceLow, "P3"},
		{"UNKNOWN", ConfidenceHigh, "P3"},
		{"", "", "P3"},
	}
	for _, tt := range tests {
		t.Run(tt.severity+"_"+tt.confidence, func(t *testing.T) {
			got := Priority(tt.severity, tt.confidence)
			if got != tt.want {
				t.Errorf("Priority(%q, %q) = %q, want %q", tt.severity, tt.confidence, got, tt.want)
			}
		})
	}
}

func TestRawFindingUnmarshalJSON(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		input   string
		check   func(t *testing.T, r *RawFinding)
		wantErr bool
	}{
		{
			name:  "standard fields",
			input: `{"finding_id":"f1","title":"SQL Injection","severity":"HIGH","confidence":"HIGH","file_path":"main.go","line_start":10,"line_end":12,"fingerprint":"abc"}`,
			check: func(t *testing.T, r *RawFinding) {
				if r.FindingID != "f1" {
					t.Errorf("FindingID = %q, want %q", r.FindingID, "f1")
				}
				if r.Title != "SQL Injection" {
					t.Errorf("Title = %q, want %q", r.Title, "SQL Injection")
				}
				if r.Severity != "HIGH" {
					t.Errorf("Severity = %q, want %q", r.Severity, "HIGH")
				}
				if r.FilePath != "main.go" {
					t.Errorf("FilePath = %q, want %q", r.FilePath, "main.go")
				}
				if r.LineStart != 10 {
					t.Errorf("LineStart = %d, want 10", r.LineStart)
				}
				if r.LineEnd != 12 {
					t.Errorf("LineEnd = %d, want 12", r.LineEnd)
				}
			},
		},
		{
			name:  "nested location object",
			input: `{"finding_id":"f2","title":"XSS","location":{"file":"app.js","line_start":5,"line_end":8,"snippet":"alert(1)"},"fingerprint":"def"}`,
			check: func(t *testing.T, r *RawFinding) {
				if r.FilePath != "app.js" {
					t.Errorf("FilePath = %q, want %q", r.FilePath, "app.js")
				}
				if r.LineStart != 5 {
					t.Errorf("LineStart = %d, want 5", r.LineStart)
				}
				if r.Snippet != "alert(1)" {
					t.Errorf("Snippet = %q, want %q", r.Snippet, "alert(1)")
				}
			},
		},
		{
			name:  "location with file_path key",
			input: `{"finding_id":"f3","location":{"file_path":"lib.go","line_start":1},"fingerprint":"ghi"}`,
			check: func(t *testing.T, r *RawFinding) {
				if r.FilePath != "lib.go" {
					t.Errorf("FilePath = %q, want %q", r.FilePath, "lib.go")
				}
			},
		},
		{
			name:  "flat fields take precedence over location",
			input: `{"finding_id":"f4","file_path":"flat.go","line_start":99,"location":{"file":"nested.go","line_start":1},"fingerprint":"jkl"}`,
			check: func(t *testing.T, r *RawFinding) {
				if r.FilePath != "flat.go" {
					t.Errorf("FilePath = %q, want %q (flat should win)", r.FilePath, "flat.go")
				}
				if r.LineStart != 99 {
					t.Errorf("LineStart = %d, want 99 (flat should win)", r.LineStart)
				}
			},
		},
		{
			name:  "remediation as object (LLM quirk)",
			input: `{"finding_id":"f5","remediation":{"step1":"fix input","step2":"add validation"},"fingerprint":"mno"}`,
			check: func(t *testing.T, r *RawFinding) {
				if r.Remediation == "" {
					t.Error("Remediation should not be empty for object input")
				}
				// Flattened as sorted "key: value; key: value"
				if r.Remediation != "step1: fix input; step2: add validation" {
					t.Errorf("Remediation = %q", r.Remediation)
				}
			},
		},
		{
			name:  "remediation as string",
			input: `{"finding_id":"f6","remediation":"use parameterized queries","fingerprint":"pqr"}`,
			check: func(t *testing.T, r *RawFinding) {
				if r.Remediation != "use parameterized queries" {
					t.Errorf("Remediation = %q, want %q", r.Remediation, "use parameterized queries")
				}
			},
		},
		{
			name:  "id fallback for finding_id",
			input: `{"id":"fallback-id","title":"test","fingerprint":"stu"}`,
			check: func(t *testing.T, r *RawFinding) {
				if r.FindingID != "fallback-id" {
					t.Errorf("FindingID = %q, want %q", r.FindingID, "fallback-id")
				}
			},
		},
		{
			name:  "finding_id takes precedence over id",
			input: `{"finding_id":"primary","id":"fallback","fingerprint":"vwx"}`,
			check: func(t *testing.T, r *RawFinding) {
				if r.FindingID != "primary" {
					t.Errorf("FindingID = %q, want %q", r.FindingID, "primary")
				}
			},
		},
		{
			name:  "null fields",
			input: `{"finding_id":"f7","title":null,"severity":null,"line_start":null,"fingerprint":"yz"}`,
			check: func(t *testing.T, r *RawFinding) {
				if r.Title != "" {
					t.Errorf("Title = %q, want empty for null", r.Title)
				}
				if r.Severity != "" {
					t.Errorf("Severity = %q, want empty for null", r.Severity)
				}
				if r.LineStart != 0 {
					t.Errorf("LineStart = %d, want 0 for null", r.LineStart)
				}
			},
		},
		{
			name:  "float line numbers",
			input: `{"finding_id":"f8","line_start":42.0,"line_end":50.5,"fingerprint":"flt"}`,
			check: func(t *testing.T, r *RawFinding) {
				if r.LineStart != 42 {
					t.Errorf("LineStart = %d, want 42", r.LineStart)
				}
				if r.LineEnd != 50 {
					t.Errorf("LineEnd = %d, want 50", r.LineEnd)
				}
			},
		},
		{
			name:  "all string fields",
			input: `{"agent":"sast","finding_id":"f9","title":"T","description":"D","severity":"HIGH","confidence":"LOW","priority":"P2","category":"injection","cwe_id":"CWE-89","owasp_category":"A1","evidence":"E","source":"tool","source_tool":"semgrep","remediation_effort":"low","false_positive_risk":"low","fingerprint":"full","code_suggestion":"fix it"}`,
			check: func(t *testing.T, r *RawFinding) {
				if r.Agent != "sast" {
					t.Errorf("Agent = %q", r.Agent)
				}
				if r.Category != "injection" {
					t.Errorf("Category = %q", r.Category)
				}
				if r.CweID != "CWE-89" {
					t.Errorf("CweID = %q", r.CweID)
				}
				if r.OwaspCategory != "A1" {
					t.Errorf("OwaspCategory = %q", r.OwaspCategory)
				}
				if r.CodeSuggestion != "fix it" {
					t.Errorf("CodeSuggestion = %q", r.CodeSuggestion)
				}
			},
		},
		{
			name:    "invalid JSON",
			input:   `{not json}`,
			wantErr: true,
		},
		{
			name:  "empty object",
			input: `{}`,
			check: func(t *testing.T, r *RawFinding) {
				if r.FindingID != "" {
					t.Errorf("FindingID = %q, want empty", r.FindingID)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var r RawFinding
			err := json.Unmarshal([]byte(tt.input), &r)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.check != nil {
				tt.check(t, &r)
			}
		})
	}
}
