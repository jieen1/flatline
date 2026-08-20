package adapters

import (
	"fmt"
	"sort"
	"sync"
)

// Registry provides deterministic lookup for the two MVP adapters.
type Registry struct {
	mu       sync.RWMutex
	bySource map[Source]Adapter
}

func NewRegistry() *Registry {
	return &Registry{bySource: make(map[Source]Adapter)}
}

func (r *Registry) Register(adapter Adapter) error {
	if adapter == nil {
		return fmt.Errorf("adapters: nil adapter")
	}
	if !adapter.Source().Valid() {
		return fmt.Errorf("adapters: unsupported source %q", adapter.Source())
	}
	if err := adapter.FieldMatrix().Validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.bySource[adapter.Source()]; exists {
		return fmt.Errorf("adapters: source %q already registered", adapter.Source())
	}
	r.bySource[adapter.Source()] = adapter
	return nil
}

func (r *Registry) Get(source Source) (Adapter, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	adapter, ok := r.bySource[source]
	return adapter, ok
}

func (r *Registry) Sources() []Source {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Source, 0, len(r.bySource))
	for source := range r.bySource {
		out = append(out, source)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
