import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile, writeFile, rm, readdir } from 'node:fs/promises';
import path from 'node:path';
import { runPlan, runShard, runShardPost, runFinish } from '../lib/workflows.mjs';
import { context, artifactName } from '../lib/runtime.mjs';
import { restoreHistory, verifyObservation } from '../lib/history.mjs';
import { fixture, argument, mockPlan, commit } from './helpers.mjs';

test('plan glob uses committed paths, emits matrix, and visibly falls back without history', async () => {
  const f = await fixture({ files: 'test/**/*.rb', environment: 'standard' });
  mockPlan(f);
  await runPlan(f.runtime);
  assert.equal(f.outputs.matrix, '{"shard":[0,1]}');
  assert.equal(f.outputs['has-tests'], 'true');
  assert.match(f.outputs['fallback-reason'], /No eligible/);
  assert.equal(f.uploads[0].name, artifactName('plan', f.ctx));
  const args = f.calls.find(call => call.args[0] === 'plan').args;
  assert.match(argument(args, '--fallback-reason'), /No eligible/);
  assert.equal(await readFile(argument(args, '--files'), 'utf8'), 'test/a test.rb\ntest/b.rb\n');
  assert.match(f.warnings[0], /no required files are skipped/);
  assert.match(f.summaries[0], /Assignment explanation/);
});

test('empty selected manifest reaches CLI unchanged and produces empty matrix', async () => {
  const f = await fixture({ 'files-from': 'selected files.txt', coverage: 'selected', environment: 'standard' });
  await writeFile(path.join(f.directory, 'selected files.txt'), '');
  mockPlan(f, { has_tests: false });
  await runPlan(f.runtime);
  const args = f.calls.find(call => call.args[0] === 'plan').args;
  assert.equal(await readFile(argument(args, '--files'), 'utf8'), '');
  assert.equal(argument(args, '--coverage'), 'selected');
  assert.equal(f.outputs['has-tests'], 'false');
});

test('invalid or ambiguous inputs stop before downloading or running a binary', async () => {
  for (const inputs of [{}, { files: '**', 'files-from': 'tests.txt' }, { files: '**', shards: '0' }, { files: '**', shards: '257' }, { files: '**', coverage: 'partial' }]) {
    const f = await fixture({ environment: 'standard', ...inputs });
    await assert.rejects(runPlan(f.runtime));
    assert.equal(f.downloads.length, 0);
    assert.equal(f.calls.length, 0);
  }
});

test('plan refuses incorrect matrix shapes or mismatched checksums from the CLI', async () => {
  for (const change of [
    result => { result.matrix = { 0: [0, 2], 1: [1, 3] }; },
    result => { result.matrix = { shard: [0, 0] }; },
    result => { result.matrix = { shard: [3] }; },
    result => { result.checksum = 'f'.repeat(64); },
    result => { result.has_tests = false; },
  ]) {
    const f = await fixture({ files: '**', environment: 'standard' });
    mockPlan(f);
    const exec = f.runtime.exec;
    f.runtime.exec = async (...args) => {
      const response = await exec(...args);
      if (args[1][0] === 'plan') {
        const result = JSON.parse(response.stdout);
        change(result);
        response.stdout = JSON.stringify(result);
      }
      return response;
    };
    await assert.rejects(runPlan(f.runtime), /expected matrix/);
    assert.equal(f.uploads.length, 0);
  }
});

test('plan history API failure is a visible fallback, but current plan upload failure is fatal', async () => {
  const f = await fixture({ files: '**', environment: 'standard' });
  f.routes.set('/actions/artifacts', new Error('History API unavailable'));
  mockPlan(f);
  f.runtime.artifacts.uploadArtifact = async () => { throw new Error('Upload denied'); };
  await assert.rejects(runPlan(f.runtime), /Upload denied/);
  const args = f.calls.find(call => call.args[0] === 'plan').args;
  assert.match(argument(args, '--fallback-reason'), /History API unavailable/);
});

test('plan falls back when run metadata cannot be fetched, but rejects contradictory metadata', async () => {
  const f = await fixture({ files: '**', environment: 'standard' });
  mockPlan(f);
  f.routes.set('/actions/runs/100', new Error('GitHub unavailable'));
  await runPlan(f.runtime);
  assert.match(f.warnings[0], /GitHub unavailable/);
  const contradictory = await fixture({ files: '**', environment: 'standard' });
  mockPlan(contradictory);
  contradictory.run.run_attempt = 1;
  await assert.rejects(runPlan(contradictory.runtime), /metadata does not match/);
  assert.equal(contradictory.uploads.length, 0);
});

test('shard refuses stale timing files without deleting them', async () => {
  const f = await fixture({ index: '0', timings: 'timings.json' });
  await writeFile(path.join(f.directory, 'timings.json'), 'old result');
  await assert.rejects(runShard(f.runtime), /Stale timing report/);
  assert.equal(await readFile(path.join(f.directory, 'timings.json'), 'utf8'), 'old result');
  assert.equal(f.downloads.length, 0);
  assert.equal(f.state.execution, undefined);
});

test('plan and shard reject a different checkout commit or missing checkout before publishing assignments', async () => {
  for (const action of [runPlan, runShard]) {
    for (const response of [{ code: 0, stdout: `${'b'.repeat(40)}\n` }, { code: 128, stdout: '', stderr: 'not a git repository' }]) {
      const f = await fixture({ files: '**', environment: 'standard', index: '0', timings: 'fresh.json' });
      f.runtime.exec = async (executable, args) => {
        assert.equal(executable, 'git');
        assert.deepEqual(args, ['-C', f.runtime.env.GITHUB_WORKSPACE, 'rev-parse', '--verify', 'HEAD']);
        return response;
      };
      await assert.rejects(action(f.runtime), /Checkout HEAD does not match GITHUB_SHA/);
      assert.equal(f.uploads.length, 0);
      assert.equal(f.downloads.length, 0);
      assert.deepEqual(f.outputs, {});
      assert.equal(f.state.execution, undefined);
    }
  }
});

test('shard downloads only current attempt and saves verified binary and manifest for post', async () => {
  const f = await fixture({ index: '0', timings: 'timings with spaces.json' });
  f.addArtifact('plan', { 'plan.json': { checksum: 'expected' } }, '', 1);
  const current = f.addArtifact('plan', { 'plan.json': { checksum: 'expected' } });
  f.runtime.exec = async (_executable, args) => {
    if (_executable === 'git' && args.includes('rev-parse')) return { code: 0, stdout: `${commit}\n` };
    assert.equal(args[0], 'files');
    assert.equal(argument(args, '--commit'), commit);
    await writeFile(argument(args, '--output'), 'test/a test.rb\n');
    return { code: 0, stdout: '' };
  };
  await runShard(f.runtime);
  assert.equal(f.downloads[0].id, current.id);
  assert.equal(f.downloads[0].options.expectedFile, 'plan.json');
  assert.equal(f.downloads[0].options.token, 'fake-token');
  assert.equal(await readFile(f.outputs.files, 'utf8'), 'test/a test.rb\n');
  assert.equal(f.outputs.checksum, 'expected');
  assert.equal(JSON.parse(f.state.execution).timings, path.join(f.directory, 'timings with spaces.json'));
  assert.ok(!f.state.execution.includes('fake-token'));
});

test('missing current-attempt plan is fatal even if a previous attempt exists', async () => {
  const f = await fixture({ index: '0', timings: 'timings.json' });
  f.addArtifact('plan', { 'plan.json': {} }, '', 1);
  await assert.rejects(runShard(f.runtime), /Current run attempt plan/);
});

test('post uploads a receipt even if some files failed; never invokes the test runner', async () => {
  const f = await fixture({ index: '0', timings: 'timings.json' });
  f.state.execution = JSON.stringify({ ctx: f.ctx, executable: '/usr/bin/true', directory: f.directory, plan: path.join(f.directory, 'plan.json'), timings: path.join(f.directory, 'timings.json'), index: 0 });
  f.runtime.exec = async (executable, args) => {
    assert.equal(executable, '/usr/bin/true');
    assert.equal(args[0], 'receipt');
    assert.equal(argument(args, '--attempt'), '2');
    await writeFile(argument(args, '--output'), '{"report":{"files":{"test/a":{"status":"failed"}}}}');
    return { code: 0, stdout: '' };
  };
  await runShardPost(f.runtime);
  assert.equal(f.uploads[0].name, artifactName('report', f.ctx, '-0'));
});

test('post rejects missing reports and wrong attempt without publishing empty success', async () => {
  const f = await fixture();
  f.state.execution = JSON.stringify({ ctx: f.ctx, executable: '/usr/bin/true', directory: f.directory, plan: 'plan', timings: 'missing', index: 0 });
  f.runtime.exec = async () => ({ code: 1, stderr: 'report is missing' });
  await assert.rejects(runShardPost(f.runtime), /report is missing/);
  assert.equal(f.uploads.length, 0);
  f.state.execution = JSON.stringify({ ...JSON.parse(f.state.execution), ctx: { ...f.ctx, attempt: 1 } });
  await assert.rejects(runShardPost(f.runtime), /does not match/);
  f.state.execution = '';
  await runShardPost(f.runtime);
});

async function finishFixture({ coverage = 'full', event = 'push', branch = 'main', failed = false, observation = true } = {}) {
  const f = await fixture({}, { GITHUB_EVENT_NAME: event });
  f.run.head_branch = branch;
  f.runtime.env.GITHUB_REF = `refs/heads/${branch}`;
  f.addArtifact('plan', { 'plan.json': { coverage, environment: 'standard' } });
  f.addArtifact('report', { 'receipt.json': { shard: 0, run_id: 100, attempt: 2, commit } }, '-0');
  f.runtime.exec = async (_executable, args) => {
    assert.equal(args[0], 'finish');
    const reports = argument(args, '--reports-dir');
    assert.equal(JSON.parse(await readFile(path.join(reports, '0.json'), 'utf8')).shard, 0);
    await writeFile(argument(args, '--summary'), 'Coverage summary');
    const source = JSON.parse(await readFile(argument(args, '--source'), 'utf8'));
    assert.equal(source.commit, commit);
    if (observation) await writeFile(argument(args, '--observation'), JSON.stringify({ version: 1, source, files: { 'test/a': { status: 'passed', duration_seconds: 1 } } }));
    return { code: failed ? 1 : 0, stderr: failed ? 'one file failed' : '' };
  };
  return f;
}

test('finish publishes passing files and summary before returning failure for failed files', async () => {
  const f = await finishFixture({ failed: true });
  f.values['retention-days'] = '60';
  await assert.rejects(runFinish(f.runtime), /one file failed/);
  assert.equal(f.summaries[0], 'Coverage summary');
  assert.equal(f.uploads[0].name, artifactName('history', f.ctx));
  assert.equal(f.uploads[0].options.retentionDays, 60);
});

test('finish never publishes PR, selected, or nondefault branch observations', async () => {
  for (const options of [{ event: 'pull_request' }, { coverage: 'selected' }, { branch: 'feature' }]) {
    const f = await finishFixture({ ...options, observation: false });
    await runFinish(f.runtime);
    assert.equal(f.uploads.length, 0);
    const violating = await finishFixture(options);
    await assert.rejects(runFinish(violating.runtime), /Refusing to publish/);
    assert.equal(violating.uploads.length, 0);
  }
});

test('tag pushes named like the default branch cannot publish history', async () => {
  const f = await finishFixture({ observation: false });
  f.runtime.env.GITHUB_REF = 'refs/tags/main';
  f.runtime.env.GITHUB_REF_TYPE = 'tag';
  const exec = f.runtime.exec;
  f.runtime.exec = async (...args) => {
    const source = JSON.parse(await readFile(argument(args[1], '--source'), 'utf8'));
    assert.equal(source.branch, 'refs/tags/main');
    return exec(...args);
  };
  await runFinish(f.runtime);
  assert.equal(f.uploads.length, 0);
});

test('history upload failure fails finish even after successful tests', async () => {
  const f = await finishFixture();
  f.runtime.artifacts.uploadArtifact = async () => { throw new Error('history upload denied'); };
  await assert.rejects(runFinish(f.runtime), /history upload denied/);
});

test('finish ignores prior attempts and marks wrong-attempt receipts invalid for CLI summary', async () => {
  const f = await finishFixture({ observation: false });
  f.addArtifact('report', { 'receipt.json': { attempt: 1 } }, '-1', 1);
  f.artifacts.set(2, { 'receipt.json': { shard: 0, run_id: 100, attempt: 1, commit } });
  f.runtime.exec = async (_executable, args) => {
    assert.equal(await readFile(path.join(argument(args, '--reports-dir'), '0.json'), 'utf8'), 'invalid receipt');
    return { code: 1, stderr: 'invalid receipt' };
  };
  await assert.rejects(runFinish(f.runtime), /invalid receipt/);
  assert.equal(f.downloads.length, 2);
  assert.equal(f.uploads.length, 0);
});

function historyData(f, environment = 'standard') {
  const run = { ...f.run, id: 90, run_attempt: 3, status: 'completed' };
  const artifact = { id: 50, name: `sb-history-${f.ctx.scope}-90-2`, expired: false, workflow_run: { id: 90, head_sha: commit, head_branch: 'main' } };
  const observation = { version: 1, source: { repository: 'owner/repo', workflow: '.github/workflows/test.yml', run_id: 90, attempt: 2, commit, environment, coverage: 'full', event: 'push', branch: 'main', default_branch: 'main' }, files: { 'test/a': { duration_seconds: 1, status: 'passed' } } };
  return { run, artifact, observation };
}

test('history provenance rejects wrong attempt, branch, commit, repository and failed files', async () => {
  const f = await fixture();
  const { run, artifact, observation } = historyData(f);
  assert.equal(verifyObservation(observation, artifact, run, f.ctx, 'main').attempt, 2);
  for (const changes of [{ attempt: 1 }, { branch: 'feature' }, { commit: 'b'.repeat(40) }, { repository: 'fork/repo' }, { event: 'pull_request' }, { coverage: 'selected' }]) {
    assert.throws(() => verifyObservation({ ...observation, source: { ...observation.source, ...changes } }, artifact, run, f.ctx, 'main'), /provenance/);
  }
  assert.throws(() => verifyObservation(observation, { ...artifact, workflow_run: undefined }, run, f.ctx, 'main'), /provenance/);
  assert.throws(() => verifyObservation({ ...observation, files: { a: { duration_seconds: 1, status: 'failed' } } }, artifact, run, f.ctx, 'main'), /passing-file/);
});

test('history restore explicitly downloads previous run and preserves compatible retries', async () => {
  const f = await fixture();
  const data = historyData(f);
  f.routes.set('/actions/artifacts', { artifacts: [data.artifact] });
  f.routes.set('/actions/runs/90', data.run);
  f.routes.set('/actions/runs/90/artifacts', { artifacts: [data.artifact] });
  f.artifacts.set(50, { 'observation.json': data.observation });
  const result = await restoreHistory(f.runtime, f.ctx, { run: f.run, repository: { default_branch: 'main' } }, 'standard', f.directory);
  assert.equal(result.fallback, '');
  assert.equal(f.downloads[0].options.artifact.workflow_run.id, 90);
  assert.equal(JSON.parse(await readFile(path.join(result.directory, '90-2.json'), 'utf8')).source.attempt, 2);
});

test('history discovery paginates repository artifacts without inspecting artifact-less runs', async () => {
  const f = await fixture();
  const data = historyData(f);
  f.routes.set('/actions/artifacts?per_page=100&page=1', { artifacts: Array.from({ length: 100 }, (_, i) => ({ id: 1000 + i, name: 'unrelated-artifact' })) });
  f.routes.set('/actions/artifacts?per_page=100&page=2', { artifacts: [data.artifact] });
  f.routes.set('/actions/runs/90', data.run);
  f.routes.set('/actions/runs/90/artifacts', { artifacts: [data.artifact] });
  f.artifacts.set(50, { 'observation.json': data.observation });
  const result = await restoreHistory(f.runtime, f.ctx, { run: f.run, repository: { default_branch: 'main' } }, 'standard', f.directory);
  assert.equal(result.fallback, '');
  assert.equal(f.downloads[0].options.artifact.workflow_run.id, 90);
});

test('history discovery caches actual run metadata across retained retry artifacts', async () => {
  const f = await fixture();
  const data = historyData(f);
  const prior = { ...data.artifact, id: 51, name: data.artifact.name.replace(/-2$/, '-1') };
  f.routes.set('/actions/artifacts', { artifacts: [data.artifact, prior] });
  f.routes.set('/actions/runs/90', data.run);
  f.artifacts.set(50, { 'observation.json': data.observation });
  f.artifacts.set(51, { 'observation.json': { ...data.observation, source: { ...data.observation.source, attempt: 1 } } });
  const fetch = f.runtime.fetchImpl;
  let lookups = 0;
  f.runtime.fetchImpl = async (...args) => {
    if (args[0].endsWith('/actions/runs/90')) lookups++;
    return fetch(...args);
  };
  const result = await restoreHistory(f.runtime, f.ctx, { run: f.run, repository: { default_branch: 'main' } }, 'standard', f.directory);
  assert.equal(result.fallback, '');
  assert.equal(lookups, 1);
  assert.equal(f.downloads.length, 2);
});

test('PR artifacts claiming a main-run name cannot alter history or force fallback, regardless of listing order', async () => {
  for (const spoofFirst of [true, false]) {
    const f = await fixture();
    const data = historyData(f);
    const spoof = { ...data.artifact, id: 51, workflow_run: { ...data.artifact.workflow_run, id: 85 } };
    const malformedSpoof = { ...spoof, id: 52, name: `sb-history-${f.ctx.scope}-malformed` };
    f.routes.set('/actions/artifacts', { artifacts: spoofFirst ? [spoof, malformedSpoof, data.artifact] : [data.artifact, spoof, malformedSpoof] });
    f.routes.set('/actions/runs/90', data.run);
    f.routes.set('/actions/runs/85', { ...data.run, id: 85, event: 'pull_request', head_branch: 'feature' });
    f.artifacts.set(50, { 'observation.json': data.observation });
    f.artifacts.set(51, new Error('PR artifact must never be downloaded'));
    const result = await restoreHistory(f.runtime, f.ctx, { run: f.run, repository: { default_branch: 'main' } }, 'standard', f.directory);
    assert.equal(result.fallback, '');
    assert.deepEqual(f.downloads.map(item => item.id), [50]);
    assert.equal(JSON.parse(await readFile(path.join(result.directory, '90-2.json'), 'utf8')).files['test/a'].duration_seconds, 1);
  }
});

test('history exceeding the 128 MiB aggregate limit forces complete visible fallback', async t => {
  const f = await fixture();
  t.after(() => rm(f.directory, { recursive: true, force: true }));
  const data = historyData(f);
  data.run.run_attempt = 17;
  const padding = 'x'.repeat(8 * 1024 * 1024 - 2048);
  const artifacts = [];
  for (let attempt = 1; attempt <= 17; attempt++) {
    const artifact = { ...data.artifact, id: 100 + attempt, name: `sb-history-${f.ctx.scope}-90-${attempt}` };
    artifacts.push(artifact);
    f.artifacts.set(artifact.id, { 'observation.json': { ...data.observation, source: { ...data.observation.source, attempt }, padding } });
  }
  f.routes.set('/actions/artifacts', { artifacts });
  f.routes.set('/actions/runs/90', data.run);
  const result = await restoreHistory(f.runtime, f.ctx, { run: f.run, repository: { default_branch: 'main' } }, 'standard', f.directory);
  assert.match(result.fallback, /128 MiB aggregate expanded limit/);
  assert.match(result.fallback, /complete round-robin fallback/);
  assert.equal((await readdir(result.directory)).length, 16);
});

test('history discovery skips expired, current/future, and actually ineligible runs but exposes false provenance', async () => {
  for (const change of [
    data => { data.artifact.expired = true; },
    data => { data.artifact.workflow_run.id = 100; },
    data => { data.artifact.workflow_run.id = 101; },
    data => { data.run.event = 'pull_request'; },
    data => { data.run.head_branch = 'feature'; },
    data => { data.run.path = '.github/workflows/other.yml'; },
  ]) {
    const f = await fixture();
    const data = historyData(f);
    change(data);
    f.routes.set('/actions/artifacts', { artifacts: [data.artifact] });
    f.routes.set('/actions/runs/90', data.run);
    const result = await restoreHistory(f.runtime, f.ctx, { run: f.run, repository: { default_branch: 'main' } }, 'standard', f.directory);
    assert.match(result.fallback, /No eligible/);
    assert.equal(f.downloads.length, 0);
  }
  const f = await fixture();
  const data = historyData(f);
  data.artifact.workflow_run.id = 89;
  f.routes.set('/actions/artifacts', { artifacts: [data.artifact] });
  f.routes.set('/actions/runs/89', { ...data.run, id: 89 });
  const result = await restoreHistory(f.runtime, f.ctx, { run: f.run, repository: { default_branch: 'main' } }, 'standard', f.directory);
  assert.match(result.fallback, /run metadata mismatch/);
});

test('incompatible history explains fallback; malformed history invalidates partial restoration', async () => {
  for (const invalid of [false, true]) {
    const f = await fixture();
    const data = historyData(f, 'other-runner');
    f.routes.set('/actions/artifacts', { artifacts: [data.artifact] });
    f.routes.set('/actions/runs/90', data.run);
    f.routes.set('/actions/runs/90/artifacts', { artifacts: [data.artifact] });
    f.artifacts.set(50, { 'observation.json': invalid ? 'malformed JSON' : data.observation });
    const result = await restoreHistory(f.runtime, f.ctx, { run: f.run, repository: { default_branch: 'main' } }, 'standard', f.directory);
    assert.match(result.fallback, invalid ? /Timing history unavailable/ : /environment does not match/);
  }
});

test('suite namespaces differ by suite, repository, workflow and attempts are immutable names', async () => {
  const f = await fixture();
  const otherSuite = context(f.runtime.env, name => name === 'suite' ? 'other' : f.runtime.input(name));
  assert.notEqual(f.ctx.scope, otherSuite.scope);
  assert.notEqual(artifactName('plan', f.ctx), artifactName('plan', { ...f.ctx, attempt: 1 }));
  assert.throws(() => context(f.runtime.env, name => name === 'suite' ? '../unsafe' : f.runtime.input(name)), /simple identifier/);
});

test('privileged pull_request_target workflows cannot plan, shard or publish', async () => {
  const f = await fixture({ files: '**', environment: 'standard', timings: 'report.json', index: '0' }, { GITHUB_EVENT_NAME: 'pull_request_target' });
  for (const action of [runPlan, runShard, runFinish]) await assert.rejects(action(f.runtime), /pull_request_target/);
});
