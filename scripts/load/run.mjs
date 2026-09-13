import { readFile, writeFile, mkdir, rm } from 'node:fs/promises';
import { resolve, dirname, relative, isAbsolute } from 'node:path';
import { randomUUID, createHash } from 'node:crypto';
import { spawnSync, execFileSync } from 'node:child_process';
import { validateFixture } from '../../tests/load/model.mjs';

const mode = process.argv[2] ?? 'smoke';
const fixturePath = resolve(process.env.KAPSORA_LOAD_FIXTURE ?? 'test-results/load/fixture.json');
const fixture = validateFixture(JSON.parse(await readFile(fixturePath, 'utf8')), mode);
if (!process.env.KAPSORA_LOAD_PASSWORD) throw new Error('KAPSORA_LOAD_PASSWORD is required.');
if (process.env.K6_HTTP_DEBUG || process.env.K6_OUT) {
  throw new Error('HTTP debug and external/raw metric output are disabled for this runner.');
}
const output = resolve(process.env.KAPSORA_LOAD_OUTPUT ?? `test-results/load/${mode}-summary.json`);
const relativeOutput = relative(resolve('test-results/load'), output);
if (relativeOutput.startsWith('..') || isAbsolute(relativeOutput) || !output.endsWith('.json')) {
  throw new Error('Aggregate reports must be JSON files under test-results/load.');
}
for (const setting of [
  'K6_DURATION',
  'K6_ITERATIONS',
  'K6_VUS',
  'K6_STAGES',
  'K6_EXECUTION_SEGMENT',
]) {
  if (process.env[setting])
    throw new Error(`Remove ${setting}; workload settings belong in the fixture profile.`);
}
await mkdir(dirname(output), { recursive: true });
const bundled =
  process.platform === 'win32'
    ? 'tools/k6/k6-v2.2.0-windows-amd64/k6.exe'
    : 'tools/k6/k6-v2.2.0-linux-amd64/k6';
const executable = process.env.KAPSORA_K6_BIN ?? resolve(bundled);
const version = execFileSync(executable, ['version'], { encoding: 'utf8' });
if (!/\bv2\.2\.0\b/.test(version)) throw new Error('Install the pinned k6 v2.2.0 before running.');
const revision = execFileSync('git', ['rev-parse', 'HEAD'], { encoding: 'utf8' }).trim();
const workloadHash = createHash('sha256');
for (const path of [
  'tests/load/model.mjs',
  'tests/load/client.js',
  'tests/load/scenarios.js',
  'scripts/load/run.mjs',
]) {
  workloadHash.update(path);
  workloadHash.update(await readFile(path));
}
const dirty = execFileSync('git', ['status', '--porcelain'], { encoding: 'utf8' }).trim() !== '';
console.log(`KAPSORA ${mode}: ${fixture.environment.name}. Results: ${output}`);
await rm(output, { force: true });
const result = spawnSync(executable, ['run', '--quiet', 'tests/load/scenarios.js'], {
  stdio: 'inherit',
  env: {
    ...process.env,
    KAPSORA_LOAD_MODE: mode,
    KAPSORA_LOAD_FIXTURE: fixturePath.replaceAll('\\', '/'),
    KAPSORA_LOAD_OUTPUT: output.replaceAll('\\', '/'),
    KAPSORA_LOAD_RUN_ID: randomUUID(),
    KAPSORA_LOAD_REVISION: revision,
    KAPSORA_LOAD_FIXTURE_SHA256: createHash('sha256')
      .update(await readFile(fixturePath))
      .digest('hex'),
    KAPSORA_LOAD_WORKLOAD_SHA256: workloadHash.digest('hex'),
    KAPSORA_LOAD_TREE_DIRTY: String(dirty),
  },
});
if (result.error) throw result.error;
const exitCode = result.status ?? 1;
try {
  const report = JSON.parse(await readFile(output, 'utf8'));
  report.processExitCode = exitCode;
  report.runCompletedSuccessfully = exitCode === 0;
  report.fullCapacityAccepted = false;
  await writeFile(output, JSON.stringify(report, null, 2) + '\n');
} catch (error) {
  if (exitCode === 0) throw error;
  console.error('The failed run did not produce a usable aggregate report.');
}
process.exitCode = exitCode;
