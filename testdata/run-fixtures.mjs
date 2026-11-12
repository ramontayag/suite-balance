// Synthetic durations exercise the integration; they are not performance measurements.
import { readFile, writeFile, mkdir } from 'node:fs/promises';
import path from 'node:path';

const [manifest, output] = process.argv.slice(2);
if (!manifest || !output) throw new Error('usage: run-fixtures.mjs MANIFEST REPORT');
const paths = (await readFile(manifest, 'utf8')).split('\n').filter(Boolean);
const files = {};
for (const file of paths) {
  files[file] = JSON.parse(await readFile(file, 'utf8'));
}
await mkdir(path.dirname(output), { recursive: true });
await writeFile(output, JSON.stringify({ version: 1, files }));
if (Object.values(files).some(result => result.status !== 'passed')) process.exitCode = 1;
