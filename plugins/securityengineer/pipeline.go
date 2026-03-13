package securityengineer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/rs/zerolog/log"

	"github.com/pixxle/solomon/internal/claude"
)

// Pipeline orchestrates the full scan pipeline:
// 1. Run external tools (store raw output to files)
// 2. Feed tool results + source code to LLM agents for enrichment
// 3. Consolidate and cross-correlate findings
type Pipeline struct {
	ToolRunner *ToolRunner
	OutputDir  string
	MaxAgents  int    // max parallel agent invocations
	Model      string // claude model shorthand (opus, sonnet, haiku)
}

// NewPipeline creates a new scan pipeline.
func NewPipeline(outputDir string, maxAgents int, model string) *Pipeline {
	if maxAgents <= 0 {
		maxAgents = 4
	}
	return &Pipeline{
		ToolRunner: NewToolRunner(outputDir),
		OutputDir:  outputDir,
		MaxAgents:  maxAgents,
		Model:      model,
	}
}

// Run executes the full pipeline against a target path.
func (p *Pipeline) Run(ctx context.Context, targetPath string) (*PipelineResult, error) {
	result := &PipelineResult{
		AgentResults: make(map[string][]*RawFinding),
	}

	// Phase 1: Run all external tools
	log.Info().Str("target", targetPath).Msg("security pipeline phase 1: running external tools")
	toolResults, err := p.ToolRunner.RunAllTools(ctx, targetPath)
	if err != nil {
		return nil, fmt.Errorf("tool execution: %w", err)
	}
	result.ToolResults = toolResults

	ran, failed, unavailable := 0, 0, 0
	for _, r := range toolResults {
		switch r.Status {
		case "ran_successfully":
			ran++
		case "failed":
			failed++
		case "unavailable":
			unavailable++
		}
	}
	log.Info().Int("ran", ran).Int("failed", failed).Int("unavailable", unavailable).Msg("tools complete")

	// Collect source files for LLM review
	sourceFiles := collectSourceFiles(targetPath)
	log.Info().Int("files", len(sourceFiles)).Msg("collected source files for agent review")

	// Phase 2: Run LLM agents in parallel
	log.Info().Msg("security pipeline phase 2: running LLM agents")
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, p.MaxAgents)

	agentNames := AgentNames()
	for _, agentName := range agentNames {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			findings, err := p.runAgent(ctx, name, toolResults, sourceFiles)
			if err != nil {
				log.Error().Err(err).Str("agent", name).Msg("agent error")
				return
			}

			mu.Lock()
			result.AgentResults[name] = findings
			mu.Unlock()

			if err := SaveAgentResult(p.OutputDir, name, findings); err != nil {
				log.Warn().Err(err).Str("agent", name).Msg("failed to save agent results")
			}

			log.Info().Str("agent", name).Int("findings", len(findings)).Msg("agent complete")
		}(agentName)
	}
	wg.Wait()

	// Phase 3: Consolidation
	log.Info().Msg("security pipeline phase 3: consolidating findings")
	consolidated, err := p.consolidate(ctx, result.AgentResults)
	if err != nil {
		log.Warn().Err(err).Msg("consolidation failed, using raw agent results")
		for _, findings := range result.AgentResults {
			consolidated = append(consolidated, findings...)
		}
	}
	result.Consolidated = consolidated

	// Save consolidated findings
	consolidatedPath := filepath.Join(p.OutputDir, "findings", "consolidated.json")
	os.MkdirAll(filepath.Dir(consolidatedPath), 0755)
	data, _ := json.MarshalIndent(consolidated, "", "  ")
	os.WriteFile(consolidatedPath, data, 0644)

	log.Info().Int("findings", len(consolidated)).Msg("security pipeline complete")
	return result, nil
}

// runAgent renders the prompt, calls the LLM, and parses the response.
func (p *Pipeline) runAgent(ctx context.Context, agentName string, toolResults []ToolResult, sourceFiles []SourceFile) ([]*RawFinding, error) {
	input, err := BuildAgentInput(agentName, toolResults, sourceFiles)
	if err != nil {
		return nil, fmt.Errorf("build input: %w", err)
	}

	prompt, err := RenderAgentPrompt(agentName, input)
	if err != nil {
		return nil, fmt.Errorf("render prompt: %w", err)
	}

	// Save rendered prompt for audit
	SavePromptToFile(p.OutputDir, agentName, prompt)

	systemPrompt := fmt.Sprintf("You are the %s security analysis agent. "+
		"Analyze the provided tool outputs and source code, then return findings as a JSON array. "+
		"Be thorough but filter obvious false positives. Return ONLY valid JSON.", agentName)

	fullPrompt := systemPrompt + "\n\n" + prompt

	result, err := claude.RunClaude(ctx, fullPrompt, ".", p.Model)
	if err != nil {
		return nil, fmt.Errorf("LLM call: %w", err)
	}

	// Save raw LLM response for debugging
	responsePath := filepath.Join(p.OutputDir, "responses", agentName+"-response.txt")
	os.MkdirAll(filepath.Dir(responsePath), 0755)
	os.WriteFile(responsePath, []byte(result.Output), 0644)

	findings, err := ParseAgentResponse(agentName, result.Output)
	if err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	return findings, nil
}

// consolidate runs the cross-correlation agent on all findings.
func (p *Pipeline) consolidate(ctx context.Context, agentResults map[string][]*RawFinding) ([]*RawFinding, error) {
	var inputs []AgentResult
	for name, findings := range agentResults {
		data, _ := json.MarshalIndent(findings, "", "  ")
		inputs = append(inputs, AgentResult{
			Agent:        name,
			FindingCount: len(findings),
			FindingsJSON: string(data),
			Findings:     findings,
		})
	}

	prompt, err := RenderConsolidatePrompt(ConsolidateInput{AgentResults: inputs})
	if err != nil {
		return nil, err
	}

	SavePromptToFile(p.OutputDir, "consolidate", prompt)

	systemPrompt := "You are the consolidation agent. " +
		"Deduplicate, cross-correlate, and identify compound findings across all agent results. " +
		"Return ONLY valid JSON."

	fullPrompt := systemPrompt + "\n\n" + prompt

	result, err := claude.RunClaude(ctx, fullPrompt, ".", p.Model)
	if err != nil {
		return nil, err
	}

	// Save response
	responsePath := filepath.Join(p.OutputDir, "responses", "consolidate-response.txt")
	os.WriteFile(responsePath, []byte(result.Output), 0644)

	// Parse — consolidation returns an object with a "findings" key
	extracted := extractJSON(result.Output)
	var consolidated struct {
		Findings []*RawFinding `json:"findings"`
	}
	if err := json.Unmarshal([]byte(extracted), &consolidated); err != nil {
		// Try as plain array
		findings, err2 := ParseAgentResponse("CONSOLIDATED", extracted)
		if err2 != nil {
			return nil, fmt.Errorf("parse consolidation: %w", err)
		}
		return findings, nil
	}

	return consolidated.Findings, nil
}
