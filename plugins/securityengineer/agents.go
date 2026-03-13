package securityengineer

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pixxle/solomon/internal/claude"
)

// AgentNames returns all agent names in execution order.
func AgentNames() []string {
	return []string{"SAST", "DEPS", "SECRETS", "CONFIG", "AUTH", "CRYPTO", "API", "DATA"}
}

// ToolAgentNames returns agents that have external tools.
func ToolAgentNames() []string {
	return []string{"SAST", "DEPS", "SECRETS", "CONFIG"}
}

// LLMOnlyAgentNames returns agents that are LLM-only.
func LLMOnlyAgentNames() []string {
	return []string{"AUTH", "CRYPTO", "API", "DATA"}
}

// RenderAgentPrompt renders an agent's prompt template with the given input.
// Uses internal/claude.RenderPrompt with path "security/<agent>.md.tmpl".
func RenderAgentPrompt(agentName string, input AgentInput) (string, error) {
	templateName := fmt.Sprintf("security/%s.md.tmpl", strings.ToLower(agentName))
	return claude.RenderPrompt(templateName, input)
}

// RenderConsolidatePrompt renders the consolidation prompt.
func RenderConsolidatePrompt(input ConsolidateInput) (string, error) {
	return claude.RenderPrompt("security/consolidate.md.tmpl", input)
}

// BuildAgentInput constructs the AgentInput for a given agent from tool results.
func BuildAgentInput(agentName string, toolResults []ToolResult, sourceFiles []SourceFile) (AgentInput, error) {
	filtered := ToolResultsForAgent(toolResults, agentName)
	var inputs []ToolInput
	for _, tr := range filtered {
		ti := ToolInput{
			Name:       tr.Name,
			Status:     tr.Status,
			OutputFile: tr.OutputFile,
		}
		if tr.OutputFile != "" {
			raw, err := ReadToolOutput(tr)
			if err != nil {
				ti.RawOutput = fmt.Sprintf("Error reading output: %v", err)
			} else {
				ti.RawOutput = raw
			}
		}
		inputs = append(inputs, ti)
	}
	return AgentInput{
		ToolResults: inputs,
		SourceFiles: sourceFiles,
	}, nil
}

// ParseAgentResponse parses the JSON findings array from an LLM response.
func ParseAgentResponse(agentName string, response string) ([]*RawFinding, error) {
	response = extractJSON(response)
	var findings []*RawFinding
	if err := json.Unmarshal([]byte(response), &findings); err != nil {
		var single RawFinding
		if err2 := json.Unmarshal([]byte(response), &single); err2 == nil {
			return []*RawFinding{&single}, nil
		}
		return nil, fmt.Errorf("parse %s response: %w\nResponse: %.500s", agentName, err, response)
	}
	for _, f := range findings {
		if f.Agent == "" {
			f.Agent = agentName
		}
		if f.Fingerprint == "" {
			f.Fingerprint = GenerateFingerprint(f.Agent, f.FilePath, f.Title, f.LineStart)
		}
	}
	return findings, nil
}

// SavePromptToFile writes a rendered prompt for debugging/audit.
func SavePromptToFile(outputDir, agentName, prompt string) error {
	promptDir := filepath.Join(outputDir, "prompts")
	os.MkdirAll(promptDir, 0755)
	path := filepath.Join(promptDir, strings.ToLower(agentName)+"-prompt.md")
	return os.WriteFile(path, []byte(prompt), 0644)
}

// SaveAgentResult writes agent findings to disk.
func SaveAgentResult(outputDir, agentName string, findings []*RawFinding) error {
	findingsDir := filepath.Join(outputDir, "findings")
	os.MkdirAll(findingsDir, 0755)
	data, err := json.MarshalIndent(findings, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(findingsDir, strings.ToLower(agentName)+".json")
	return os.WriteFile(path, data, 0644)
}

// GenerateFingerprint creates a stable hash for deduplication.
func GenerateFingerprint(agent, filePath, title string, lineStart int) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s:%s:%s:%d", agent, filePath, title, lineStart)))
	return fmt.Sprintf("%x", h[:12])
}

// extractJSON strips markdown code fences and finds the JSON array/object.
func extractJSON(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```json") {
		s = strings.TrimPrefix(s, "```json")
	} else if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```")
	}
	if strings.HasSuffix(s, "```") {
		s = strings.TrimSuffix(s, "```")
	}
	s = strings.TrimSpace(s)
	start := strings.IndexAny(s, "[{")
	if start < 0 {
		return s
	}
	opener := s[start]
	var closer byte
	if opener == '[' {
		closer = ']'
	} else {
		closer = '}'
	}
	end := strings.LastIndexByte(s, closer)
	if end < start {
		return s
	}
	return s[start : end+1]
}

// collectSourceFiles gathers relevant source files from the target path.
func collectSourceFiles(targetPath string) []SourceFile {
	var files []SourceFile
	exts := map[string]bool{
		".go": true, ".py": true, ".js": true, ".ts": true, ".jsx": true, ".tsx": true,
		".java": true, ".rb": true, ".php": true, ".cs": true, ".rs": true,
		".yml": true, ".yaml": true, ".json": true, ".toml": true,
		".tf": true, ".hcl": true,
		".env": true, ".ini": true, ".cfg": true, ".conf": true,
	}
	names := map[string]bool{
		"Dockerfile": true, "docker-compose.yml": true, "docker-compose.yaml": true,
		"Jenkinsfile": true, ".gitlab-ci.yml": true,
		"Makefile": true, "Gemfile": true, "Cargo.toml": true,
		"package.json": true, "package-lock.json": true,
		"go.mod": true, "go.sum": true,
		"requirements.txt": true, "Pipfile": true, "pyproject.toml": true,
		".gitignore": true,
	}
	maxFiles := 100
	maxFileSize := int64(50000)

	filepath.Walk(targetPath, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			if info != nil && info.IsDir() {
				base := filepath.Base(path)
				if base == "node_modules" || base == "vendor" || base == ".git" || base == "__pycache__" || base == ".venv" {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if len(files) >= maxFiles {
			return filepath.SkipAll
		}
		if info.Size() > maxFileSize || info.Size() == 0 {
			return nil
		}
		ext := filepath.Ext(path)
		base := filepath.Base(path)
		if !exts[ext] && !names[base] {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(targetPath, path)
		files = append(files, SourceFile{Path: rel, Content: string(content)})
		return nil
	})

	return files
}
