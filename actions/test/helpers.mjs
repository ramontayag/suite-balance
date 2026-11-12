import { mkdtemp, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { tmpdir } from 'node:os';
import { context, artifactName } from '../lib/runtime.mjs';

export const commit = 'a'.repeat(40);

export async function fixture(inputs = {}, overrides = {}) {
  const directory = await mkdtemp(path.join(process.env.RUNNER_TEMP || tmpdir(), 'suite-balance-test-'));
  const env = {
    RUNNER_TEMP: directory, GITHUB_WORKSPACE: directory, GITHUB_REPOSITORY: 'owner/repo',
    GITHUB_WORKFLOW_REF: 'owner/repo/.github/workflows/test.yml@refs/heads/main',
    GITHUB_SHA: commit, GITHUB_RUN_ID: '100', GITHUB_RUN_ATTEMPT: '2', GITHUB_EVENT_NAME: 'push',
    GITHUB_REF: 'refs/heads/main', GITHUB_REF_TYPE: 'branch',
    ...overrides,
  };
  const values = { token: 'fake-token', 'binary-path': '/usr/bin/true', ...inputs };
  const outputs = {}, state = {}, warnings = [], summaries = [], uploads = [], downloads = [], calls = [];
  const artifacts = new Map();
  const listed = [];
  const run = {
    id: 100, run_attempt: 2, path: '.github/workflows/test.yml', event: env.GITHUB_EVENT_NAME,
    head_sha: env.GITHUB_SHA, head_branch: 'main', workflow_id: 9, status: 'in_progress',
    repository: { full_name: env.GITHUB_REPOSITORY }, head_repository: { full_name: env.GITHUB_REPOSITORY },
  };
  const routes = new Map([
    ['', { default_branch: 'main' }], ['/actions/runs/100', run],
  ]);
  const core = {
    info() {}, warning(message) { warnings.push(message); },
    setOutput(name, value) { outputs[name] = value; },
    saveState(name, value) { state[name] = value; }, getState(name) { return state[name] || ''; },
    summary: { addRaw(text) { summaries.push(text); return this; }, async write() {} },
  };
  const runtime = {
    env, input: name => values[name] ?? '', core,
    async fetchImpl(url) {
      const parsed = new URL(url);
      const endpoint = parsed.pathname.replace('/repos/owner/repo', '');
      let data = routes.get(endpoint + parsed.search) ?? routes.get(endpoint);
      if (endpoint === '/actions/runs/100/artifacts') data = { artifacts: listed };
      if (data instanceof Error) throw data;
      if (!data && endpoint === '/actions/artifacts') data = { artifacts: [] };
      if (!data) throw new Error(`Unexpected API route ${endpoint}${parsed.search}`);
      return { ok: true, async json() { return data; } };
    },
    async downloadArtifactJSON(options) {
        const id = options.artifact.id;
        downloads.push({ id, options });
        const files = artifacts.get(id);
        if (files instanceof Error) throw files;
        if (!files) throw new Error(`No artifact fixture ${id}`);
        const data = files[options.expectedFile];
        if (data === undefined) throw new Error(`Missing expected file ${options.expectedFile}`);
        return Buffer.from(typeof data === 'string' ? data : JSON.stringify(data));
    },
    artifacts: {
      async uploadArtifact(name, files, root, options) { uploads.push({ name, files, root, options }); return { id: 77 }; },
    },
    async exec(executable, args, options) {
      calls.push({ executable, args, options });
      if (executable === 'git' && args.includes('rev-parse')) return { code: 0, stdout: `${env.GITHUB_SHA}\n` };
      throw new Error(`Unexpected command ${args[0]}`);
    },
  };
  const ctx = context(env, runtime.input);
  function addArtifact(kind, files, suffix = '', attempt = ctx.attempt) {
    const id = artifacts.size + 1;
    const artifact = { id, name: artifactName(kind, { ...ctx, attempt }, suffix), expired: false };
    artifacts.set(id, files); listed.push(artifact); return artifact;
  }
  return { runtime, directory, values, ctx, run, routes, outputs, state, warnings, summaries, uploads, downloads, calls, artifacts, listed, addArtifact };
}

export function argument(args, name) { return args[args.indexOf(name) + 1]; }

export function mockPlan(f, { has_tests = true } = {}) {
  f.runtime.exec = async (executable, args, options) => {
    f.calls.push({ executable, args, options });
    if (executable === 'git') return { code: 0, stdout: args.includes('rev-parse') ? `${f.runtime.env.GITHUB_SHA}\n` : 'test/a test.rb\0test/b.rb\0README.md\0' };
    if (args[0] === 'explain') return { code: 0, stdout: 'Assignment explanation' };
    if (args[0] === 'plan') {
      await writeFile(argument(args, '--output'), JSON.stringify({ version: 1, commit, environment: 'standard', coverage: 'full', checksum: 'c'.repeat(64) }));
      return { code: 0, stdout: JSON.stringify({ matrix: { shard: has_tests ? [0, 1] : [] }, has_tests, checksum: 'c'.repeat(64), fallback_reason: args.includes('--fallback-reason') ? argument(args, '--fallback-reason') : '' }) };
    }
    throw new Error(`Unexpected command ${args[0]}`);
  };
}
