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
