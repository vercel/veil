import type {
  PlatformDependentHook,
  PlatformDependentHookContext,
  PlatformFS,
} from '../veil-types';

// A platform depending on a datastore is declaring it on behalf of the
// services that depend on the platform — the same edge is forwarded to
// them, carrying these params. `describe` already lists the edge under
// `provides`; this records where the thing actually lives, which only
// the datastore's own kind knows.
const registerWithPlatform: PlatformDependentHook = {
  render(ctx: PlatformDependentHookContext, fs: PlatformFS): PlatformFS {
    const line = `redis=${ctx.self.metadata.name}.${ctx.vars.region}.cache.acme.internal`;
    const existing = fs.get('sources/backing-services');
    if (existing) {
      existing.setContent(`${existing.getContent()}\n${line}`);
    } else {
      fs.add('sources/backing-services', line);
    }
    return fs;
  },
};

export default registerWithPlatform;
