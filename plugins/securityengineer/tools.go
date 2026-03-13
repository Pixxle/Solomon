package securityengineer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/rs/zerolog/log"
)

// ToolRunner executes external security tools and writes raw results to files.
type ToolRunner struct {
	OutputDir string
}

// NewToolRunner creates a ToolRunner that writes output files to outputDir.
func NewToolRunner(outputDir string) *ToolRunner {
	return &ToolRunner{OutputDir: outputDir}
}

// toolDef defines a single tool invocation.
type toolDef struct {
	Name       string
	Agent      string
	Binary     string
	Args       []string
	OutputFile string
}

// RunAllTools executes all applicable tools against targetPath in parallel (semaphore of 4).
// Returns results manifest and writes tool-manifest.json.
func (tr *ToolRunner) RunAllTools(ctx context.Context, targetPath string) ([]ToolResult, error) {
	if err := os.MkdirAll(tr.OutputDir, 0o755); err != nil {
		return nil, fmt.Errorf("create output dir: %w", err)
	}

	tools := tr.buildToolList(targetPath)

	var (
		mu      sync.Mutex
		wg      sync.WaitGroup
		results = make([]ToolResult, 0, len(tools))
		sem     = make(chan struct{}, 4)
	)

	for _, td := range tools {
		wg.Add(1)
		go func(td toolDef) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			result := tr.runTool(ctx, td)

			mu.Lock()
			results = append(results, result)
			mu.Unlock()
		}(td)
	}

	wg.Wait()

	// Write tool-manifest.json.
	manifestPath := filepath.Join(tr.OutputDir, "tool-manifest.json")
	manifestData, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return results, fmt.Errorf("marshal manifest: %w", err)
	}
	if err := os.WriteFile(manifestPath, manifestData, 0o644); err != nil {
		return results, fmt.Errorf("write manifest: %w", err)
	}

	log.Info().Int("tools", len(results)).Str("manifest", manifestPath).Msg("tool run complete")
	return results, nil
}

// buildToolList returns the full list of tool definitions for a target.
func (tr *ToolRunner) buildToolList(targetPath string) []toolDef {
	return []toolDef{
		// SAST tools
		{
			Name:       "semgrep",
			Agent:      "sast",
			Binary:     "semgrep",
			Args:       []string{"scan", "--config", "auto", "--json", "--output", filepath.Join(tr.OutputDir, "semgrep.json"), targetPath},
			OutputFile: filepath.Join(tr.OutputDir, "semgrep.json"),
		},
		{
			Name:       "bandit",
			Agent:      "sast",
			Binary:     "bandit",
			Args:       []string{"-r", "-f", "json", "-o", filepath.Join(tr.OutputDir, "bandit.json"), targetPath},
			OutputFile: filepath.Join(tr.OutputDir, "bandit.json"),
		},
		{
			Name:       "gosec",
			Agent:      "sast",
			Binary:     "gosec",
			Args:       []string{"-fmt=json", "-out=" + filepath.Join(tr.OutputDir, "gosec.json"), targetPath + "/..."},
			OutputFile: filepath.Join(tr.OutputDir, "gosec.json"),
		},
		// DEPS tools
		{
			Name:       "osv-scanner",
			Agent:      "deps",
			Binary:     "osv-scanner",
			Args:       []string{"--format", "json", "--output", filepath.Join(tr.OutputDir, "osv-scanner.json"), "-r", targetPath},
			OutputFile: filepath.Join(tr.OutputDir, "osv-scanner.json"),
		},
		{
			Name:       "grype",
			Agent:      "deps",
			Binary:     "grype",
			Args:       []string{"dir:" + targetPath, "-o", "json", "--file", filepath.Join(tr.OutputDir, "grype.json")},
			OutputFile: filepath.Join(tr.OutputDir, "grype.json"),
		},
		{
			Name:       "trivy",
			Agent:      "deps",
			Binary:     "trivy",
			Args:       []string{"fs", "--format", "json", "--output", filepath.Join(tr.OutputDir, "trivy.json"), targetPath},
			OutputFile: filepath.Join(tr.OutputDir, "trivy.json"),
		},
		// SECRETS tools
		{
			Name:       "gitleaks",
			Agent:      "secrets",
			Binary:     "gitleaks",
			Args:       []string{"detect", "--source", targetPath, "--report-format", "json", "--report-path", filepath.Join(tr.OutputDir, "gitleaks.json"), "--no-git"},
			OutputFile: filepath.Join(tr.OutputDir, "gitleaks.json"),
		},
		{
			Name:       "trufflehog",
			Agent:      "secrets",
			Binary:     "trufflehog",
			Args:       []string{"filesystem", "--json", targetPath},
			OutputFile: filepath.Join(tr.OutputDir, "trufflehog.json"),
		},
		// CONFIG tools
		{
			Name:       "checkov",
			Agent:      "config",
			Binary:     "checkov",
			Args:       []string{"-d", targetPath, "--output", "json"},
			OutputFile: filepath.Join(tr.OutputDir, "checkov.json"),
		},
	}
}

// runTool executes a single tool and returns the result.
func (tr *ToolRunner) runTool(ctx context.Context, td toolDef) ToolResult {
	result := ToolResult{
		Name:       td.Name,
		Agent:      td.Agent,
		OutputFile: td.OutputFile,
	}

	// Check if the tool is available.
	binPath, err := exec.LookPath(td.Binary)
	if err != nil {
		log.Debug().Str("tool", td.Name).Msg("tool unavailable")
		result.Status = "unavailable"
		result.Error = fmt.Sprintf("%s not found in PATH", td.Binary)
		return result
	}
	_ = binPath

	// Get version.
	result.Version = getToolVersion(td.Binary)

	log.Info().Str("tool", td.Name).Str("version", result.Version).Msg("running tool")

	cmd := exec.CommandContext(ctx, td.Binary, td.Args...)

	// Special cases: trufflehog and checkov write to stdout, so we capture it.
	if td.Name == "trufflehog" || td.Name == "checkov" {
		output, err := cmd.CombinedOutput()
		if err != nil {
			// Many security tools exit 1 when findings exist; that's not an error.
			if exitErr, ok := err.(*exec.ExitError); ok {
				log.Debug().Str("tool", td.Name).Int("exit_code", exitErr.ExitCode()).Msg("non-zero exit (may indicate findings)")
			} else {
				log.Warn().Str("tool", td.Name).Err(err).Msg("tool execution failed")
				result.Status = "failed"
				result.Error = err.Error()
				return result
			}
		}
		if writeErr := os.WriteFile(td.OutputFile, output, 0o644); writeErr != nil {
			log.Warn().Str("tool", td.Name).Err(writeErr).Msg("failed to write output file")
			result.Status = "failed"
			result.Error = writeErr.Error()
			return result
		}
		result.Status = "ran_successfully"
		log.Info().Str("tool", td.Name).Str("output", td.OutputFile).Msg("tool completed")
		return result
	}

	// Standard tools that write their own output file.
	output, err := cmd.CombinedOutput()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			// Many security tools exit 1 when findings exist.
			log.Debug().Str("tool", td.Name).Int("exit_code", exitErr.ExitCode()).Msg("non-zero exit (may indicate findings)")
		} else {
			log.Warn().Str("tool", td.Name).Err(err).Str("output", string(output)).Msg("tool execution failed")
			result.Status = "failed"
			result.Error = err.Error()
			return result
		}
	}

	// Check if the output file was created.
	if _, statErr := os.Stat(td.OutputFile); statErr != nil {
		log.Warn().Str("tool", td.Name).Str("file", td.OutputFile).Msg("output file not created")
		result.Status = "failed"
		result.Error = "output file not created"
		return result
	}

	result.Status = "ran_successfully"
	log.Info().Str("tool", td.Name).Str("output", td.OutputFile).Msg("tool completed")
	return result
}

// getToolVersion attempts to get a tool's version string.
func getToolVersion(binary string) string {
	cmd := exec.Command(binary, "--version")
	out, err := cmd.Output()
	if err != nil {
		return "unknown"
	}
	version := strings.TrimSpace(string(out))
	// Take only the first line.
	if idx := strings.IndexByte(version, '\n'); idx != -1 {
		version = version[:idx]
	}
	return version
}

// ToolResultsForAgent filters results for a specific agent.
func ToolResultsForAgent(results []ToolResult, agent string) []ToolResult {
	var filtered []ToolResult
	for _, r := range results {
		if r.Agent == agent {
			filtered = append(filtered, r)
		}
	}
	return filtered
}

// ReadToolOutput reads raw JSON from a tool result file, truncating at 100KB.
func ReadToolOutput(result ToolResult) (string, error) {
	const maxBytes = 100 * 1024 // 100KB

	if result.OutputFile == "" {
		return "", fmt.Errorf("no output file for tool %s", result.Name)
	}

	data, err := os.ReadFile(result.OutputFile)
	if err != nil {
		return "", fmt.Errorf("read %s output: %w", result.Name, err)
	}

	if len(data) > maxBytes {
		data = data[:maxBytes]
	}

	return string(data), nil
}
