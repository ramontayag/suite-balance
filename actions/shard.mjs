import { createRuntime } from './lib/runtime.mjs';
import { runShard } from './lib/workflows.mjs';
const runtime = createRuntime();
runShard(runtime).catch(error => runtime.core.setFailed(error.message));
