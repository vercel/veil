import type { FS, RenderHook, RenderHookContext } from './veil-types';

// Reads an asset the kind ships — a `files` entry with render unset, so
// it reaches the hook FS but is never written to the output. Proves both
// halves of that: the content is here to read, and nothing puts
// labels.json in the rendered directory.
const applyLabels: RenderHook = {
  render(_ctx: RenderHookContext, fs: FS): FS {
    const asset = fs.get('files/labels.json');
    if (!asset) {
      throw new Error('files/labels.json should be readable by hooks');
    }
    const labels = JSON.parse(String(asset.getContent()));

    const file = fs.getSourcesDeploymentYaml();
    const deployment = file.getContent();
    // A resource's own labels win over the kind's defaults.
    deployment.labels = { ...labels, ...(deployment.labels ?? {}) };
    file.setContent(deployment);
    return fs;
  },
};

export default applyLabels;
