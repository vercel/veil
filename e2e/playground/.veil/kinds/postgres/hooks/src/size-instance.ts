import type { FS, RenderHook, RenderHookContext } from './veil-types';

// Turns the author-facing t-shirt size into a concrete instance class.
const INSTANCE_CLASS: Record<string, string> = {
  small: 'db.t4g.micro',
  medium: 'db.m6g.large',
  large: 'db.m6g.4xlarge',
};

const sizeInstance: RenderHook = {
  render(ctx: RenderHookContext, fs: FS): FS {
    const file = fs.getSourcesDatabaseJson();
    const db = file.getContent();
    db.instanceClass = INSTANCE_CLASS[ctx.resource.spec.size] ?? INSTANCE_CLASS.small;
    db.storageGb = ctx.resource.spec.storageGb ?? 20;
    db.multiAz = ctx.resource.spec.multiAz ?? false;
    file.setContent(db);
    return fs;
  },
};

export default sizeInstance;
