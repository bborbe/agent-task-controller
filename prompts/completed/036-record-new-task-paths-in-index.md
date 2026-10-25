---
status: completed
summary: Added Record to TaskPathResolver and VaultScanner so a task created after the index build (or found by a unique walk match) is indexed and resolves with one read; the create-task executor records on a clean miss, FindTaskFilePath records a unique walk match, Record never overwrites an entry with two or more paths; regenerated mocks, added tests, docs and CHANGELOG entry
execution_id: agent-task-controller-index-record-exec-036-record-new-task-paths-in-index
dark-factory-version: v0.198.7
created: "2026-10-25T00:00:00Z"
queued: "2026-10-25T19:35:26Z"
started: "2026-10-25T19:36:55Z"
completed: "2026-10-25T20:07:25Z"
---

<summary>
- A task file the controller creates is added to the identifier→path index straight away, so later commands for that task find it with one read instead of walking the whole vault
- A task file that a full walk finds is added to the index too, so the same task is never walked twice
- A trigger-only controller's index no longer goes stale for every task created after the pod started
- A known duplicate identifier is still reported as a duplicate; recording never hides one
- A create whose identifier the index already knows does not overwrite that entry
- Every existing lookup, create, collision and reopen behaviour is unchanged
- New tests prove each recording path and each case where nothing is recorded
- The changelog and design doc record the fix
</summary>

<objective>
Stop the trigger-only controller's identifier→path index from freezing at pod start: on prod openclaw-0 (2026-10-25) 155 of 159 walked identifiers were created after pod start, the walks pushed the single-threaded consumer 60 minutes behind, and commands were dropped as `command expired`. After this change, a create and a successful walk each record the path, so a task this controller creates costs zero walks and any other file created after start costs at most one.
</objective>

<context>
Read CLAUDE.md for project conventions, if it exists. This repo has no root CLAUDE.md today, so do not treat its absence as an error.

Read these files before changing anything:
- `pkg/result/task_path_resolver.go`: the interface `TaskPathResolver` with one method `Resolve(ctx context.Context, id lib.TaskIdentifier) (string, bool, error)` and a `//counterfeiter:generate -o ../../mocks/task_path_resolver.go --fake-name TaskPathResolver . TaskPathResolver` directive. Its doc comment lists the three outcomes of `Resolve`.
- `pkg/scanner/vault_scanner.go`:
  - The interface `VaultScanner` (counterfeiter directive `-o ../../mocks/vault_scanner.go --fake-name VaultScanner`) with `Run`, `RunCycle`, `Resolve`. `main.go` passes the `VaultScanner` returned by `scanner.NewGitRestVaultScanner` as the `result.TaskPathResolver` argument of `factory.CreateCommandConsumer` and of `result.NewResultWriter`, so `VaultScanner` must keep satisfying `result.TaskPathResolver` structurally. `pkg/scanner` must not import `pkg/result`.
  - `type vaultScanner struct` with `indexMutex sync.RWMutex` and `index map[lib.TaskIdentifier][]string`, whose comment says the map is "Rebuilt wholesale once per completed scan cycle; never mutated in place".
  - `func (v *vaultScanner) Resolve(...)`: it reads `paths := v.index[id]` under `RLock`, releases the lock, then switches on `len(paths)` (0 → miss, 1 → hit, ≥2 → `duplicate task_identifier %s in %s and %s` error). Because the slice is used after the lock is released, a writer must never append to or modify an existing slice in place — it may only store a NEW slice.
  - `publishIndex` and `buildIndex` both assign a whole new map under `indexMutex.Lock()`.
- `pkg/result/result_writer.go`: `func FindTaskFilePath(ctx, gitClient, taskDir, id, resolver TaskPathResolver) (string, lib.TaskFrontmatter, error)`. It calls `resolveFromIndex` first; on a miss it lists `taskDir + "/*.md"`, reads every file, returns an error naming both paths on a duplicate identifier, and otherwise returns `matchedRelPath` (empty when nothing matched). `resolver` may be nil (tests pass nil).
- `pkg/command/task_create_task_executor.go`: `func NewCreateTaskExecutor(gitClient gitclient.GitClient, taskDir string, vaultName string, currentDateTime libtime.CurrentDateTimeGetter, k int) cdb.CommandObjectExecutorTx`. Inside the executor func, `writeTaskFile(ctx, gitClient, relPath, cmd, vaultName, reopened, priorStatus)` is called inside `if err := ...; err != nil { return nil, nil, err }`, followed by `retireSupersededBuildFixTasks`, `supersedePriorRecurringTask`, `supersedePriorUpdateGoTasks`, then `return nil, nil, nil`. `relPath` at that point is the vault-relative path that was written (possibly the disambiguated `"{Title} - {id[:8]}.md"` path). The package already imports `result "github.com/bborbe/agent-task-controller/pkg/result"`.
- `pkg/factory/factory.go`: `CreateCommandConsumer` already has a `resolver result.TaskPathResolver` parameter and builds `command.NewCreateTaskExecutor(gitClient, taskDir, vaultName, currentDateTime, k)` inside `cdb.CommandObjectExecutorTxs{...}`.
- `pkg/command/task_create_task_executor_test.go`: every `command.NewCreateTaskExecutor(` call (around lines 75, 775, 802, 827, 852, 986). The shared `BeforeEach` builds `fakeGit`, makes `AtomicWriteIfAbsentAndCommitPushStub` write the file to disk, and defaults `fakeGit.ReadFileReturns(nil, errors.New("GET file returned 404: not found"))` so every title path is free.
- `pkg/result/result_writer_index_test.go`: `Describe("ResultWriter identifier index", ...)` using `resolver = &mocks.TaskPathResolver{}`; follow its `It("still resolves through the walk on an index miss", ...)` for style.
- `pkg/result/find_task_file_path_test.go`: calls `result.FindTaskFilePath(ctx, fakeGC, "tasks", "<id>", nil)`.
- `pkg/scanner/vault_scanner_index_internal_test.go`: `Describe("vaultScanner identifier index", ...)`, package `scanner` (internal), with `s *vaultScanner`, the constants `alphaID` / `betaID` / `dupID`, helpers `write`, `cycle`, `taskFile`, the spec `"preserves duplicate identifiers and reports two paths for one identifier as an error"` (seeds `s.hashes` directly and calls `s.publishIndex()`), the race spec `It("stays clean under the race detector when scanning and resolving concurrently", ...)`, and `Context("trigger-only mode (poll interval zero)", ...)` with its `triggerOnly := func() *vaultScanner` helper. The fixture scanner is built with `autoInject=true`, so a scan cycle REPAIRS a second file carrying a duplicate identifier — a duplicate can only be seeded through `s.hashes` + `s.publishIndex()`.
- `specs/in-progress/016-bug-task-identifier-path-index.md`: freezes `TaskPathResolver` as declaring the single method `Resolve`. This change deliberately extends that seam (see constraints).
- `docs/controller-design.md`: the paragraph starting `**Index bootstrap in trigger-only mode.**`.
- `CHANGELOG.md`: the top section is `## v0.18.4`; there is no `## Unreleased` section. Copy the bullet style (`- fix: **bold lead.** ...`).
- `Makefile.precommit`: `precommit` runs `generate` (`go generate -mod=mod ./...`), which regenerates the counterfeiter mocks under `mocks/`.
</context>

<requirements>
1. In `pkg/result/task_path_resolver.go`, add a second method to `TaskPathResolver`:
   ```go
   // Record tells the resolver that relPath is the vault-relative path of the task
   // file carrying id, so a later Resolve(id) hits instead of missing. The caller
   // must have just established that fact — by writing that file or by a walk that
   // found exactly one file carrying id. It returns nothing: recording is a cache
   // fill, and a failure to record only costs a later walk. An empty id or empty
   // relPath is ignored. A resolver that already knows of two or more paths for id
   // keeps them, so a known duplicate is never hidden.
   Record(ctx context.Context, id lib.TaskIdentifier, relPath string)
   ```
   Extend the interface's doc comment with one sentence saying `Record` fills the cache between rebuilds.

2. In `pkg/scanner/vault_scanner.go`:
   - Add the same `Record(ctx context.Context, id lib.TaskIdentifier, relPath string)` method (same doc comment) to the `VaultScanner` interface.
   - Implement it on `*vaultScanner`:
     ```go
     func (v *vaultScanner) Record(ctx context.Context, id lib.TaskIdentifier, relPath string) {
     	if id == "" || relPath == "" {
     		return
     	}
     	v.indexMutex.Lock()
     	defer v.indexMutex.Unlock()
     	if len(v.index[id]) >= 2 {
     		// A known duplicate stays reported as one (2026-08-31 incident).
     		return
     	}
     	// Store a NEW slice: Resolve uses the slice it read after releasing the
     	// read lock, so an existing slice must never be modified in place.
     	v.index[id] = []string{relPath}
     	glog.V(2).Infof("index: recorded %s for task %s", relPath, id)
     }
     ```
     `ctx` may be unused; if the linter rejects an unused parameter, name it `_ context.Context`. If `v.index` can be nil at this point (check the constructors; both initialise it with `make`), keep it non-nil — do not add a nil-map write.
   - Update the comment on the `index` field: it is rebuilt wholesale per scan cycle (or once by `buildIndex` in trigger-only mode) and, between rebuilds, single entries are replaced by `Record` — always with a new slice, never mutated in place. A `Record` that lands just before a `publishIndex`/`buildIndex` swap is lost with the old map, which costs at most one later walk. Update the `Resolve` doc comment in the interface to mention entries recorded since the last rebuild.
   - In the `buildIndex` doc comment, replace the sentence `Files created after this pass miss and fall back to the walk, exactly as before;` (it is wrapped across two `//` lines after `not a repair path, and the walk stays the recovery.`) with `Files created after this pass are added by Record — on create, or on their first walk;`.

3. In `pkg/result/result_writer.go`, function `FindTaskFilePath`: after the walk loop, immediately before the final `return matchedRelPath, existingFrontmatter, nil`, add:
   ```go
   if matchedRelPath != "" && resolver != nil {
   	// The walk just proved matchedRelPath is the only file carrying id; record it
   	// so the next lookup for id is one read, not another full walk.
   	resolver.Record(ctx, id, matchedRelPath)
   }
   ```
   Do not record on the duplicate-error return, on an index hit, or when nothing matched.

4. In `pkg/command/task_create_task_executor.go`:
   - Add a final parameter `resolver result.TaskPathResolver` to `NewCreateTaskExecutor`. A nil resolver is allowed and means "record nothing". Update the function's doc comment with one sentence about this.
   - Directly after the `writeTaskFile` error check (before `retireSupersededBuildFixTasks`), call a new unexported helper `recordCreatedTaskPath(ctx, resolver, cmd.TaskIdentifier, relPath)`.
   - Implement the helper in the same file:
     ```go
     // recordCreatedTaskPath adds the file just written to the identifier→path index, so
     // the results and frontmatter updates that follow a create resolve with one read
     // instead of a full-vault walk. A trigger-only controller builds its index once at
     // pod start, so without this every task created afterwards missed for the pod's
     // whole life (2026-10-25: 155 of 159 walked identifiers, consumer 60 min behind,
     // commands dropped as expired). It records only when the resolver reports a clean
     // miss: a hit means the index already knows a file for this identifier, and a
     // resolver error means a known duplicate — neither may be overwritten.
     func recordCreatedTaskPath(
     	ctx context.Context,
     	resolver result.TaskPathResolver,
     	id lib.TaskIdentifier,
     	relPath string,
     ) {
     	if resolver == nil {
     		return
     	}
     	if _, found, err := resolver.Resolve(ctx, id); err != nil || found {
     		return
     	}
     	resolver.Record(ctx, id, relPath)
     }
     ```
   - Do not record on any error path (`ErrTaskAlreadyExists`, the skipped vault mismatch, validation failures, write failures).

5. In `pkg/factory/factory.go`, pass `resolver` as the new last argument: `command.NewCreateTaskExecutor(gitClient, taskDir, vaultName, currentDateTime, k, resolver)`.

6. Run `ROOTDIR=/workspace make generate` (or `go generate -mod=mod ./...`) so `mocks/task_path_resolver.go` and `mocks/vault_scanner.go` gain `Record`. Do not hand-edit mocks.

7. Update existing tests to compile: every `command.NewCreateTaskExecutor(` call in `pkg/command/task_create_task_executor_test.go` gains a final `nil` argument, except where requirement 8c needs a fake. Fix any other compile break the interface change causes (e.g. a hand-written `TaskPathResolver` test double — add a no-op `Record`).

8. Add tests:
   a. `pkg/scanner/vault_scanner_index_internal_test.go`, inside `Describe("vaultScanner identifier index", ...)`:
      - `It("resolves an identifier recorded after the index was built", ...)`: run `cycle()` on an empty dir (or one file), call `s.Record(ctx, lib.TaskIdentifier(betaID), "beta.md")`, then `s.Resolve` returns `("beta.md", true, nil)`.
      - `It("ignores an empty identifier or path", ...)`: `s.Record(ctx, "", "x.md")` and `s.Record(ctx, lib.TaskIdentifier(betaID), "")`, then `Resolve(betaID)` is a miss with nil error.
      - `It("does not hide a known duplicate", ...)`: do NOT build the duplicate through `cycle()` — with `autoInject=true` the scan repairs the second file's identifier, so the index never holds two paths. Seed it the way the existing "preserves duplicate identifiers" spec does: `s.hashes = map[string]fileEntry{"first.md": {taskIdentifier: lib.TaskIdentifier(dupID)}, "second.md": {taskIdentifier: lib.TaskIdentifier(dupID)}}`, `s.publishIndex()`, then `s.Record(ctx, lib.TaskIdentifier(dupID), "first.md")`; `Resolve(dupID)` still returns `found == false` and an error containing `"duplicate task_identifier " + dupID`.
      - `It("replaces a single stale entry", ...)`: `cycle()` with `alpha.md` carrying `alphaID`, `s.Record(ctx, lib.TaskIdentifier(alphaID), "renamed.md")`, then `Resolve` returns `"renamed.md"`.
      - Inside the existing `Context("trigger-only mode (poll interval zero)", ...)`: `It("resolves an identifier recorded after a trigger-only boot build", ...)`: `built := triggerOnly()`, `built.buildIndex(ctx)`, `built.Record(ctx, lib.TaskIdentifier(betaID), "beta.md")`, then `built.Resolve` returns `("beta.md", true, nil)`. Follow the surrounding specs in that Context for how files are written before `buildIndex`.
      - In the existing race spec `It("stays clean under the race detector when scanning and resolving concurrently", ...)`, add `s.Record(ctx, lib.TaskIdentifier(betaID), "beta.md")` inside the main-goroutine resolving loop, before the `s.Resolve` call. Keep every existing assertion about `alphaID` unchanged; do not assert on `betaID` (a concurrent cycle may drop the recorded entry, by design).
   b. `pkg/result/result_writer_index_test.go` (or `find_task_file_path_test.go`, whichever already has the fixtures for a walk with a `mocks.TaskPathResolver`):
      - an index miss followed by a walk that matches exactly one file calls `resolver.Record` once, with `RecordArgsForCall(0)` returning that id and the matched relative path;
      - an index hit does not call `Record` (`RecordCallCount()` is 0);
      - a walk that matches nothing does not call `Record`;
      - a walk that finds a duplicate returns the error and does not call `Record`;
      - an index hit whose file no longer carries the identifier (as in the existing `"falls back to the walk when the indexed path no longer carries the identifier"` spec) calls `Record` once with the walked path.
   c. `pkg/command/task_create_task_executor_test.go`, a new `Context("index recording", ...)` using `fakeResolver := &mocks.TaskPathResolver{}` passed to `NewCreateTaskExecutor`:
      - a successful first-ever create with `fakeResolver.ResolveReturns("", false, nil)` calls `Record` once with the command's task identifier and the same vault-relative path the file was written to (`tasks/<Title>.md` relative form, matching what `writeTaskFile` received as `relPath`);
      - when `fakeResolver.ResolveReturns("tasks/other.md", true, nil)`, a successful create does not call `Record`;
      - when `fakeResolver.ResolveReturns("", false, errors.New("duplicate"))`, a successful create does not call `Record`;
      - a create that returns `ErrTaskAlreadyExists` (title path occupied by a file with the same `task_identifier`, as existing tests in this file set up) does not call `Record` and does not call `Resolve`.

9. In `docs/controller-design.md`, in the `**Index bootstrap in trigger-only mode.**` paragraph:
   - Replace the clause `and a file created after the pass still misses and falls back to the walk, which stays the recovery` with `and a file created after the pass misses once, falls back to the walk, and is then recorded`.
   - At the end of the paragraph, add this text verbatim:
     ```
     Between rebuilds the index is kept current by `Record`: the create-task executor records the path it just wrote (only when the index reports a clean miss for that identifier), and `FindTaskFilePath` records the single file a walk found, so a task this controller creates costs no walk and any other file created after pod start costs at most one — measured 2026-10-25 on openclaw-0 at 155 of 159 walked identifiers created after start, each walked ~14 times, the consumer 60 minutes behind and dropping commands as expired. `Record` never replaces an entry holding two or more paths, so a known duplicate stays an error; a `Record` that lands just before a rebuild swap is lost with the old map and costs at most one later walk.
     ```

10. In `CHANGELOG.md`, insert a `## Unreleased` section directly above `## v0.18.4`, one blank line before and after the heading, containing exactly this bullet:
    ```
    - fix: **a task created after the controller started is added to the identifier→path index, so its later commands stop re-walking the whole vault.** A trigger-only controller (`POLL_INTERVAL=0s`) builds the index once at pod start and nothing added a file created afterwards, so every result and frontmatter command for such a task walked all ~17,000 files over git-rest. Measured 2026-10-25 on prod `agent-task-controller-openclaw-0` (`v0.18.3`): 155 of 159 walked identifiers were created after pod start, each walked ~14 times; walk time reached 1,600–2,000 s per hour, the single-threaded consumer fell 60 minutes behind, and from 17:28Z commands were dropped as `command expired` — a PR-review create landed an hour late and blocked a merge. `TaskPathResolver` gains `Record`; the create-task executor records the path it wrote when the index reports a clean miss, and `FindTaskFilePath` records the single file a walk matched. `Record` never replaces an entry holding two or more paths, so a known duplicate stays an error. New log line `index: recorded <path> for task <id>` at `glog.V(2)`.
    ```
    Leave `## v0.18.4` and everything below unchanged.

11. Self-check: re-run every `<verification>` command and confirm each passes, then confirm against each requirement: `Record` stores a new slice and skips entries with ≥2 paths; the walk records only on a unique match; the create executor records only after a successful write and only on a clean miss; factory passes the resolver; mocks are regenerated, not hand-edited; all new tests are present; the CHANGELOG bullet sits under `## Unreleased`.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
- `pkg/scanner` must not import `pkg/result`; `VaultScanner` keeps satisfying `result.TaskPathResolver` structurally
- Never append to or modify a slice already stored in `v.index`; store a new slice
- No change to `Resolve`'s outcomes, the walk's duplicate detection, `checkTitlePathFree`, `disambiguateCreateTaskRelPath`, the reopen path, `commandExpireDuration`, the batch size, or the expiry filter
- No new metrics, flags, env vars, or config fields; observability is the one V(2) log line
- Use the `github.com/bborbe/errors` idioms already in these files; no `fmt.Errorf`
- Do not run `go mod vendor` and never use `-mod=vendor`. This change adds no dependencies.
- This change deliberately extends the `TaskPathResolver` seam that spec 016 froze as "the single method `Resolve`" with `Record`. Do not edit spec 016; its grep-based acceptance checks still pass.
</constraints>

<verification>
Run `ROOTDIR=/workspace make precommit` — must pass.

Run `grep -q 'Record(ctx context.Context, id lib.TaskIdentifier, relPath string)' pkg/result/task_path_resolver.go` — must exit 0.

Run `grep -q 'Record(ctx context.Context, id lib.TaskIdentifier, relPath string)' pkg/scanner/vault_scanner.go` — must exit 0.

Run `grep -q 'resolves an identifier recorded after a trigger-only boot build' pkg/scanner/vault_scanner_index_internal_test.go` — must exit 0.

Run `grep -q 'func (v \*vaultScanner) Record(' pkg/scanner/vault_scanner.go` — must exit 0.

Run `grep -q 'resolver.Record(ctx, id, matchedRelPath)' pkg/result/result_writer.go` — must exit 0.

Run `grep -q 'func recordCreatedTaskPath(' pkg/command/task_create_task_executor.go` — must exit 0.

Run `grep -q 'NewCreateTaskExecutor(gitClient, taskDir, vaultName, currentDateTime, k, resolver)' pkg/factory/factory.go` — must exit 0.

Run `grep -q 'RecordStub' mocks/task_path_resolver.go && grep -q 'RecordStub' mocks/vault_scanner.go` — must exit 0.

Run `grep -q 'does not hide a known duplicate' pkg/scanner/vault_scanner_index_internal_test.go` — must exit 0.

Run `grep -q 'index recording' pkg/command/task_create_task_executor_test.go` — must exit 0.

Run `awk '/^## /{sec=$0} /is added to the identifier→path index, so its later commands stop re-walking/{print sec}' CHANGELOG.md` — must print `## Unreleased`.
</verification>
