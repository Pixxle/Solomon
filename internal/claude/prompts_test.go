package claude

import (
	"testing"
)

func TestStripCodeFence(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"no fence", `{"key": "value"}`, `{"key": "value"}`},
		{
			"json fence",
			"```json\n{\"key\": \"value\"}\n```",
			`{"key": "value"}`,
		},
		{
			"bare fence",
			"```\n{\"key\": \"value\"}\n```",
			`{"key": "value"}`,
		},
		{
			"with surrounding whitespace",
			"  \n```json\n{\"key\": \"value\"}\n```\n  ",
			`{"key": "value"}`,
		},
		{"empty string", "", ""},
		{"only whitespace", "   \n\t  ", ""},
		{
			"fence with extra content after closing",
			"```json\n[1,2,3]\n```\n",
			"[1,2,3]",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StripCodeFence(tt.input)
			if got != tt.want {
				t.Errorf("StripCodeFence() = %q, want %q", got, tt.want)
			}
		})
	}
}
