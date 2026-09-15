import type { ServiceDependentHook, ServiceDependentHookContext, ServiceFS } from '../veil-types';

// Runs against the *consumer's* bundle: a service that depends on this
// database gets the connection string written into its env file under
// whatever name it asked for. ctx.self is the database, ctx.consumer the
// service — and the FS is the service's, typed as such.
const injectDatabaseUrl: ServiceDependentHook = {
  render(ctx: ServiceDependentHookContext, fs: ServiceFS): ServiceFS {
    const host = `${ctx.self.metadata.name}.${ctx.vars.region}.rds.acme.internal`;
    const pool = ctx.params.poolSize ?? 10;
    const line = `${ctx.params.envVar}=postgres://acme@${host}:5432/${ctx.self.metadata.name}?pool=${pool}`;
    const file = fs.getSourcesEnv();
    const existing = file.getContent();
    file.setContent(existing ? `${existing}\n${line}` : line);
    return fs;
  },
};

export default injectDatabaseUrl;
