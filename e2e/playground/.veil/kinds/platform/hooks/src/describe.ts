import type { FS, RenderHook, RenderHookContext } from './veil-types';

// Records what this platform hands its consumers. The list is derived
// from the platform's own dependencies, which is exactly the set
// forward_dependencies passes along.
const describe: RenderHook = {
  render(ctx: RenderHookContext, fs: FS): FS {
    const file = fs.getSourcesPlatformJson();
    const platform = file.getContent();
    platform.owner = ctx.resource.spec.owner;
    platform.tier = ctx.resource.spec.tier ?? 'standard';
    platform.provides = (ctx.resource.dependencies ?? []).map(
      (d: { kind: string; name: string }) => `${d.kind}/${d.name}`,
    );
    file.setContent(platform);
    return fs;
  },
};

export default describe;
