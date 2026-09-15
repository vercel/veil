import type { PostgresDependentHook, PostgresDependentHookContext, PostgresFS } from '../veil-types';

// Anything placed in the VPC gets a record of where it landed. Written as
// a new file rather than into the consumer's own source, which is
// schema-closed and has no field for it.
const placePostgres: PostgresDependentHook = {
  render(ctx: PostgresDependentHookContext, fs: PostgresFS): PostgresFS {
    fs.add('sources/vpc', `vpc=${ctx.self.metadata.name}\ncidr=${ctx.self.spec.cidr}\n`);
    return fs;
  },
};

export default placePostgres;
