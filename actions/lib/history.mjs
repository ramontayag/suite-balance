import path from 'node:path';
import { mkdir, writeFile, unlink } from 'node:fs/promises';
import { api, pages, download, readArtifactBytes } from './runtime.mjs';

export function verifyObservation(observation, artifact, run, ctx, defaultBranch) {
  const prefix = `sb-history-${ctx.scope}-${run.id}-`;
  if (!artifact.name.startsWith(prefix)) throw new Error('Unexpected history artifact namespace');
  const attemptText = artifact.name.slice(prefix.length);
  if (!/^[1-9]\d*$/.test(attemptText)) throw new Error('Invalid history artifact attempt');
  const attempt = Number(attemptText);
  const metadata = artifact.workflow_run;
  const source = observation.source;
  if (artifact.expired || !metadata || metadata.id !== run.id || metadata.head_sha !== run.head_sha || metadata.head_branch !== defaultBranch ||
      run.repository?.full_name !== ctx.repository || run.head_repository?.full_name !== ctx.repository ||
      run.event !== 'push' || run.head_branch !== defaultBranch || run.status !== 'completed' || run.path?.split('@')[0] !== ctx.workflow ||
      !Number.isSafeInteger(attempt) || attempt > run.run_attempt || observation.version !== 1 ||
      !source || source.repository !== ctx.repository || source.workflow !== ctx.workflow ||
      source.run_id !== run.id || source.attempt !== attempt || source.commit !== run.head_sha ||
      source.event !== 'push' || source.branch !== defaultBranch || source.default_branch !== defaultBranch || source.coverage !== 'full') {
    throw new Error(`History provenance mismatch: ${artifact.name}`);
  }
  if (!observation.files || typeof observation.files !== 'object' || Array.isArray(observation.files) || !Object.keys(observation.files).length) throw new Error('History observation has no file results');
  for (const result of Object.values(observation.files)) {
    if (!result || result.status !== 'passed' || typeof result.duration_seconds !== 'number' || !Number.isFinite(result.duration_seconds) || result.duration_seconds < 0) throw new Error('Invalid passing-file history result');
  }
  return source;
}

export async function restoreHistory(runtime, ctx, metadata, environment, directory) {
  const history = path.join(directory, 'history');
  await mkdir(history);
  let count = 0;
  let incompatible = 0;
  let restoredBytes = 0;
  try {
    const prefix = `sb-history-${ctx.scope}-`;
    const runs = new Map();
    const seen = new Set();
    for await (const artifact of pages(runtime, ctx, '/actions/artifacts', 'artifacts')) {
      if (!artifact.name.startsWith(prefix) || artifact.expired) continue;
      const actualRunID = artifact.workflow_run?.id;
      if (!Number.isSafeInteger(actualRunID) || actualRunID <= 0 || actualRunID >= ctx.run) continue;
      if (!runs.has(actualRunID)) runs.set(actualRunID, await api(runtime, ctx, `/actions/runs/${actualRunID}`));
      const run = runs.get(actualRunID);
      // A name is uploader-controlled. Decide whether the actual producing run
      // is trusted before allowing that name to influence history restoration.
      if (run.event !== 'push' || run.status !== 'completed' || run.head_branch !== metadata.repository.default_branch || run.path?.split('@')[0] !== ctx.workflow ||
          run.repository?.full_name !== ctx.repository || run.head_repository?.full_name !== ctx.repository) {
        runtime.core.info(`Skipped ineligible history artifact: ${artifact.name}`);
        continue;
      }
      const match = artifact.name.slice(prefix.length).match(/^([1-9]\d*)-([1-9]\d*)$/);
      if (!match || !Number.isSafeInteger(Number(match[1])) || !Number.isSafeInteger(Number(match[2]))) throw new Error(`Invalid history artifact name: ${artifact.name}`);
      const runID = Number(match[1]);
      if (runID !== actualRunID || run.id !== actualRunID) throw new Error(`History artifact run metadata mismatch: ${artifact.name}`);
      if (seen.has(artifact.name)) throw new Error(`Duplicate history artifact: ${artifact.name}`);
      seen.add(artifact.name);
      const destination = path.join(directory, `restore-${artifact.id}`);
      await download(runtime, ctx, artifact, destination, 'observation.json');
      const bytes = await readArtifactBytes(path.join(destination, 'observation.json'));
      await unlink(path.join(destination, 'observation.json'));
      const observation = JSON.parse(bytes.toString('utf8'));
      const source = verifyObservation(observation, artifact, run, ctx, metadata.repository.default_branch);
      if (source.environment !== environment) { incompatible++; continue; }
      if (restoredBytes + bytes.length > 128 * 1024 * 1024) throw new Error('Timing history exceeds the 128 MiB aggregate expanded limit; using complete round-robin fallback');
      await writeFile(path.join(history, `${run.id}-${source.attempt}.json`), bytes, { flag: 'wx' });
      restoredBytes += bytes.length;
      count++;
    }
    const fallback = !count ? incompatible ? 'Stored runner environment does not match this run' : 'No eligible timing history is available' : '';
    if (incompatible) runtime.core.info(`Skipped ${incompatible} history observations for other environments.`);
    return { directory: history, fallback };
  } catch (error) {
    // Even if earlier downloads succeeded, a broken history restore must not
    // silently use a partial sample. The CLI's fallback flag ignores that sample.
    return { directory: history, fallback: `Timing history unavailable: ${error.message}` };
  }
}
