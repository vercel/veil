package registry

import (
	"fmt"

	"github.com/puzpuzpuz/xsync/v4"
)

// MemRegistry is an in-memory Registry that `veil render --build`
// populates directly from the build pipeline, then reads straight back —
// no registry.json / kind.json serialize-and-reparse round trip. build
// calls Add once per compiled kind; the render pipeline resolves them
// through the Registry interface (LoadKind).
//
// It holds only the default (unaliased) registry: the single project
// whose kinds were just compiled in this process. Backed by an xsync.Map
// so the parallel render pool's concurrent LoadKind calls need no
// external locking.
type MemRegistry struct {
	kinds *xsync.Map[string, *LoadedKind]
}

// NewMemRegistry returns an empty in-memory registry ready for Add.
func NewMemRegistry() *MemRegistry {
	return &MemRegistry{kinds: xsync.NewMap[string, *LoadedKind]()}
}

var _ Registry = (*MemRegistry)(nil)

// Add records a compiled kind under its bare name. The build pipeline
// calls this as it emits each kind; the *veilv1.Kind and schema bytes
// are handed over as-is, with no marshaling.
func (m *MemRegistry) Add(name string, k *LoadedKind) {
	m.kinds.Store(name, k)
}

// LoadKind implements Registry. The reference must resolve to the default
// registry — an in-memory registry only ever holds the project's own
// freshly-compiled kinds, so there are no aliases to resolve.
func (m *MemRegistry) LoadKind(ref string) (*LoadedKind, error) {
	alias, name, err := ParseRef(ref)
	if err != nil {
		return nil, err
	}
	if alias != "" {
		return nil, fmt.Errorf("kind %q: in-memory registry holds only the default registry (no alias %q)", ref, alias)
	}
	k, ok := m.kinds.Load(name)
	if !ok {
		return nil, fmt.Errorf("kind %q not found in the in-memory registry", name)
	}
	return k, nil
}
