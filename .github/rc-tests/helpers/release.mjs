import { createRequire } from 'node:module';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import semanticRelease from 'semantic-release';

const require = createRequire(import.meta.url);
const config = require(join(process.cwd(), '.releaserc.cjs'));
const dependencyRequire = createRequire(require.resolve('semantic-release'));
const localPlugins = new Set([
  '@semantic-release/commit-analyzer',
  '@semantic-release/release-notes-generator',
]);

// Preserve the discovered plugin options and omit only the GitHub publisher.
const plugins = config.plugins
  .filter((entry) => (Array.isArray(entry) ? entry[0] : entry) !== '@semantic-release/github')
  .map((entry) => {
    const name = Array.isArray(entry) ? entry[0] : entry;
    if (name === './.github/rc/validate-version.cjs') return entry;
    if (!localPlugins.has(name)) throw new Error(`Not a local fixture plugin: ${name}`);
    const resolved = dependencyRequire.resolve(name);
    return Array.isArray(entry) ? [resolved, ...entry.slice(1)] : resolved;
  });

const [repositoryUrl, dryRun] = process.argv.slice(2);
fileURLToPath(repositoryUrl);
try {
  const result = await semanticRelease(
    { plugins, repositoryUrl, ci: false, dryRun: dryRun === 'true' },
    { cwd: process.cwd(), env: process.env, stdout: process.stderr, stderr: process.stderr },
  );
  process.stdout.write(JSON.stringify(result));
} catch (error) {
  const errors = error.errors || [error];
  process.stdout.write(JSON.stringify({
    errors: errors.map(({ message, pluginName }) => ({ message, pluginName })),
  }));
  process.exitCode = 1;
}
