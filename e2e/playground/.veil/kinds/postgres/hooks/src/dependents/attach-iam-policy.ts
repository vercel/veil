import type {
  ServiceDependentHook,
  ServiceDependentHookContext,
  ServiceFS,
} from '../veil-types';

// The database ships the IAM policy that grants access to it, and hands
// a filled-in copy to every service that depends on it. The template
// lives with the postgres kind — the kind that knows what access its
// consumers need — and is reachable through ctx.selfFS.
//
// The policy is edited as data, not as text: std.terraform.parse gives
// the same object Terraform's own JSON syntax would, so renaming the
// resource is a field assignment rather than a regex over HCL.
const attachIamPolicy: ServiceDependentHook = {
  render(ctx: ServiceDependentHookContext, fs: ServiceFS): ServiceFS {
    const template = ctx.selfFS.get('files/iam-policy.tf');
    if (!template) {
      throw new Error('the postgres kind should ship files/iam-policy.tf');
    }

    const db = ctx.self.metadata.name;
    const region = String(ctx.vars.region);
    const tf = ctx.std.terraform.parse(String(template.getContent())) as any;

    const body = tf.resource.aws_iam_policy.db_access[0];
    body.name = `${db}-access`;
    body.policy = body.policy
      .replace(/PLACEHOLDER-region/g, region)
      .replace(/PLACEHOLDER/g, db);
    // Each consumer gets the policy under the database's own label.
    tf.resource.aws_iam_policy = { [db.replace(/-/g, '_')]: [body] };

    fs.add(`terraform/${db}-access.tf`, ctx.std.terraform.stringify(tf));
    return fs;
  },
};

export default attachIamPolicy;
