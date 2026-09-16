package render

import (
	"os"
	"path/filepath"

	"github.com/vercel/veil/pkg/registry"
)

func (s *RenderSuite) writeSourceTarget(kind, consumer, code string, sources []map[string]any) {
	s.writeDependentKind(kind, consumer, code)
	s.writeJSON(filepath.Join(s.root, "r", kind, "kind.json"), map[string]any{
		"name":  kind,
		"hooks": map[string]any{"dependents": []map[string]any{{"kind": consumer, "params_schema": "{}", "sources": sources, "hooks": []map[string]any{{"name": "dependent.js", "content": code}}}}},
	})
}

func (s *RenderSuite) sourceResources(rootName string, targets ...string) string {
	dir := filepath.Join(s.root, "resources")
	s.Require().NoError(os.MkdirAll(dir, 0755))
	deps := make([]map[string]any, 0, len(targets))
	for _, name := range targets {
		deps = append(deps, map[string]any{"kind": "table", "name": name, "params": map[string]any{}})
		s.writeJSON(filepath.Join(dir, "table-"+name+".json"), map[string]any{"metadata": map[string]any{"kind": "table", "name": name}, "spec": map[string]any{}})
	}
	s.writeJSON(filepath.Join(dir, rootName+".json"), map[string]any{"metadata": map[string]any{"kind": "service", "name": rootName}, "spec": map[string]any{}, "dependencies": deps})
	return dir
}

const mutateDependentSource = `var __veilMod={default:{render(ctx,fs){const f=ctx.sources.getGrantTxt(); if (f !== fs.get(f.getPath())) throw new Error("not shared"); f.setContent(f.getContent()+":"+ctx.consumer.metadata.name+":"+ctx.self.metadata.name);}}};`

func (s *RenderSuite) TestDependentSourcesFreshPerTargetAndRoot() {
	s.writeSimpleKind("service")
	s.writeSourceTarget("table", "service", mutateDependentSource, compiledSources(map[string]string{"grant.txt": "base"}, nil))
	s.reloadRegistryWithKinds("service", "table")
	dir := s.sourceResources("one", "alpha", "beta")
	s.sourceResources("two", "alpha", "beta")
	fsys, cat := s.catalogFor(dir)
	for _, name := range []string{"one", "two"} {
		result, err := Render(&Options{Kind: "service", Name: name, FS: fsys, Catalog: cat, OutDir: filepath.Join(s.root, "out")})
		s.Require().NoError(err)
		for _, target := range []string{"alpha", "beta"} {
			id := registry.DependencySourceID("table", target, "grant.txt")
			content, err := os.ReadFile(filepath.Join(result.OutDir, id))
			s.Require().NoError(err)
			s.Equal("base:"+name+":"+target, string(content))
		}
	}
}

func (s *RenderSuite) TestDependentSourcesOverridesAndLaterHooks() {
	s.writeSimpleKind("service")
	code := `var __veilMod={default:{render(ctx,fs){const f=ctx.sources.getGrantTxt();f.setOutputPath(ctx.self.metadata.name+".txt");f.setContent(f.getContent()+":dep");f.setDeleted(true);}}};`
	s.writeSourceTarget("table", "service", code, compiledSources(map[string]string{"grant.txt": "base"}, nil))
	id := registry.DependencySourceID("table", "alpha", "grant.txt")
	s.writeJSON(filepath.Join(s.root, "r", "service", "kind.json"), map[string]any{"name": "service", "hooks": map[string]any{
		"render":      []map[string]any{{"name": "early", "content": `var __veilMod={default:{render(ctx,fs){const f=fs.get("` + id + `");if(f.getContent()!=="override")throw new Error("override not visible before dependents");f.setContent(f.getContent()+":early");}}};`}},
		"post_render": []map[string]any{{"name": "late", "content": `var __veilMod={default:{render(ctx,fs){const f=fs.get("` + id + `");fs.add("observed.txt",f.getContent()+":"+f.isDeleted());}}};`}},
	}})
	s.reloadRegistryWithKinds("service", "table")
	dir := s.sourceResources("one", "alpha")
	s.Require().NoError(os.WriteFile(filepath.Join(dir, "override.txt"), []byte("override"), 0644))
	s.writeJSON(filepath.Join(dir, "one.json"), map[string]any{"metadata": map[string]any{"kind": "service", "name": "one", "overrides": []map[string]any{{"source": id, "path": "override.txt", "skip_hooks": true}}}, "spec": map[string]any{}, "dependencies": []map[string]any{{"kind": "table", "name": "alpha", "params": map[string]any{}}}})
	out := filepath.Join(s.root, "out")
	_, err := s.renderKind("service", "one", dir, out)
	s.Require().NoError(err)
	content, err := os.ReadFile(filepath.Join(out, "one", "alpha.txt"))
	s.Require().NoError(err)
	s.Equal("override", string(content))
	observed, err := os.ReadFile(filepath.Join(out, "one", "observed.txt"))
	s.Require().NoError(err)
	s.Equal("override:early:dep:true", string(observed))
}

func (s *RenderSuite) TestDependentSourceSchemasSurviveViewsAndDestinations() {
	for _, body := range []string{
		`ctx.sources.getGrantJson().setOutputPath("renamed.txt");ctx.sources.getGrantJson().setContent({replicas:"bad"});`,
		`const f=ctx.sources.getGrantJson();f.setOutputPath("renamed.txt");fs.get(fs.keys()[0]).setContent('{"replicas":"bad"}');`,
	} {
		s.Run(body, func() {
			s.writeSimpleKind("service")
			s.writeSourceTarget("table", "service", `var __veilMod={default:{render(ctx,fs){`+body+`}}};`, compiledSources(map[string]string{"./grant.json": `{"replicas":1}`}, map[string]string{"./grant.json": replicasSchemaJSON}))
			s.reloadRegistryWithKinds("service", "table")
			dir := s.sourceResources("one", "alpha")
			_, err := s.renderKind("service", "one", dir, filepath.Join(s.root, "out"))
			s.Require().Error(err)
			s.Contains(err.Error(), "replicas")
			s.NoDirExists(filepath.Join(s.root, "out", "one"))
		})
	}
}

func (s *RenderSuite) TestDependentSourceDestinationCollisionWritesNothing() {
	s.writeSimpleKind("service")
	s.writeSourceTarget("table", "service", `var __veilMod={default:{render(ctx,fs){ctx.sources.getGrantTxt().setOutputPath(ctx.self.metadata.name==="alpha"?"nested/../same.txt":"same.txt");}}};`, compiledSources(map[string]string{"grant.txt": "base"}, nil))
	s.reloadRegistryWithKinds("service", "table")
	dir := s.sourceResources("one", "alpha", "beta")
	_, err := s.renderKind("service", "one", dir, filepath.Join(s.root, "out"))
	s.Require().Error(err)
	s.Contains(err.Error(), registry.DependencySourceID("table", "alpha", "grant.txt"))
	s.Contains(err.Error(), registry.DependencySourceID("table", "beta", "grant.txt"))
	s.NoFileExists(filepath.Join(s.root, "out", "one", "same.txt"))
}

func (s *RenderSuite) TestDependentSourcesDiamondInstantiatedOnce() {
	s.writeSimpleKind("diamond-root")
	s.writeDependentKind("diamond-branch", "diamond-root", noopDependentHookIIFE)
	s.writeSourceTarget("diamond-leaf", "diamond-root", `var __veilMod={default:{render(ctx,fs){const f=ctx.sources.getGrantTxt();f.setContent(f.getContent()+":"+ctx.params.tag);}}};`, compiledSources(map[string]string{"grant.txt": "base"}, nil))
	s.reloadRegistryWithKinds("diamond-root", "diamond-branch", "diamond-leaf")
	dir := s.writeDiamond(map[string]string{"b1": "shared", "c1": "shared"})
	fsys, cat := s.catalogFor(dir)
	result, err := Render(&Options{Kind: "diamond-root", Name: "r1", FS: fsys, Catalog: cat, OutDir: filepath.Join(s.root, "out")})
	s.Require().NoError(err)
	content, err := os.ReadFile(filepath.Join(result.OutDir, registry.DependencySourceID("diamond-leaf", "d1", "grant.txt")))
	s.Require().NoError(err)
	s.Equal("base:shared", string(content))
}

func (s *RenderSuite) TestDependentSourcesQualifiedKindsNeverAlias() {
	s.writeSimpleKind("service")
	s.writeSourceTarget("table", "service", mutateDependentSource, compiledSources(map[string]string{"grant.txt": "base"}, nil))
	s.reloadRegistryWithKinds("service", "table")
	regPath := filepath.Join(s.root, "r", "registry.json")
	reg, err := registry.Load([]registry.Reference{{Path: regPath}, {Alias: "vendor", Path: regPath}})
	s.Require().NoError(err)
	s.registry = reg
	dir := s.sourceResources("one", "same")
	s.writeJSON(filepath.Join(dir, "vendor.json"), map[string]any{"metadata": map[string]any{"kind": "vendor/table", "name": "same"}, "spec": map[string]any{}})
	s.writeJSON(filepath.Join(dir, "one.json"), map[string]any{"metadata": map[string]any{"kind": "service", "name": "one"}, "spec": map[string]any{}, "dependencies": []map[string]any{{"kind": "table", "name": "same", "params": map[string]any{}}, {"kind": "vendor/table", "name": "same", "params": map[string]any{}}}})
	fsys, cat := s.catalogFor(dir)
	result, err := Render(&Options{Kind: "service", Name: "one", FS: fsys, Catalog: cat, OutDir: filepath.Join(s.root, "out")})
	s.Require().NoError(err)
	for _, kind := range []string{"table", "vendor/table"} {
		content, err := os.ReadFile(filepath.Join(result.OutDir, registry.DependencySourceID(kind, "same", "grant.txt")))
		s.Require().NoError(err)
		s.Equal("base:one:same", string(content))
	}
}

func (s *RenderSuite) TestDependentSourceInvalidOverrideFailsBeforeHooks() {
	s.writeSimpleKind("service")
	s.writeSourceTarget("table", "service", `var __veilMod={default:{render(){throw new Error("hooks ran too early");}}};`, compiledSources(map[string]string{"grant.json": `{"replicas":1}`}, map[string]string{"grant.json": replicasSchemaJSON}))
	s.reloadRegistryWithKinds("service", "table")
	dir := s.sourceResources("one", "alpha")
	s.Require().NoError(os.WriteFile(filepath.Join(dir, "override.json"), []byte(`{"replicas":"bad"}`), 0644))
	id := registry.DependencySourceID("table", "alpha", "grant.json")
	s.writeJSON(filepath.Join(dir, "one.json"), map[string]any{"metadata": map[string]any{"kind": "service", "name": "one", "overrides": []map[string]any{{"source": id, "path": "override.json"}}}, "spec": map[string]any{}, "dependencies": []map[string]any{{"kind": "table", "name": "alpha", "params": map[string]any{}}}})
	_, err := s.renderKind("service", "one", dir, filepath.Join(s.root, "out"))
	s.Require().Error(err)
	s.Contains(err.Error(), "replicas")
	s.NotContains(err.Error(), "hooks ran too early")
}

func (s *RenderSuite) TestDependentSourceDefaultOverrideFlowsThroughHooks() {
	s.writeSimpleKind("service")
	s.writeSourceTarget("table", "service", mutateDependentSource, compiledSources(map[string]string{"grant.txt": "base"}, nil))
	s.reloadRegistryWithKinds("service", "table")
	dir := s.sourceResources("one", "alpha")
	s.Require().NoError(os.WriteFile(filepath.Join(dir, "override.txt"), []byte("override"), 0644))
	id := registry.DependencySourceID("table", "alpha", "grant.txt")
	s.writeJSON(filepath.Join(dir, "one.json"), map[string]any{"metadata": map[string]any{"kind": "service", "name": "one", "overrides": []map[string]any{{"source": id, "path": "override.txt"}}}, "spec": map[string]any{}, "dependencies": []map[string]any{{"kind": "table", "name": "alpha", "params": map[string]any{}}}})
	fsys, cat := s.catalogFor(dir)
	result, err := Render(&Options{Kind: "service", Name: "one", FS: fsys, Catalog: cat, OutDir: filepath.Join(s.root, "out")})
	s.Require().NoError(err)
	content, err := os.ReadFile(filepath.Join(result.OutDir, id))
	s.Require().NoError(err)
	s.Equal("override:one:alpha", string(content))
}

func (s *RenderSuite) TestDependentSourcesDirectParamsWinAfterConflictingDiamond() {
	s.writeSimpleKind("diamond-root")
	s.writeDependentKind("diamond-branch", "diamond-root", noopDependentHookIIFE)
	s.writeSourceTarget("diamond-leaf", "diamond-root", `var __veilMod={default:{render(ctx,fs){const f=ctx.sources.getGrantTxt();f.setContent(f.getContent()+":"+ctx.params.tag);}}};`, compiledSources(map[string]string{"grant.txt": "base"}, nil))
	s.reloadRegistryWithKinds("diamond-root", "diamond-branch", "diamond-leaf")
	dir := s.writeDiamond(map[string]string{"b1": "left", "c1": "right"})
	s.writeJSON(filepath.Join(dir, "r1.json"), map[string]any{"metadata": map[string]any{"kind": "diamond-root", "name": "r1"}, "spec": map[string]any{}, "dependencies": []map[string]any{{"kind": "diamond-branch", "name": "b1", "params": map[string]any{}}, {"kind": "diamond-branch", "name": "c1", "params": map[string]any{}}, {"kind": "diamond-leaf", "name": "d1", "params": map[string]any{"tag": "direct"}}}})
	fsys, cat := s.catalogFor(dir)
	result, err := Render(&Options{Kind: "diamond-root", Name: "r1", FS: fsys, Catalog: cat, OutDir: filepath.Join(s.root, "out")})
	s.Require().NoError(err)
	content, err := os.ReadFile(filepath.Join(result.OutDir, registry.DependencySourceID("diamond-leaf", "d1", "grant.txt")))
	s.Require().NoError(err)
	s.Equal("base:direct", string(content))
}

func (s *RenderSuite) TestDependentSourceFrozenOverrideRestoresDroppedEntry() {
	s.writeSimpleKind("service")
	s.writeSourceTarget("table", "service", `var __veilMod={default:{render(){return {};}}};`, compiledSources(map[string]string{"grant.txt": "base"}, nil))
	s.reloadRegistryWithKinds("service", "table")
	dir := s.sourceResources("one", "alpha")
	s.Require().NoError(os.WriteFile(filepath.Join(dir, "override.txt"), []byte("final"), 0644))
	id := registry.DependencySourceID("table", "alpha", "grant.txt")
	s.writeJSON(filepath.Join(dir, "one.json"), map[string]any{"metadata": map[string]any{"kind": "service", "name": "one", "overrides": []map[string]any{{"source": id, "path": "override.txt", "skip_hooks": true}}}, "spec": map[string]any{}, "dependencies": []map[string]any{{"kind": "table", "name": "alpha", "params": map[string]any{}}}})
	fsys, cat := s.catalogFor(dir)
	result, err := Render(&Options{Kind: "service", Name: "one", FS: fsys, Catalog: cat, OutDir: filepath.Join(s.root, "out")})
	s.Require().NoError(err)
	content, err := os.ReadFile(filepath.Join(result.OutDir, id))
	s.Require().NoError(err)
	s.Equal("final", string(content))
}

func (s *RenderSuite) TestDependentSourceSchemaPersistsIntoLaterConsumerHook() {
	s.writeSimpleKind("service")
	id := registry.DependencySourceID("table", "alpha", "./grant.json")
	s.writeSourceTarget("table", "service", `var __veilMod={default:{render(ctx){ctx.sources.getGrantJson().setOutputPath("grant.txt");ctx.sources.getGrantJson().setContent({replicas:2});}}};`, compiledSources(map[string]string{"./grant.json": `{"replicas":1}`}, map[string]string{"./grant.json": replicasSchemaJSON}))
	s.writeJSON(filepath.Join(s.root, "r", "service", "kind.json"), map[string]any{"name": "service", "hooks": map[string]any{"post_render": []map[string]any{{"name": "late", "content": `var __veilMod={default:{render(ctx,fs){fs.get("` + id + `").setContent({replicas:"invalid"});}}};`}}}})
	s.reloadRegistryWithKinds("service", "table")
	dir := s.sourceResources("one", "alpha")
	_, err := s.renderKind("service", "one", dir, filepath.Join(s.root, "out"))
	s.Require().Error(err)
	s.Contains(err.Error(), "replicas")
	s.Contains(err.Error(), "late")
	s.NoDirExists(filepath.Join(s.root, "out", "one"))
}
