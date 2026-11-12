import * as core from '@actions/core';
import { DefaultArtifactClient } from '@actions/artifact';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { mkdir, mkdtemp, lstat, open, writeFile } from 'node:fs/promises';
import { constants } from 'node:fs';
import path from 'node:path';
import { createHash } from 'node:crypto';
import { installBinary } from './install.mjs';
import { downloadArtifactJSON, MAX_JSON_BYTES } from './artifact-download.mjs';

const execute = promisify(execFile);

export function positiveInteger(value, name, allowZero = false) {
  if (!/^\d+$/.test(String(value)) || !Number.isSafeInteger(Number(value)) || Number(value) < (allowZero ? 0 : 1)) {
    throw new Error(`${name} must be ${allowZero ? 'a nonnegative' : 'a positive'} integer`);
  }
  return Number(value);
}

export function context(env, input) {
  const repository = env.GITHUB_REPOSITORY;
  if (!/^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/.test(repository ?? '')) throw new Error('Invalid GITHUB_REPOSITORY');
  const suite = input('suite') || 'tests';
  if (!/^[A-Za-z0-9_-]{1,64}$/.test(suite)) throw new Error('suite must be a simple identifier (letters, digits, _ or -)');
  const ref = env.GITHUB_WORKFLOW_REF ?? '';
  const workflow = ref.slice(repository.length + 1, ref.lastIndexOf('@'));
  if (!ref.startsWith(`${repository}/.github/workflows/`) || !workflow.endsWith('.yml') && !workflow.endsWith('.yaml')) throw new Error('Invalid GITHUB_WORKFLOW_REF');
  const commit = env.GITHUB_SHA;
  if (!/^(?:[a-fA-F0-9]{40}|[a-fA-F0-9]{64})$/.test(commit ?? '')) throw new Error('Invalid GITHUB_SHA');
  const run = positiveInteger(env.GITHUB_RUN_ID, 'GITHUB_RUN_ID');
  const attempt = positiveInteger(env.GITHUB_RUN_ATTEMPT, 'GITHUB_RUN_ATTEMPT');
  const scope = createHash('sha256').update(JSON.stringify([repository, workflow, suite])).digest('hex').slice(0, 24);
  return { repository, workflow, commit, run, attempt, scope, token: input('token'), event: env.GITHUB_EVENT_NAME };
}

export function artifactName(kind, ctx, suffix = '') {
  return `sb-${kind}-${ctx.scope}-${ctx.run}-${ctx.attempt}${suffix}`;
}

export async function assertCheckout(runtime, ctx) {
  const root = runtime.env.GITHUB_WORKSPACE;
  if (!root || !path.isAbsolute(root)) throw new Error('GITHUB_WORKSPACE must be an absolute checkout directory');
  const result = await runtime.exec('git', ['-C', root, 'rev-parse', '--verify', 'HEAD']);
  if (result.code !== 0 || result.stdout.trim() !== ctx.commit) {
    throw new Error('Checkout HEAD does not match GITHUB_SHA; check out the tested commit before running Suite Balance');
  }
}

export function createRuntime() {
  const env = process.env;
  return {
    env, core,
    input: name => core.getInput(name),
    artifacts: new DefaultArtifactClient(),
    fetchImpl: fetch,
    exec: async (binary, args, options = {}) => {
      try {
        const result = await execute(binary, args, { maxBuffer: 16 * 1024 * 1024, ...options });
        if (result.stderr) core.info(result.stderr.trim());
        return { ...result, code: 0 };
      } catch (error) {
        if (typeof error.code !== 'number') throw error;
        if (error.stderr) core.warning(error.stderr.trim());
        return { stdout: error.stdout, stderr: error.stderr, code: error.code };
      }
    },
  };
}

export async function workspace(runtime) {
  const base = runtime.env.RUNNER_TEMP;
  if (!base || !path.isAbsolute(base)) throw new Error('RUNNER_TEMP must be an absolute directory');
  await mkdir(base, { recursive: true });
  return mkdtemp(path.join(base, 'suite-balance-'));
}

export async function binary(runtime) {
  return installBinary({ binaryPath: runtime.input('binary-path'), version: runtime.input('version') || 'v1.0.0', tempDir: runtime.env.RUNNER_TEMP, fetchImpl: runtime.fetchImpl });
}

export class GitHubAPIError extends Error {}

export async function api(runtime, ctx, endpoint) {
  const base = runtime.env.GITHUB_API_URL || 'https://api.github.com';
  try {
    const response = await runtime.fetchImpl(`${base}/repos/${ctx.repository}${endpoint}`, {
      headers: { authorization: `Bearer ${ctx.token}`, accept: 'application/vnd.github+json', 'X-GitHub-Api-Version': '2022-11-28' },
      signal: AbortSignal.timeout(30_000),
    });
    if (!response.ok) throw new Error(`HTTP ${response.status}`);
    return await response.json();
  } catch (error) {
    throw new GitHubAPIError(`GitHub API ${endpoint}: ${error.message}`);
  }
}

export async function* pages(runtime, ctx, endpoint, key) {
  for (let page = 1; ; page++) {
    const data = await api(runtime, ctx, `${endpoint}${endpoint.includes('?') ? '&' : '?'}per_page=100&page=${page}`);
    if (!Array.isArray(data[key])) throw new Error(`Invalid GitHub API ${key} response`);
    yield* data[key];
    if (data[key].length < 100) return;
  }
}

export async function metadata(runtime, ctx) {
  const [repository, run] = await Promise.all([api(runtime, ctx, ''), api(runtime, ctx, `/actions/runs/${ctx.run}`)]);
  if (run.id !== ctx.run || run.run_attempt !== ctx.attempt || run.path?.split('@')[0] !== ctx.workflow || run.event !== ctx.event || run.repository?.full_name !== ctx.repository) {
    throw new Error('Current workflow run metadata does not match GitHub context');
  }
  if (!repository.default_branch) throw new Error('Repository default branch is unavailable');
  if (ctx.event === 'push' && run.head_sha !== ctx.commit) throw new Error('Push commit does not match workflow run');
  return { repository, run };
}

export async function download(runtime, ctx, artifact, directory, expectedFile) {
  const bytes = await (runtime.downloadArtifactJSON || downloadArtifactJSON)({
    fetchImpl: runtime.fetchImpl, apiBase: runtime.env.GITHUB_API_URL || 'https://api.github.com',
    repository: ctx.repository, token: ctx.token, artifact, expectedFile,
  });
  await mkdir(directory, { recursive: true });
  await writeFile(path.join(directory, expectedFile), bytes, { flag: 'wx', mode: 0o600 });
  return directory;
}

export async function currentArtifacts(runtime, ctx) {
  const artifacts = [];
  for await (const artifact of pages(runtime, ctx, `/actions/runs/${ctx.run}/artifacts`, 'artifacts')) artifacts.push(artifact);
  return artifacts;
}

export async function downloadPlan(runtime, ctx, directory) {
  const matches = (await currentArtifacts(runtime, ctx)).filter(item => item.name === artifactName('plan', ctx));
  if (matches.length !== 1 || matches[0].expired) throw new Error('Current run attempt plan artifact is missing or ambiguous');
  await download(runtime, ctx, matches[0], directory, 'plan.json');
  return path.join(directory, 'plan.json');
}

export async function upload(runtime, name, file, retentionDays) {
  const result = await runtime.artifacts.uploadArtifact(name, [file], path.dirname(file), retentionDays ? { retentionDays } : {});
  if (!result.id) throw new Error(`Artifact upload did not create ${name}`);
}

export async function command(runtime, executable, args, options) {
  const result = await runtime.exec(executable, args, options);
  if (result.code !== 0) throw new Error(`suite-balance ${args[0]} failed (${result.code}): ${result.stderr || result.stdout || 'see job log'}`);
  return result.stdout;
}

export async function appendSummary(runtime, markdown) {
  await runtime.core.summary.addRaw(markdown).write();
}

export async function readArtifactBytes(file) {
  const maximum = MAX_JSON_BYTES;
  if (!(await lstat(file)).isFile()) throw new Error('Artifact JSON must be a regular file');
  const handle = await open(file, constants.O_RDONLY | constants.O_NOFOLLOW | constants.O_NONBLOCK);
  try {
    const info = await handle.stat();
    if (!info.isFile()) throw new Error('Artifact JSON must be a regular file');
    if (info.size > maximum) throw new Error('Artifact JSON exceeds the 8 MiB limit');
    const chunks = [];
    let size = 0;
    while (size <= maximum) {
      const chunk = Buffer.allocUnsafe(Math.min(64 * 1024, maximum + 1 - size));
      const { bytesRead } = await handle.read(chunk, 0, chunk.length, null);
      if (!bytesRead) return Buffer.concat(chunks, size);
      size += bytesRead;
      if (size > maximum) throw new Error('Artifact JSON exceeds the 8 MiB limit');
      chunks.push(chunk.subarray(0, bytesRead));
    }
  } finally { await handle.close(); }
}

export async function readJSON(file) { return JSON.parse((await readArtifactBytes(file)).toString('utf8')); }
export async function writeJSON(file, data) { await writeFile(file, JSON.stringify(data)); }
