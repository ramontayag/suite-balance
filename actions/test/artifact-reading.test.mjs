import test from 'node:test';
import assert from 'node:assert/strict';
import { open, symlink, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { readArtifactBytes, readJSON, download } from '../lib/runtime.mjs';
import { fixture } from './helpers.mjs';

test('artifact reads preserve original duplicate keys and enforce the 8 MiB bound before parsing', async () => {
  const f = await fixture();
  const file = path.join(f.directory, 'artifact.json');
  const raw = '{"version":1,"version":1}';
  await writeFile(file, raw);
  assert.equal((await readArtifactBytes(file)).toString(), raw);
  const handle = await open(file, 'w');
  await handle.truncate(8 * 1024 * 1024 + 1);
  await handle.close();
  await assert.rejects(readArtifactBytes(file), /8 MiB/);
  await assert.rejects(readJSON(file), /8 MiB/);
});

test('artifact JSON reads reject directories and symlinks', async () => {
  const f = await fixture();
  const file = path.join(f.directory, 'artifact.json');
  await writeFile(file, '{}');
  const link = path.join(f.directory, 'link.json');
  await symlink(file, link);
  await assert.rejects(readJSON(f.directory), /regular file/);
  await assert.rejects(readJSON(link), /regular file/);
});

test('artifact downloader forwards GitHub sha256-prefixed digests unchanged and rejects mismatch', async () => {
  const f = await fixture();
  const digest = `sha256:${'a'.repeat(64)}`;
  const artifact = f.addArtifact('plan', { 'plan.json': {} });
  artifact.digest = digest;
  await download(f.runtime, f.ctx, artifact, path.join(f.directory, 'download'), 'plan.json');
  assert.equal(f.downloads[0].options.artifact.digest, digest);
  f.runtime.downloadArtifactJSON = async () => { throw new Error('Artifact ZIP checksum mismatch'); };
  await assert.rejects(download(f.runtime, f.ctx, artifact, path.join(f.directory, 'bad-download'), 'plan.json'), /checksum mismatch/);
});
