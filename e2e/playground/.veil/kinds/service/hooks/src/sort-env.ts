import type { FS, RenderHook, RenderHookContext } from './veil-types';

// post_render runs after every dependent hook has injected its env line,
// so this is the one place that sees the finished file. Sorting makes the
// output stable no matter what order the dependencies resolved in.
const sortEnv: RenderHook = {
  render(ctx: RenderHookContext, fs: FS): FS {
    const file = fs.get('sources/env');
    if (!file) return fs;
    const lines = String(file.getContent())
      .split('\n')
      .filter((l: string) => l.trim() !== '')
      .sort();
    file.setContent(lines.join('\n') + (lines.length ? '\n' : ''));
    return fs;
  },
};

export default sortEnv;
