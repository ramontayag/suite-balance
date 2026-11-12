# Suite Balance

**Split your test suite by time, not file count.**

Suite Balance uses recent test durations to spread files across parallel CI jobs. Keep your test runner, your test command, and your isolation setup. Suite Balance decides which files belong in each job.

- **No account or server.** Timing history stays in your repository's CI artifacts.
- **No framework dependency.** Supply file paths and durations. Ruby, JavaScript, Python, and other languages use the same input.
- **No history required to start.** The first run distributes files round-robin. Complete, passing files teach the next run.
- **No tests lost to an optimization.** Every required file appears in exactly one shard. Missing history changes the assignment, not the coverage.

A **shard** is one group of files assigned to one parallel job.

## See what changed

Each run explains its assignment and compares estimates with actual test execution:

```text
Suite Balance · 148 files · 3 shards

History: 10 observations per known file · 2 new files

Shard     Files     Predicted     Actual
0            49        4m 12s     4m 18s
1            51        4m 11s     4m 09s
2            48        4m 13s     4m 16s

Actual spread: 9s
Coverage: 148 / 148 files reported · no duplicates
History: recorded
```

Illustrative output, not a benchmark. Test execution time excludes queueing, builds, and job setup.

## Use with GitHub Actions

Three actions handle the shared assignment, shard reports, and history. You supply the test command and its timing report.

```yaml
name: Tests

on: [push, pull_request]

permissions:
  contents: read
  actions: read

jobs:
  plan:
    runs-on: ubuntu-24.04
    outputs:
      matrix: ${{ steps.balance.outputs.matrix }}
      has-tests: ${{ steps.balance.outputs.has-tests }}
    steps:
      - uses: actions/checkout@v7
      - uses: ramontayag/suite-balance/plan@v1
        id: balance
        with:
          files: test/**/*_test.rb
          # Alternatively, replace files with a list from your test-selection step:
          # files-from: tmp/selected-tests.txt
          # coverage: selected
          shards: 3
          environment: ubuntu-24.04-standard

  test:
    needs: plan
    if: ${{ needs.plan.outputs.has-tests == 'true' }}
    runs-on: ubuntu-24.04
    strategy:
      fail-fast: false
      matrix: ${{ fromJSON(needs.plan.outputs.matrix) }}
    steps:
      - uses: actions/checkout@v7
      # Install your language runtime and test dependencies here.
      - uses: ramontayag/suite-balance/shard@v1
        id: balance
        with:
          index: ${{ matrix.shard }}
          timings: tmp/timings.json
      - name: Run assigned files
        env:
          TEST_FILES: ${{ steps.balance.outputs.files }}
        run: ./bin/test-files --manifest "$TEST_FILES" --timings tmp/timings.json

  finish:
    needs: [plan, test]
    if: ${{ always() }}
    runs-on: ubuntu-24.04
    steps:
      - uses: ramontayag/suite-balance/finish@v1
```

`./bin/test-files` represents **your own runner integration**, not a command supplied by Suite Balance. It reads a manifest containing one repository-relative path per line, runs those files, and writes the timing report below. It must return a nonzero exit status when tests fail. Pass the manifest into your existing Docker or test-isolation setup if needed.

Use the `test` and `finish` jobs as required checks. `finish` checks reports even when a test job fails; it cannot turn a failed test job into a successful run.

### What the actions do

- **`plan`** reads required files from your pattern or manifest, checks that they belong to the tested commit, downloads eligible history once, and publishes one assignment. Its matrix contains zero-based shard indices. Its `has-tests` output tells the workflow whether to start workers.
- **`shard`** downloads that assignment and returns a **manifest file path**, not a string of shell arguments. Its post step uploads the timing report and assignment metadata, including after test failures.
- **`finish`** checks that all expected shards and files reported, writes the job summary, and publishes eligible timing observations. Missing, duplicate, or invalid results fail this check.

No separate artifact steps, shared service, or repository token secret are needed. The actions use the workflow's GitHub token. Release binaries are checksum-verified. Pin action references to commit SHAs when your repository requires immutable dependencies.

`environment` names a comparable runner setup. Change it when runner capacity or other material execution conditions change. Do not use the same name for different runner classes.

The actions support Linux and macOS runners on Intel/AMD and ARM64. They download the executable automatically; Suite Balance itself needs no Go installation. Your tests still need their own runtime and dependencies.

Use either `files` for a pattern or `files-from` for a manifest containing one repository-relative path per line. For a selected subset, also set `coverage: selected`. Suite Balance divides the selected files among workers and checks that each file was reported exactly once; it does not change which files your selector requires. Selected runs can read timing history but do not update it.

An empty selected list is valid. For example, a documentation-only change may need no system tests. No workers start, `finish` reports "No tests selected," and history stays unchanged. An empty full-suite input or a missing or unreadable manifest is an error, not a request to skip tests.

### Optional inputs

| Input | Action | Default | Use |
| --- | --- | --- | --- |
| `coverage` | `plan` | `full` | Set `selected` when another step chooses a subset. |
| `suite` | All three | `tests` | Give each suite a distinct name when one workflow balances multiple suites. Use the same name in its three actions. |
| `retention-days` | `finish` | `30` | Retain timing history for this many days, within your repository's artifact-retention limit. |
| `version` | All three | `v1.0.0` | Choose the executable release. |
| `binary-path` | All three | Automatic download | Use an existing executable at an absolute path. |

The `plan` action also outputs `fallback-reason`: an empty string when it uses historical estimates, or an explanation when it uses round-robin.

Suite Balance accepts 1–256 shards. Manifests and JSON documents are limited to 8 MiB each, and a preparation can restore up to 128 MiB of timing history. Exceeding the history limit produces a visible round-robin fallback. Oversized required inputs or result documents fail validation.

## Report durations

Suite Balance accepts a small JSON report:

```json
{
  "version": 1,
  "files": {
    "test/accounts_test.rb": {
      "duration_seconds": 12.4,
      "status": "passed"
    },
    "test/checkout_test.rb": {
      "duration_seconds": 38.1,
      "status": "failed"
    }
  }
}
```

- Paths are relative to the repository root, even when tests run inside a container.
- Durations are seconds, must be finite and nonnegative, and cover a complete file's test execution.
- If your runner measures individual tests, sum their durations by file.
- Report a file only after all its test cases have finished. Use `passed` only when the file's test run succeeds; otherwise use `failed`. Your runner owns that reporting contract.
- If execution stops before a file completes, leave that file out of the report. Suite Balance reports the missing result and does not learn from it.
- Do not submit a filtered run as a complete file observation.

Translate your runner's existing timing output into this format.

The actions attach the commit, assignment checksum, run, attempt, shard, and environment metadata. Your test runner does not need to collect GitHub metadata.

## Use outside GitHub Actions

We have not used Suite Balance outside GitHub Actions. This integration path is untested, but the balancing logic is independent of GitHub.

The idea is to:

1. Supply the required file paths, timing history, and shard count.
2. Generate one shared assignment before starting the workers.
3. Give each worker its assigned files and run your existing test command.
4. Collect complete timing reports and save eligible observations for the next run.

Your CI system would own history storage and transfer between jobs. Each worker receives its group from the shared assignment.

## How balancing works

1. Use the mean of each file's last **10 valid observations**. Fewer observations are enough to start.
2. Give new files the median estimate of known files.
3. Sort files by descending estimated duration, then by path.
4. Assign each file to the least-loaded shard. Break load ties by shard index.

The same files, history, and shard count produce the same assignment. Files stay whole. A single slow file can still determine the run's completion time; the summary identifies that limit.

### History you can trust

The GitHub Actions integration learns from complete, passing files in full-suite push runs on the default branch. A full-suite run is one intended to run every required file; it does not mean every file passed. A passing file can contribute an observation even if another file, a later step, or another job fails. Failed or incomplete files do not contribute observations.

Each observation must come from a valid report for that file's assignment and tested commit. Publishing an eligible observation does not make an incomplete or failed run pass its required checks. Pull requests and selected runs can read history but cannot publish authoritative observations.

Observations are immutable and identified by run and attempt. Preparation reconstructs each file's latest 10 observations in workflow-run order, with retries deduplicated. An older run finishing late cannot replace a newer history snapshot. Incompatible environments remain separate.

On GitHub Actions, timing history is stored as artifacts with a 30-day retention period by default. Expiry is supported: the next run starts without history rather than failing or skipping files.

If your selection tool already supplies assignments, keep using those assignments rather than running a second allocator over them.

### When something is wrong

Unavailable or unusable history produces a visible fallback:

```text
History unavailable: stored runner environment does not match this run.
Using round-robin assignment. All 148 required files are included.
```

An invalid required-file list, a missing assignment, or incomplete shard results is different: Suite Balance stops with an error. It never treats missing execution evidence as a successful test run.

## What Suite Balance does not do

- Choose which tests are necessary for a change.
- Execute tests or manage browsers, databases, containers, or runners.
- Split individual test cases or run a dynamic work queue.
- Promise equal job completion times. Builds, setup, contention, and queueing are separate costs.

Suite Balance changes the distribution of test work, not what your tests prove.
