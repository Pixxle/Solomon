package plugin

import "fmt"

// Factory creates a plugin instance from its configuration and shared libraries.
type Factory func(cfg PluginConfig, libs *SharedLibs) (Plugin, error)

var registry = map[string]Factory{}

// Register adds a plugin factory to the global registry. Typically called from
// a plugin package's init() function.
func Register(name string, factory Factory) {
	registry[name] = factory
}

// New creates a new plugin instance by looking up the factory in the registry.
func New(name string, cfg PluginConfig, libs *SharedLibs) (Plugin, error) {
	factory, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("unknown plugin type: %q", name)
	}
	return factory(cfg, libs)
}

// RegisteredTypes returns the names of all registered plugin types.
func RegisteredTypes() []string {
	types := make([]string, 0, len(registry))
	for name := range registry {
		types = append(types, name)
	}
	return types
}
