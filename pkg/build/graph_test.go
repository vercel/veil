package build

import (
	"strings"
	"testing"
)

// In package mode a target kind imports + re-exports each consumer's spec / FS
// from the consumer's own module instead of replicating them, so a change to a
// widely-consumed spec regenerates only the consumer's module. In inline mode
// (single-file output) it still replicates them.
func TestPackageModeImportsConsumerSpecInsteadOfReplicating(t *testing.T) {
	service := &KindNode{
		Name: "service",
		Spec: map[string]any{
			"type":       "object",
			"properties": map[string]any{"app_port": map[string]any{"type": "number"}},
		},
		Sources: []string{"./sources/app.yaml"},
	}
	cache := &KindNode{
		Name:    "cache",
		Spec:    map[string]any{"type": "object", "properties": map[string]any{}},
		Sources: []string{"./sources/app.yaml"},
	}
	cache.dependents = []*DependencyEdge{{
		Consumer: service,
		Target:   cache,
		ParamsSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"env_var": map[string]any{"type": "string"}},
		},
	}}

	imports := dependentSpecImports(cache)
	for _, want := range []string{
		"import type { ServiceSpec, FS as ServiceFS } from './service';",
		"export type { ServiceSpec, ServiceFS };",
	} {
		if !strings.Contains(imports, want) {
			t.Errorf("dependentSpecImports missing %q in:\n%s", want, imports)
		}
	}

	pkg, err := dependentInterfaces(cache, true)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(pkg, "export interface ServiceSpec") ||
		strings.Contains(pkg, "export interface ServiceFS") {
		t.Errorf("package mode must not replicate the consumer spec/FS:\n%s", pkg)
	}
	if !strings.Contains(pkg, "consumer: Resource<ServiceSpec>") {
		t.Errorf("package mode must reference the imported ServiceSpec:\n%s", pkg)
	}

	inline, err := dependentInterfaces(cache, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(inline, "export interface ServiceSpec") ||
		!strings.Contains(inline, "export interface ServiceFS") {
		t.Errorf("inline mode must replicate the consumer spec/FS:\n%s", inline)
	}
}

// A kind no other kind consumes needs no consumer imports.
func TestDependentSpecImportsEmptyWhenNoDependents(t *testing.T) {
	n := &KindNode{Name: "service", Spec: map[string]any{"type": "object"}}
	if got := dependentSpecImports(n); got != "" {
		t.Errorf("expected empty imports for a kind with no consumers, got %q", got)
	}
}

// In inline mode, a target kind's replicated <Consumer>FS must type a
// schema-declared source's accessor to File<T> the same way the
// consumer's own FS would — not fall back to the plain File a nil
// typeNameByPath produces. Package mode never replicates FS at all
// (it imports the consumer's own module instead), so this only
// applies to the inline branch.
func TestInlineModeTypesReplicatedConsumerFSToSchema(t *testing.T) {
	service := &KindNode{
		Name:            "service",
		Spec:            map[string]any{"type": "object", "properties": map[string]any{}},
		Sources:         []string{"./sources/app.yaml"},
		SourceTypeNames: map[string]string{"./sources/app.yaml": "ServiceDeployment"},
		SourceTypes:     "export interface ServiceDeployment {\n  replicas: number;\n}\n\n",
	}
	cache := &KindNode{
		Name: "cache",
		Spec: map[string]any{"type": "object", "properties": map[string]any{}},
	}
	cache.dependents = []*DependencyEdge{{
		Consumer:     service,
		Target:       cache,
		ParamsSchema: map[string]any{"type": "object", "properties": map[string]any{}},
	}}

	inline, err := dependentInterfaces(cache, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(inline, "export interface ServiceDeployment {") {
		t.Errorf("inline mode must emit the consumer's schema-derived interface:\n%s", inline)
	}
	if !strings.Contains(inline, "getSourcesAppYaml(): File<ServiceDeployment>;") {
		t.Errorf("replicated ServiceFS must type its schema-declared accessor to File<ServiceDeployment>:\n%s", inline)
	}

	pkg, err := dependentInterfaces(cache, true)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(pkg, "export interface ServiceDeployment") {
		t.Errorf("package mode must not replicate the consumer's schema types:\n%s", pkg)
	}
}
