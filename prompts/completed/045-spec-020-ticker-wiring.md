---
status: completed
spec: [020-open-task-census]
summary: Wired recomputeOpenTaskCensus into Run's metrics ticker (start-up plus each tick) as a sibling of the age pass, with internal specs proving publish-under-pollInterval=0, single-writer, failed-listing, cancelled-pass and sibling-isolation behaviour.
execution_id: agent-task-controller-open-tasks-gauge-exec-045-spec-020-ticker-wiring
dark-factory-version: v0.198.7
created: "2026-11-01T14:25:00Z"
queued: "2026-11-01T13:28:07Z"
started: "2026-11-01T13:39:34Z"
completed: "2026-11-01T13:44:29Z"
branch: dark-factory/open-task-census
---

# Drive the census from the metrics ticker, never from the scan path

<summary>

- The census is now recomputed by the same dedicated 60-second metrics ticker the task-age series already uses.
- It runs once at start-up and once per tick, so a fresh pod publishes without waiting an interval.
- It runs even when the periodic scan is disabled, which is the deployment shape of every production controller the alert targets.
- The scan cycle, the trigger path and the start-up index build never write it — the census has exactly one writer.
- The census stays a sibling recompute: a defect or early return in one pass cannot blank the other.
- A failed listing leaves the previous values in place instead of publishing a false zero.
- A pass cancelled part-way publishes nothing at all rather than a partial board.
- Specs prove each of those negatives, including that no scan, pull or write happens in trigger-only mode.
- No configuration surface is added and the 60-second cadence stays a package constant.

</summary>

<objective>

Wire the census into `Run`'s existing metrics ticker arm — one recompute before the loop when the metrics interval is positive, and one per tick, as a sibling of `recomputeTaskAgeMetrics` — and prove with internal specs that the census is published under `pollInterval = 0`, is never written by `RunCycle`, `scanFiles` or the start-up `buildIndex`, writes nothing on a failed listing, writes nothing when cancelled mid-pass, and does not overwrite the age series (nor the reverse) (spec 020, Desired Behavior 5, 6 and 7, AC 15 to 20).

</objective>

<context>

This repo has no root `CLAUDE.md`; the global YOLO container `CLAUDE.md` (already in your context) governs project conventions.

Read the spec: `specs/in-progress/020-open-task-census.md` — Desired Behavior 5, 6 and 7, Acceptance Criteria 15 through 20, Constraints ("Frozen placement", "Sibling recompute", "Single writer", "No new configuration", "The recompute runs inline in `Run`'s goroutine"), Failure Modes rows 1, 4, 6, 10 and 11, and the "Suggested Decomposition" table (this prompt is row 3).

DEPENDENCY — prompts 1 and 2 must have shipped before this prompt runs. Verify first:

```bash
grep -n 'OpenTasks() \*prometheus.GaugeVec' pkg/metrics/metrics.go
grep -n 'func (v \*vaultScanner) recomputeOpenTaskCensus' pkg/scanner/open_task_census.go
grep -n 'func (v \*vaultScanner) publishOpenTaskCensus' pkg/scanner/open_task_census.go
```

Expect all three. If any is missing, STOP and report `status: failed` with message `"census computation not yet deployed (prompt 2)"` — do not wire a function that does not exist.

Read these files before writing anything:

- `pkg/scanner/vault_scanner.go` — `func (v *vaultScanner) Run(ctx context.Context, results chan<- ScanResult) error` in full. The arm to change is the metrics block:
  - the `var metricsTick <-chan time.Time` declaration and the `if v.metricsInterval > 0 { … }` guard, whose comment explains that a non-positive interval constructs neither the ticker nor the start-up recompute because `time.NewTicker` panics on a non-positive duration;
  - the start-up call `v.recomputeTaskAgeMetrics(ctx)` inside that guard;
  - the `select` arm `case <-metricsTick:` at the bottom of the loop.
  Also read `RunCycle`, `scanFiles` and `buildIndex` to confirm this prompt adds nothing to them.
- `pkg/scanner/open_task_census.go` — the functions prompt 2 added (`recomputeOpenTaskCensus`, `publishOpenTaskCensus`, `openTaskPairFromFile`).
- `pkg/scanner/task_age_ticker_internal_test.go` — the exact template for this prompt's specs. Reuse its hand-written `taskAgeTickerGitClient` (`newTaskAgeTickerGitClient()`, `setFile`, `counts`, mutex-guarded `Pull`/`ListFiles`/`ReadFile`/`WriteFile`) and its sentinel and `Eventually`/`Consistently` idioms. Do NOT define a second fake git client.
- `pkg/scanner/open_task_census_internal_test.go` — the helpers prompt 2 added: `openTaskCensusScanner`, `openTaskCensusRecord`, `openTaskCensusGathered`, `openTaskCensusValue`. Reuse them; do not duplicate them.
- `pkg/scanner/task_age.go` — `recomputeTaskAgeMetrics`, so the sibling-isolation spec drives the real age pass.
- `main.go` — the `const metricsInterval = 60 * time.Second` declaration and its comment. Confirm you are not touching it.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-context-cancellation-in-loops.md` — cancellation semantics the specs assert.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-concurrency-patterns.md` — why the pass runs inline in `Run`'s goroutine and no second goroutine is added.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` and `/home/node/.claude/plugins/marketplaces/coding/docs/definition-of-done.md` — Ginkgo/Gomega style, `Eventually`/`Consistently` usage, coverage expectations.

Environment facts that shape this prompt:

- The execution container's `.git` is masked (the daemon runs with `hideGit=true`; `git status` fails with `fatal: not a git repository`). Do NOT run any `git` command anywhere in this prompt — a failed `git` command would read as a pass. The spec's `git grep` / `git diff --stat HEAD -- main.go` checks are expressed as filesystem greps in `<verification>` below.
- Do NOT run `kubectl*`, `docker`, `make build`, `make buca`, `gh`, or any deploy command. The `bborbe/nuke` image bump, the dev/prod apply and the live scrape are another task's steps.
- Do NOT commit. dark-factory owns git.

</context>

<requirements>

1. **Wire the census into `Run`'s metrics arm in `pkg/scanner/vault_scanner.go`.** Inside the existing `if v.metricsInterval > 0 { … }` block, add `v.recomputeOpenTaskCensus(ctx)` immediately after `v.recomputeTaskAgeMetrics(ctx)` and before the `time.NewTicker` call. Extend that block's comment so it names both passes as siblings:

   ```go
   	var metricsTick <-chan time.Time
   	if v.metricsInterval > 0 {
   		// One recompute before the loop, so a fresh pod publishes both the age and
   		// the open-task census series immediately instead of reporting "no data"
   		// for the first interval. The two are siblings, not one folded pass: they
   		// have different definitions and different failure modes, and an early
   		// return in one must not blank the other.
   		v.recomputeTaskAgeMetrics(ctx)
   		v.recomputeOpenTaskCensus(ctx)
   		metricsTicker := time.NewTicker(v.metricsInterval)
   		defer metricsTicker.Stop()
   		metricsTick = metricsTicker.C
   	}
   ```

   And in the loop's `select`, make the metrics arm call both, in this order:

   ```go
   		case <-metricsTick:
   			v.recomputeTaskAgeMetrics(ctx)
   			v.recomputeOpenTaskCensus(ctx)
   ```

   The census therefore runs inline in `Run`'s goroutine — do NOT start a goroutine, a second ticker, a cron or a timer for it, and do NOT move it out of this arm.

2. **Add nothing to `RunCycle`, `scanFiles`, `buildIndex`, `processFile` or `publishIndex`.** The census has exactly one writer. A `Set` from the scan path would make the value depend on which path ran last, and the scan path is dead on the deployments the alert runs against.

3. **Do not touch `main.go`.** The cadence stays the existing package constant `metricsInterval = 60 * time.Second`, already threaded into the scanner constructor. No environment variable, flag, chart value or interval knob is added.

4. **Create `pkg/scanner/open_task_census_ticker_internal_test.go`** with `package scanner`, the BSD license header, a file comment stating that these are internal specs because they drive the unexported `Run`/`recomputeOpenTaskCensus` paths and read the package gauges, and the specs below. Every spec uses a vault name unique to itself (`census-ticker`, `census-runcycle`, `census-build-index`, `census-list-fail`, `census-list-fail-first`, `census-cancelled`, `census-sibling`) so an absence assertion cannot depend on spec order. Add one local record builder:

   ```go
   // openTaskCensusTickerRecord builds an open record with a distinct identifier and
   // an explicit phase/task_type, in the shape the scanner parses.
   func openTaskCensusTickerRecord(id, phase, taskType string) string {
   	return openTaskCensusRecord(
   		"task_identifier: "+id,
   		"status: in_progress",
   		"assignee: claude",
   		"phase: "+phase,
   		"task_type: "+taskType,
   	)
   }
   ```

   Use valid distinct UUIDs for `id` (for example `11111111-1111-4111-8111-111111111111` and `22222222-2222-4222-8222-222222222222`) so `buildIndex` and the scan cycle treat them as well-formed records.

5. **Spec: the census publishes under `pollInterval = 0` (AC 16).** Build the scanner with `vault := "census-ticker"` and `NewGitRestVaultScanner(git, "tasks", 0, make(chan struct{}), metrics.New(), true, vault, 25*time.Millisecond)` — zero poll interval, short metrics interval — and start `Run` in a goroutine exactly as `task_age_ticker_internal_test.go` does. Then:

   - `Consistently` for ~100 ms that `openTaskCensusValue(vault, "planning", "github-update-go")` returns `ok == false`: the start-up recompute ran over an empty board and published nothing.
   - `git.setFile("tasks/open.md", openTaskCensusTickerRecord("11111111-1111-4111-8111-111111111111", "planning", "github-update-go"))`.
   - `Eventually` within 2 s that the value reads `1` — no trigger fired and the poll interval is zero, so only the metrics tick can have published it.
   - `Consistently(results, 300*time.Millisecond).ShouldNot(Receive())`, and `git.counts()` returns zero `pullCalls` and zero `writeFileCalls`.
   - Cancel the run context and `Eventually(done, "2s").Should(Receive(BeNil()))`.

   This covers both halves of the requirement: the start-up recompute (a fresh pod publishes without waiting an interval) and the tick.

6. **Spec: single writer — `RunCycle` (AC 15, half a).** Seed the sentinel `424242` with `metrics.New().OpenTasks().WithLabelValues(vault, "planning", "github-update-go").Set(424242)` where `vault = "census-runcycle"`. Build a `newTaskAgeTickerGitClient()` holding two open records at that pair, construct the scanner with `metricsInterval = 0` and `pollInterval = 0`, call `vs.RunCycle(ctx, make(chan ScanResult, 1))` over a board that would publish `2` at that pair, and assert the sample still reads `424242`.

7. **Spec: single writer — `Run`'s start-up index build (AC 15, half b).** Seed the same sentinel at `vault = "census-build-index"`. Build the scanner with `pollInterval = 0` and `metricsInterval = 0` over a board holding one open record with a known identifier. Start `Run`, then `Eventually` that `vs.Resolve(ctx, "11111111-1111-4111-8111-111111111111")` returns `"tasks/open.md"` — the seeded record resolving proves `buildIndex` ran — then cancel, `Eventually(done, "2s").Should(Receive(BeNil()))`, and assert the sentinel still reads `424242`. A non-positive metrics interval suppresses both the census ticker and its start-up recompute, so only `buildIndex` could have written it, and it must not.

8. **Spec: a failed listing writes nothing (AC 17).** Two halves in one `It` or two `It`s:

   - Seed `{vault="census-list-fail", phase="planning", task_type="github-update-go"}` to `424242`; build a `&vaultScanner{metrics: metrics.New(), vaultName: vault, ops: fileOps{listFiles: func(_ context.Context, _ string) ([]string, error) { return nil, fmt.Errorf("listing failed") }}}`; call `recomputeOpenTaskCensus(ctx)`; assert the sentinel reads `424242` and the total number of gathered census samples is unchanged (`before := len(openTaskCensusGathered())` captured first, `Expect(openTaskCensusGathered()).To(HaveLen(before))`).
   - On a fresh vault `"census-list-fail-first"` with a listing error on its very first pass, assert `openTaskCensusValue(vault, "planning", "github-update-go")` returns `ok == false`.

9. **Spec: a pass cancelled mid-way publishes nothing (AC 18).** Seed `{vault="census-cancelled", phase="planning", task_type="github-update-go"}` to `424242`. Build a scanner whose `listFiles` returns several records at **other** pairs and whose `readFile` calls the pass context's `cancel()` on the first call. Drive `recomputeOpenTaskCensus(passCtx)` and assert the sentinel still reads `424242` and that no sample exists for any of the other pairs. Counts are published only after the whole loop, so a partial map is never written.

10. **Spec: the census is a sibling of the age pass (AC 19).** Two directions in one `It`:

    - Seed `{vault="census-sibling", phase="planning", task_type="github-update-go"}` to `424242`; build a board with two records at that pair; call `vs.recomputeTaskAgeMetrics(ctx)` (the real age pass) and assert the census sentinel still reads `424242`. A folded implementation would publish the census from the age pass and overwrite the sentinel.
    - Seed the age series with sentinels — `metrics.New().TaskOldestAgeSeconds(vault).Set(424242)` and `metrics.New().TaskAgeBucketTasks(vault, bucket).Set(424242)` for every bucket in `metrics.AvailableTaskAgeBuckets` — then call `vs.recomputeOpenTaskCensus(ctx)` and assert every age sentinel is unchanged, read back with `testutil.ToFloat64`.

11. **Do not modify any existing spec.** `pkg/scanner/task_age_ticker_internal_test.go`, `pkg/scanner/task_age_internal_test.go` and the scanner's external specs stay byte-for-byte identical; the shared fake git client is reused, not changed.

</requirements>

<constraints>

- **Frozen placement:** the existing metrics ticker only — start-up plus each tick — never `scanFiles`, `RunCycle` or the start-up `buildIndex`. `CHANGELOG.md` v0.22.6 records what happened when a sweep was hung off the scan path on a trigger-only controller.
- **Sibling recompute:** the census is its own recompute called from the ticker arm, not folded into the task-age pass. The two must be independently failable.
- **Single writer:** the census is written only by the metrics ticker. Two writers would make the value depend on which path ran last, and the scan path is dead on the deployments the alert runs against.
- **Inline in `Run`'s goroutine** — no second goroutine, ticker, timer or cron. The census and the age pass never overlap, and a trigger arriving during a recompute waits in the buffered channel rather than being lost.
- **No new configuration:** `main.go` is untouched and no env var, flag or chart value is added. The cadence stays the existing 60-second package constant.
- **Failure behaviour favours a stale value over a false zero:** a failed listing publishes nothing; a cancelled pass publishes nothing.
- **Existing behaviour untouched:** the age series, the stuck-planning gauge, the scan cycle, the identifier index and the escalation paths are unchanged.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.

</constraints>

<verification>

Run from the repository root:

```bash
# the wiring exists exactly twice, both inside Run
grep -c 'recomputeOpenTaskCensus' pkg/scanner/vault_scanner.go          # expect 2
sed -n '/func (v \*vaultScanner) Run(/,/func (v \*vaultScanner) RunCycle(/p' pkg/scanner/vault_scanner.go | grep -c 'recomputeOpenTaskCensus'   # expect 2
sed -n '/func (v \*vaultScanner) RunCycle(/,/func (v \*vaultScanner) buildIndex(/p' pkg/scanner/vault_scanner.go | grep -c 'recomputeOpenTaskCensus'   # expect 0

# no new configuration surface and main.go unchanged
grep -rn -Ei 'env:"[^"]*census|arg:"[^"]*census|CENSUS_INTERVAL|OPEN_TASKS_INTERVAL' --include='*.go' .   # expect no output
grep -c 'metricsInterval = 60 \* time.Second' main.go                                                    # expect 1
grep -ci 'census' main.go                                                                                # expect 0

# the specs and the package build
go test -count=1 ./pkg/scanner/...
go test -count=1 ./pkg/metrics/...
go build ./...
```

Expected: `recomputeOpenTaskCensus` appears exactly twice in `pkg/scanner/vault_scanner.go`, both between the `Run(` and `RunCycle(` function headers, and zero times between `RunCycle(` and `buildIndex(`; the configuration grep and the `census`-in-`main.go` grep print nothing; `metricsInterval` is still the 60-second constant; both `go test` runs and `go build` exit 0.

Note on the spec's AC 20 wording: the spec expresses the no-configuration check as `git grep …` and `git diff --stat HEAD -- main.go`. Both are impossible here because `.git` is masked. The filesystem greps above are the in-container equivalents; the git form belongs to the spec's operator verification rung, not to this prompt.

Then the full gate once, at the end:

```bash
make precommit
```

`make precommit` runs `ensure format generate test check addlicense`; `check` also runs `vulncheck osv-scanner trivy`, which query external vulnerability databases — a failure there is a database or dependency finding, not evidence against this change; report it verbatim and do not edit `.trivyignore` or `VULNCHECK_IGNORE`.

If `make precommit` exits non-zero, fix the failure, re-run only the failing target (`make lint`, `make test`, …), and report `"status":"failed"` unless the final `make precommit` exits 0.

</verification>
