import type { FS, RenderHook, RenderHookContext } from './veil-types';

// Copies the spec into the source. rotationLabel is a string where the
// source schema demands an integer, so a resource that sets it makes
// setContent reject the write at this line — which is how the e2e suite
// exercises schema enforcement inside a hook.
const setRotation: RenderHook = {
  render(ctx: RenderHookContext, fs: FS): FS {
    const file = fs.getSourcesSecretJson();
    const secret = file.getContent();
    secret.rotationDays = (ctx.resource.spec.rotationLabel ?? ctx.resource.spec.rotationDays) as number;
    file.setContent(secret);
    return fs;
  },
};

export default setRotation;
