import type { FS, RenderHook, RenderHookContext } from './veil-types';

// Emits a file that exists in no kind's sources — created wholly by a
// hook. Runs in post_render so it can summarize what the dependent hooks
// wired up, which is the case fs.add exists for.
const writeManifest: RenderHook = {
  render(ctx: RenderHookContext, fs: FS): FS {
    const env = fs.get('sources/env');
    const keys = String(env ? env.getContent() : '')
      .split('\n')
      .filter((line: string) => line.includes('='))
      .map((line: string) => line.split('=')[0]);

    fs.add(
      'sources/manifest.json',
      JSON.stringify({
        service: ctx.resource.metadata.name,
        region: String(ctx.vars.region),
        wiredWith: keys,
      }),
    );
    return fs;
  },
};

export default writeManifest;
