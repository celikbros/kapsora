import { mkdir, readFile, writeFile } from 'node:fs/promises';
import { resolve } from 'node:path';
import { createHash } from 'node:crypto';
import { execFileSync } from 'node:child_process';

const packages = {
  win32: {
    file: 'k6-v2.2.0-windows-amd64.zip',
    sha256: 'ceb2b1e1cf9dbe1303c6c33ec83ffda86dda5c610b4def92064d3c7ebae8d9f4',
  },
  linux: {
    file: 'k6-v2.2.0-linux-amd64.tar.gz',
    sha256: 'b5a8003c86f35f5cd5ceef1490312c48e587696c94d998cefc6d7b3b4cb1597d',
  },
};
const artifact = packages[process.platform];
if (!artifact || process.arch !== 'x64')
  throw new Error('The pinned installer supports Windows/Linux amd64.');
const directory = resolve('tools/k6');
const path = resolve(directory, artifact.file);
await mkdir(directory, { recursive: true });
let bytes;
try {
  bytes = await readFile(path);
} catch {
  /* download below */
}
if (!bytes || createHash('sha256').update(bytes).digest('hex') !== artifact.sha256) {
  const response = await fetch(
    `https://github.com/grafana/k6/releases/download/v2.2.0/${artifact.file}`,
    {
      signal: AbortSignal.timeout(120000),
    },
  );
  if (!response.ok) throw new Error(`Official k6 download failed: ${response.status}`);
  bytes = Buffer.from(await response.arrayBuffer());
  if (createHash('sha256').update(bytes).digest('hex') !== artifact.sha256)
    throw new Error('k6 SHA-256 mismatch.');
  await writeFile(path, bytes);
}
// Windows ships bsdtar, which also extracts ZIP archives. Only the verified release is extracted.
execFileSync('tar', ['-xf', path, '-C', directory], { stdio: 'inherit' });
console.log('Installed verified k6 v2.2.0 under tools/k6.');
