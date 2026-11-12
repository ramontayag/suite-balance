import test from 'node:test';
import assert from 'node:assert/strict';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { mkdir, readFile, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { runPlan, runShard, runShardPost, runFinish } from '../lib/workflows.mjs';
import { fixture, argument } from './helpers.mjs';

const exec = promisify(execFile);
const executable = process.env.SUITE_BALANCE_TEST_BINARY;
async function liveFixture({ coverage = 'full', event = 'push', selectedEmpty = false } = {}) {
  const f = await fixture({ 'binary-path': executable, files: '*.test', shards: '2', coverage, environment: 'standard' }, { GITHUB_EVENT_NAME: event });
  // Only this disposable test repository is written; the project checkout and
  // its index/branches remain untouched.
  const root = path.join(f.directory, 'fixture repository');
  await mkdir(root);
  const files = ['first.test', 'second.test', 'file with spaces.test', '$(not-a-command).test'];
  for (const file of files) await writeFile(path.join(root, file), 'fixture');
  await exec('git', ['init', '-q'], { cwd: root });
  await exec('git', ['add', '--', ...files], { cwd: root });
  await exec('git', ['-c', 'user.name=Suite Balance Test', '-c', 'user.email=tests@example.invalid', '-c', 'commit.gpgsign=false', 'commit', '-qm', 'Test fixture'], { cwd: root });
  const { stdout } = await exec('git', ['rev-parse', 'HEAD'], { cwd: root });
  const commit = stdout.trim();
  f.runtime.env.GITHUB_WORKSPACE = root;
  f.runtime.env.GITHUB_SHA = commit;
  f.run.head_sha = commit;
  if (selectedEmpty) {
    f.values.files = '';
    f.values['files-from'] = path.join(f.directory, 'empty.txt');
    await writeFile(f.values['files-from'], '');
  }
  f.runtime.exec = async (binary, args, options = {}) => {
    try { return { ...await exec(binary, args, { maxBuffer: 16 * 1024 * 1024, ...options }), code: 0 }; }
    catch (error) {
      if (typeof error.code !== 'number') throw error;
      return { stdout: error.stdout, stderr: error.stderr, code: error.code };
    }
  };
  f.runtime.artifacts.uploadArtifact = async (name, files, directory, options) => {
    const id = f.artifacts.size + 1;
    const contents = {};
    for (const file of files) contents[path.relative(directory, file)] = await readFile(file, 'utf8');
    f.artifacts.set(id, contents);
    f.listed.push({ id, name, expired: false, workflow_run: { id: Number(f.runtime.env.GITHUB_RUN_ID), head_sha: commit, head_branch: 'main' } });
    f.uploads.push({ id, name, files, directory, options });
    return { id };
  };
  return f;
}

async function runWorkers(f, { failOne = false, omitOne = false } = {}) {
  let observed = 0;
  for (const index of JSON.parse(f.outputs.matrix).shard) {
    f.values.index = String(index);
    f.values.timings = path.join(f.directory, `timings-${index}.json`);
    await runShard(f.runtime);
    const files = (await readFile(f.outputs.files, 'utf8')).trim().split('\n');
    const report = { version: 1, files: {} };
    for (const file of files) {
      if (omitOne && observed++ === 0) continue;
      const status = failOne && observed++ === 0 ? 'failed' : 'passed';
      report.files[file] = { duration_seconds: 5 + observed, status };
    }
    await writeFile(f.values.timings, JSON.stringify(report));
    await runShardPost(f.runtime);
  }
}

test('real CLI: plan → shard/post → finish → immutable history → next plan', { skip: !executable && 'Set SUITE_BALANCE_TEST_BINARY to a built CLI' }, async () => {
  const f = await liveFixture();
  await runPlan(f.runtime);
  assert.equal(f.outputs['has-tests'], 'true');
  assert.match(f.outputs['fallback-reason'], /No eligible/);
  await runWorkers(f);
  await runFinish(f.runtime);
  const history = f.uploads.find(upload => upload.name.startsWith('sb-history-'));
  assert.ok(history);
  const observation = JSON.parse(f.artifacts.get(history.id)['observation.json']);
  assert.ok(Object.keys(observation.files).length >= 2);
  assert.equal(observation.source.attempt, 2);
  const completed = { ...f.run, status: 'completed' };
  f.routes.set('/actions/artifacts', { artifacts: f.listed });
  f.routes.set('/actions/runs/100', completed);
  f.runtime.env.GITHUB_RUN_ID = '101';
  f.routes.set('/actions/runs/101', { ...f.run, id: 101 });
  await runPlan(f.runtime);
  assert.equal(f.outputs['fallback-reason'], '');
  const latestPlan = f.uploads.filter(upload => upload.name.startsWith('sb-plan-')).at(-1);
  const plan = JSON.parse(f.artifacts.get(latestPlan.id)['plan.json']);
  assert.equal(plan.allocation.fallback_reason || '', '');
  assert.ok(plan.history_sources.some(source => source.run_id === 100 && source.attempt === 2));
});

test('real CLI: failed or missing files still publish independent passing-file observations', { skip: !executable && 'Set SUITE_BALANCE_TEST_BINARY to a built CLI' }, async () => {
  for (const options of [{ failOne: true }, { omitOne: true }]) {
    const f = await liveFixture();
    await runPlan(f.runtime);
    await runWorkers(f, options);
    await assert.rejects(runFinish(f.runtime), /finish failed/);
    const history = f.uploads.find(upload => upload.name.startsWith('sb-history-'));
    assert.ok(history, 'passing files must contribute even when finish fails');
    const observation = JSON.parse(f.artifacts.get(history.id)['observation.json']);
    assert.ok(Object.values(observation.files).every(file => file.status === 'passed'));
    assert.ok(f.summaries.at(-1).includes('Coverage'));
  }
});

test('real CLI: selected and PR suites validate results but never publish history', { skip: !executable && 'Set SUITE_BALANCE_TEST_BINARY to a built CLI' }, async () => {
  for (const options of [{ coverage: 'selected' }, { event: 'pull_request' }, { coverage: 'selected', selectedEmpty: true }]) {
    const f = await liveFixture(options);
    await runPlan(f.runtime);
    await runWorkers(f);
    await runFinish(f.runtime);
    assert.ok(!f.uploads.some(upload => upload.name.startsWith('sb-history-')));
    if (options.selectedEmpty) assert.match(f.summaries.at(-1), /No tests selected/);
  }
});

test('real CLI: modified checksum and tested-commit mismatch fail before workers receive files', { skip: !executable && 'Set SUITE_BALANCE_TEST_BINARY to a built CLI' }, async () => {
  for (const changedField of ['checksum', 'commit']) {
    const f = await liveFixture();
    await runPlan(f.runtime);
    const planArtifact = f.uploads[0];
    const plan = JSON.parse(f.artifacts.get(planArtifact.id)['plan.json']);
    plan[changedField] = '0'.repeat(changedField === 'commit' ? 40 : 64);
    f.artifacts.set(planArtifact.id, { 'plan.json': JSON.stringify(plan) });
    f.values.index = '0';
    f.values.timings = path.join(f.directory, 'fresh-report.json');
    await assert.rejects(runShard(f.runtime), /files failed/);
    assert.equal(f.state.execution, undefined);
  }
});

test('real checkout: plan and shard reject HEAD different from GITHUB_SHA before handing out files', { skip: !executable && 'Set SUITE_BALANCE_TEST_BINARY to a built CLI' }, async () => {
  for (const action of [runPlan, runShard]) {
    const f = await liveFixture();
    f.runtime.env.GITHUB_SHA = 'b'.repeat(40);
    f.values.index = '0';
    f.values.timings = path.join(f.directory, 'fresh.json');
    await assert.rejects(action(f.runtime), /Checkout HEAD does not match GITHUB_SHA/);
    assert.equal(f.uploads.length, 0);
    assert.equal(f.downloads.length, 0);
    assert.equal(f.outputs.files, undefined);
  }
});

test('real CLI: finish surfaces source environment or coverage mismatches as failures without publishing history', { skip: !executable && 'Set SUITE_BALANCE_TEST_BINARY to a built CLI' }, async () => {
  for (const field of ['environment', 'coverage']) {
    const f = await liveFixture();
    await runPlan(f.runtime);
    await runWorkers(f);
    const originalExec = f.runtime.exec;
    f.runtime.exec = async (binary, args, options) => {
      if (args[0] === 'finish') {
        const sourcePath = argument(args, '--source');
        const source = JSON.parse(await readFile(sourcePath, 'utf8'));
        source[field] = field === 'coverage' ? 'selected' : 'different-runner';
        await writeFile(sourcePath, JSON.stringify(source));
      }
      return originalExec(binary, args, options);
    };
    await assert.rejects(runFinish(f.runtime), /finish failed/);
    assert.ok(!f.uploads.some(upload => upload.name.startsWith('sb-history-')));
  }
});

function duplicateJSON(raw, kind) {
  if (kind === 'status') return raw.replace('"status":"passed"', '"status":"failed","status":"passed"');
  if (kind === 'key') return raw.replace('"version":1', '"version":1,"version":1');
  const observation = JSON.parse(raw);
  const files = observation.files || observation.report.files;
  const [file, result] = Object.entries(files)[0];
  return raw.replace('"files":{', `"files":{${JSON.stringify(file)}:${JSON.stringify(result)},`);
}

test('real CLI: history duplicate file, status, or root keys reach Go unchanged and cause visible fallback', { skip: !executable && 'Set SUITE_BALANCE_TEST_BINARY to a built CLI' }, async () => {
  for (const kind of ['file', 'status', 'key']) {
    const f = await liveFixture();
    await runPlan(f.runtime);
    await runWorkers(f);
    await runFinish(f.runtime);
    const history = f.uploads.find(upload => upload.name.startsWith('sb-history-'));
    const raw = f.artifacts.get(history.id)['observation.json'];
    const duplicate = duplicateJSON(raw, kind);
    assert.notEqual(duplicate, raw);
    f.artifacts.set(history.id, { 'observation.json': duplicate });
    f.routes.set('/actions/artifacts', { artifacts: f.listed });
    f.routes.set('/actions/runs/100', { ...f.run, status: 'completed' });
    f.runtime.env.GITHUB_RUN_ID = '101';
    f.routes.set('/actions/runs/101', { ...f.run, id: 101 });
    await runPlan(f.runtime);
    assert.match(f.outputs['fallback-reason'], /duplicate/i);
    assert.match(f.summaries.at(-1), /Fallback/i);
  }
});

test('real CLI: receipt duplicate file, status, or root keys fail finish without creating history', { skip: !executable && 'Set SUITE_BALANCE_TEST_BINARY to a built CLI' }, async () => {
  for (const kind of ['file', 'status', 'key']) {
    const f = await liveFixture();
    f.values.shards = '1';
    await runPlan(f.runtime);
    await runWorkers(f);
    const report = f.uploads.find(upload => upload.name.startsWith('sb-report-'));
    const raw = f.artifacts.get(report.id)['receipt.json'];
    const duplicate = duplicateJSON(raw, kind);
    assert.notEqual(duplicate, raw);
    f.artifacts.set(report.id, { 'receipt.json': duplicate });
    await assert.rejects(runFinish(f.runtime), /duplicate/i);
    assert.ok(!f.uploads.some(upload => upload.name.startsWith('sb-history-')));
  }
});

test('real CLI: oversized history falls back and oversized receipts fail without publishing history', { skip: !executable && 'Set SUITE_BALANCE_TEST_BINARY to a built CLI' }, async () => {
  const f = await liveFixture();
  f.values.shards = '1';
  await runPlan(f.runtime);
  await runWorkers(f);
  await runFinish(f.runtime);
  const history = f.uploads.find(upload => upload.name.startsWith('sb-history-'));
  const oversized = ' '.repeat(8 * 1024 * 1024 + 1);
  f.artifacts.set(history.id, { 'observation.json': oversized });
  f.routes.set('/actions/artifacts', { artifacts: f.listed });
  f.routes.set('/actions/runs/100', { ...f.run, status: 'completed' });
  f.runtime.env.GITHUB_RUN_ID = '101';
  f.routes.set('/actions/runs/101', { ...f.run, id: 101 });
  await runPlan(f.runtime);
  assert.match(f.outputs['fallback-reason'], /8 MiB/);

  const receiptFixture = await liveFixture();
  receiptFixture.values.shards = '1';
  await runPlan(receiptFixture.runtime);
  await runWorkers(receiptFixture);
  const report = receiptFixture.uploads.find(upload => upload.name.startsWith('sb-report-'));
  receiptFixture.artifacts.set(report.id, { 'receipt.json': oversized });
  await assert.rejects(runFinish(receiptFixture.runtime), /finish failed/);
  assert.ok(receiptFixture.warnings.some(warning => warning.includes('8 MiB')));
  assert.ok(!receiptFixture.uploads.some(upload => upload.name.startsWith('sb-history-')));
});
