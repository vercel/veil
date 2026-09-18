// Package e2e drives the real veil binary against the playground
// project in ./playground — a miniature Acme infrastructure with a
// global VPC, two databases, a cache, a secret, a platform bundle and
// two services. Unlike the package tests, nothing here is faked: the
// binary is built from source, run as a subprocess, and judged on the
// files it leaves on disk.
//
// The suite type and its helpers live in e2e_helpers_test.go.
package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/suite"
)

func TestE2ESuite(t *testing.T) {
	suite.Run(t, new(E2ESuite))
}

// TestBuildCompilesEveryKind checks what SetupSuite's build produced.
// The registry is not in the repo, so everything here rests on it.
func (s *E2ESuite) TestBuildCompilesEveryKind() {
	for _, kind := range []string{"vpc", "postgres", "redis", "platform", "service"} {
		s.FileExists(filepath.Join(s.root, "public", "r", kind, "kind.json"), "kind %q", kind)
	}
	s.FileExists(filepath.Join(s.root, "public", "r", "registry.json"))
}

// TestForwardedDependenciesReachTheConsumer is the headline: checkout
// declares exactly one dependency — the platform — yet is wired to the
// database, cache and VPC the platform itself depends on, because the
// platform kind sets forward_dependencies.
func (s *E2ESuite) TestForwardedDependenciesReachTheConsumer() {
	env := s.read(s.render("resources/services/checkout.json"), "checkout", "sources/env")

	s.Contains(env, "ACME_PLATFORM=commerce-platform", "the platform's own dependent hook")
	s.Contains(env, "ORDERS_DATABASE_URL=postgres://", "database forwarded through the platform")
	s.Contains(env, "SESSIONS_REDIS_URL=redis://", "cache forwarded through the platform")
	s.Contains(env, "ACME_VPC=acme-global", "VPC forwarded through the platform")
	// Params travel with the forwarded edge — these are the platform's.
	s.Contains(env, "pool=25")
}

// TestUnforwardedDependencyStaysPrivate is the other half: billing marks
// only its database forwarded, so nothing it depends on leaks sideways —
// it never sees the orders database or the sessions cache.
func (s *E2ESuite) TestUnforwardedDependencyStaysPrivate() {
	env := s.read(s.render("resources/services/billing.json"), "billing", "sources/env")

	s.Contains(env, "BILLING_DATABASE_URL=postgres://")
	s.NotContains(env, "ORDERS_DATABASE_URL")
	s.NotContains(env, "SESSIONS_REDIS_URL")
	s.NotContains(env, "ACME_PLATFORM")
}

// TestPostRenderNormalizesAfterEveryDependent proves ordering: the env
// file is assembled by several dependent hooks in whatever order the
// graph resolves, then sorted by the kind's post_render pass.
func (s *E2ESuite) TestPostRenderNormalizesAfterEveryDependent() {
	env := s.read(s.render("resources/services/checkout.json"), "checkout", "sources/env")

	lines := strings.Split(strings.TrimSpace(env), "\n")
	s.Require().Greater(len(lines), 1)
	s.True(sortedAscending(lines), "post_render should have sorted the env file: %q", lines)
}

// TestTypedSourcesRoundTripPerEncoding covers the source codecs: the
// service's deployment is YAML and the VPC's network is JSON, and each
// hook mutates a parsed object without ever naming its encoding.
func (s *E2ESuite) TestTypedSourcesRoundTripPerEncoding() {
	deployment := s.read(s.render("resources/services/checkout.json"), "checkout", "sources/deployment.yaml")
	s.Contains(deployment, "image: acme/checkout:1.4.2")
	s.Contains(deployment, "region: us-east-1", "render-time variable reached the source")

	// JSON sources round-trip through JSON.stringify, so they come back
	// compact rather than indented — the encoding is the source's, not
	// the hook author's choice.
	network := s.read(s.render("resources/network/acme-global.json"), "acme-global", "sources/network.json")
	s.JSONEq(`{"cidr":"10.0.0.0/16","region":"us-east-1","zones":3}`, network)
}

// TestOverlayAppliesOnMatchingVariable renders the same resource twice
// and checks the production overlay only takes effect for production.
func (s *E2ESuite) TestOverlayAppliesOnMatchingVariable() {
	staging := s.read(s.render("resources/services/checkout.json"), "checkout", "sources/deployment.yaml")
	s.Contains(staging, "replicas: 2")

	production := s.read(
		s.render("resources/services/checkout.json", "--var", "environment=production"),
		"checkout", "sources/deployment.yaml")
	s.Contains(production, "replicas: 12")
}

// TestRenderIsDeterministic guards against map-ordering leaking into
// output: two renders of the same inputs must be byte-identical.
func (s *E2ESuite) TestRenderIsDeterministic() {
	first := s.read(s.render("resources/services/checkout.json"), "checkout", "sources/env")
	second := s.read(s.render("resources/services/checkout.json"), "checkout", "sources/env")
	s.Equal(first, second)
}

// TestGraphReportsTheEffectiveEdges checks `veil graph` agrees with what
// render actually applies — the forwarded targets, not just the declared
// one.
func (s *E2ESuite) TestGraphReportsTheEffectiveEdges() {
	out, err := s.run("graph", "resources/services/checkout.json", "--format", "tree")
	s.Require().NoError(err, out)
	for _, node := range []string{"platform/commerce-platform", "postgres/orders-db", "redis/sessions-cache", "vpc/acme-global"} {
		s.Contains(out, node)
	}
}

// ---- failure paths -----------------------------------------------------
//
// Everything above proves veil produces the right output. These prove it
// refuses to produce the wrong one — which is the half that matters when
// someone is editing infrastructure.

// TestValidateHookRejectsInProduction covers a kind-authored rule that
// JSON Schema can't express, because it spans the spec and a variable:
// a small single-AZ database is fine in staging and not in production.
func (s *E2ESuite) TestValidateHookRejectsInProduction() {
	out := s.renderFails("resources/edge-cases/tiny-prod-db.json", "--var", "environment=production")
	s.Contains(out, "production databases must be multi-AZ")
	s.Contains(out, `production databases must be larger than "small"`)
	s.Contains(out, "spec.multiAz", "the issue should carry its path")

	// The same resource is legal in staging.
	dir := s.render("resources/edge-cases/tiny-prod-db.json")
	s.FileExists(filepath.Join(dir, "tiny-prod-db", "sources", "database.json"))
}

// TestValidateHookRejectsServiceWithoutDatabase covers the other validate
// hook: a service that ends up with no connection string, however it was
// supposed to get one.
func (s *E2ESuite) TestValidateHookRejectsServiceWithoutDatabase() {
	out := s.renderFails("resources/edge-cases/orphan-service.json")
	s.Contains(out, "service has no database connection")
}

// TestHookWriteRejectedBySourceSchema is the setContent guarantee: the
// secret's hook writes a string where its source schema demands an
// integer, and the render fails at that call site — named hook, named
// field — rather than emitting a source that violates its own schema.
func (s *E2ESuite) TestHookWriteRejectedBySourceSchema() {
	out := s.renderFails("resources/edge-cases/bad-rotation-secret.json")
	s.Contains(out, "got string, want integer")
	s.Contains(out, "/rotationDays")
	s.Contains(out, "set-rotation.ts", "the error should point at the hook that wrote it")
}

// TestMissingDependencyFailsWithItsChain proves a dangling reference is
// caught when the resource loads, and reports how the walk reached it.
func (s *E2ESuite) TestMissingDependencyFailsWithItsChain() {
	dir := s.sandbox()
	s.write(dir, "resources/edge-cases/dangling.json", `{
  "metadata": { "kind": "service", "name": "dangling" },
  "spec": { "image": "acme/dangling:0.1.0", "replicas": 1 },
  "dependencies": [{ "kind": "postgres", "name": "no-such-db", "params": { "envVar": "X_URL" } }]
}`)
	out, err := s.runIn(dir, "render", "resources/edge-cases/dangling.json", "--out", s.T().TempDir())
	s.Require().Error(err, out)
	msg := s.errorMessage(out)
	s.Contains(msg, "service/dangling -> postgres/no-such-db")
	s.Contains(msg, "not in catalog")
}

// TestDependencyCycleFailsWithItsLineage pins the cycle report: the chain
// that closes the loop, so the author can see which edge to cut. It runs
// in a sandbox because a cycle anywhere in a project fails `veil build`
// for the whole project, not just the resources involved.
func (s *E2ESuite) TestDependencyCycleFailsWithItsLineage() {
	dir := s.sandbox()
	for _, pair := range [][2]string{{"cycle-a", "cycle-b"}, {"cycle-b", "cycle-a"}} {
		s.write(dir, "resources/edge-cases/"+pair[0]+".json", `{
  "metadata": { "kind": "platform", "name": "`+pair[0]+`" },
  "spec": { "owner": "team-loops" },
  "dependencies": [{ "kind": "platform", "name": "`+pair[1]+`" }]
}`)
	}
	out, err := s.runIn(dir, "render", "resources/edge-cases/cycle-a.json", "--out", s.T().TempDir())
	s.Require().Error(err, out)
	s.Contains(s.errorMessage(out), "platform/cycle-a -> platform/cycle-b -> platform/cycle-a")

	// And it stops the build too, rather than compiling a broken graph.
	out, err = s.runIn(dir, "build")
	s.Require().Error(err, out)
	s.Contains(s.errorMessage(out), "dependency cycle")
}

// TestPrivateDependencyStaysWithItsOwner is the shape the playground
// ships: orders-db holds a secret, the secret kind accepts postgres and
// nothing else, and the edge is not forwarded — so a service adopting
// the platform inherits the database and never the credential behind it.
func (s *E2ESuite) TestPrivateDependencyStaysWithItsOwner() {
	env := s.read(s.render("resources/services/checkout.json"), "checkout", "sources/env")
	s.Contains(env, "ORDERS_DATABASE_URL=", "the platform's database is inherited")
	s.NotContains(env, "signing-key", "the database's secret is not")

	// The database itself is wired to it.
	dir := s.render("resources/data/orders-db.json")
	s.Contains(s.read(dir, "orders-db", "sources/secret-ref"), "secret=commerce-signing-key")
}

// TestForwardingUnacceptableDependencyFails covers the mistake this
// rejects: marking a private dependency `forward: true` pushes it at a
// consumer whose kind the target never agreed to serve. Dropping it
// silently would leave the service missing wiring it cannot see, so the
// load fails and names both ends and the fix.
func (s *E2ESuite) TestForwardingUnacceptableDependencyFails() {
	dir := s.sandbox()
	// orders-db forwards its credential — which the secret kind accepts
	// from postgres, but never from a service.
	s.write(dir, "resources/data/orders-db.json", `{
  "metadata": { "kind": "postgres", "name": "orders-db" },
  "spec": { "size": "medium", "storageGb": 200, "multiAz": true },
  "dependencies": [
    { "kind": "vpc", "name": "acme-global" },
    { "kind": "secret", "name": "commerce-signing-key", "forward": true }
  ]
}`)
	out, err := s.runIn(dir, "render", "resources/services/checkout.json", "--out", s.T().TempDir())
	s.Require().Error(err, out)

	// The failure surfaces at the nearest consumer that cannot take it —
	// the platform, which absorbs the database's forwarded set before the
	// service ever sees it. That is also where the fix belongs.
	msg := s.errorMessage(out)
	s.Contains(msg, "cannot inherit secret/commerce-signing-key")
	s.Contains(msg, "forwarded by postgres/orders-db")
	s.Contains(msg, `kind "secret" declares no dependents for kind "platform"`)
	s.Contains(msg, "stop forwarding that dependency", "the error should say how to fix it")
	s.Contains(msg, "commerce-platform.json", "and name the resource that inherited it")
}

// ---- the rest of the CLI ------------------------------------------------

// TestConcurrentRenderSharesOneCatalog renders several resources in one
// invocation, which fans them out across a goroutine pool that shares a
// single catalog. checkout and billing both reach the VPC, so this is the
// path where resolution has to be safe under concurrent load.
func (s *E2ESuite) TestConcurrentRenderSharesOneCatalog() {
	out := s.T().TempDir()
	stdout, err := s.run("render",
		"resources/services/checkout.json",
		"resources/services/billing.json",
		"resources/network/acme-global.json",
		"resources/data/orders-db.json",
		"--out", out, "--quiet")
	s.Require().NoError(err, stdout)

	// Each got its own wiring, and neither picked up the other's.
	checkout := s.read(out, "checkout", "sources/env")
	billing := s.read(out, "billing", "sources/env")
	s.Contains(checkout, "ORDERS_DATABASE_URL=")
	s.NotContains(checkout, "BILLING_DATABASE_URL=")
	s.Contains(billing, "BILLING_DATABASE_URL=")
	s.NotContains(billing, "ORDERS_DATABASE_URL=")

	// The shared VPC resolved once but reached both.
	s.Contains(checkout, "ACME_VPC=acme-global")
	s.Contains(billing, "ACME_VPC=acme-global")
	s.FileExists(filepath.Join(out, "acme-global", "sources", "network.json"))
}

// TestRenderBuildUsesAnInMemoryRegistry covers --build: compile the kinds
// into memory and render off that, with nothing on disk. Proven by
// deleting public/ from a sandbox first.
func (s *E2ESuite) TestRenderBuildUsesAnInMemoryRegistry() {
	dir := s.sandbox()
	s.Require().NoError(os.RemoveAll(filepath.Join(dir, "public")))

	out := filepath.Join(s.T().TempDir(), "rendered")
	stdout, err := s.runIn(dir, "render", "resources/services/checkout.json", "--build", "--out", out)
	s.Require().NoError(err, stdout)
	s.Contains(readFile(s.T(), filepath.Join(out, "checkout", "sources", "env")), "ORDERS_DATABASE_URL=")

	// Still nothing on disk — the registry never left memory.
	s.NoDirExists(filepath.Join(dir, "public"))
}

// TestBuildIsReproducible guards the canonical-artifact formatting:
// sorted keys and stable output, so a rebuild is not a diff.
func (s *E2ESuite) TestBuildIsReproducible() {
	first := s.snapshotRegistry()
	out, err := s.run("build")
	s.Require().NoError(err, out)
	s.Equal(first, s.snapshotRegistry(), "a second build should be byte-identical")
}

// TestBuildSchemasOnlyOmitsTheRegistry covers the flag teams use to commit
// editor-facing schemas without the compiled bodies.
func (s *E2ESuite) TestBuildSchemasOnlyOmitsTheRegistry() {
	dir := s.sandbox()
	s.Require().NoError(os.RemoveAll(filepath.Join(dir, "public")))

	out, err := s.runIn(dir, "build", "--schemas-only")
	s.Require().NoError(err, out)
	s.FileExists(filepath.Join(dir, "public", "r", "service", "kind.schema.json"))
	s.NoFileExists(filepath.Join(dir, "public", "r", "service", "kind.json"))
	s.NoFileExists(filepath.Join(dir, "public", "r", "registry.json"))
}

// TestGraphRendersEveryFormat checks the three human renderings agree on
// the graph they are drawing.
func (s *E2ESuite) TestGraphRendersEveryFormat() {
	for _, format := range []string{"tree", "mermaid", "dot"} {
		s.Run(format, func() {
			out, err := s.run("graph", "resources/services/checkout.json", "--format", format, "--output", "pretty")
			s.Require().NoError(err, out)
			s.Contains(out, "orders-db")
			s.Contains(out, "sessions-cache")
		})
	}
	out, err := s.run("graph", "resources/services/checkout.json", "--format", "nonsense")
	s.Require().Error(err, out)
	s.Contains(s.errorMessage(out), "unknown --format")
}

// TestNewScaffoldsABuildableProject is the bootstrap path: `veil new` in
// an empty directory has to produce something that builds without edits.
func (s *E2ESuite) TestNewScaffoldsABuildableProject() {
	dir := s.T().TempDir()

	out, err := s.runIn(dir, "new", "kind", "widget")
	s.Require().NoError(err, out)
	s.FileExists(filepath.Join(dir, "veil.json"))
	s.FileExists(filepath.Join(dir, ".veil", "kinds", "widget", "kind.json"))
	s.FileExists(filepath.Join(dir, ".veil", "kinds", "widget", "hooks", "src", "veil-types.ts"))

	out, err = s.runIn(dir, "build")
	s.Require().NoError(err, out)

	out, err = s.runIn(dir, "new", "resource", "my-widget", "--kind", "widget")
	s.Require().NoError(err, out)

	// Scaffolding a resource into a project with no resource_discovery
	// adds the path, so the very next command can render it — no edit to
	// veil.json in between.
	s.Contains(readFile(s.T(), filepath.Join(dir, "veil.json")), `"my-widget.json"`)

	out, err = s.runIn(dir, "render", "my-widget.json", "--out", filepath.Join(dir, "out"))
	s.Require().NoError(err, out)
}

// TestNewResourceLeavesMatchingDiscoveryAlone is the other half: the
// playground already globs resources/**/*.json, so scaffolding into it
// must not append a redundant entry.
func (s *E2ESuite) TestNewResourceLeavesMatchingDiscoveryAlone() {
	dir := s.sandbox()
	before := readFile(s.T(), filepath.Join(dir, "veil.json"))

	out, err := s.runIn(dir, "new", "resource", "probe-svc",
		"--kind", "service", "--out", "resources/services/probe-svc.json")
	s.Require().NoError(err, out)

	s.FileExists(filepath.Join(dir, "resources", "services", "probe-svc.json"))
	s.Equal(before, readFile(s.T(), filepath.Join(dir, "veil.json")),
		"an existing pattern already covers it, so veil.json should be untouched")
}

// TestOverrideCopiesASourceForEditing covers `veil override`: it lifts a
// kind's source next to the resource so a team can hand-edit one file
// without forking the kind.
func (s *E2ESuite) TestOverrideCopiesASourceForEditing() {
	dir := s.sandbox()

	// --skip-hooks pins the file: without it the kind's render hook runs
	// after the override is applied and overwrites it again, which is the
	// point of the flag.
	out, err := s.runIn(dir, "override", "resources/services/billing.json", "sources/deployment.yaml", "--skip-hooks")
	s.Require().NoError(err, out)

	local := filepath.Join(dir, "resources", "services", "deployment.yaml")
	s.FileExists(local, "the source should be copied next to the resource")
	s.Contains(readFile(s.T(), filepath.Join(dir, "resources", "services", "billing.json")),
		"overrides", "the resource should record the override")

	// The override wins over whatever the kind's hooks produce.
	s.Require().NoError(os.WriteFile(local, []byte("image: overridden\nreplicas: 1\nport: 1\nregion: x\npublic: false\n"), 0o644))
	rendered := filepath.Join(s.T().TempDir(), "out")
	out, err = s.runIn(dir, "render", "resources/services/billing.json", "--out", rendered)
	s.Require().NoError(err, out)
	s.Contains(readFile(s.T(), filepath.Join(rendered, "billing", "sources", "deployment.yaml")), "image: overridden")
}

// TestGeneratedTypesDescribeEachSource is what hook authors program
// against: an accessor per declared source, typed by that source's own
// schema, plus the dependent-hook surface for each registered consumer.
func (s *E2ESuite) TestGeneratedTypesDescribeEachSource() {
	types := readFile(s.T(), filepath.Join(s.root, ".veil", "kinds", "service", "hooks", "src", "veil-types.ts"))
	s.Contains(types, "getSourcesDeploymentYaml(): File<Deployment>", "typed accessor for the schema'd source")
	s.Contains(types, "getSourcesEnv(): File", "plain accessor for the unschema'd one")
	s.Contains(types, "replicas: number")

	// The VPC registers dependents for four consumer kinds, each with its
	// own context and its own view of the consumer's FS.
	vpcTypes := readFile(s.T(), filepath.Join(s.root, ".veil", "kinds", "vpc", "hooks", "src", "veil-types.ts"))
	for _, consumer := range []string{"Service", "Postgres", "Redis", "Platform"} {
		s.Contains(vpcTypes, "export interface "+consumer+"DependentHook", consumer)
		s.Contains(vpcTypes, "export interface "+consumer+"FS", consumer)
	}
}

// TestMissingRegistryTellsYouHowToFixIt covers the first thing a fresh
// clone hits: public/ is build output and gitignored, so every command
// that reads a compiled kind fails until something builds one.
func (s *E2ESuite) TestMissingRegistryTellsYouHowToFixIt() {
	dir := s.sandbox()
	s.Require().NoError(os.RemoveAll(filepath.Join(dir, "public")))

	for _, args := range [][]string{
		{"graph", "resources/services/checkout.json"},
		{"render", "resources/services/checkout.json", "--out", s.T().TempDir()},
	} {
		s.Run(args[0], func() {
			out, err := s.runIn(dir, args...)
			s.Require().Error(err, out)
			msg := s.errorMessage(out)
			s.Contains(msg, "no compiled registry found")
			s.Contains(msg, "run `veil build` first")
			s.Contains(msg, "-b", "and mention the flag that avoids the build")
		})
	}
}

// TestGraphBuildsInMemory is graph's -b: the same flag render has, for a
// project that has not built yet.
func (s *E2ESuite) TestGraphBuildsInMemory() {
	dir := s.sandbox()
	s.Require().NoError(os.RemoveAll(filepath.Join(dir, "public")))

	out, err := s.runIn(dir, "graph", "resources/services/checkout.json", "-b", "--format", "tree")
	s.Require().NoError(err, out)
	s.Contains(out, "orders-db")
	s.Contains(out, "sessions-cache")
	s.NoDirExists(filepath.Join(dir, "public"), "-b should not write the registry to disk")
}

// TestHookAddedFileIsWrittenOut covers fs.add: a file that exists in no
// kind's sources, created wholly by a hook in post_render, summarizing
// what the dependent hooks wired up. It lands in the output alongside
// the declared sources, and is per-resource.
func (s *E2ESuite) TestHookAddedFileIsWrittenOut() {
	out := s.T().TempDir()
	stdout, err := s.run("render",
		"resources/services/checkout.json",
		"resources/services/billing.json",
		"--out", out, "--quiet")
	s.Require().NoError(err, stdout)

	var checkout struct {
		Service   string   `json:"service"`
		Region    string   `json:"region"`
		WiredWith []string `json:"wiredWith"`
	}
	s.Require().NoError(json.Unmarshal([]byte(s.read(out, "checkout", "sources/manifest.json")), &checkout))
	s.Equal("checkout", checkout.Service)
	s.Equal("us-east-1", checkout.Region)
	s.Contains(checkout.WiredWith, "ORDERS_DATABASE_URL")
	s.Contains(checkout.WiredWith, "ACME_PLATFORM")

	// billing has its own wiring, and the added file reflects that rather
	// than leaking across the concurrent renders.
	billing := s.read(out, "billing", "sources/manifest.json")
	s.Contains(billing, "BILLING_DATABASE_URL")
	s.NotContains(billing, "ORDERS_DATABASE_URL")
}

// TestResourceHookAddingAnExistingPathFails covers fs.add's one refusal:
// a path already in the bundle. Declared on the resource rather than the
// kind, which also exercises the resource-hook path — compiled on the fly
// at render time rather than baked into kind.json.
func (s *E2ESuite) TestResourceHookAddingAnExistingPathFails() {
	dir := s.sandbox()
	s.write(dir, "resources/services/collide.ts", `
export default {
  render(ctx, fs) {
    fs.add('sources/env', 'SNEAKY=1');
    return fs;
  },
};
`)
	s.write(dir, "resources/services/collider.json", `{
  "metadata": {
    "kind": "service",
    "name": "collider",
    "hooks": { "render": ["./collide.ts"] }
  },
  "spec": { "image": "acme/collider:1.0.0", "replicas": 1 },
  "dependencies": [
    { "kind": "postgres", "name": "billing-db", "params": { "envVar": "DB_URL" } },
    { "kind": "vpc", "name": "acme-global" }
  ]
}`)
	out, err := s.runIn(dir, "render", "resources/services/collider.json", "--out", s.T().TempDir())
	s.Require().Error(err, out)

	msg := s.errorMessage(out)
	s.Contains(msg, "sources/env")
	s.Contains(msg, "already exists")
	s.Contains(msg, "collide.ts", "the error should name the hook that tried it")
}

// TestResourceHookFeedsLaterKindHooks pins the pipeline order. A
// resource hook runs after the kind's render and dependent hooks but
// *before* post_render, so what it writes is what the kind's final
// normalization pass sees — here the manifest, which summarizes the env
// file the resource hook just added a line to.
func (s *E2ESuite) TestResourceHookFeedsLaterKindHooks() {
	dir := s.sandbox()
	s.write(dir, "resources/services/annotate.ts", `
export default {
  render(ctx, fs) {
    // The manifest does not exist yet — post_render adds it after this.
    if (fs.get('sources/manifest.json')) throw new Error('post_render ran too early');
    const env = fs.get('sources/env');
    env.setContent(env.getContent() + '\nRESOURCE_HOOK_URL=set-by-resource-hook');
    return fs;
  },
};
`)
	s.write(dir, "resources/services/annotator.json", `{
  "metadata": {
    "kind": "service",
    "name": "annotator",
    "hooks": { "render": ["./annotate.ts"] }
  },
  "spec": { "image": "acme/annotator:1.0.0", "replicas": 1 },
  "dependencies": [
    { "kind": "postgres", "name": "billing-db", "params": { "envVar": "DB_URL" } },
    { "kind": "vpc", "name": "acme-global" }
  ]
}`)
	out := filepath.Join(s.T().TempDir(), "rendered")
	stdout, err := s.runIn(dir, "render", "resources/services/annotator.json", "--out", out)
	s.Require().NoError(err, stdout)

	// post_render sorted the env file the resource hook wrote to...
	env := readFile(s.T(), filepath.Join(out, "annotator", "sources", "env"))
	s.Contains(env, "RESOURCE_HOOK_URL=set-by-resource-hook")
	s.True(sortedAscending(strings.Split(strings.TrimSpace(env), "\n")))

	// ...and the manifest it added afterwards saw that line.
	s.Contains(readFile(s.T(), filepath.Join(out, "annotator", "sources", "manifest.json")),
		"RESOURCE_HOOK_URL")
}

// TestDirectDependencyWinsOverForwardedParams is the Acme version of the
// same collision: checkout reaches orders-db through the platform, which
// asks for ORDERS_DATABASE_URL with a pool of 25, and also declares it
// directly asking for DB_URL with a pool of 5. One database, one wiring —
// the service's own declaration.
func (s *E2ESuite) TestDirectDependencyWinsOverForwardedParams() {
	dir := s.sandbox()
	checkout := filepath.Join(dir, "resources", "services", "checkout.json")
	var doc map[string]any
	s.Require().NoError(json.Unmarshal([]byte(readFile(s.T(), checkout)), &doc))
	doc["dependencies"] = append(doc["dependencies"].([]any), map[string]any{
		"kind": "postgres", "name": "orders-db",
		"params": map[string]any{"envVar": "DB_URL", "poolSize": 5},
	})
	updated, err := json.Marshal(doc)
	s.Require().NoError(err)
	s.Require().NoError(os.WriteFile(checkout, updated, 0o644))

	out := filepath.Join(s.T().TempDir(), "rendered")
	stdout, err := s.runIn(dir, "render", "resources/services/checkout.json", "--out", out)
	s.Require().NoError(err, stdout)

	env := readFile(s.T(), filepath.Join(out, "checkout", "sources", "env"))
	s.Contains(env, "DB_URL=", "the service's own params apply")
	s.Contains(env, "pool=5")
	s.NotContains(env, "ORDERS_DATABASE_URL=", "the platform's forwarded params must not also apply")
	s.NotContains(env, "pool=25")
}

// TestKindAssetReachesHooksButNotOutput covers the point of `files`: a
// kind can ship something for its hooks to read that is not part of the
// rendered resource. The service kind declares files/labels.json with
// render unset, and apply-labels.ts reads it — so the labels land in the
// deployment while the asset itself never does.
func (s *E2ESuite) TestKindAssetReachesHooksButNotOutput() {
	out := s.render("resources/services/checkout.json")

	deployment := s.read(out, "checkout", "sources/deployment.yaml")
	s.Contains(deployment, "app.acme.io/managed-by: veil",
		"the hook should have read the asset and applied it")

	s.NoFileExists(filepath.Join(out, "checkout", "files", "labels.json"),
		"an asset is not rendered output")
	// Nor anywhere else under the resource, whatever the layout.
	var rendered []string
	s.Require().NoError(filepath.Walk(filepath.Join(out, "checkout"), func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rendered = append(rendered, filepath.Base(p))
		return nil
	}))
	s.NotContains(rendered, "labels.json")
}

// TestKindDeclaresSourcesAndFilesTogether pins that the two lists
// coexist: the service kind still declares its sources the old way while
// adding an asset through `files`, and everything from both is present
// to hooks.
func (s *E2ESuite) TestKindDeclaresSourcesAndFilesTogether() {
	compiled := s.readRegistryKind("service")

	paths := map[string]bool{}
	for _, f := range compiled.Files {
		paths[f.Path] = f.Render
	}
	s.Equal(true, paths["sources/deployment.yaml"], "a source seeds the output")
	s.Equal(true, paths["sources/env"], "a source seeds the output")
	s.Equal(false, paths["files/labels.json"], "a file without render is an asset")

	// And rendering still produces exactly the sources.
	out := s.render("resources/services/checkout.json")
	s.FileExists(filepath.Join(out, "checkout", "sources", "deployment.yaml"))
	s.FileExists(filepath.Join(out, "checkout", "sources", "env"))
}

// TestBuildMirrorsRenderFilesIntoSources is the forward half of
// compatibility: a registry this veil builds has to stay readable by one
// that predates `files`. Such a reader only knows `sources`, so every
// render file is mirrored there — and assets are not, since that reader
// would write them out.
func (s *E2ESuite) TestBuildMirrorsRenderFilesIntoSources() {
	compiled := s.readRegistryKind("service")

	var mirrored []string
	for _, src := range compiled.Sources {
		mirrored = append(mirrored, src.Path)
	}
	s.ElementsMatch([]string{"sources/deployment.yaml", "sources/env"}, mirrored,
		"sources should mirror exactly the render files")
	s.NotContains(mirrored, "files/labels.json",
		"an older veil would render an asset it found in sources")

	for _, src := range compiled.Sources {
		for _, f := range compiled.Files {
			if f.Path == src.Path {
				s.Equal(f.Contents, src.Contents, "%s: mirrored contents must match", src.Path)
				s.Equal(f.Schema, src.Schema, "%s: mirrored schema must match", src.Path)
			}
		}
	}
}

// TestLegacySourcesOnlyRegistryRenders is the backward half: a registry
// built before `files` existed carries only `sources`, with no `files`
// key at all. Stripping `files` from every compiled kind reproduces that
// exactly, and the render has to come out byte for byte the same.
func (s *E2ESuite) TestLegacySourcesOnlyRegistryRenders() {
	// Baseline from the current registry.
	want := s.render("resources/data/orders-db.json")

	dir := s.sandbox()
	kinds, err := filepath.Glob(filepath.Join(dir, "public", "r", "*", "kind.json"))
	s.Require().NoError(err)
	s.Require().NotEmpty(kinds)
	for _, path := range kinds {
		raw, err := os.ReadFile(path)
		s.Require().NoError(err)
		var doc map[string]any
		s.Require().NoError(json.Unmarshal(raw, &doc))
		delete(doc, "files")
		out, err := json.MarshalIndent(doc, "", "  ")
		s.Require().NoError(err)
		s.Require().NoError(os.WriteFile(path, out, 0644))
	}

	got := s.T().TempDir()
	stdout, err := s.runIn(dir, "render", "resources/data/orders-db.json", "--out", got, "--quiet")
	s.Require().NoError(err, "a sources-only registry must still render: %s", stdout)

	s.Equal(s.read(want, "orders-db", "sources/database.json"),
		s.read(got, "orders-db", "sources/database.json"),
		"a registry with only `sources` must render exactly as one with `files`")
}

// intentionalRenderFailures are the playground resources that are
// supposed to fail, each a fixture some other test asserts on. Anything
// else under resources/ has to render — that is what makes
// TestEveryResourceRenders able to catch a fixture that quietly stopped
// working, which is how a kind ended up depending on one that listed no
// dependent hooks for it.
var intentionalRenderFailures = map[string]string{
	"resources/edge-cases/bad-rotation-secret.json": "a hook writes past its source's schema",
	"resources/edge-cases/orphan-service.json":      "a validate hook rejects a service with no database",
}

// TestEveryResourceRenders renders the whole playground. Overlays are
// skipped — they are fragments of another resource, not resources — and
// the fixtures above are asserted to fail rather than silently excluded.
func (s *E2ESuite) TestEveryResourceRenders() {
	var renderable, expectedFailures []string
	err := filepath.Walk(filepath.Join(s.root, "resources"), func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".json") {
			return err
		}
		rel, relErr := filepath.Rel(s.root, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)

		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		var doc struct {
			Metadata struct {
				Kind     string `json:"kind"`
				FileType string `json:"file_type"`
			} `json:"metadata"`
		}
		if jsonErr := json.Unmarshal(raw, &doc); jsonErr != nil {
			return jsonErr
		}
		switch {
		case doc.Metadata.FileType == "overlay":
			// Applied to the resource it overlays, never rendered alone.
		case intentionalRenderFailures[rel] != "":
			expectedFailures = append(expectedFailures, rel)
		default:
			renderable = append(renderable, rel)
		}
		return nil
	})
	s.Require().NoError(err)
	s.Require().NotEmpty(renderable)

	// One pass over everything, which is also how a real project renders:
	// many resources, one invocation.
	out := s.T().TempDir()
	args := append([]string{"render"}, renderable...)
	stdout, err := s.run(append(args, "--out", out, "--quiet")...)
	s.Require().NoError(err, "the whole playground should render: %s", stdout)

	// Every one of them produced files.
	for _, rel := range renderable {
		raw, readErr := os.ReadFile(filepath.Join(s.root, rel))
		s.Require().NoError(readErr)
		var doc struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		}
		s.Require().NoError(json.Unmarshal(raw, &doc))
		entries, readErr := os.ReadDir(filepath.Join(out, doc.Metadata.Name))
		s.Require().NoError(readErr, "%s rendered no directory", rel)
		s.NotEmpty(entries, "%s rendered no files", rel)
	}

	// And the fixtures that are meant to fail still do, for their reason
	// rather than by having rotted into some unrelated error.
	for _, rel := range expectedFailures {
		msg := s.renderFails(rel)
		s.NotEmpty(msg, "%s: %s", rel, intentionalRenderFailures[rel])
	}
}

// TestAssetPromotedToOutputPerResource covers setRendered end to end and,
// with it, that the decision is per render rather than per kind: billing
// asks for the kind's labels asset in its output, checkout does not, and
// the same declaration serves both.
func (s *E2ESuite) TestAssetPromotedToOutputPerResource() {
	billing := s.render("resources/services/billing.json")
	s.FileExists(filepath.Join(billing, "billing", "files", "labels.json"),
		"billing sets publishLabels, so the hook promotes the asset")
	s.Contains(s.read(billing, "billing", "files/labels.json"), "app.acme.io/tier")

	checkout := s.render("resources/services/checkout.json")
	s.NoFileExists(filepath.Join(checkout, "checkout", "files", "labels.json"),
		"checkout does not, so the same asset stays unwritten")
}

// TestRenderedAndDeletedAreOneFlag drives the whole of it through the
// real binary on one resource. `reports` promotes the kind's labels
// asset into its output and drops the generated manifest, so the two
// files trade places: the thing the kind never meant to write is
// written, and the thing it did is not.
//
// Both directions come from the same flag — setRendered(true) on the
// asset, setDeleted(true) on the manifest — and the hooks assert the
// inverse reads back (isRendered false after deleting) as they go, so a
// regression fails the render rather than just the file list.
func (s *E2ESuite) TestRenderedAndDeletedAreOneFlag() {
	out := s.render("resources/services/reports.json")

	// The asset was promoted.
	s.FileExists(filepath.Join(out, "reports", "files", "labels.json"))
	s.Contains(s.read(out, "reports", "files/labels.json"), "app.acme.io/tier")

	// The render file was dropped, after post_render had created it.
	s.NoFileExists(filepath.Join(out, "reports", "sources", "manifest.json"))

	// Everything else is untouched — deleting one file does not disturb
	// the rest of the bundle.
	s.FileExists(filepath.Join(out, "reports", "sources", "deployment.yaml"))
	s.Contains(s.read(out, "reports", "sources/env"), "REPORTS_DATABASE_URL=postgres://")

	// And the same kind renders the other way round for a resource that
	// asks for neither, so this is the resource's decision and not the
	// declaration's.
	checkout := s.render("resources/services/checkout.json")
	s.NoFileExists(filepath.Join(checkout, "checkout", "files", "labels.json"))
	s.FileExists(filepath.Join(checkout, "checkout", "sources", "manifest.json"))
}

// TestDependentHookShipsItsOwnFileToTheConsumer is the case ctx.selfFS
// exists for. The postgres kind ships an IAM policy template — an asset,
// so no database renders it — and its dependent hook fills in the
// database's name and region and adds the result to each service that
// depends on it. The file belongs to the kind that knows what access its
// consumers need, rather than being copy-pasted into every service.
func (s *E2ESuite) TestDependentHookShipsItsOwnFileToTheConsumer() {
	checkout := s.render("resources/services/checkout.json")

	policy := s.read(checkout, "checkout", "terraform/orders-db-access.tf")
	s.Contains(policy, `name = "orders-db-access"`, "templated with the database's name")
	s.Contains(policy, "arn:aws:rds-db:us-east-1:acme:dbuser:orders-db/*",
		"and with the render's region")
	s.NotContains(policy, "PLACEHOLDER", "no placeholder should survive")
	s.Contains(policy, "policy = jsonencode(",
		"the expression round-tripped through the terraform codec unquoted")

	// It arrives through a forwarded edge too — checkout reaches
	// orders-db via the platform, not by declaring it.
	s.NotContains(s.read(s.root, "resources/services", "checkout.json"), "orders-db")

	// A service with a different database gets that one's policy.
	billing := s.render("resources/services/billing.json")
	s.Contains(s.read(billing, "billing", "terraform/billing-db-access.tf"), `name = "billing-db-access"`)
	s.NoFileExists(filepath.Join(billing, "billing", "terraform", "orders-db-access.tf"))

	// And the template itself stays out of the database's own output —
	// it is an asset, reachable through selfFS and never rendered.
	db := s.render("resources/data/orders-db.json")
	s.NoFileExists(filepath.Join(db, "orders-db", "files", "iam-policy.tf"))
	s.NoDirExists(filepath.Join(db, "orders-db", "terraform"))
}

// TestTFWriteSuiteRunsInJavaScript runs the pkg/tfwrite test suite again
// on the JS side, through the real binary, against the same fixtures.
//
// Go proving the tree is correct says nothing about the class layer over
// it: the JS holds its own copy of the label positions, the block-type
// mapping and the dirty marking, and those can drift from Go's. Porting
// the suite is what catches that — it already has once, on the flag the
// file root uses to record a change.
//
// Each check throws on failure, so a break fails the render rather than
// turning up as a wrong file later. The count is asserted too, so a
// check that silently stops running is also a failure.
func (s *E2ESuite) TestTFWriteSuiteRunsInJavaScript() {
	out := s.render("resources/tfcheck.json")
	ran := strings.TrimSpace(s.read(out, "tfwrite-suite", "sources/tfwrite-checks.txt"))
	s.Equal("108", ran, "every check in the JS port ran")
}
