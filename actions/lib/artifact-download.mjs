import { createHash } from 'node:crypto';
import { promisify } from 'node:util';
import yauzl from 'yauzl';

export const MAX_ZIP_BYTES = 10 * 1024 * 1024;
export const MAX_JSON_BYTES = 8 * 1024 * 1024;
const fromBuffer = promisify(yauzl.fromBuffer);
const expectedFiles = new Set(['plan.json', 'receipt.json', 'observation.json']);

async function boundedResponse(response) {
  if (Number(response.headers.get('content-length')) > MAX_ZIP_BYTES) {
    await response.body?.cancel();
    throw new Error('Artifact ZIP exceeds the 10 MiB compressed limit');
  }
  if (!response.body) throw new Error('Artifact ZIP response has no body');
  const chunks = [];
  let size = 0;
  for await (const chunk of response.body) {
    size += chunk.length;
    if (size > MAX_ZIP_BYTES) throw new Error('Artifact ZIP exceeds the 10 MiB compressed limit');
    chunks.push(Buffer.from(chunk));
  }
  return Buffer.concat(chunks, size);
}

export async function extractJSON(bytes, expectedFile) {
  if (!expectedFiles.has(expectedFile)) throw new Error('Unexpected artifact JSON filename');
  if (bytes.length > MAX_ZIP_BYTES) throw new Error('Artifact ZIP exceeds the 10 MiB compressed limit');
  const zip = await fromBuffer(bytes, { lazyEntries: true, validateEntrySizes: true, strictFileNames: true });
  return new Promise((resolve, reject) => {
    let content;
    let stream;
    let settled = false;
    function fail(error) {
      if (settled) return;
      settled = true;
      stream?.destroy();
      zip.close();
      reject(error);
    }
    zip.on('error', fail);
    zip.on('end', () => {
      if (settled) return;
      settled = true;
      zip.close();
      if (!content) reject(new Error('Artifact ZIP has no expected JSON file'));
      else resolve(content);
    });
    zip.on('entry', entry => {
      (async () => {
        const type = (entry.externalFileAttributes >>> 16) & 0xf000;
        if (content || entry.fileName !== expectedFile || entry.externalFileAttributes & 0x10 || type !== 0 && type !== 0x8000 || entry.generalPurposeBitFlag & 1) {
          throw new Error('Artifact ZIP must contain only the expected regular JSON file');
        }
        if (entry.uncompressedSize > MAX_JSON_BYTES) throw new Error('Artifact JSON exceeds the 8 MiB expanded limit');
        stream = await new Promise((resolveStream, rejectStream) => zip.openReadStream(entry, (error, value) => error ? rejectStream(error) : resolveStream(value)));
        const chunks = [];
        let size = 0;
        for await (const chunk of stream) {
          size += chunk.length;
          if (size > MAX_JSON_BYTES) throw new Error('Artifact JSON exceeds the 8 MiB expanded limit');
          chunks.push(chunk);
        }
        if (settled) return;
        content = Buffer.concat(chunks, size);
        zip.readEntry();
      })().catch(fail);
    });
    if (zip.entryCount !== 1) fail(new Error('Artifact ZIP must contain exactly one JSON file'));
    else zip.readEntry();
  });
}

export async function downloadArtifactJSON({ fetchImpl = fetch, apiBase = 'https://api.github.com', repository, token, artifact, expectedFile }) {
  if (!Number.isSafeInteger(artifact.id) || artifact.id <= 0) throw new Error('Invalid artifact ID');
  if (!expectedFiles.has(expectedFile)) throw new Error('Unexpected artifact JSON filename');
  if (artifact.size_in_bytes > MAX_ZIP_BYTES) throw new Error('Artifact ZIP exceeds the 10 MiB compressed limit');
  if (!/^sha256:[a-f0-9]{64}$/i.test(artifact.digest ?? '')) throw new Error('Artifact ZIP SHA256 digest is missing or invalid');
  let url = new URL(`${apiBase}/repos/${repository}/actions/artifacts/${artifact.id}/zip`);
  let response;
  for (let redirects = 0; ; redirects++) {
    if (url.protocol !== 'https:' || url.username || url.password) throw new Error('Artifact download requires an HTTPS URL without credentials');
    response = await fetchImpl(url.href, {
      redirect: 'manual', signal: AbortSignal.timeout(120_000),
      // Authentication is only sent to the initial GitHub API request, never
      // to a signed storage URL (including further storage redirects).
      headers: redirects === 0 ? { authorization: `Bearer ${token}`, accept: 'application/vnd.github+json', 'X-GitHub-Api-Version': '2022-11-28' } : {},
    });
    if (![301, 302, 303, 307, 308].includes(response.status)) break;
    await response.body?.cancel();
    if (redirects >= 5 || !response.headers.get('location')) throw new Error('Invalid artifact download redirect');
    url = new URL(response.headers.get('location'), url);
  }
  if (!response.ok) {
    await response.body?.cancel();
    throw new Error(`Artifact ZIP download failed: HTTP ${response.status}`);
  }
  const bytes = await boundedResponse(response);
  const digest = `sha256:${createHash('sha256').update(bytes).digest('hex')}`;
  if (digest !== artifact.digest.toLowerCase()) throw new Error('Artifact ZIP checksum mismatch');
  return extractJSON(bytes, expectedFile);
}
