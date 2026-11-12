import { createRuntime } from './lib/runtime.mjs';
import { runShardPost } from './lib/workflows.mjs';
const runtime = createRuntime();
runShardPost(runtime).catch(error => runtime.core.setFailed(error.message));
