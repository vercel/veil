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
// The policy is edited as a tree, not as text: the resource is renamed
// through setName and the ARN through setExpr, so the file's comments
// and formatting come back untouched rather than being regenerated.
const attachIamPolicy: ServiceDependentHook = {
  render(ctx: ServiceDependentHookContext, fs: ServiceFS): ServiceFS {
    const template = ctx.selfFS.get('files/iam-policy.tf');
    if (!template) {
      throw new Error('the postgres kind should ship files/iam-policy.tf');
    }

    const db = ctx.self.metadata.name;
    const region = String(ctx.vars.region);
    const tf = ctx.std.terraform.parse(String(template.getContent()));

    const policy = tf.resource('aws_iam_policy', 'db_access');
    if (!policy) {
      throw new Error('iam-policy.tf should declare aws_iam_policy.db_access');
    }
    // Each consumer gets the policy under its own database's name.
    policy.setName(db.replace(/-/g, '_'));
    policy.body().setAttribute('name', JSON.stringify(`${db}-access`));

    const arn = policy.body().attribute('policy');
    if (arn) {
      arn.setExpr(
        arn
          .expr()
          .replace(/PLACEHOLDER-region/g, region)
          .replace(/PLACEHOLDER/g, db),
      );
    }

    fs.add(`terraform/${db}-access.tf`, ctx.std.terraform.stringify(tf));
    return fs;
  },
};

export default attachIamPolicy;
