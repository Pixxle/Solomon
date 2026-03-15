package plugin

import (
	"testing"
)

func TestSettingString(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		m        map[string]interface{}
		key      string
		fallback string
		want     string
	}{
		{"present", map[string]interface{}{"k": "v"}, "k", "fb", "v"},
		{"missing key", map[string]interface{}{}, "k", "fb", "fb"},
		{"empty string", map[string]interface{}{"k": ""}, "k", "fb", "fb"},
		{"non-string value", map[string]interface{}{"k": 42}, "k", "fb", "fb"},
		{"nil map", nil, "k", "fb", "fb"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SettingString(tt.m, tt.key, tt.fallback)
			if got != tt.want {
				t.Errorf("SettingString() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSettingBool(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		m    map[string]interface{}
		key  string
		want bool
	}{
		{"bool true", map[string]interface{}{"k": true}, "k", true},
		{"bool false", map[string]interface{}{"k": false}, "k", false},
		{"string true", map[string]interface{}{"k": "true"}, "k", true},
		{"string false", map[string]interface{}{"k": "false"}, "k", false},
		{"string other", map[string]interface{}{"k": "yes"}, "k", false},
		{"missing key", map[string]interface{}{}, "k", false},
		{"int value", map[string]interface{}{"k": 1}, "k", false},
		{"nil map", nil, "k", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SettingBool(tt.m, tt.key)
			if got != tt.want {
				t.Errorf("SettingBool() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSettingStringSlice(t *testing.T) {
	t.Parallel()
	fb := []string{"default"}

	tests := []struct {
		name     string
		m        map[string]interface{}
		key      string
		fallback []string
		want     []string
	}{
		{
			"[]interface{} with strings",
			map[string]interface{}{"k": []interface{}{"a", "b"}},
			"k", fb, []string{"a", "b"},
		},
		{
			"[]string",
			map[string]interface{}{"k": []string{"x", "y"}},
			"k", fb, []string{"x", "y"},
		},
		{
			"empty []interface{}",
			map[string]interface{}{"k": []interface{}{}},
			"k", fb, fb,
		},
		{
			"empty []string",
			map[string]interface{}{"k": []string{}},
			"k", fb, fb,
		},
		{
			"missing key",
			map[string]interface{}{},
			"k", fb, fb,
		},
		{
			"wrong type",
			map[string]interface{}{"k": "not a slice"},
			"k", fb, fb,
		},
		{
			"nil fallback",
			map[string]interface{}{},
			"k", nil, nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SettingStringSlice(tt.m, tt.key, tt.fallback)
			if len(got) != len(tt.want) {
				t.Fatalf("SettingStringSlice() len = %d, want %d", len(got), len(tt.want))
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("SettingStringSlice()[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestSettingInt(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		m        map[string]interface{}
		key      string
		fallback int
		want     int
	}{
		{"int value", map[string]interface{}{"k": 42}, "k", 0, 42},
		{"float64 value", map[string]interface{}{"k": 3.14}, "k", 0, 3},
		{"missing key", map[string]interface{}{}, "k", 99, 99},
		{"string value", map[string]interface{}{"k": "10"}, "k", 99, 99},
		{"nil map", nil, "k", 5, 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SettingInt(tt.m, tt.key, tt.fallback)
			if got != tt.want {
				t.Errorf("SettingInt() = %d, want %d", got, tt.want)
			}
		})
	}
}
