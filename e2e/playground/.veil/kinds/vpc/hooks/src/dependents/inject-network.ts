import type { ServiceDependentHook, ServiceDependentHookContext, ServiceFS } from '../veil-types';

// Every service lands in a VPC, so every service is told which one and
// which subnet it sits in. An edge that declares no params still gets
// an object, so there is nothing to guard against here.
const injectNetwork: ServiceDependentHook = {
  render(ctx: ServiceDependentHookContext, fs: ServiceFS): ServiceFS {
    const file = fs.getSourcesEnv();
    const existing = file.getContent();
    const lines = [
      `ACME_VPC=${ctx.self.metadata.name}`,
      `ACME_VPC_CIDR=${ctx.self.spec.cidr}`,
      `ACME_SUBNET=${ctx.params.subnet ?? 'private'}`,
    ].join('\n');
    file.setContent(existing ? `${existing}\n${lines}` : lines);
    return fs;
  },
};

export default injectNetwork;
