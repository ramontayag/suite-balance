import test from 'node:test';
import assert from 'node:assert/strict';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import path from 'node:path';

const exec = promisify(execFile);

test('committed CommonJS entry points load without module or dependency resolution errors', async () => {
  for (const action of ['plan', 'shard', 'finish', 'shard-post']) {
    const file = path.resolve(import.meta.dirname, `../../dist/${action}.js`);
    const env = { ...process.env };
    delete env.GITHUB_REPOSITORY;
    delete env.STATE_execution;
    try {
      await exec(process.execPath, [file], { env });
      assert.equal(action, 'shard-post');
    } catch (error) {
      assert.equal(error.code, 1);
      assert.match(error.stdout, /Invalid GITHUB_REPOSITORY/);
      assert.equal(error.stderr, '');
    }
  }
});
