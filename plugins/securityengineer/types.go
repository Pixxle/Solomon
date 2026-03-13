package securityengineer

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
	ToolResults  []ToolResult              `json:"tool_results"`
	AgentResults map[string][]*RawFinding  `json:"agent_results"`
	Consolidated []*RawFinding             `json:"consolidated"`
	Summary      string                    `json:"summary"`
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
