import type { FS, ValidateHook, RenderHookContext } from './veil-types';

// Every Acme service must end up with a database connection, however it
// got one — directly, or forwarded through a platform.
const requireDatabase: ValidateHook = {
  validate(ctx: RenderHookContext, fs: FS) {
    const file = fs.get('sources/env');
    const env = file ? String(file.getContent()) : '';
    if (!/_URL=postgres:\/\//.test(env)) {
      return 'service has no database connection: depend on a postgres, or on a platform that forwards one';
    }
    return [];
  },
};

export default requireDatabase;
