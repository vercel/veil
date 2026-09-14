import type { PostgresDependentHook, PostgresDependentHookContext, PostgresFS } from '../veil-types';

// A database may hold a secret; nothing else may. That asymmetry is the
// point of this kind — it accepts postgres as a consumer and no one
// else, so forwarding it to a service is an error rather than a silent
// omission.
const attachToPostgres: PostgresDependentHook = {
  render(ctx: PostgresDependentHookContext, fs: PostgresFS): PostgresFS {
    fs.add('sources/secret-ref', `secret=${ctx.self.metadata.name}\nrole=${ctx.params.role ?? 'owner'}\n`);
    return fs;
  },
};

export default attachToPostgres;
