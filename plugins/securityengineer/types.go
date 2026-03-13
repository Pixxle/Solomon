package securityengineer

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/pixxle/solomon/internal/db"
)

// Severity levels.
const (
	SeverityCritical = "CRITICAL"
	SeverityHigh     = "HIGH"
	SeverityMedium   = "MEDIUM"
	SeverityLow      = "LOW"
)

// Confidence levels.
const (
	ConfidenceHigh   = "HIGH"
	ConfidenceMedium = "MEDIUM"
	ConfidenceLow    = "LOW"
)

// Finding statuses.
const (
	StatusOpen          = "open"
	StatusMitigated     = "mitigated"
	StatusFalsePositive = "false_positive"
	StatusAccepted      = "accepted"
)

// Scan types.
const (
	ScanTypeQuick = "quick"
	ScanTypeFull  = "full"
)

// Scan lifecycle statuses.
const (
	ScanStatusRunning   = "running"
	ScanStatusCompleted = "completed"
	ScanStatusFailed    = "failed"
)

// PersistResult holds the outcome of persisting findings, including sync data.
type PersistResult struct {
	NewCount       int
	OpenCount      int
	MitigatedCount int
	Mitigated      []*db.SecurityFinding // just-mitigated findings with Jira tickets
	Regressed      []*db.SecurityFinding // reappeared findings with Jira tickets
}

// RawFinding is the output from an individual agent before DB persistence.
type RawFinding struct {
	Agent             string `json:"agent"`
	FindingID         string `json:"finding_id"`
	Title             string `json:"title"`
	Description       string `json:"description"`
	Severity          string `json:"severity"`
	Confidence        string `json:"confidence"`
	Priority          string `json:"priority"`
	Category          string `json:"category"`
	CweID             string `json:"cwe_id,omitempty"`
	OwaspCategory     string `json:"owasp_category,omitempty"`
	FilePath          string `json:"file_path"`
	LineStart         int    `json:"line_start"`
	LineEnd           int    `json:"line_end"`
	Snippet           string `json:"snippet,omitempty"`
	Evidence          string `json:"evidence,omitempty"`
	Source            string `json:"source"`
	SourceTool        string `json:"source_tool,omitempty"`
	Remediation       string `json:"remediation,omitempty"`
	RemediationEffort string `json:"remediation_effort,omitempty"`
	CodeSuggestion    string `json:"code_suggestion,omitempty"`
	FalsePositiveRisk string `json:"false_positive_risk,omitempty"`
	Fingerprint       string `json:"fingerprint"`
}

// UnmarshalJSON handles LLM response variations where fields like remediation
// may be returned as objects instead of strings, or location may be nested.
func (r *RawFinding) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	// getString extracts a JSON string value, returning "" for null or non-string types.
	getString := func(key string) string {
		v, ok := raw[key]
		if !ok || string(v) == "null" {
			return ""
		}
		var s string
		if err := json.Unmarshal(v, &s); err != nil {
			return ""
		}
		return s
	}

	// getInt extracts an integer, handling both int and float JSON numbers.
	getInt := func(key string) int {
		v, ok := raw[key]
		if !ok || string(v) == "null" {
			return 0
		}
		var f float64
		if err := json.Unmarshal(v, &f); err != nil {
			return 0
		}
		return int(f)
	}

	// flattenObject converts a JSON object to "key: value; key: value" with sorted keys.
	flattenObject := func(v json.RawMessage) string {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(v, &m); err != nil {
			return strings.TrimSpace(string(v))
		}
		parts := make([]string, 0, len(m))
		for k, val := range m {
			var s string
			if err := json.Unmarshal(val, &s); err != nil {
				s = strings.TrimSpace(string(val))
			}
			parts = append(parts, fmt.Sprintf("%s: %s", k, s))
		}
		sort.Strings(parts)
		return strings.Join(parts, "; ")
	}

	// getFlexString extracts a string or flattens an object/array to a readable string.
	getFlexString := func(key string) string {
		v, ok := raw[key]
		if !ok || string(v) == "null" {
			return ""
		}
		var s string
		if err := json.Unmarshal(v, &s); err == nil {
			return s
		}
		return flattenObject(v)
	}

	r.Agent = getString("agent")
	r.Title = getString("title")
	r.Description = getString("description")
	r.Severity = getString("severity")
	r.Confidence = getString("confidence")
	r.Priority = getString("priority")
	r.Category = getString("category")
	r.CweID = getString("cwe_id")
	r.OwaspCategory = getString("owasp_category")
	r.Evidence = getString("evidence")
	r.Source = getString("source")
	r.SourceTool = getString("source_tool")
	r.RemediationEffort = getString("remediation_effort")
	r.FalsePositiveRisk = getString("false_positive_risk")
	r.Fingerprint = getString("fingerprint")

	// These fields may come back as objects from the LLM.
	r.Remediation = getFlexString("remediation")
	r.CodeSuggestion = getFlexString("code_suggestion")

	// finding_id or id
	r.FindingID = getString("finding_id")
	if r.FindingID == "" {
		r.FindingID = getString("id")
	}

	// Handle flat or nested location for file path / lines / snippet.
	r.FilePath = getString("file_path")
	r.LineStart = getInt("line_start")
	r.LineEnd = getInt("line_end")
	r.Snippet = getString("snippet")

	if locRaw, ok := raw["location"]; ok {
		var loc struct {
			File      string `json:"file"`
			FilePath  string `json:"file_path"`
			LineStart int    `json:"line_start"`
			LineEnd   int    `json:"line_end"`
			Snippet   string `json:"snippet"`
		}
		if err := json.Unmarshal(locRaw, &loc); err == nil {
			if r.FilePath == "" {
				r.FilePath = loc.File
				if r.FilePath == "" {
					r.FilePath = loc.FilePath
				}
			}
			if r.LineStart == 0 {
				r.LineStart = loc.LineStart
			}
			if r.LineEnd == 0 {
				r.LineEnd = loc.LineEnd
			}
			if r.Snippet == "" {
				r.Snippet = loc.Snippet
			}
		}
	}

	return nil
}

// ToolResult records what happened when a tool was executed.
type ToolResult struct {
	Name       string `json:"name"`
	Agent      string `json:"agent"`
	Status     string `json:"status"` // ran_successfully, failed, skipped, unavailable
	OutputFile string `json:"output_file,omitempty"`
	Version    string `json:"version,omitempty"`
	Error      string `json:"error,omitempty"`
}

// ScanResult is the full output of a scan run.
type ScanResult struct {
	RepoName       string        `json:"repo_name"`
	CommitHash     string        `json:"commit_hash"`
	ScanType       string        `json:"scan_type"`
	Findings       []*RawFinding `json:"findings"`
	NewCount       int           `json:"new_count"`
	OpenCount      int           `json:"open_count"`
	MitigatedCount int           `json:"mitigated_count"`
	Summary        string        `json:"summary"`
}

// SourceFile represents a source file to include in the agent prompt.
type SourceFile struct {
	Path    string
	Content string
}

// ToolInput holds tool result data for template rendering.
type ToolInput struct {
	Name       string
	Status     string
	OutputFile string
	RawOutput  string
}

// AgentInput is the data passed to an agent prompt template.
type AgentInput struct {
	ToolResults []ToolInput
	SourceFiles []SourceFile
}

// AgentResult holds an agent's output after processing.
type AgentResult struct {
	Agent        string
	FindingCount int
	FindingsJSON string
	Findings     []*RawFinding
}

// ConsolidateInput is the data passed to the consolidation prompt template.
type ConsolidateInput struct {
	AgentResults []AgentResult
}

// PipelineResult is the output of a full pipeline run.
type PipelineResult struct {
	ToolResults  []ToolResult             `json:"tool_results"`
	AgentResults map[string][]*RawFinding `json:"agent_results"`
	Consolidated []*RawFinding            `json:"consolidated"`
	Summary      string                   `json:"summary"`
}

// Priority computes priority from severity x confidence.
func Priority(severity, confidence string) string {
	switch severity {
	case SeverityCritical:
		if confidence == ConfidenceLow {
			return "P1"
		}
		return "P0"
	case SeverityHigh:
		if confidence == ConfidenceLow {
			return "P2"
		}
		return "P1"
	case SeverityMedium:
		if confidence == ConfidenceLow {
			return "P3"
		}
		return "P2"
	default:
		return "P3"
	}
}
