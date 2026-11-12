import { createRuntime } from './lib/runtime.mjs';
import { runPlan } from './lib/workflows.mjs';
const runtime = createRuntime();
runPlan(runtime).catch(error => runtime.core.setFailed(error.message));
