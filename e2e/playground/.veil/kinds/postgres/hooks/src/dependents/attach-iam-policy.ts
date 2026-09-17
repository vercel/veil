import type {
  ServiceDependentHook,
  ServiceDependentHookContext,
  ServiceFS,
} from '../veil-types';

// The database ships the IAM policy that grants access to it, and hands
// a filled-in copy to every service that depends on it. The template
// lives with the postgres kind — the kind that knows what access its
// consumers need — and is reachable here through ctx.selfFS, the same FS
// the database's own hooks see.
//
// ctx.selfFS is read-only in effect: nothing is read back from it, so
// the database's own render is unaffected by what happens here. Only the
// consumer's fs returns.
const attachIamPolicy: ServiceDependentHook = {
  render(ctx: ServiceDependentHookContext, fs: ServiceFS): ServiceFS {
    const template = ctx.selfFS.get('files/iam-policy.tf');
    if (!template) {
      throw new Error('the postgres kind should ship files/iam-policy.tf');
    }

    const db = ctx.self.metadata.name;
    const policy = String(template.getContent())
      .replace(/DB_ACCESS/g, db.replace(/-/g, '_'))
      .replace(/DB_NAME/g, db)
      .replace(/REGION/g, String(ctx.vars.region));

    fs.add(`terraform/${db}-access.tf`, policy);
    return fs;
  },
};

export default attachIamPolicy;
