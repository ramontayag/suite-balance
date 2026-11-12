import { access, readFile, writeFile, lstat, mkdir } from 'node:fs/promises';
import path from 'node:path';
import { minimatch } from 'minimatch';
import { restoreHistory } from './history.mjs';
import { context, artifactName, assertCheckout, workspace, binary, metadata, command, upload, downloadPlan, currentArtifacts, download, positiveInteger, appendSummary, readArtifactBytes, readJSON, writeJSON, GitHubAPIError } from './runtime.mjs';

export async function runPlan(runtime) {
  const ctx = context(runtime.env, runtime.input);
  if (ctx.event === 'pull_request_target') throw new Error('pull_request_target is not supported; use pull_request to avoid privileged untrusted tests');
  const pattern = runtime.input('files');
  const manifestInput = runtime.input('files-from');
  if (Boolean(pattern) === Boolean(manifestInput)) throw new Error('Supply exactly one of files or files-from');
  const environment = runtime.input('environment');
  if (!environment.trim()) throw new Error('environment is required');
  const coverage = runtime.input('coverage') || 'full';
  if (!['full', 'selected'].includes(coverage)) throw new Error('coverage must be full or selected');
  const shards = positiveInteger(runtime.input('shards') || '3', 'shards');
  if (shards > 256) throw new Error('shards must not exceed 256');
  await assertCheckout(runtime, ctx);
  const directory = await workspace(runtime);
  const executable = await binary(runtime);
  const root = runtime.env.GITHUB_WORKSPACE || process.cwd();
  const files = path.join(directory, 'required-files.txt');
  if (manifestInput) {
    await writeFile(files, await readFile(path.resolve(root, manifestInput)));
  } else {
    const result = await runtime.exec('git', ['ls-tree', '-rz', '--name-only', ctx.commit], { cwd: root });
    if (result.code !== 0) throw new Error(`Cannot enumerate committed files: ${result.stderr}`);
    const matches = result.stdout.split('\0').filter(file => file && minimatch(file, pattern, { dot: true, nonegate: true, nocomment: true }));
    if (matches.some(file => /[\r\n]/.test(file))) throw new Error('File names containing newlines cannot be represented in a manifest');
    await writeFile(files, matches.length ? `${matches.join('\n')}\n` : '');
  }
  let history;
  try {
    const meta = await metadata(runtime, ctx);
    history = await restoreHistory(runtime, ctx, meta, environment, directory);
  } catch (error) {
    if (!(error instanceof GitHubAPIError)) throw error;
    const emptyHistory = path.join(directory, 'history');
    await mkdir(emptyHistory);
    history = { directory: emptyHistory, fallback: `Timing history unavailable: ${error.message}` };
  }
  if (history.fallback) runtime.core.warning(`${history.fallback}. Using round-robin; no required files are skipped.`);
  const planPath = path.join(directory, 'plan.json');
  const args = ['plan', '--files', files, '--shards', String(shards), '--coverage', coverage, '--environment', environment, '--commit', ctx.commit, '--history-dir', history.directory, '--output', planPath, '--root', root];
  if (history.fallback) args.push('--fallback-reason', history.fallback);
  const result = JSON.parse(await command(runtime, executable, args));
  const indices = result.matrix?.shard;
  if (!Array.isArray(indices) || Object.keys(result.matrix).length !== 1 ||
      indices.some((index, offset) => !Number.isInteger(index) || index < 0 || index >= shards || offset > 0 && index <= indices[offset - 1]) ||
      typeof result.has_tests !== 'boolean' || result.has_tests !== (indices.length > 0) ||
      !/^[a-f0-9]{64}$/.test(result.checksum ?? '') || (await readJSON(planPath)).checksum !== result.checksum) {
    throw new Error('CLI plan output does not match the expected matrix, has_tests, and checksum contract');
  }
  await upload(runtime, artifactName('plan', ctx), planPath);
  runtime.core.setOutput('matrix', JSON.stringify(result.matrix));
  runtime.core.setOutput('has-tests', String(result.has_tests));
  runtime.core.setOutput('checksum', result.checksum);
  runtime.core.setOutput('fallback-reason', result.fallback_reason || '');
  const explanation = await command(runtime, executable, ['explain', '--plan', planPath, '--commit', ctx.commit]);
  await appendSummary(runtime, `## Suite Balance assignment\n\n\`\`\`text\n${explanation}\n\`\`\`\n`);
}

async function assertAbsent(file) {
  try { await lstat(file); } catch (error) { if (error.code === 'ENOENT') return; throw error; }
  throw new Error(`Stale timing report already exists at ${file}. Choose a fresh timings path or remove it before the shard action; reports must belong to this execution.`);
}

export async function runShard(runtime) {
  const ctx = context(runtime.env, runtime.input);
  if (ctx.event === 'pull_request_target') throw new Error('pull_request_target is not supported');
  const index = positiveInteger(runtime.input('index'), 'index', true);
  if (!runtime.input('timings')) throw new Error('timings is required');
  const timings = path.resolve(runtime.env.GITHUB_WORKSPACE || process.cwd(), runtime.input('timings'));
  await assertAbsent(timings);
  await assertCheckout(runtime, ctx);
  const directory = await workspace(runtime);
  const executable = await binary(runtime);
  const plan = await downloadPlan(runtime, ctx, path.join(directory, 'plan'));
  const files = path.join(directory, 'files.txt');
  await command(runtime, executable, ['files', '--plan', plan, '--shard', String(index), '--commit', ctx.commit, '--output', files]);
  // A single state record ensures post either sees a fully prepared assignment
  // or does nothing when setup failed before tests could run.
  const binding = { run: ctx.run, attempt: ctx.attempt, commit: ctx.commit, scope: ctx.scope };
  runtime.core.saveState('execution', JSON.stringify({ ctx: binding, directory, executable, plan, timings, index }));
  runtime.core.setOutput('files', files);
  runtime.core.setOutput('checksum', (await readJSON(plan)).checksum);
}

export async function runShardPost(runtime) {
  const state = runtime.core.getState('execution');
  if (!state) return;
  const saved = JSON.parse(state);
  const ctx = context(runtime.env, runtime.input);
  for (const key of ['run', 'attempt', 'commit', 'scope']) {
    if (saved.ctx[key] !== ctx[key]) throw new Error('Shard post state does not match current run attempt');
  }
  const receipt = path.join(saved.directory, 'receipt.json');
  await command(runtime, saved.executable, ['receipt', '--plan', saved.plan, '--shard', String(saved.index), '--commit', ctx.commit, '--run-id', String(ctx.run), '--attempt', String(ctx.attempt), '--report', saved.timings, '--output', receipt]);
  await upload(runtime, artifactName('report', ctx, `-${saved.index}`), receipt);
}

export async function runFinish(runtime) {
  const ctx = context(runtime.env, runtime.input);
  if (ctx.event === 'pull_request_target') throw new Error('pull_request_target is not supported');
  const retention = positiveInteger(runtime.input('retention-days') || '30', 'retention-days');
  if (retention > 400) throw new Error('retention-days must not exceed 400 (repository limits may be lower)');
  const directory = await workspace(runtime);
  const executable = await binary(runtime);
  const plan = await downloadPlan(runtime, ctx, path.join(directory, 'plan'));
  const planData = await readJSON(plan);
  const meta = await metadata(runtime, ctx);
  const source = path.join(directory, 'source.json');
  const pushBranch = runtime.env.GITHUB_REF?.startsWith('refs/heads/') ? runtime.env.GITHUB_REF.slice('refs/heads/'.length) : runtime.env.GITHUB_REF;
  await writeJSON(source, {
    repository: ctx.repository, workflow: ctx.workflow, run_id: ctx.run, attempt: ctx.attempt, commit: ctx.commit,
    environment: planData.environment, coverage: planData.coverage, event: meta.run.event,
    branch: ctx.event === 'push' ? pushBranch : meta.run.head_branch, default_branch: meta.repository.default_branch,
  });
  const reports = path.join(directory, 'reports');
  await mkdir(reports);
  const prefix = `${artifactName('report', ctx)}-`;
  let reportNumber = 0;
  for (const artifact of await currentArtifacts(runtime, ctx)) {
    if (!artifact.name.startsWith(prefix)) continue;
    const shard = artifact.name.slice(prefix.length);
    const output = path.join(reports, `${reportNumber++}.json`);
    try {
      if (!/^(0|[1-9]\d*)$/.test(shard) || artifact.expired) throw new Error(`Invalid shard artifact: ${artifact.name}`);
      const destination = path.join(directory, `report-${artifact.id}`);
      await download(runtime, ctx, artifact, destination, 'receipt.json');
      const bytes = await readArtifactBytes(path.join(destination, 'receipt.json'));
      const receipt = JSON.parse(bytes.toString('utf8'));
      if (receipt.shard !== Number(shard) || receipt.run_id !== ctx.run || receipt.attempt !== ctx.attempt || receipt.commit !== ctx.commit) throw new Error('Report artifact name and receipt provenance do not match');
      await writeFile(output, bytes);
    } catch (error) {
      runtime.core.warning(`Cannot use ${artifact.name}: ${error.message}`);
      // Preserve a validation failure for the CLI summary, but let independently
      // valid receipts contribute passing files before finish returns failure.
      await writeFile(output, 'invalid receipt');
    }
  }
  const observation = path.join(directory, 'observation.json');
  const summary = path.join(directory, 'summary.md');
  const result = await runtime.exec(executable, ['finish', '--plan', plan, '--commit', ctx.commit, '--run-id', String(ctx.run), '--attempt', String(ctx.attempt), '--reports-dir', reports, '--source', source, '--observation', observation, '--summary', summary]);
  try { await appendSummary(runtime, await readFile(summary, 'utf8')); } catch (error) { if (error.code !== 'ENOENT') throw error; }
  let hasObservation = true;
  try { await access(observation); } catch (error) { if (error.code === 'ENOENT') hasObservation = false; else throw error; }
  if (hasObservation) {
    // Check trusted GitHub metadata as well as relying on CLI source checks.
    if (meta.run.event !== 'push' || runtime.env.GITHUB_REF !== `refs/heads/${meta.repository.default_branch}` || meta.run.head_branch !== meta.repository.default_branch || meta.run.head_repository?.full_name !== ctx.repository || planData.coverage !== 'full') {
      throw new Error('Refusing to publish history outside a full default-branch push');
    }
    await upload(runtime, artifactName('history', ctx), observation, retention);
  }
  if (result.code !== 0) throw new Error(`Suite Balance finish failed: ${result.stderr || result.stdout || 'see summary for missing or failed file results'}`);
}
