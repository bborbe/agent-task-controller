---
status: completed
spec: [016-bug-task-identifier-path-index]
summary: Made the shared task-file lookup consult the scanner's identifier→path index first (one read, zero listings) with the unchanged vault walk as fallback, threaded the single scanner instance from main.go through the result writer and all four resolving executors, and added hit/miss/duplicate/nil/read-failure/parse-failure specs.
execution_id: agent-task-controller-taskpath-index-exec-028-resolver-first-lookup-and-wiring
dark-factory-version: v0.196.0
created: "2026-09-25T18:46:00Z"
queued: "2026-09-25T17:04:41Z"
started: "2026-09-25T17:10:46Z"
completed: "2026-09-25T17:20:20Z"
branch: dark-factory/bug-task-identifier-path-index
---

# Consult the identifier index before walking the vault

<summary>

- Writing a result back to a task file no longer reads the entire vault first: when the process already knows which file carries the identifier, the write reads exactly that one file.
- An identifier the process does not know about still resolves through the existing full directory walk, so nothing that resolves today stops resolving.
- A write for a known identifier costs one file read and zero directory listings, down from one listing plus one read per file in the vault.
- An identifier carried by two files is still refused loudly, naming both files, and that refusal now happens without any vault scan at all.
- A lookup that fails outright is returned as an error and never re-run through the walk, so an ambiguous identifier can never be resolved by re-deriving the ambiguity.
- The one scanner the process already runs is handed to the result writer and to every command handler that resolves a task file directly, so there is exactly one scanner and one mapping — never a second, empty one.
- The order of operations inside each command handler is unchanged: the cross-vault routing guard still runs before the task-file lookup, so a command for another vault still resolves nothing and writes nothing.
- A successful lookup from the mapping is recorded in the pod log at the level the deployed controllers run, so a running controller's behaviour is readable rather than inferred.
- No new setting, flag, metric or retry loop is introduced, and the three-attempt retry for an identifier the process does not know is untouched.

</summary>

<objective>

Make a result write resolve a task identifier from the scanner's published identifier→path index instead of walking the whole vault, so the single-threaded command consumer stops serializing behind thousands of file reads per command and commands stop expiring before they are processed — while an identifier the index does not hold still resolves through the unchanged walk and a duplicate identifier is still refused loudly. Satisfies spec 016 Desired Behavior 5-7 and Acceptance Criteria 2, 3, 4, 8, 9, 10 (plus AC 1).

</objective>

<context>

This repo has no root `CLAUDE.md`; the global YOLO container `CLAUDE.md` already in your context governs project conventions. The spec for this work is `specs/in-progress/016-bug-task-identifier-path-index.md`.

Read the spec first, in full. Pay particular attention to: Problem, Expected vs Actual, Goal, Non-goals (all of them — several are load-bearing vetoes), Desired Behavior 5-7, Constraints, Assumptions, Failure Modes (every row), Security / Abuse Cases, Acceptance Criteria 2, 3, 4, 8, 9, 10, and the "Suggested Decomposition" table (this prompt is its row 2). AC 11-12 and the doc/changelog work belong to prompt 3; AC 13-14 are the Post-Deploy rungs and are out of scope.

**This prompt depends on prompt 1 having shipped.** Prompt 1 is `prompts/1-resolver-seam-and-index.md`; it must already have added:

- `pkg/result/task_path_resolver.go`, declaring `result.TaskPathResolver` with the single method `Resolve(ctx context.Context, id lib.TaskIdentifier) (string, bool, error)`.
- `Resolve` on the `scanner.VaultScanner` interface (so `scanner.VaultScanner` satisfies `result.TaskPathResolver` structurally) plus `vaultScanner.publishIndex`, called at the end of `scanFiles`.
- `mocks/task_path_resolver.go` (counterfeiter fake with `ResolveCallCount()`, `ResolveReturns(...)`) and a regenerated `mocks/vault_scanner.go`.

Verify that before starting: `grep -n 'type TaskPathResolver interface' pkg/result/task_path_resolver.go` must print one line, and `grep -c 'ResolveCallCount' mocks/task_path_resolver.go` must print at least 1. If either is missing, STOP and report `status: failed` with the message `"prompt 1 (resolver seam and index) has not shipped"` — do not create the interface yourself and do not stub it.

Read these files IN FULL before changing anything:

- `pkg/result/result_writer.go` (1153 lines) — the whole file. The edit sites are the `resultWriter` struct, `NewResultWriter`, `FindTaskFilePath` (~lines 309-375: its doc comment, its signature and the `glob := taskDir + "/*.md"` walk that becomes the fallback), and the single call site inside `WriteResult` (~line 384). Read and preserve: the frozen duplicate error at ~lines 356-361, `notFoundAttempts = 3` at ~line 271, the retry loop and its log lines at ~lines 382-420, the `unowned` / `not_found` metric split at ~lines 422-447, and `AtomicReadModifyWriteAndCommitPush` at ~line 482.
- `pkg/command/task_increment_frontmatter_executor.go` (174 lines) — the guard call at ~line 55 and the lookup at ~line 64.
- `pkg/command/task_update_frontmatter_executor.go` (174 lines) — `frontmatterCommandVaultMismatch` is DEFINED at ~line 37 (and named in its doc comment at ~line 27); the guard CALL is at ~line 88 and the lookup at ~line 101. Do not confuse the definition with the call.
- `pkg/command/task_complete_task_executor.go` (187 lines) — the guard call at ~line 60, the identifier validation at ~line 69, the lookup at ~line 72.
- `pkg/command/planning_retry.go` (~lines 1-140) — the `PlanningRetryGate` interface, `NewPlanningRetryGate`, the `planningRetryGate` struct, and `Handle`'s lookup at ~line 82 with its error check at ~line 85.
- `pkg/factory/factory.go` (77 lines) — `commandExpireDuration` at ~lines 25-33 (frozen), `CreateCommandConsumer` and the `cdb.CommandObjectExecutorTxs` literal it builds.
- `main.go` (246 lines) — the single `scanner.NewGitRestVaultScanner(...)` construction at ~line 133, the `pkgsync.NewSyncLoop(...)` it is passed into, `result.NewResultWriter(...)` at ~line 182, and `factory.CreateCommandConsumer(...)` at ~line 191.
- `pkg/result/result_writer_test.go` (~lines 1-160) — the harness this prompt's new spec mirrors: `tmpDir`, `taskDir = "tasks"`, `fakeGit.PathReturns(tmpDir)` with the `ListFilesStub` / `ReadFileStub` / `AtomicReadModifyWriteAndCommitPushStub` trio, `fakeTime`, and the `writeTaskFile` closure.
- `pkg/command/task_complete_task_executor_test.go` (319 lines) — the whole file. Its `BeforeEach` builds the executor, `writeTaskFile` / `readFile` / `parseFrontmatter` / `buildCmdObj` are the harness, and `executor.HandleCommand(ctx, nil, cmdObj)` is the invocation shape.
- `docs/controller-design.md` — § 2's step list (~line 38: `walk task directory, find file matching task_identifier in frontmatter`) and its routing-guard paragraph (~line 52: the guard runs **before** the task-file lookup and a cross-vault command "performs no vault scan, increments no metric, writes nothing, and publishes no result event"). These two lines are the contract requirement 2 and the AC 9 loop rest on. Prompt 3 updates this file; this prompt does not.

Read the coding-plugin docs (in-container paths):

- `/home/node/.claude/plugins/marketplaces/coding/docs/go-patterns.md` — public interface + private struct + `New*` constructor; `errors.Wrapf` from `github.com/bborbe/errors`, never `fmt.Errorf`.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo/Gomega `Describe` / `Context` / `It` style, counterfeiter mocks (never hand-written mocks), external test package.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-logging-guide.md` — `glog` is this repo's logger; V(2) is an observable outcome, V(3) is per-item, V(4) is trace.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-precommit.md` — `funlen` 80 lines / 50 statements, `nestif` 4, `gocognit` 20, `golines --max-len=100` (which `make format` runs over test files too).
- `/home/node/.claude/plugins/marketplaces/coding/docs/definition-of-done.md` — coverage rules for new code (≥80% statement coverage on new code).

Environment facts that shape this prompt:

- The execution container's `.git` is masked (the daemon launches with `hideGit=true`; this repo's `.dark-factory.yaml` sets `workflow: direct` and no `hideGit`, so the masking comes from the launch flags). Do NOT run any `git` command anywhere in this prompt — it dies with `fatal: not a git repository`, and the daemon does not check `<verification>` exit codes, so a failed git command reads as a pass. There is no `git`-dependent step in this prompt.
- Do NOT run `kubectl*`, `docker`, `make build`, `make buca`, `gh`, or any operator/deploy command.
- `make precommit` runs `make format`, which rewrites `.go` files in place, and `make generate`, which wipes and regenerates `mocks/`. Both are expected, not failures.
- `make precommit` and `make test` run from the **repo root** — this is a single Go module and `pkg/result/` carries no Makefile, so a per-directory invocation has no target to run. The focused equivalent is `go test -count=1 ./pkg/result/... ./pkg/scanner/... ./pkg/command/...`.
- The race detector is opted into with `ENABLE_RACE=true` (`Makefile.precommit:29-34`) and is scoped to `./pkg/result/... ./pkg/scanner/...` because the whole-suite `-race` run is documented-flaky in the `cmd/*`-style binary smoke tests.
- Both deployed controllers run `-v=2` (`logLevel: "2"` in `nuke/agent/values-{dev,prod}.yaml` — that file lives in the nuke config repo, not this one; do not attempt to read it), so a `glog.V(2)` line is visible in the pod logs the Post-Deploy ACs read. A `glog.V(3)` line is not.

</context>

<requirements>

## 1. Change the shared lookup helper — `pkg/result/result_writer.go`

### 1a. The new signature

`FindTaskFilePath` gains the resolver as its **last** parameter, so every call site reads `(ctx, gitClient, taskDir, id, resolver)`:

```go
func FindTaskFilePath(
	ctx context.Context,
	gitClient gitclient.GitClient,
	taskDir string,
	id lib.TaskIdentifier,
	resolver TaskPathResolver,
) (string, lib.TaskFrontmatter, error) {
```

Its return triple is unchanged. Its doc comment must be replaced with one that states the resolver-first contract and the resolver-error rule — the current comment describes a pure walk and would be false after this change.

### 1b. Resolver first, walk as fallback

At the top of the body, before the existing `glob := taskDir + "/*.md"` line, delegate to a new package-level helper and fall through to the walk when it reports no hit:

```go
	indexPath, indexFrontmatter, hit, resolveErr := resolveFromIndex(ctx, gitClient, id, resolver)
	if resolveErr != nil {
		return "", nil, resolveErr
	}
	if hit {
		return indexPath, indexFrontmatter, nil
	}
```

Everything from `glob := taskDir + "/*.md"` down — the `ListFiles` call, the per-file `ReadFile`, the frontmatter extraction, the `task_identifier` comparison, the frozen duplicate error, the `matched` V(2) line and the existing-frontmatter unmarshal — stays **byte-for-byte as it is**, including its variable names, so the fallback path is provably the pre-fix path. Do not rename its locals. The two new locals above are deliberately named `indexPath` / `indexFrontmatter`, **not** `relPath` / `existingFrontmatter`: the fallback declares `var existingFrontmatter lib.TaskFrontmatter` in this same function-body block (`pkg/result/result_writer.go:327`), so reusing that name is a `existingFrontmatter redeclared in this block` compile error, not benign shadowing — and a `relPath` in the outer scope would shadow the fallback loop's own `relPath`.

### 1c. The helper `resolveFromIndex`

Add this package-level function directly above `FindTaskFilePath`:

```go
// resolveFromIndex consults the resolver for one identifier. It reports hit=false —
// meaning "run the walk" — for a nil resolver, for a miss, and for a hit whose single
// read or frontmatter parse failed; the snapshot is rebuilt every scan cycle, so a
// stale entry lives at most one cycle and the walk is the correct recovery. A resolver
// error is returned as an error and never downgraded to a miss: an ambiguous
// identifier reported as a miss would be re-derived by the walk, which could pick one
// of the two files — the 2026-08-31 incident.
//
// A hit reads exactly one file and issues no directory listing. The path is the one
// the resolver observed; it is never built from id.
func resolveFromIndex(
	ctx context.Context,
	gitClient gitclient.GitClient,
	id lib.TaskIdentifier,
	resolver TaskPathResolver,
) (string, lib.TaskFrontmatter, bool, error) {
	if resolver == nil {
		return "", nil, false, nil
	}
	relPath, found, resolveErr := resolver.Resolve(ctx, id)
	if resolveErr != nil {
		return "", nil, false, resolveErr
	}
	if !found {
		return "", nil, false, nil
	}
	content, readErr := gitClient.ReadFile(ctx, relPath)
	if readErr != nil {
		glog.V(3).
			Infof("FindTaskFilePath: index hit %s unreadable (%v), falling back to walk", relPath, readErr)
		return "", nil, false, nil
	}
	frontmatter, fmErr := ExtractFrontmatter(ctx, content)
	if fmErr != nil {
		glog.V(3).
			Infof("FindTaskFilePath: index hit %s has invalid frontmatter (%v), falling back to walk", relPath, fmErr)
		return "", nil, false, nil
	}
	glog.V(2).Infof("FindTaskFilePath: index hit for task %s at %s", id, relPath)
	var existingFrontmatter lib.TaskFrontmatter
	if umErr := yaml.Unmarshal([]byte(frontmatter), &existingFrontmatter); umErr != nil {
		glog.V(3).
			Infof("FindTaskFilePath: could not unmarshal existing frontmatter for %s: %v", relPath, umErr)
		existingFrontmatter = nil
	}
	return relPath, existingFrontmatter, true, nil
}
```

Rules for this helper, each of which a spec below falsifies:

- **A nil resolver selects the walk-only behaviour** and is never passed in production. It exists so the existing specs and a resolver-less construction keep working.
- **A miss (`found == false`, nil error) falls through to the walk.** It is not an error, and it must not be turned into "not found".
- **A resolver error is returned and the walk does NOT run.** Returning `false, nil` here would let the walk re-derive the ambiguity and pick one of the two files.
- **A hit reads exactly one file** via `gitClient.ReadFile` and never calls `ListFiles`.
- **A hit whose read or frontmatter parse fails falls back to the walk**, logging at `glog.V(3)` (per-item, not visible at the deployed level).
- **The hit log line is `glog.V(2)` and its text contains `index hit for task`.** V(2) is deliberate: the deployed controllers run at `-v=2`, and the existing per-file walk diagnostics are V(3), so without a V(2) line the fix would be invisible in the pod log. The wording around the substring is yours; the substring is not.
- On an unmarshal failure of the hit file's frontmatter, the path is still returned with `hit == true` and a nil frontmatter — matching the walk's existing behaviour for the same failure.
- **No I/O other than that single `ReadFile`.** No `ListFiles`, no `Pull`, no retry, no timeout, no second resolver call.

### 1d. Thread the resolver into the writer

- Add a `resolver TaskPathResolver` field to the `resultWriter` struct, after `notificationSender`.
- Add `resolver TaskPathResolver` as the **last** parameter of `NewResultWriter` and assign it in the returned literal. Update the constructor's doc comment to say what the resolver is for (the identifier→path index; a nil resolver selects the walk-only behaviour and is a test-only value).
- In `WriteResult`, pass `r.resolver` as the fifth argument of the `FindTaskFilePath` call (~line 384). Do not change the retry loop, its log lines, the `unowned` / `not_found` metric split, the `AtomicReadModifyWriteAndCommitPush` call, or anything else in `WriteResult`.

## 2. Thread the resolver into the four executors and the retry gate — `pkg/command/`

For each of the four files below, add `resolver result.TaskPathResolver` as the **last** constructor parameter, reference the captured parameter as the fifth argument of the `result.FindTaskFilePath` call, and extend the constructor's doc comment with one sentence naming the resolver. `pkg/command` already imports `github.com/bborbe/agent-task-controller/pkg/result`, so no import is added. Each constructor returns a closure that captures its parameters directly — do not introduce a struct, a field or a getter for the resolver.

| File | Constructor | Lookup call site (hint) |
|---|---|---|
| `pkg/command/task_increment_frontmatter_executor.go` | `NewIncrementFrontmatterExecutor` | ~line 64 |
| `pkg/command/task_update_frontmatter_executor.go` | `NewUpdateFrontmatterExecutor` | ~line 101 |
| `pkg/command/task_complete_task_executor.go` | `NewCompleteTaskExecutor` | ~line 72 |
| `pkg/command/planning_retry.go` | `NewPlanningRetryGate` | ~line 82 |

For `pkg/command/planning_retry.go` specifically: `NewPlanningRetryGate` returns a `PlanningRetryGate` interface backed by the `planningRetryGate` struct, so this one **does** need a `resolver result.TaskPathResolver` struct field, assigned in the constructor, and `Handle` must pass `g.resolver` to the lookup.

**Do not move any call site.** In each frontmatter executor the vault-routing guard (`if err := frontmatterCommandVaultMismatch(`) and the identifier validation stay **before** the lookup, exactly where they are. That ordering is the contract `docs/controller-design.md:52` records: a cross-vault command performs no lookup, no metric, no write and no result event. The guard is *defined* in `task_update_frontmatter_executor.go` (~line 37) and *called* at ~line 88 — the call must stay above the lookup.

## 3. Wire the single scanner through the process

### 3a. `pkg/factory/factory.go`

Add `resolver result.TaskPathResolver` to `CreateCommandConsumer`, immediately after the `resultWriter result.ResultWriter` parameter, and hand it to every constructor that resolves a path:

```go
	retryGate := command.NewPlanningRetryGate(
		gitClient, taskDir, vaultName, currentDateTime, prCommenter, m, resolver,
	)
	executors := cdb.CommandObjectExecutorTxs{
		command.NewTaskResultExecutor(resultWriter, retryGate, vaultName),
		command.NewIncrementFrontmatterExecutor(gitClient, taskDir, vaultName, m, resolver),
		command.NewUpdateFrontmatterExecutor(gitClient, taskDir, vaultName, m, resolver),
		command.NewCreateTaskExecutor(gitClient, taskDir, vaultName, currentDateTime, k),
		command.NewCompleteTaskExecutor(gitClient, taskDir, vaultName, currentDateTime, m, resolver),
	}
```

`NewTaskResultExecutor` and `NewCreateTaskExecutor` are unchanged — they do not resolve a path directly. `pkg/factory` already imports `pkg/result`. Do not touch `commandExpireDuration`.

### 3b. `main.go`

The process must construct the scanner **exactly once** and hand that one instance to both the sync loop and the command consumer. Hoist the currently-inline construction at ~line 133 into a variable and pass it to both:

```go
	vaultScanner := scanner.NewGitRestVaultScanner(
		gitClient,
		a.TaskDir,
		a.PollInterval,
		trigger,
		metrics.New(),
		autoInject,
	)
	syncLoop := pkgsync.NewSyncLoop(
		vaultScanner,
		publisher.NewTaskPublisher(eventObjectSender, lib.TaskV1SchemaID, currentDateTime),
		trigger,
		metrics.New(),
	)
```

Only the scanner construction is hoisted: the `publisher.NewTaskPublisher(...)` argument is the one already there, unchanged.

Then pass `vaultScanner` as the **last** argument of `result.NewResultWriter(...)` (~line 182) and as the `resolver` argument of `factory.CreateCommandConsumer(...)` (~line 191), in the position the new parameter occupies.

- `scanner.VaultScanner` satisfies `result.TaskPathResolver` structurally (prompt 1 gave it `Resolve` with the same signature), so no adapter, no wrapper and no type assertion is needed.
- Do NOT construct a second scanner for the consumer path. Two scanners would each publish their own index and the consumer would read an index that never reflects the vault — the spec's wiring AC and its first Failure Modes row both forbid it.
- Do NOT pass a nil resolver in production. Nil is a test-only value.

## 4. Add `pkg/result/result_writer_index_test.go`

Create that exact path. It is `package result_test` with the BSD license header, a top-level `var _ = Describe(...)`, and no `TestXxx` function (`result_suite_test.go` owns the bootstrap).

**4a. The harness.** Mirror `pkg/result/result_writer_test.go`'s `BeforeEach`: `tmpDir` via `os.MkdirTemp`, `taskDir = "tasks"` and `os.MkdirAll(filepath.Join(tmpDir, taskDir), 0750)`, `fakeGit = &mocks.GitClient{}` with `PathReturns(tmpDir)` and the same three stubs (`ListFilesStub` globbing `filepath.Join(tmpDir, glob)` and returning paths relative to `tmpDir`, `ReadFileStub` reading `filepath.Join(tmpDir, relPath)`, `AtomicReadModifyWriteAndCommitPushStub` reading the file, running the modify closure and writing back), `fakeTime = &libtimemocks.CurrentDateTimeGetter{}` with `NowReturns(libtime.DateTime(time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)))`, and an `AfterEach` removing `tmpDir`.

Add `resolver = &mocks.TaskPathResolver{}` with `resolver.ResolveReturns("", false, nil)` so every spec that does not opt into a hit behaves exactly as the walk-only path does today.

Define one helper that constructs a writer with a given resolver, so each spec reads explicitly and the counts are read from the **same** `fakeGit` instance the writer was constructed with:

```go
	newWriter := func(r result.TaskPathResolver) result.ResultWriter {
		return result.NewResultWriter(
			fakeGit,
			taskDir,
			"openclaw",
			fakeTime,
			metrics.New(),
			libtime.NewWaiterDuration(),
			nil,
			r,
		)
	}
```

Do not declare a second `errTest` — `result_writer_test.go` already declares it at package scope in the same package. Use an inline `errors.New(...)` where a sentinel is needed.

**4b. The five specs.** Constants for the identifiers: `indexedID = "11111111-1111-4111-8111-111111111111"` and `missingID = "22222222-2222-4222-8222-222222222222"`. Every spec passes the same incoming payload shape the existing specs use (`result_writer_test.go:134-144`), so the keys the assertions re-read from disk are the ones the payload carried:

```go
lib.Task{
    TaskIdentifier: lib.TaskIdentifier(indexedID),
    Frontmatter: lib.TaskFrontmatter{
        "task_identifier": indexedID,
        "status":          "done",
        "phase":           "done",
    },
    Content: lib.TaskContent("New content\n"),
}
```

The `status: done` / `phase: done` values asserted below come from this payload, not from the fixture file on disk.

1. **A hit costs one read and no listing** (spec AC 2). Write the fixture `tasks/indexed.md` with `---\ntask_identifier: <indexedID>\nstatus: in-progress\nassignee: backtest-agent\n---\nOld content\n`; set `resolver.ResolveReturns("tasks/indexed.md", true, nil)`; build `writer := newWriter(resolver)`; call `writer.WriteResult(ctx, <task for indexedID>)` and expect success. Then assert, in this order: `Expect(fakeGit.ListFilesCallCount()).To(Equal(0))`, `Expect(fakeGit.ReadFileCallCount()).To(Equal(1))`, `Expect(resolver.ResolveCallCount()).To(Equal(1))`. Also re-read `tasks/indexed.md` from disk with `os.ReadFile` and assert it contains `status: done`, so the hit is proven to have written. `AtomicReadModifyWriteAndCommitPush` is the only other git call `WriteResult` makes and it does not route through `ReadFile` or `ListFiles`, so the pair of counts is exact rather than approximate. The counters MUST be read from the `fakeGit` the writer was constructed with — counting on a second fake instance passes vacuously.

2. **A miss still resolves through the walk** (spec AC 3). Write `tasks/walked.md` carrying `indexedID` with the same frontmatter shape; leave the resolver at its default miss (`resolver.ResolveReturns("", false, nil)`); build the writer and call `WriteResult`. Expect success, `Expect(resolver.ResolveCallCount()).To(Equal(1))`, `Expect(fakeGit.ListFilesCallCount()).To(BeNumerically(">=", 1))`, and — re-reading the fixture from disk with `os.ReadFile` — that the file's frontmatter afterwards carries the merged result (`status: done` and `phase: done`), proving the write landed. This spec is the falsifier against an implementation that returns "not found" on an index miss instead of falling back.

3. **A resolver error is returned without a walk and without a write** (spec AC 4). Set `resolver.ResolveReturns("", false, errors.New("duplicate task_identifier " + indexedID + " in tasks/first.md and tasks/second.md"))`; build the writer and call `WriteResult`. Expect an error whose text contains `tasks/first.md` **and** `tasks/second.md`; then `Expect(resolver.ResolveCallCount()).To(Equal(1))`, `Expect(fakeGit.ListFilesCallCount()).To(Equal(0))` and `Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(0))`. The zero-listing clause is the falsifier against an implementation that treats a resolver error as a miss and re-runs the full walk — which would re-derive the ambiguity and could return one of the two paths. Do not let this spec retry: a resolver error returns on the first attempt, so the counts above are exact.

4. **A nil resolver selects the walk-only behaviour** (spec AC 8, the "no production call site passes a nil resolver" half). Write `tasks/walk-only.md` carrying `indexedID`; build `writer := newWriter(nil)`; call `WriteResult`; expect success, `Expect(fakeGit.ListFilesCallCount()).To(BeNumerically(">=", 1))`, and the re-read fixture containing `status: done`. This is the falsifier against an implementation that requires a non-nil resolver.

5. **A hit whose file cannot be read falls back to the walk** (spec Failure Modes row 2 — the stale-path recovery). Write `tasks/walked.md` carrying `indexedID`; set `resolver.ResolveReturns("tasks/gone.md", true, nil)` — a path that does not exist, standing in for a hit whose file was deleted or renamed since the last scan cycle; build the writer and call `WriteResult`. Expect success, `Expect(resolver.ResolveCallCount()).To(Equal(1))`, `Expect(fakeGit.ListFilesCallCount()).To(BeNumerically(">=", 1))`, and the re-read `tasks/walked.md` carrying `status: done`. This is the only spec that exercises `resolveFromIndex`'s read-failure branch, which is the recovery the spec's Failure Modes row 2 depends on; without it that branch ships untested.

## 5. Add the executor behavioural spec — `pkg/command/task_complete_task_executor_test.go`

Spec AC 8's behavioural counterpart exists because its greps also pass on the untouched pre-fix tree: an implementation that threads the resolver into the writer but hands the four executors a fresh empty resolver would satisfy every grep while leaving four of the five call sites walking the vault. One executor path must therefore be asserted by behaviour.

- Add `fakeResolver *mocks.TaskPathResolver` to the spec's `var (...)` block, construct it in `BeforeEach` with `fakeResolver = &mocks.TaskPathResolver{}` and `fakeResolver.ResolveReturns("", false, nil)`, and pass it as the last argument of `command.NewCompleteTaskExecutor(...)`. The default miss keeps every existing spec's behaviour identical.
- Add one new `It` inside `Describe("HandleCommand")` (a new `Context("identifier index", ...)` after the `Context("task resolution")` block is a good home). It writes `tasks/index-hit.md` with `---\ntask_identifier: <uuid>\nstatus: in_progress\nphase: ai_review\n---\nbody\n`, sets `fakeResolver.ResolveReturns("tasks/index-hit.md", true, nil)`, and invokes the executor with `executor.HandleCommand(ctx, nil, buildCmdObj(task.CompleteCommand{TaskIdentifier: lib.TaskIdentifier(<uuid>)}))` — the same invocation shape the existing specs use. Then assert: `Expect(err).NotTo(HaveOccurred())`, `Expect(fakeResolver.ResolveCallCount()).To(Equal(1))`, `Expect(fakeGit.ListFilesCallCount()).To(Equal(0))`, `Expect(fakeGit.ReadFileCallCount()).To(Equal(1))`, and `parseFrontmatter(taskFile)["status"]` equal to `"completed"`.
- Use a real UUID for the identifier (e.g. `"aaaaaaaa-1111-4111-8111-111111111111"`) so the executor's identifier validation is exercised on a realistic value.
- The count of `1` for `ReadFile` is exact because the executor's `AtomicReadModifyWriteAndCommitPushStub` reads the file with `os.ReadFile` directly, not through the fake's `ReadFile`.
- Do not change any existing `Expect(` line in this file.

## 6. Mechanical call-site updates in the existing specs

The new last parameter forces one added argument at each existing call site. This is the only permitted edit to these files — do not change, reorder, weaken or reformat any `Expect(` line, and do not change any existing assertion.

`result.NewResultWriter(...)` — add `nil` as the final argument:

| File | Call site (hint) |
|---|---|
| `pkg/result/find_task_file_path_test.go` | ~line 147 |
| `pkg/result/result_writer_deeplink_test.go` | ~line 108 |
| `pkg/result/result_writer_test.go` | ~line 105 |
| `pkg/result/result_writer_escalation_test.go` | ~lines 110 and 431 |
| `pkg/result/result_writer_body_merge_test.go` | ~line 89 |
| `pkg/result/result_writer_unowned_test.go` | ~line 60 |
| `pkg/result/result_writer_accumulate_test.go` | ~line 90 |

`result.FindTaskFilePath(...)` — add `nil` as the final argument, at ~lines 35, 57, 76, 86, 98, 109 and 118 of `pkg/result/find_task_file_path_test.go`.

`pkg/command` constructors — add `nil` as the final argument:

| File | Call site (hint) |
|---|---|
| `pkg/command/task_update_frontmatter_executor_test.go` | ~line 82 |
| `pkg/command/task_increment_frontmatter_executor_test.go` | ~line 83 |
| `pkg/command/task_frontmatter_sequence_test.go` | ~lines 81 and 87 |
| `pkg/command/planning_retry_test.go` | ~line 51 |
| `pkg/command/planning_retry_integration_test.go` | ~line 232 |

(`pkg/command/task_complete_task_executor_test.go` is requirement 5's file and gets `fakeResolver`, not `nil`.)

`nil` is legal in tests — the spec forbids it only at production call sites.

## 7. Self-check before finishing

Re-run every command in `<verification>` and confirm each one passes. Then walk the spec's Acceptance Criteria against the change:

- **AC 2** — a known identifier costs zero listings and exactly one read, asserted on the same fake the writer was constructed with.
- **AC 3** — an unknown identifier still resolves through the walk and the write lands on disk.
- **AC 4** — a resolver error is returned, no walk runs and no write happens, and the error names both paths.
- **AC 8** — `main.go` constructs exactly one scanner; `CreateCommandConsumer` takes the frozen seam type and hands it to the result writer's consumer path and all four resolving executors; no production call site passes a nil resolver; and one executor path is asserted behaviourally (zero listings, one read, one resolver call).
- **AC 9** — in each of the three frontmatter executors the vault-routing guard's *call* line is above the lookup's line.
- **AC 10** — `grep -c 'index hit for task' pkg/result/result_writer.go` returns at least 1, and the line is at V(2).
- **AC 1** — `make precommit` exits 0 at the repo root.

Also confirm you did NOT: add a config field, env var, CLI flag or metric; change `notFoundAttempts`, the backoff, the retry log lines or the `unowned` / `not_found` metric split; move a guard below a lookup; construct a second scanner; or delete the walk.

</requirements>

<constraints>

- **The lookup helper's signature is the shared contract.** It gains the resolver as a parameter; all five call sites use the new signature. No caller keeps the old one.
- **The five call sites keep their positions.** In each frontmatter executor the vault-routing guard and the identifier validation stay before the lookup; the guard's ordering is what `docs/controller-design.md:52` contracts.
- **A resolver miss is not an error.** The resolver reports absence with a nil error so the caller falls back; only an ambiguous identifier is an error.
- **A resolver error is an error, not a miss.** The two mean opposite things. Returning `false, nil` on a resolver error would re-derive the ambiguity through the walk.
- **A nil resolver is a test-only value.** It selects the walk-only behaviour and is never passed in production.
- **The full walk is not removed.** `gitClient.ListFiles(taskDir+"/*.md")` plus the per-file read and parse stays as the fallback path inside the lookup helper, unchanged byte-for-byte.
- **The duplicate error's message shape is frozen:** `duplicate task_identifier %s in %s and %s` (`pkg/result/result_writer.go:357-361`). Do not reword it and do not add a second variant.
- **The retry loop is frozen:** `notFoundAttempts = 3` (`pkg/result/result_writer.go:271`) with its backoff, its retry log line and its `unowned` / `not_found` metric split (`pkg/result/result_writer.go:422-447`) are unchanged.
- **One scanner instance only.** `main.go` constructs it once and passes that instance both to the sync loop and to the command-consumer construction. Two scanners would each publish their own index.
- **Import direction is frozen:** `pkg/scanner` must not import `pkg/result`. The seam is satisfied structurally — no adapter.
- **Error wrapping uses `errors.Wrapf` from `github.com/bborbe/errors`** — never `fmt.Errorf`.
- **Forbidden shapes:** exposing the live bookkeeping map by pointer to a reader; a package-level index variable; `init()` or `sync.Once` lazy wiring; a second goroutine added to "sync" the index; a package-level lookup function on the scanner called from the consumer's methods.
- **No new config field, env var, CLI flag, or metric.** The hit is observable through the existing V(2) log line and the existing `agent_controller_git_rest_calls_total{op="list"}` counter. Do not invent a counter or a gauge.
- **Frozen:** `commandExpireDuration` (`pkg/factory/factory.go:25-33` — the window is not widened, narrowed or made configurable), the routing predicates, the frontmatter merge and its ownership table, the escalation path, the heal-on-write stamp.
- **The hit log line is `glog.V(2)` and contains the substring `index hit for task`.** V(1) or V(3) would be invisible at the deployed log level 2 and would fail the spec's observability AC.
- **`make precommit` and `make test` run from the repo root** (single Go module; `pkg/result/` carries no Makefile). The race detector is opted into with `ENABLE_RACE=true`.
- The existing specs in `pkg/result/`, `pkg/scanner/` and `pkg/command/` pass with unmodified `Expect(` lines, apart from the mechanical argument additions listed in requirement 6.
- Per `go-precommit.md`: keep `funlen` under 80 lines / 50 statements, `nestif` under 4, `gocognit` under 20, and every line under 100 characters. Write the new spec file already within that budget.
- Do NOT commit — dark-factory handles git.
- Do NOT run any `git` command — the container's `.git` is masked and a failed git command reads as a pass.
- Do NOT run `kubectl*`, `docker`, `make build`, `make buca`, `gh`, or any operator/deploy command.

</constraints>

<verification>

Run from the repo root. Fast loop while iterating:

```
go test -count=1 ./pkg/result/... ./pkg/scanner/... ./pkg/command/...
```

Expect every Ginkgo suite to report `SUCCESS` with `0 Skipped` and `0 Pending`. This is the primary evidence for AC 2, AC 3, AC 4 and the behavioural half of AC 8.

The race detector (AC 6's run, which this prompt must not break):

```
ENABLE_RACE=true go test -race -count=1 ./pkg/result/... ./pkg/scanner/...
```

Expect exit 0 with no `DATA RACE` line in the output.

Spec AC 2 — the cost assertion exists in `pkg/result`'s specs. The glob spans all of `pkg/result` on purpose: the only file in that package asserting `ListFilesCallCount` before this change is `find_task_file_path_test.go`, which a `result_writer_*_test.go` glob would miss. This grep is a **shape check inherited from the spec, not the AC's evidence**: it already returns ≥1 on the pre-fix tree (`find_task_file_path_test.go:36` asserts the walk's own count). The real evidence for AC 2 is the `result_writer_index_test.go` spec above, which asserts `0` listings and exactly `1` read on the same `fakeGit` the writer was constructed with. The count form is used because the expected count is non-zero:

```
grep -c 'ListFilesCallCount' pkg/result/*_test.go || true
```

Expect at least one file to report a count of 1 or more (and total matches well above 1). `grep -c` exits 1 when a file has zero matches, hence the `|| true`.

Spec AC 10 — the V(2) hit line exists:

```
grep -c 'index hit for task' pkg/result/result_writer.go || true
grep -n 'index hit for task' pkg/result/result_writer.go
```

Expect the first to print at least 1 and the second to show the `glog.V(2).Infof` line (confirm the `V(2)`, not V(1) or V(3)).

Spec AC 8 — no production call site passes a nil resolver. The pattern carries a trailing comma on purpose: a bare `nil` would also match the `if err != nil {` check that immediately follows each of the five calls, so a `nil` pattern reports 1 on a correct implementation. A passed argument ends with a comma. The expected count is **0**:

```
grep -n -A5 'FindTaskFilePath(' pkg/result/result_writer.go pkg/command/task_increment_frontmatter_executor.go pkg/command/task_update_frontmatter_executor.go pkg/command/task_complete_task_executor.go pkg/command/planning_retry.go | grep -cE 'nil,' || true
```

Expect `0`. If it prints anything else, inspect the matched lines — a `nil,` here means a production call site is passing a nil resolver. One caveat before chasing it: the `-A5` window reaches five lines past each `FindTaskFilePath(`, so if `make format` has re-wrapped one of those calls onto fewer lines the window can also reach the neighbouring `return nil, nil, errors.Wrapf(...)` and report a `nil,` on a correct implementation. Read the call shape first (`grep -n -A6 'FindTaskFilePath(' <file>`) — only a `nil` sitting in the resolver's own argument position is a real failure.

Spec AC 8 — the resolver reaches the single wiring point, and there is exactly one scanner:

```
grep -c 'TaskPathResolver' pkg/factory/factory.go || true
grep -c 'NewGitRestVaultScanner' main.go || true
grep -n 'NewGitRestVaultScanner' main.go
```

Expect the first to print at least 1, the second to print exactly `1`, and the third to show the single hoisted construction whose result is passed to both `pkgsync.NewSyncLoop` and `factory.CreateCommandConsumer`.

Spec AC 9 — in each of the three frontmatter executors the vault-routing guard's **call** runs before the lookup. This prints one `PASS` or `FAIL` line per file and exits non-zero if any file fails:

```
for f in pkg/command/task_increment_frontmatter_executor.go pkg/command/task_update_frontmatter_executor.go pkg/command/task_complete_task_executor.go; do
  g=$(grep -n 'if err := frontmatterCommandVaultMismatch(' "$f" | cut -d: -f1)
  l=$(grep -n 'result.FindTaskFilePath(' "$f" | cut -d: -f1)
  if [ -n "$g" ] && [ -n "$l" ] && [ "$g" -lt "$l" ]; then echo "PASS $f guard=$g lookup=$l"; else echo "FAIL $f guard=$g lookup=$l"; exit 1; fi
done
```

Expect three `PASS` lines. The pattern is the **call** shape `if err := frontmatterCommandVaultMismatch(`, not the bare identifier: the helper is *defined* in `task_update_frontmatter_executor.go`, so a bare-identifier grep on that file would always report the guard first and would pass even if the guard call were moved below the lookup.

The mechanical call-site updates landed (no stale 4-argument or 7-argument call remains):

```
grep -c 'result.FindTaskFilePath(' pkg/result/find_task_file_path_test.go
grep -n 'FindTaskFilePath' pkg/result/find_task_file_path_test.go
```

Expect the count to be 7. To confirm each call really received the extra argument, check that the file contains seven `nil)` or `nil,` closers on those calls (`make format` may wrap a long call, so do not assert on the exact line shape):

```
grep -n 'FindTaskFilePath' pkg/result/find_task_file_path_test.go
```

Read the seven printed call sites and confirm each one's argument list ends with `nil`.

The full gate, run ONCE at the end (AC 1):

```
make precommit
```

Expect exit 0 with the full suite green. `make precommit` runs `ensure format generate test check addlicense`; `format` rewrites `.go` files in place and `generate` wipes and regenerates `mocks/`, both expected. If it fails, fix the failing target and re-run only that target (`make lint`, `make vet`, `make test`, `make gosec`) until it passes, then re-run the full `make precommit` once.

NOT run here, and deliberately so — AC 11-12 (the design doc and changelog) are prompt 3's scope; AC 13-14 read a running controller's pod log on dev and prod and are operator-side; the image build, pin bump and deploy steps are operator-side; and no `git` command runs here because the container's `.git` is masked.

</verification>

<!--
NOTES FOR THE HUMAN REVIEWER — decisions taken and open questions, non-blocking:

1. The spec's Suggested Decomposition is followed exactly: this is its row 2, depending on prompt 1.
   AC 4's evidence spans prompt 1 (the multimap) and this prompt (the no-walk rule); this prompt owns the
   "resolver error is returned without a walk and without a write" half, and prompt 1 owns the half that
   produces the two-path error from the index. The two specs together are the AC.

2. AMBIGUITY RESOLVED — the spec's AC 4 evidence says the returned error string "contains both fixture
   paths" while the index holds two paths. Since this prompt's spec drives `WriteResult` through a
   resolver double, the duplicate error is built by the double with the frozen format string naming two
   fixture paths. That is faithful to the AC's wording, and the stronger, real-path version of the same
   guarantee is prompt 1's scanner spec, which produces the two-path error from the real index. If the
   reviewer wants one spec to span both, the decomposition table would have to merge rows 1 and 2, which
   would break prompt 1's "independently testable without touching the consumer path" property.

3. `nil` is used as the added argument at every existing test call site. The spec forbids a nil resolver
   only at production call sites (AC 8's `nil,` grep is scoped to the five production files), and a nil
   resolver reproduces today's walk-only behaviour exactly, which is what keeps every existing
   `Expect(` line valid unmodified. The one exception is `task_complete_task_executor_test.go`, which
   gets a `mocks.TaskPathResolver` defaulting to a miss so the new behavioural spec can flip it to a hit.

4. Requirement 3b shows the `pkgsync.NewSyncLoop(...)` call only to make clear where `vaultScanner`
   replaces the inline `scanner.NewGitRestVaultScanner(...)`; the `publisher.NewTaskPublisher(...)`
   argument in that snippet is the one already in `main.go`, unchanged.

5. The V(2) hit line lives in `pkg/result/result_writer.go` (in the new `resolveFromIndex` helper), which
   is what AC 10's grep targets. The substring `index hit for task` is frozen; the surrounding wording is
   left to the implementer.

6. Not in this prompt, by design: `docs/controller-design.md` § 1/§ 2 and the `## Unreleased` changelog
   bullet (prompt 3), and any new metric (the spec's Non-goals forbid one).

7. HANDOFF FROM PROMPT 1, partially closable — prompt 1 records that the frozen duplicate format string
   `duplicate task_identifier %s in %s and %s` is now hand-copied in two places (the scanner's `Resolve`
   and this package's walk) and that nothing asserts their equality. The full comparison is not achievable
   from here: prompt 1's own note establishes that a real duplicate is unreachable through `RunCycle` (it
   must be seeded in `package scanner`'s unexported bookkeeping). The achievable half is a spec pinning the
   WALK's duplicate error to the exact frozen format string — `find_task_file_path_test.go:57` asserts only
   substrings today. Not required by any AC; worth the one assertion only if the agent is already editing
   that file for the mechanical argument addition.
-->

