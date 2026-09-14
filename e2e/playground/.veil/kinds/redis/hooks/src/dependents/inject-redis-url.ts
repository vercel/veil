import type { ServiceDependentHook, ServiceDependentHookContext, ServiceFS } from '../veil-types';

const injectRedisUrl: ServiceDependentHook = {
  render(ctx: ServiceDependentHookContext, fs: ServiceFS): ServiceFS {
    const host = `${ctx.self.metadata.name}.${ctx.vars.region}.cache.acme.internal`;
    const line = `${ctx.params.envVar}=redis://${host}:6379`;
    const file = fs.getSourcesEnv();
    const existing = file.getContent();
    file.setContent(existing ? `${existing}\n${line}` : line);
    return fs;
  },
};

export default injectRedisUrl;
