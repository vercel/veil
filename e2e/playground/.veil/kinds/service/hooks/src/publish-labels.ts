import type { FS, RenderHook, RenderHookContext } from './veil-types';

// The other side of an asset: files/labels.json ships with the kind and
// never renders, but a service can ask for a copy of it in its output.
// setRendered(true) is what promotes it — nothing about the declaration
// changes, only this render's decision.
const publishLabels: RenderHook = {
  render(ctx: RenderHookContext, fs: FS): FS {
    if (!ctx.resource.spec.publishLabels) return fs;
    const asset = fs.get('files/labels.json');
    if (!asset) {
      throw new Error('files/labels.json should be readable by hooks');
    }
    asset.setRendered(true);
    return fs;
  },
};

export default publishLabels;
