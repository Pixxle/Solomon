package plugin

import (
	"context"
	"testing"
)

type dummyPlugin struct{ name string }

func (d *dummyPlugin) Name() string                    { return d.name }
func (d *dummyPlugin) Start(ctx context.Context) error { return nil }
func (d *dummyPlugin) Stop(ctx context.Context) error  { return nil }

func resetRegistry(t *testing.T) {
	t.Helper()
	orig := registry
	registry = map[string]Factory{}
	t.Cleanup(func() { registry = orig })
}

func TestRegisterAndNew(t *testing.T) {
	resetRegistry(t)

	Register("test-plugin", func(cfg PluginConfig, libs *SharedLibs) (Plugin, error) {
		return &dummyPlugin{name: "test-plugin"}, nil
	})

	p, err := New("test-plugin", PluginConfig{}, nil)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if p.Name() != "test-plugin" {
		t.Errorf("Name() = %q, want %q", p.Name(), "test-plugin")
	}
}

func TestNewUnknownPlugin(t *testing.T) {
	resetRegistry(t)

	_, err := New("nonexistent", PluginConfig{}, nil)
	if err == nil {
		t.Fatal("expected error for unknown plugin, got nil")
	}
}

func TestRegisteredTypes(t *testing.T) {
	resetRegistry(t)

	Register("alpha", func(cfg PluginConfig, libs *SharedLibs) (Plugin, error) {
		return &dummyPlugin{}, nil
	})
	Register("beta", func(cfg PluginConfig, libs *SharedLibs) (Plugin, error) {
		return &dummyPlugin{}, nil
	})

	types := RegisteredTypes()
	if len(types) != 2 {
		t.Fatalf("RegisteredTypes() returned %d, want 2", len(types))
	}

	found := map[string]bool{}
	for _, tt := range types {
		found[tt] = true
	}
	if !found["alpha"] || !found["beta"] {
		t.Errorf("RegisteredTypes() = %v, want [alpha, beta]", types)
	}
}
