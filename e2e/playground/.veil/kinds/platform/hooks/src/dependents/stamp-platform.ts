import type { ServiceDependentHook, ServiceDependentHookContext, ServiceFS } from '../veil-types';

// A service adopting a platform gets a marker for it. The database and
// cache lines in the same env file arrive separately, from the platform's
// own dependencies — forwarded because the platform kind sets
// forward_dependencies.
const stampPlatform: ServiceDependentHook = {
  render(ctx: ServiceDependentHookContext, fs: ServiceFS): ServiceFS {
    const file = fs.getSourcesEnv();
    const existing = file.getContent();
    const line = `ACME_PLATFORM=${ctx.self.metadata.name}`;
    file.setContent(existing ? `${existing}\n${line}` : line);
    return fs;
  },
};

export default stampPlatform;
