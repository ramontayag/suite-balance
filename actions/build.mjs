import { build } from 'esbuild';

await build({
  entryPoints: ['actions/plan.mjs', 'actions/shard.mjs', 'actions/shard-post.mjs', 'actions/finish.mjs'],
  bundle: true,
  platform: 'node',
  target: 'node24',
  format: 'cjs',
  outdir: 'dist',
  outExtension: { '.js': '.js' },
  legalComments: 'eof',
});
