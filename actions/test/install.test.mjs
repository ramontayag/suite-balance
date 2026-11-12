import test from 'node:test';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { access, mkdtemp, readFile, writeFile } from 'node:fs/promises';
import { constants } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { installBinary } from '../lib/install.mjs';

test('installer verifies the exact asset before making it executable, including paths with spaces', async () => {
  const tempDir = await mkdtemp(path.join(tmpdir(), 'suite balance '));
  const bytes = Buffer.from('verified binary');
  const checksum = createHash('sha256').update(bytes).digest('hex');
  const urls = [];
  const result = await installBinary({ tempDir, platform: 'linux', arch: 'x64', fetchImpl: async url => {
    urls.push(url);
    return new Response(url.endsWith('SHA256SUMS') ? `${checksum}  suite-balance_linux_amd64\n` : bytes);
  } });
  assert.deepEqual(await readFile(result), bytes);
  await access(result, constants.X_OK);
  assert.equal(urls.length, 2);
  assert.ok(urls.every(url => url.startsWith('https://github.com/ramontayag/suite-balance/releases/download/v1.0.0/')));
});

test('installer rejects missing, wrong, or duplicated checksums', async () => {
  for (const sums of ['', `${'0'.repeat(64)}  suite-balance_linux_amd64`, `${'0'.repeat(64)}  suite-balance_linux_amd64\n${'0'.repeat(64)}  suite-balance_linux_amd64`]) {
    await assert.rejects(installBinary({ tempDir: tmpdir(), platform: 'linux', arch: 'x64', fetchImpl: async url => new Response(url.endsWith('SHA256SUMS') ? sums : 'bytes') }), /Checksum/);
  }
});

test('installer validates platform and version before network access', async () => {
  const fetchImpl = () => { throw new Error('Must not fetch'); };
  await assert.rejects(installBinary({ tempDir: tmpdir(), platform: 'win32', fetchImpl }), /Unsupported/);
  await assert.rejects(installBinary({ tempDir: tmpdir(), version: '../../main', fetchImpl }), /version/);
  await assert.rejects(installBinary({ binaryPath: 'relative/path', fetchImpl }), /absolute/);
});

test('explicit binary installation requires a regular executable, and never downloads', async () => {
  const tempDir = await mkdtemp(path.join(tmpdir(), 'suite-balance-install-'));
  const binaryPath = path.join(tempDir, 'binary with spaces');
  await writeFile(binaryPath, 'not executable');
  await assert.rejects(installBinary({ binaryPath }));
  assert.equal(await installBinary({ binaryPath: '/usr/bin/true', fetchImpl() { throw new Error('Must not fetch'); } }), '/usr/bin/true');
  await assert.rejects(installBinary({ binaryPath: tempDir }), /regular file/);
});

test('release download HTTP errors fail installation', async () => {
  await assert.rejects(installBinary({ tempDir: tmpdir(), fetchImpl: async () => new Response('', { status: 404 }) }), /download failed/);
});
