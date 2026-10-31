---
status: approved
spec: [019-task-age-metrics]
created: "2026-10-31T16:30:00Z"
queued: "2026-10-31T15:31:37Z"
branch: dark-factory/spec-019
---

# Run the age recompute on its own ticker and wire it up

<summary>
- A running controller now publishes the task-age series on a dedicated cadence that works even when the periodic vault scan is disabled.
- The recompute happens once before the loop and then once per tick, so a fresh pod publishes values immediately instead of reporting no data for a full interval.
- The recompute only lists and reads the vault; it never pulls, scans, writes, commits or sends scan results.
- A failed listing leaves the previously published values untouched instead of zeroing them, so a transient vault error cannot make the board look clean.
- The scan cycle and the start-up index build never write the age series, so there is exactly one writer.
- The cadence is a fixed package constant of sixty seconds in the binary's entry point, with no environment variable, flag or chart value able to widen or disable it.
- A zero or negative interval from a caller disables the whole metrics path rather than panicking the process.
- Both scanner constructors take the vault name and the metrics interval, and every call site in the repository is updated.
- The vault label carries the controller's configured vault name verbatim.
- The wiring is exercisable without running the process, and a test records the exact vault name and interval the wiring passes.
</summary>

<objective>
Give the age summary from the previous prompts a clock-driven caller: a second ticker in `Run` that is independent of the poll interval, the two constructor parameters it needs, the fixed sixty-second constant and an injectable construction helper in `main.go`. Without this prompt the series exist but nothing publishes them on a trigger-only controller — the exact dead-on-prod shape this spec exists to avoid.
</objective>

<context>
Read these files fully before changing anything:
- `pkg/scanner/vault_scanner.go`:
  - `type vaultScanner struct` (~line 83) — the fields to extend.
  - `func NewVaultScanner(...)` and `func NewGitRestVaultScanner(...)` (~lines 146 and 170) — both must gain the same two trailing parameters.
  - `func (v *vaultScanner) Run(ctx, results)` (~line 195) — note the existing non-positive poll-interval guard (`time.NewTicker` panics on a non-positive duration) which the metrics guard must mirror, and note that the trigger-only branch calls `v.buildIndex(ctx)`.
  - `func (v *vaultScanner) RunCycle` and `func (v *vaultScanner) scanFiles` — neither may write the age series.
- `pkg/scanner/task_age.go` — `publishTaskAgeMetrics(ctx, paths, now, vaultName)` added by the previous prompt.
- `main.go` — the `application` struct (field `VaultName`), the construction block inside `Run` around the `trigger := make(chan struct{}, 1)` / `scanner.NewGitRestVaultScanner(...)` lines, and the existing package-level constants (`vaultLocalPath`, `githubAPIBaseURL`).
- `main_internal_test.go` — an existing `package main` test file; note that `main_test.go` is `package main_test` and cannot name the unexported `application` type.
- `pkg/scanner/vault_scanner_test.go` and `pkg/scanner/vault_scanner_index_internal_test.go` — the call sites that must be updated, and the `testGitClient` / `fileOpsTestGitClient` doubles showing how file I/O is faked.
- `pkg/command/task_create_task_executor_index_duplicate_test.go` (~line 124) — the other constructor call site.

Relevant coding guides (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-concurrency-patterns.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-context-cancellation-in-loops.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-factory-pattern.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-precommit.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-glog-guide.md`
</context>

<requirements>
1. Extend `type vaultScanner struct` in `pkg/scanner/vault_scanner.go` with two fields after `autoInject bool`:
   ```go
   vaultName       string
   metricsInterval time.Duration
   ```

2. Add the two trailing parameters to **both** constructors, in this frozen order — git client, task directory, poll interval, trigger channel, metrics implementation, auto-inject flag, **vault name**, **metrics interval**:
   ```go
   func NewVaultScanner(
   	gitClient gitclient.GitClient,
   	taskDir string,
   	pollInterval time.Duration,
   	trigger <-chan struct{},
   	m metrics.Metrics,
   	autoInject bool,
   	vaultName string,
   	metricsInterval time.Duration,
   ) VaultScanner
   ```
   and the identical parameter list on `NewGitRestVaultScanner`. Set both new fields in both struct literals. Do not reorder the existing parameters.

3. Add the read-only recompute caller in `pkg/scanner/task_age.go` (or `vault_scanner.go` if it fits better beside `Run`):
   ```go
   // recomputeTaskAgeMetrics lists the task directory once and publishes the
   // task-age series from a single clock read. It never pulls, scans, writes,
   // commits or sends on the results channel.
   func (v *vaultScanner) recomputeTaskAgeMetrics(ctx context.Context) {
   	now := time.Now()
   	glob := v.taskDir + "/*.md"
   	paths, err := v.ops.listFiles(ctx, glob)
   	if err != nil {
   		glog.Warningf("task age metrics: list %s failed: %v", glob, err)
   		return
   	}
   	v.publishTaskAgeMetrics(ctx, paths, now, v.vaultName)
   }
   ```
   On a listing failure nothing is written, so the values from the previous successful pass stay in place. Never `Set` 0 on a failed read.

4. Extend `Run` with the second ticker, inside the same goroutine as the scan loop (no second goroutine):
   - After the existing poll-interval block and before the `for` loop, add:
     ```go
     var metricsTick <-chan time.Time
     if v.metricsInterval > 0 {
     	// One recompute before the loop, so a fresh pod publishes all five series
     	// immediately instead of reporting "no data" for the first interval.
     	v.recomputeTaskAgeMetrics(ctx)
     	metricsTicker := time.NewTicker(v.metricsInterval)
     	defer metricsTicker.Stop()
     	metricsTick = metricsTicker.C
     }
     ```
   - Add a case to the existing `select`: `case <-metricsTick: v.recomputeTaskAgeMetrics(ctx)`.
   - A non-positive `metricsInterval` constructs neither the ticker nor the start-up recompute. This is the guard against `time.NewTicker` panicking, mirroring the poll-interval guard; configuration cannot produce the value, only a caller can.
   - `Run` must still return promptly on `ctx.Done()`; the recompute abandons at the next file boundary because `publishTaskAgeMetrics` checks the context per file.

5. In `main.go`, add a package-level constant next to the existing constants, and an injectable construction helper:
   ```go
   // metricsInterval is the fixed cadence of the task-age metrics recompute. It is a
   // package constant, not configuration: the alert contract is "at most 60 s", and a
   // knob that could widen or disable it would be an opt-out on the shipped behaviour.
   const metricsInterval = 60 * time.Second
   ```
   ```go
   // vaultScannerConstructor is the signature shared by both scanner constructors.
   // It is a parameter of createVaultScanner so the wiring can be exercised without
   // running Run.
   type vaultScannerConstructor func(
   	gitClient gitrestclient.GitClient,
   	taskDir string,
   	pollInterval time.Duration,
   	trigger <-chan struct{},
   	m metrics.Metrics,
   	autoInject bool,
   	vaultName string,
   	metricsInterval time.Duration,
   ) scanner.VaultScanner

   // createVaultScanner builds the process's single VaultScanner. The constructor is
   // injected so a test can record the arguments this wiring passes.
   func (a *application) createVaultScanner(
   	gitClient gitrestclient.GitClient,
   	trigger chan struct{},
   	autoInject bool,
   	constructor vaultScannerConstructor,
   ) scanner.VaultScanner {
   	return constructor(
   		gitClient,
   		a.TaskDir,
   		a.PollInterval,
   		trigger,
   		metrics.New(),
   		autoInject,
   		a.VaultName,
   		metricsInterval,
   	)
   }
   ```
   In `Run`, replace the direct `scanner.NewGitRestVaultScanner(...)` construction with:
   ```go
   vaultScanner := a.createVaultScanner(gitClient, trigger, autoInject, scanner.NewGitRestVaultScanner)
   ```
   `scanner.NewGitRestVaultScanner` must appear exactly once in `main.go`, as that argument, and that occurrence must be inside `Run`. Do not add a `_ = scanner.NewGitRestVaultScanner` placeholder.

6. Update **every** constructor call site in the repository so the module compiles. Run `grep -rn 'NewVaultScanner(\|NewGitRestVaultScanner(' --include='*.go' .` and confirm the list is exactly these; append the two arguments to each (use vault name `"test-vault"` and interval `0` unless the spec below says otherwise, so existing specs keep their behaviour and no background ticker starts):
   - `main.go` (now through the helper)
   - `pkg/scanner/vault_scanner_test.go`: the `BeforeEach` around line 233, and the specs around lines 595, 604, 631, 647, 676 and 1077
   - `pkg/scanner/vault_scanner_index_internal_test.go`: around lines 117 and 407
   - `pkg/command/task_create_task_executor_index_duplicate_test.go`: around line 124 (use vault name `"openclaw"`, interval `0`)

7. Add `pkg/scanner/task_age_ticker_internal_test.go` (package `scanner`, license header). Define a mutex-guarded fake `gitclient.GitClient` (embed the interface, override `Path`, `Pull`, `CommitAndPush`, `ListFiles`, `ReadFile`, `WriteFile`; count `Pull` and `WriteFile` calls under the mutex, because the scanner goroutine and the spec goroutine both touch it). Helper names must start with `taskAgeTicker` (not bare `taskAge`): `task_age_internal_test.go` from the previous prompt registers specs into the same `package scanner` test binary, and a bare prefix would let the two files declare the same symbol.

   Required specs:
   - **Ticker under a zero poll interval:** construct through `NewGitRestVaultScanner(fakeGit, "tasks", 0, make(chan struct{}), metrics.New(), true, "openclaw", 25*time.Millisecond)`, run `Run` in a goroutine with a cancellable context and a buffered results channel. Read the oldest age through `testutil.ToFloat64(metrics.New().TaskOldestAgeSeconds("openclaw"))` as 0 for the empty board, write a non-terminal record dated before now into the fake store, then `Eventually` (with an explicit timeout, e.g. `"2s"`) the value becomes non-zero. Read it through the accessor only — never through a metric-name string literal. Then `Consistently` the results channel receives nothing (proving no scan cycle ran) and assert the fake recorded zero `Pull` calls and zero `WriteFile` calls across the whole run. Cancel and wait for `Run` to return.
   - **Vault label verbatim:** construct through the constructor with vault name `"openclaw"` and interval `0`, call `recomputeTaskAgeMetrics(ctx)` over a board with one dated non-terminal record, and read both series by the label combination `vault="openclaw"` with `testutil.ToFloat64` on `metrics.New()` accessors. Assert no display-name or slug mapping is applied.

8. Add specs (same file) for the single-writer rule and the failed-listing path. Every one of them must construct the scanner with `metrics.New()` — the default implementation backed by the package-level gauge vectors — so the collector the sentinel is seeded into is the collector the code writes through; a fake returning a freshly constructed same-named `GaugeVec` would pass while the real series is written elsewhere. Seed and read the sentinel **only through the accessor**: `testutil.ToFloat64(metrics.New().TaskOldestAgeSeconds("openclaw"))`. Never write a frozen metric name as a string literal anywhere in `pkg/scanner/` — not in a spec and not in a `prometheus.DefaultGatherer` family lookup — because the verification grep rejects any occurrence of either name in that package, and the spec's single-writer negative evidence depends on it printing nothing.
   - **(a) `RunCycle` does not write:** seed the sentinel `424242` through the accessor, run `RunCycle` over a board holding a 10-day-old non-terminal record (valid `task_identifier` UUID, `status: in_progress`, `assignee: claude`, a body), and read `424242` back unchanged.
   - **(b) `Run`'s start-up index build does not write:** seed the same sentinel through the accessor, then run `Run` with `pollInterval = 0` and `metricsInterval = 0` over that same board. Trigger-only mode executes `Run`'s start-up `buildIndex` while the non-positive interval suppresses both the metrics ticker and its start-up recompute. Anchor on the index build completing — `Eventually` `Resolve(ctx, id)` returns the seeded record's path, proving `buildIndex` ran — then cancel the context, wait for `Run` to return, and assert `424242` is unchanged.
   - **(c) A failed listing writes nothing:** seed the sentinel `424242` through the accessor and seed each of the four buckets through `metrics.New().TaskAgeBucketTasks("openclaw", bucket)` to a distinct non-zero value, then construct the scanner as `&vaultScanner{metrics: metrics.New(), vaultName: "openclaw", ops: fileOps{listFiles: func(_ context.Context, _ string) ([]string, error) { return nil, fmt.Errorf("listing failed") }}}`, and call `recomputeTaskAgeMetrics(ctx)`. Assert the oldest-age value is still `424242` and every bucket still reads its seeded value — a failed read must never `Set` 0, because 0 would claim a clean board. This falsifies Failure Modes row 1 ("the recompute is abandoned before any value is written; the previously published values stay in place"), which is otherwise untested. The row's warning half is proven by the verification grep for the `task age metrics: list` line anywhere in `pkg/scanner/`: `glog` exposes no in-process output hook in this repo, so that line is asserted at the source rather than in the spec.

9. Add **a new file** `main_task_age_metrics_internal_test.go` in the repo root (`package main`, license header, plain `testing` — no Ginkgo needed). Create the new file rather than extending `main_internal_test.go`: the spec's wiring AC names a new internal `package main` test file, and a new file keeps this diff isolated. Do not write it as `package main_test` — that package (the existing `main_test.go`) cannot name the unexported `application` type, which is exactly why the new file is required.
   ```go
   func TestCreateVaultScannerPassesVaultNameAndMetricsInterval(t *testing.T)
   ```
   Construct `&application{VaultName: "sentinel-vault"}`, define a recording function with the `vaultScannerConstructor` signature that captures the vault-name and interval arguments (and returns `nil`), call `app.createVaultScanner(nil, make(chan struct{}), true, recording)`, and assert:
   - the recorded vault-name argument is exactly `"sentinel-vault"`;
   - the recorded interval argument is exactly `metricsInterval`;
   - `metricsInterval == 60*time.Second`.
</requirements>

<constraints>
- Frozen constructor parameter order: git client, task directory, poll interval, trigger channel, metrics implementation, auto-inject flag, vault name, metrics interval. A reordering is a cross-cutting break, not a local edit.
- Frozen recompute interval: a package-level `const metricsInterval = 60 * time.Second` in `main.go`. There is no `METRICS_INTERVAL` environment variable, no flag and no chart value. Do not add one.
- Single writer: the age series are written only by the metrics ticker. `scanFiles`, `RunCycle` and `Run`'s start-up index build must not `Set` them; two writers would make the value depend on which path ran last.
- No vault writes from the recompute: it uses the existing listing and read operations only.
- A non-positive metrics interval is a caller-only guard, not a failure mode: no ticker and no start-up recompute, so `time.NewTicker` is never handed a non-positive duration.
- The recompute runs inline in `Run`'s goroutine, not in a second goroutine; a trigger arriving during a recompute waits in the buffered channel rather than being lost.
- The `vault` label carries the constructor's `vaultName` verbatim; the vault-api display name (`private-agent`) must not be applied.
- Do not change the stuck-planning gauge or its placement at the end of `scanFiles`, the scan cycle, the identifier index, or the escalation paths.
- Do not add any configuration knob of any kind — no enable/disable flag, no per-bucket threshold, no per-vault allowlist, no interval setting.
- Do not commit; dark-factory handles git.
- Existing tests must still pass.
- License headers are required on both new test files (`make addlicense` enforces it).
</constraints>

<verification>
Run from the repo root:

```bash
GOFLAGS=-buildvcs=false go test -count=1 -mod=mod ./pkg/scanner/... .
```

```bash
grep -n 'metricsInterval' main.go                       # prints the const and its uses
# Exactly one occurrence, and it is the constructor argument on the helper call inside Run.
[ "$(grep -c 'NewGitRestVaultScanner' main.go)" = "1" ] \
  && grep -q 'createVaultScanner(.*scanner.NewGitRestVaultScanner)' main.go
# The failed-listing warning that Failure Modes row 1 depends on.
grep -q 'task age metrics: list' pkg/scanner/*.go
! grep -q -r --include='*.go' 'METRICS_INTERVAL\|metrics-interval' . && echo "no interval configuration surface"
! grep -qE 'agent_controller_task_oldest_age_seconds|agent_controller_task_age_bucket_tasks' pkg/scanner/*.go && echo "scanner never names the series"
```

```bash
ROOTDIR=/workspace make precommit
```

`make precommit` runs `generate` (which deletes and regenerates `mocks/`) and the full test suite, so it is also the compile gate for every constructor call site. Before finishing, re-run the commands above, confirm the five specs in requirements 7 and 8 (the ticker, the vault label, the two single-writer halves and the failed-listing pass) and the wiring test in requirement 9 pass, and walk every numbered requirement against the change. `make precommit` must exit 0.
</verification>
