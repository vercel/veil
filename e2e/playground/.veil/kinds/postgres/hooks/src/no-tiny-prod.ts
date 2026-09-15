import type { FS, ValidateHook, ValidationIssue, RenderHookContext } from './veil-types';

// Production databases must not be single-AZ micro instances. A validate
// hook rather than JSON Schema because the rule spans the spec and the
// environment variable.
const noTinyProd: ValidateHook = {
  validate(ctx: RenderHookContext, fs: FS) {
    if (ctx.vars.environment !== 'production') return [];
    const db = fs.getSourcesDatabaseJson().getContent();
    const issues: ValidationIssue[] = [];
    if (!db.multiAz) {
      issues.push({ path: 'spec.multiAz', message: 'production databases must be multi-AZ', severity: 'error' });
    }
    if (db.instanceClass === 'db.t4g.micro') {
      issues.push({ path: 'spec.size', message: 'production databases must be larger than "small"', severity: 'error' });
    }
    return issues;
  },
};

export default noTinyProd;
