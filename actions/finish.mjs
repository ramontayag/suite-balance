import { createRuntime } from './lib/runtime.mjs';
import { runFinish } from './lib/workflows.mjs';
const runtime = createRuntime();
runFinish(runtime).catch(error => runtime.core.setFailed(error.message));
