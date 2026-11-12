import test from 'node:test';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import yazl from 'yazl';
import { downloadArtifactJSON, extractJSON, MAX_ZIP_BYTES, MAX_JSON_BYTES } from '../lib/artifact-download.mjs';

export async function archive(entries) {
  const zip = new yazl.ZipFile();
  const chunks = [];
  const completed = new Promise((resolve, reject) => {
    zip.outputStream.on('data', chunk => chunks.push(chunk));
    zip.outputStream.on('error', reject);
    zip.outputStream.on('end', () => resolve(Buffer.concat(chunks)));
  });
  for (const { name, bytes = '{}', mode, directory } of entries) {
    if (directory) zip.addEmptyDirectory(name);
    else zip.addBuffer(Buffer.from(bytes), name, mode ? { mode } : {});
  }
  zip.end();
  return completed;
}

function options(bytes, extra = {}) {
  return {
    repository: 'owner/repo', token: 'secret', expectedFile: 'receipt.json',
    artifact: { id: 123, digest: `sha256:${createHash('sha256').update(bytes).digest('hex')}`, size_in_bytes: bytes.length },
    fetchImpl: async () => new Response(bytes), ...extra,
  };
}

test('bounded downloader verifies the original ZIP digest and preserves JSON bytes through signed redirects', async () => {
  const raw = '{"version":1,"version":1}';
  const bytes = await archive([{ name: 'receipt.json', bytes: raw }]);
  const calls = [];
  const value = await downloadArtifactJSON(options(bytes, { fetchImpl: async (url, init) => {
    calls.push({ url, init });
    if (calls.length === 1) return new Response(null, { status: 302, headers: { location: 'https://storage.example/signed' } });
    if (calls.length === 2) return new Response(null, { status: 307, headers: { location: 'https://other-storage.example/signed' } });
    return new Response(bytes);
  } }));
  assert.equal(value.toString(), raw);
  assert.equal(calls[0].url, 'https://api.github.com/repos/owner/repo/actions/artifacts/123/zip');
  assert.equal(calls[0].init.headers.authorization, 'Bearer secret');
  assert.ok(calls.every(call => call.init.redirect === 'manual'));
  assert.deepEqual(calls[1].init.headers, {});
  assert.deepEqual(calls[2].init.headers, {});
});

test('downloader rejects unverified ZIP bytes before attempting extraction', async () => {
  const bytes = Buffer.from('not a zip');
  const value = options(bytes);
  value.artifact.digest = `sha256:${'0'.repeat(64)}`;
  await assert.rejects(downloadArtifactJSON(value), /checksum mismatch/);
  value.artifact.digest = undefined;
  await assert.rejects(downloadArtifactJSON(value), /digest is missing/);
});

test('compressed ZIP cap is enforced from artifact metadata, HTTP length, and actual streaming bytes', async () => {
  const value = options(Buffer.from('zip'));
  value.artifact.size_in_bytes = MAX_ZIP_BYTES + 1;
  value.fetchImpl = () => { throw new Error('Must not fetch'); };
  await assert.rejects(downloadArtifactJSON(value), /10 MiB/);
  value.artifact.size_in_bytes = 1;
  value.fetchImpl = async () => new Response('small', { headers: { 'content-length': String(MAX_ZIP_BYTES + 1) } });
  await assert.rejects(downloadArtifactJSON(value), /10 MiB/);
  value.fetchImpl = async () => new Response(new ReadableStream({ start(controller) {
    controller.enqueue(Buffer.alloc(MAX_ZIP_BYTES));
    controller.enqueue(Buffer.from('one byte too many'));
    controller.close();
  } }));
  await assert.rejects(downloadArtifactJSON(value), /10 MiB/);
});

test('archive validation rejects extra files, directories, duplicate entries, and symbolic links', async () => {
  for (const entries of [
    [{ name: 'receipt.json' }, { name: 'extra.json' }],
    [{ name: 'receipt.json' }, { name: 'receipt.json' }],
    [{ name: 'directory/', directory: true }],
    [{ name: 'receipt.json', mode: 0o120777, bytes: '/etc/passwd' }],
    [{ name: 'unexpected.json' }],
  ]) {
    await assert.rejects(extractJSON(await archive(entries), 'receipt.json'), /exactly one|regular JSON/);
  }
});

test('archive validation rejects traversal filenames without writing any entry', async () => {
  const bytes = await archive([{ name: 'xx/receipt.json' }]);
  // Corrupt both stored filename copies; the ZIP writer itself refuses traversal.
  for (let offset = bytes.indexOf('xx/receipt.json'); offset !== -1; offset = bytes.indexOf('xx/receipt.json')) bytes.write('../receipt.json', offset);
  await assert.rejects(extractJSON(bytes, 'receipt.json'), /invalid relative path|regular JSON/);
});

test('compressed bombs and JSON over 8 MiB are rejected, while the exact bound is accepted', async () => {
  const bomb = await archive([{ name: 'receipt.json', bytes: Buffer.alloc(MAX_JSON_BYTES + 1, 32) }]);
  assert.ok(bomb.length < 16 * 1024);
  await assert.rejects(extractJSON(bomb, 'receipt.json'), /8 MiB/);
  const valid = await archive([{ name: 'receipt.json', bytes: Buffer.alloc(MAX_JSON_BYTES, 32) }]);
  assert.equal((await extractJSON(valid, 'receipt.json')).length, MAX_JSON_BYTES);
});

test('artifact redirects must remain HTTPS and have a bounded chain', async () => {
  const value = options(Buffer.from('zip'));
  value.fetchImpl = async () => new Response(null, { status: 302, headers: { location: 'http://storage.example/zip' } });
  await assert.rejects(downloadArtifactJSON(value), /HTTPS/);
  value.fetchImpl = async () => new Response(null, { status: 302, headers: { location: 'https://storage.example/zip' } });
  await assert.rejects(downloadArtifactJSON(value), /redirect/);
});
