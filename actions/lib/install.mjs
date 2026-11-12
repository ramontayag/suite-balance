import { createHash, randomUUID } from 'node:crypto';
import { access, chmod, mkdir, stat, writeFile } from 'node:fs/promises';
import { constants } from 'node:fs';
import path from 'node:path';

async function download(fetchImpl, url, limit) {
  const response = await fetchImpl(url, { signal: AbortSignal.timeout(120_000) });
  if (!response.ok) throw new Error(`Binary download failed (${response.status}): ${url}`);
  const chunks = [];
  let size = 0;
  for await (const chunk of response.body) {
    size += chunk.length;
    if (size > limit) throw new Error('Release download exceeds size limit');
    chunks.push(chunk);
  }
  return Buffer.concat(chunks);
}

export async function installBinary({ binaryPath, version = 'v1.0.0', tempDir, platform = process.platform, arch = process.arch, fetchImpl = fetch }) {
  if (binaryPath) {
    if (!path.isAbsolute(binaryPath)) throw new Error('binary-path must be an absolute executable path');
    if (!(await stat(binaryPath)).isFile()) throw new Error('binary-path must be a regular file');
    await access(binaryPath, constants.X_OK);
    return binaryPath;
  }
  if (!['linux', 'darwin'].includes(platform) || !['x64', 'arm64'].includes(arch)) {
    throw new Error(`Unsupported runner: ${platform}/${arch}. Supply binary-path for this platform.`);
  }
  if (!/^v\d+\.\d+\.\d+(?:-[A-Za-z0-9.-]+)?$/.test(version)) throw new Error('version must be a release version such as v1.0.0');
  const asset = `suite-balance_${platform}_${arch === 'x64' ? 'amd64' : 'arm64'}`;
  const base = `https://github.com/ramontayag/suite-balance/releases/download/${version}`;
  const [binary, sums] = await Promise.all([
    download(fetchImpl, `${base}/${asset}`, 100 * 1024 * 1024),
    download(fetchImpl, `${base}/SHA256SUMS`, 1024 * 1024),
  ]);
  const matches = sums.toString('utf8').split('\n').map(line => line.match(/^([a-fA-F0-9]{64})\s+\*?(\S+)\s*$/)).filter(match => match?.[2] === asset);
  if (matches.length !== 1 || createHash('sha256').update(binary).digest('hex') !== matches[0][1].toLowerCase()) {
    throw new Error(`Checksum verification failed for ${asset}`);
  }
  const directory = path.join(tempDir, `suite-balance-binary-${randomUUID()}`);
  await mkdir(directory, { recursive: true, mode: 0o700 });
  const destination = path.join(directory, asset);
  await writeFile(destination, binary, { mode: 0o600, flag: 'wx' });
  await chmod(destination, 0o755);
  return destination;
}
