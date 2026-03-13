package claude

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"text/template"
)

var promptsDir string

func init() {
	// Find the prompts directory relative to the binary or working directory
	if dir, err := os.Getwd(); err == nil {
		promptsDir = filepath.Join(dir, "prompts")
	}
}

// SetPromptsDir overrides the prompts directory (useful for testing or when binary is elsewhere).
func SetPromptsDir(dir string) {
	promptsDir = dir
}

func getPromptsDir() string {
	if promptsDir != "" {
		if _, err := os.Stat(promptsDir); err == nil {
			return promptsDir
		}
	}
	// Try relative to the binary
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Join(filepath.Dir(exe), "prompts")
		if _, err := os.Stat(dir); err == nil {
			return dir
		}
	}
	// Try relative to source
	_, filename, _, ok := runtime.Caller(0)
	if ok {
		dir := filepath.Join(filepath.Dir(filename), "..", "..", "prompts")
		if _, err := os.Stat(dir); err == nil {
			return dir
		}
	}
	return "prompts"
}

// RenderPrompt renders a Go template from the prompts directory with the given context.
func RenderPrompt(templateName string, data interface{}) (string, error) {
	tmplPath := filepath.Join(getPromptsDir(), templateName)
	tmplData, err := os.ReadFile(tmplPath)
	if err != nil {
		return "", fmt.Errorf("reading template %s: %w", templateName, err)
	}

	funcMap := template.FuncMap{
		"add": func(a, b int) int { return a + b },
	}
	tmpl, err := template.New(templateName).Funcs(funcMap).Parse(string(tmplData))
	if err != nil {
		return "", fmt.Errorf("parsing template %s: %w", templateName, err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("executing template %s: %w", templateName, err)
	}

	return buf.String(), nil
}

// StripCodeFence removes markdown code fences (```json ... ```) that LLMs
// sometimes wrap around JSON output despite being told not to.
func StripCodeFence(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		if i := strings.Index(s, "\n"); i != -1 {
			s = s[i+1:]
		}
		if i := strings.LastIndex(s, "```"); i != -1 {
			s = s[:i]
		}
	}
	return strings.TrimSpace(s)
}
