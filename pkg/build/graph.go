package build

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/goccy/go-json"

	"github.com/vercel/veil/pkg/config"
)

// KindGraph is a directed graph of kinds and the dependency relationships
// declared between them. Each node is a kind (carrying its spec schema
// and source list, cached so per-kind generation doesn't re-read every
// file). Each edge encodes "consumer kind may depend on target kind"
// with the params schema the consumer must supply for that target.
//
// Edges are stored in both directions so callers can ask either side of
// the relationship — Node.Dependencies() walks outgoing edges (the
// targets a consumer may depend on), Node.Dependents() walks incoming
// edges (the consumers that may declare a dependency on this target).
//
// The graph is built once at the start of `veil build` and threaded
// through schema and TypeScript emission so each kind's output reflects
// what the rest of the registry knows about it.
type KindGraph struct {
	nodes      map[string]*KindNode
	nodesOrder []string
}

// KindNode is one kind in the graph: its name, parsed spec schema,
// declared source paths, and the dependency edges it participates in.
type KindNode struct {
	Name    string
	Spec    map[string]any
	Sources []string

	dependencies []*DependencyEdge // outgoing — what this kind may depend on
	dependents   []*DependencyEdge // incoming — who may depend on this kind
}

// DependencyEdge is one allowed (consumer → target) dependency
// relationship in the graph, plus the JSON Schema of the params the
// consumer must supply.
type DependencyEdge struct {
	Consumer     *KindNode
	Target       *KindNode
	ParamsSchema map[string]any
}

// BuildGraph constructs a KindGraph from the source-side registry. Errors
// when a dependent's params_path can't be read or parsed, or when an
// edge references a consumer kind not present in the registry.
func BuildGraph(kinds []*config.Kind) (*KindGraph, error) {
	g := &KindGraph{
		nodes:      make(map[string]*KindNode, len(kinds)),
		nodesOrder: make([]string, 0, len(kinds)),
	}

	// Pass 1: create nodes with spec + sources cached.
	for _, k := range kinds {
		spec, err := LoadSpecSchema(k)
		if err != nil {
			return nil, fmt.Errorf("%s: spec: %w", k.Name, err)
		}
		g.nodes[k.Name] = &KindNode{
			Name:    k.Name,
			Spec:    spec,
			Sources: append([]string(nil), k.Sources...),
		}
		g.nodesOrder = append(g.nodesOrder, k.Name)
	}

	// Pass 2: walk each kind's `dependents` declarations and add edges.
	// Each declaration is "I (target) accept consumer kind C with these
	// params" — that's an incoming edge on the target and an outgoing
	// edge on the consumer.
	for _, k := range kinds {
		target := g.nodes[k.Name]
		for _, d := range k.GetHooks().GetDependents() {
			consumer := g.nodes[d.Kind]
			if consumer == nil {
				return nil, fmt.Errorf("%s: dependents[%q]: no such kind in the registry", k.Name, d.Kind)
			}
			schema, err := loadParamsSchema(k, d.ParamsPath)
			if err != nil {
				return nil, fmt.Errorf("%s: dependents[%q] params: %w", k.Name, d.Kind, err)
			}
			edge := &DependencyEdge{
				Consumer:     consumer,
				Target:       target,
				ParamsSchema: schema,
			}
			target.dependents = append(target.dependents, edge)
			consumer.dependencies = append(consumer.dependencies, edge)
		}
	}

	// Sort each node's edge lists so generated output is deterministic
	// regardless of map iteration order during construction.
	for _, n := range g.nodes {
		sort.Slice(n.dependents, func(i, j int) bool {
			return n.dependents[i].Consumer.Name < n.dependents[j].Consumer.Name
		})
		sort.Slice(n.dependencies, func(i, j int) bool {
			return n.dependencies[i].Target.Name < n.dependencies[j].Target.Name
		})
	}

	return g, nil
}

// Node returns the node for the given kind name, or nil if absent.
func (g *KindGraph) Node(name string) *KindNode {
	if g == nil {
		return nil
	}
	return g.nodes[name]
}

// Nodes returns every node in registry-declaration order.
func (g *KindGraph) Nodes() []*KindNode {
	if g == nil {
		return nil
	}
	out := make([]*KindNode, 0, len(g.nodesOrder))
	for _, name := range g.nodesOrder {
		out = append(out, g.nodes[name])
	}
	return out
}

// Dependencies returns the outgoing edges from this node — the targets
// this kind may declare a dependency on. Sorted by target name.
func (n *KindNode) Dependencies() []*DependencyEdge {
	if n == nil {
		return nil
	}
	return n.dependencies
}

// Dependents returns the incoming edges to this node — the consumers
// that may declare a dependency on this kind. Sorted by consumer name.
func (n *KindNode) Dependents() []*DependencyEdge {
	if n == nil {
		return nil
	}
	return n.dependents
}

// loadParamsSchema reads the params_path JSON Schema for a dependent
// declaration, resolving relative paths against the kind directory.
func loadParamsSchema(k *config.Kind, p string) (map[string]any, error) {
	abs := p
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(k.Dir, p)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", p, err)
	}
	var s map[string]any
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", p, err)
	}
	return s, nil
}

// dependenciesProperty returns the JSON Schema fragment for the
// `dependencies` property on the given node's resource schema. Each
// outgoing edge becomes one branch of a discriminated `oneOf` keyed on
// `kind`. Returns nil when the node has no eligible targets — in which
// case the caller should omit the property so additionalProperties:false
// rejects any `dependencies` value.
func dependenciesProperty(n *KindNode) map[string]any {
	if n == nil || len(n.dependencies) == 0 {
		return nil
	}
	branches := make([]any, 0, len(n.dependencies))
	for _, edge := range n.dependencies {
		branches = append(branches, map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"kind", "name", "params"},
			"properties": map[string]any{
				"kind":   map[string]any{"const": edge.Target.Name},
				"name":   map[string]any{"type": "string", "minLength": 1},
				"params": edge.ParamsSchema,
			},
		})
	}
	return map[string]any{
		"type": "array",
		"items": map[string]any{
			"oneOf": branches,
		},
	}
}

// dependencyTypes emits the consumer-side TypeScript types for a node:
// one params interface per outgoing edge plus a `Dependency` union that
// the generated Resource template uses to type the optional
// `dependencies` array. Always emits `Dependency` — when there are no
// outgoing edges it resolves to `never`, so `Resource<Spec, Dependency>`
// stays well-typed everywhere.
func dependencyTypes(n *KindNode) (string, error) {
	var b strings.Builder
	deps := n.Dependencies()
	if len(deps) == 0 {
		b.WriteString("/** No targets accept this kind as a consumer; ")
		b.WriteString("`dependencies` may only be omitted or empty. */\n")
		b.WriteString("export type Dependency = never;\n")
		return b.String(), nil
	}
	branches := make([]string, 0, len(deps))
	for _, edge := range deps {
		paramsType := PascalCase(edge.Target.Name) + "DependencyParams"
		iface, err := interfaceFromSchemaMap(paramsType, edge.ParamsSchema)
		if err != nil {
			return "", fmt.Errorf("dependency params for target %q: %w", edge.Target.Name, err)
		}
		b.WriteString(iface)
		b.WriteString("\n")
		branches = append(branches, fmt.Sprintf(`{ kind: %q; name: string; params: %s }`, edge.Target.Name, paramsType))
	}
	b.WriteString("/** A single dependency this kind may declare. The discriminator\n")
	b.WriteString(" *  `kind` selects the params shape. */\n")
	b.WriteString("export type Dependency =\n  | ")
	b.WriteString(strings.Join(branches, "\n  | "))
	b.WriteString(";\n")
	return b.String(), nil
}

// dependentInterfaces emits the target-side TypeScript types for a node:
// for each consumer kind that may depend on this one, a replicated
// consumer Spec / FS / Params interface plus a per-consumer
// DependentHookContext and DependentHook. Returns "" when the node has
// no incoming edges.
func dependentInterfaces(n *KindNode) (string, error) {
	dependents := n.Dependents()
	if len(dependents) == 0 {
		return "", nil
	}
	selfTypeName := PascalCase(n.Name) + "Spec"

	var b strings.Builder
	b.WriteString("// ---- Dependent hook types ----------------------------------------------\n")
	b.WriteString("// One block per consumer kind that may declare a dependency on this\n")
	b.WriteString("// kind. Each block replicates the consumer's spec / FS shape so the\n")
	b.WriteString("// per-consumer hook receives concretely-typed `consumer` and `fs`.\n\n")

	for _, edge := range dependents {
		consumer := edge.Consumer
		consumerPascal := PascalCase(consumer.Name)
		consumerSpecName := consumerPascal + "Spec"
		consumerFSName := consumerPascal + "FS"
		paramsName := consumerPascal + "Params"
		ctxName := consumerPascal + "DependentHookContext"
		hookName := consumerPascal + "DependentHook"

		consumerSpec, err := interfaceFromSchemaMap(consumerSpecName, consumer.Spec)
		if err != nil {
			return "", fmt.Errorf("consumer %q spec: %w", consumer.Name, err)
		}
		consumerFS, err := fsInterfaceNamed(consumerFSName, consumer.Sources)
		if err != nil {
			return "", fmt.Errorf("consumer %q fs: %w", consumer.Name, err)
		}
		paramsIface, err := interfaceFromSchemaMap(paramsName, edge.ParamsSchema)
		if err != nil {
			return "", fmt.Errorf("consumer %q params: %w", consumer.Name, err)
		}

		b.WriteString(consumerSpec)
		b.WriteString("\n")
		b.WriteString(consumerFS)
		b.WriteString("\n")
		b.WriteString(paramsIface)
		b.WriteString("\n")

		fmt.Fprintf(&b, "export interface %s {\n", ctxName)
		fmt.Fprintf(&b, "  /** This kind's resolved resource. */\n")
		fmt.Fprintf(&b, "  self: Resource<%s, Dependency>;\n", selfTypeName)
		fmt.Fprintf(&b, "  /** The consumer resource that declared a dependency on us. */\n")
		fmt.Fprintf(&b, "  consumer: Resource<%s>;\n", consumerSpecName)
		fmt.Fprintf(&b, "  /** Params the consumer supplied for this dependency. */\n")
		fmt.Fprintf(&b, "  params: %s;\n", paramsName)
		b.WriteString("  vars: RegistryVariables;\n")
		b.WriteString("  root: string;\n")
		b.WriteString("  std: Std;\n")
		b.WriteString("  os: Os;\n")
		b.WriteString("  fetch: Fetch;\n")
		b.WriteString("}\n\n")

		fmt.Fprintf(&b, "export interface %s {\n", hookName)
		fmt.Fprintf(&b, "  /** Runs after the consumer's render hooks complete. Mutates the\n")
		fmt.Fprintf(&b, "   *  consumer's bundle to wire it up against this resource. */\n")
		fmt.Fprintf(&b, "  render(ctx: %s, fs: %s): %s | void | Promise<%s | void>;\n", ctxName, consumerFSName, consumerFSName, consumerFSName)
		b.WriteString("}\n\n")
	}
	return b.String(), nil
}
