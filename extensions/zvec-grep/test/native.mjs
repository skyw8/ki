import { spawnSync } from 'node:child_process';
import { existsSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = dirname(dirname(fileURLToPath(import.meta.url)));
const env = { ...process.env };
env.CMAKE_BUILD_PARALLEL_LEVEL ??= env.KI_ZVEC_GREP_BUILD_JOBS || '4';

// Bindgen runs in dependency build scripts, before native/build.rs can help.
// Apply the launcher's Linux header fallback before starting Cargo tests too.
if (process.platform === 'linux' && env.BINDGEN_EXTRA_CLANG_ARGS === undefined) {
  const probe = spawnSync('cc', ['-print-file-name=include'], { encoding: 'utf8' });
  const include = probe.stdout?.trim();
  if (probe.status === 0 && include && existsSync(join(include, 'stdbool.h'))) {
    const argument = /[\s"'\\]/.test(include) ? JSON.stringify(include) : include;
    env.BINDGEN_EXTRA_CLANG_ARGS = `-I${argument}`;
  }
}

for (const [command, args] of [
  ['rustup', ['run', '1.98.0', 'cargo', 'test', '--release', '--locked', '--manifest-path', join(root, 'native', 'Cargo.toml')]],
  ['cargo', ['test', '--release', '--locked', '--manifest-path', join(root, 'Cargo.toml')]],
]) {
  const result = spawnSync(command, args, { cwd: root, env, stdio: 'inherit' });
  if (result.error) {
    console.error(result.error.message);
    process.exit(1);
  }
  if (result.status !== 0) process.exit(result.status ?? 1);
}
