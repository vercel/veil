package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/vercel/veil/pkg/config"
	"github.com/vercel/veil/pkg/registry"
	"github.com/vercel/veil/pkg/resource"
)

const (
	graphFormatTree    = "tree"
	graphFormatMermaid = "mermaid"
	graphFormatDot     = "dot"
)

// Graph returns the "graph" subcommand.
func Graph() *cli.Command {
	configDefault := "veil.json"
	if cwd, err := os.Getwd(); err == nil {
		if reg, err := config.Discover(cwd); err == nil {
			configDefault = filepath.Join(reg.Root, "veil.json")
		}
	}

	return &cli.Command{
		Name:      "graph",
		Usage:     "Visualize the dependency graph rooted at a resource file",
		UsageText: "veil graph <path> [flags]",
		Arguments: []cli.Argument{
			&cli.StringArg{
				Name:      "path",
				UsageText: "Path to the resource JSON file to graph",
			},
		},
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  "config",
				Usage: "Path to veil.json",
				Value: configDefault,
			},
			&cli.StringFlag{
				Name:  "format",
				Usage: "Output format: \"tree\" (default), \"mermaid\", or \"dot\"",
				Value: graphFormatTree,
			},
		},
		Action: runGraph,
	}
}

func runGraph(ctx context.Context, c *cli.Command) error {
	pathArg := c.StringArg("path")
	if pathArg == "" {
		return fmt.Errorf("graph: path is required (pass a resource file)")
	}
	format := c.String("format")
	switch format {
	case graphFormatTree, graphFormatMermaid, graphFormatDot:
	default:
		return fmt.Errorf("graph: unknown --format %q (want tree|mermaid|dot)", format)
	}

	reg, err := registry.LoadProject(c.String("config"))
	if err != nil {
		return err
	}

	projectFS := os.DirFS(reg.Root)
	handles, err := resource.Discover(ctx, projectFS, reg.ResourceDiscovery.GetPaths())
	if err != nil {
		return fmt.Errorf("discovering resources: %w", err)
	}
	catalog, err := resource.NewCatalog(projectFS, handles)
	if err != nil {
		return fmt.Errorf("building resource catalog: %w", err)
	}

	absPath, err := filepath.Abs(pathArg)
	if err != nil {
		return fmt.Errorf("resolving path: %w", err)
	}
	rel, err := filepath.Rel(reg.Root, absPath)
	if err != nil {
		return fmt.Errorf("resolving %s against project root: %w", pathArg, err)
	}
	if strings.HasPrefix(rel, "..") {
		return fmt.Errorf("%s is outside the project root %s", pathArg, reg.Root)
	}
	root, err := catalog.LoadByPath(filepath.ToSlash(rel))
	if err != nil {
		return err
	}

	g, err := buildResourceGraph(catalog, root)
	if err != nil {
		return err
	}

	w := c.Root().Writer
	switch format {
	case graphFormatTree:
		return renderTree(w, g)
	case graphFormatMermaid:
		return renderMermaid(w, g)
	case graphFormatDot:
		return renderDot(w, g)
	}
	return nil
}

// resourceGraph is the resolved dependency graph rooted at one resource.
// Nodes are keyed by "kind/name"; edges record params (so the tree
// rendering can surface what the consumer asked for).
type resourceGraph struct {
	rootID string
	nodes  map[string]*graphNode
	edges  map[string][]*graphEdge
}

type graphNode struct {
	kind string
	name string
	path string
}

type graphEdge struct {
	to     string
	params map[string]any
}

func nodeID(kind, name string) string { return kind + "/" + name }

// buildResourceGraph walks dependencies breadth-first from root, loading
// each target via the catalog. Resources visited more than once collapse
// to a single node (graph, not tree); cycles are tolerated and pruned.
// A missing target is reported with the chain that led to it.
func buildResourceGraph(catalog resource.Catalog, root *resource.Resource) (*resourceGraph, error) {
	rootID := nodeID(root.GetMetadata().GetKind(), root.GetMetadata().GetName())
	g := &resourceGraph{
		rootID: rootID,
		nodes:  map[string]*graphNode{},
		edges:  map[string][]*graphEdge{},
	}
	g.nodes[rootID] = &graphNode{
		kind: root.GetMetadata().GetKind(),
		name: root.GetMetadata().GetName(),
		path: root.Path,
	}

	type queued struct {
		res *resource.Resource
		id  string
	}
	queue := []queued{{res: root, id: rootID}}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, dep := range cur.res.GetDependencies() {
			depID := nodeID(dep.GetKind(), dep.GetName())
			edge := &graphEdge{to: depID, params: dep.GetParams().AsMap()}
			g.edges[cur.id] = append(g.edges[cur.id], edge)
			if _, seen := g.nodes[depID]; seen {
				continue
			}
			target, err := catalog.LoadResource(dep.GetKind(), dep.GetName())
			if err != nil {
				return nil, fmt.Errorf("loading dependency %s required by %s: %w", depID, cur.id, err)
			}
			g.nodes[depID] = &graphNode{
				kind: target.GetMetadata().GetKind(),
				name: target.GetMetadata().GetName(),
				path: target.Path,
			}
			queue = append(queue, queued{res: target, id: depID})
		}
	}
	return g, nil
}

// renderTree prints a Unicode box-drawing tree rooted at the graph's
// root. Nodes that appear under multiple parents (shared deps) print
// once per occurrence with a "(↺)" marker on repeats so the operator
// can see structure without the renderer claiming false uniqueness.
func renderTree(w writer, g *resourceGraph) error {
	root := g.nodes[g.rootID]
	if _, err := fmt.Fprintf(w, "%s\n", formatNode(root)); err != nil {
		return err
	}
	visited := map[string]bool{g.rootID: true}
	return walkTree(w, g, g.rootID, "", visited)
}

func walkTree(w writer, g *resourceGraph, id, prefix string, visited map[string]bool) error {
	edges := append([]*graphEdge(nil), g.edges[id]...)
	sort.SliceStable(edges, func(i, j int) bool { return edges[i].to < edges[j].to })
	for i, edge := range edges {
		last := i == len(edges)-1
		branch := "├── "
		nextPrefix := prefix + "│   "
		if last {
			branch = "└── "
			nextPrefix = prefix + "    "
		}
		node := g.nodes[edge.to]
		line := prefix + branch + formatNode(node)
		if visited[edge.to] {
			line += " (↺)"
		}
		if len(edge.params) > 0 {
			line += "  " + formatParams(edge.params)
		}
		if _, err := fmt.Fprintln(w, line); err != nil {
			return err
		}
		if visited[edge.to] {
			continue
		}
		visited[edge.to] = true
		if err := walkTree(w, g, edge.to, nextPrefix, visited); err != nil {
			return err
		}
	}
	return nil
}

func formatNode(n *graphNode) string {
	if n.path == "" {
		return fmt.Sprintf("%s/%s", n.kind, n.name)
	}
	return fmt.Sprintf("%s/%s  (%s)", n.kind, n.name, n.path)
}

// formatParams renders a dependency's params map compactly on one line.
// Keys are sorted so output is stable across runs.
func formatParams(params map[string]any) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", k, params[k]))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// renderMermaid emits a flowchart-LR mermaid graph. Node IDs are
// sanitized "kind_name" so they're valid mermaid identifiers; the
// human-readable label keeps the original "kind/name".
func renderMermaid(w writer, g *resourceGraph) error {
	if _, err := fmt.Fprintln(w, "flowchart LR"); err != nil {
		return err
	}
	ids := make([]string, 0, len(g.nodes))
	for id := range g.nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		n := g.nodes[id]
		if _, err := fmt.Fprintf(w, "    %s[%q]\n", mermaidID(id), fmt.Sprintf("%s/%s", n.kind, n.name)); err != nil {
			return err
		}
	}
	for _, fromID := range ids {
		edges := append([]*graphEdge(nil), g.edges[fromID]...)
		sort.SliceStable(edges, func(i, j int) bool { return edges[i].to < edges[j].to })
		for _, edge := range edges {
			if _, err := fmt.Fprintf(w, "    %s --> %s\n", mermaidID(fromID), mermaidID(edge.to)); err != nil {
				return err
			}
		}
	}
	return nil
}

// renderDot emits a graphviz DOT representation. Pipe through `dot
// -Tsvg` (or similar) to render. Node labels carry "kind/name"; ID
// strings are quoted so slashes and hyphens are accepted verbatim.
func renderDot(w writer, g *resourceGraph) error {
	if _, err := fmt.Fprintln(w, "digraph veil {"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, "    rankdir=LR;"); err != nil {
		return err
	}
	ids := make([]string, 0, len(g.nodes))
	for id := range g.nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if _, err := fmt.Fprintf(w, "    %q [label=%q];\n", id, id); err != nil {
			return err
		}
	}
	for _, fromID := range ids {
		edges := append([]*graphEdge(nil), g.edges[fromID]...)
		sort.SliceStable(edges, func(i, j int) bool { return edges[i].to < edges[j].to })
		for _, edge := range edges {
			if _, err := fmt.Fprintf(w, "    %q -> %q;\n", fromID, edge.to); err != nil {
				return err
			}
		}
	}
	_, err := fmt.Fprintln(w, "}")
	return err
}

// mermaidID maps a node ID like "service/api-feature-flags" into a
// mermaid-safe identifier ("service_api_feature_flags").
func mermaidID(id string) string {
	r := strings.NewReplacer("/", "_", "-", "_", ".", "_")
	return r.Replace(id)
}

// writer is the small subset of io.Writer the renderers need; we pull
// it from c.Root().Writer (cli sets the same writer the printer uses).
type writer interface {
	Write(p []byte) (n int, err error)
}
