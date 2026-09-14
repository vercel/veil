import type { FS, RenderHook, RenderHookContext } from './veil-types';

// Stamps the VPC's declared spec and the render-time region onto the
// network source. Everything downstream reads the rendered file, so this
// is the single place the network shape is decided.
const applyCidr: RenderHook = {
  render(ctx: RenderHookContext, fs: FS): FS {
    const file = fs.getSourcesNetworkJson();
    const network = file.getContent();
    network.cidr = ctx.resource.spec.cidr;
    network.region = String(ctx.vars.region);
    network.zones = ctx.resource.spec.availabilityZones ?? 3;
    file.setContent(network);
    return fs;
  },
};

export default applyCidr;
