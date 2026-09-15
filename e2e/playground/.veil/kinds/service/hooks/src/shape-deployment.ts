import type { FS, RenderHook, RenderHookContext } from './veil-types';

const shapeDeployment: RenderHook = {
  render(ctx: RenderHookContext, fs: FS): FS {
    const file = fs.getSourcesDeploymentYaml();
    const deployment = file.getContent();
    deployment.image = ctx.resource.spec.image;
    deployment.replicas = ctx.resource.spec.replicas;
    deployment.port = ctx.resource.spec.port ?? 8080;
    deployment.public = ctx.resource.spec.public ?? false;
    deployment.region = String(ctx.vars.region);
    file.setContent(deployment);
    return fs;
  },
};

export default shapeDeployment;
