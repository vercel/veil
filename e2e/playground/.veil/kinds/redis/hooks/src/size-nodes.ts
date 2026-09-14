import type { FS, RenderHook, RenderHookContext } from './veil-types';

// The cache source is YAML, so getContent hands back a parsed object and
// setContent writes YAML back — the hook never sees the encoding.
const sizeNodes: RenderHook = {
  render(ctx: RenderHookContext, fs: FS): FS {
    const file = fs.getSourcesCacheYaml();
    const cache = file.getContent();
    cache.nodes = ctx.resource.spec.size === 'large' ? 6 : 1;
    cache.evictionPolicy = ctx.resource.spec.evictionPolicy ?? 'allkeys-lru';
    file.setContent(cache);
    return fs;
  },
};

export default sizeNodes;
