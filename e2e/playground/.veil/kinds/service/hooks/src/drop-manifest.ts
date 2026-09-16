import type { FS, RenderHook, RenderHookContext } from './veil-types';

// Some services ship their infra without the generated manifest. Runs in
// post_render, after write-manifest has added it, and drops it again.
//
// Deleting and declining to render are one flag, so this reads back
// through isRendered as well as isDeleted — and the reverse holds for the
// labels asset, which publish-labels turned on.
const dropManifest: RenderHook = {
  render(ctx: RenderHookContext, fs: FS): FS {
    if (!ctx.resource.spec.dropManifest) return fs;

    const manifest = fs.get('sources/manifest.json');
    if (!manifest) throw new Error('write-manifest should have added it');
    manifest.setDeleted(true);
    if (manifest.isRendered()) {
      throw new Error('deleting a file should stop it rendering');
    }
    return fs;
  },
};

export default dropManifest;
