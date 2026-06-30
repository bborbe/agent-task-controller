---
status: completed
spec: ["002"]
summary: Added auto-supersede hook to create-task executor that transitions prior-period in_progress recurring-task instances to aborted after new instance write
execution_id: agent-task-controller-auto-supersede-exec-004-spec-002-supersede-hook-and-wiring
dark-factory-version: dev
created: "2026-06-30T10:00:00Z"
queued: "2026-06-30T09:49:56Z"
started: "2026-06-30T09:50:42Z"
completed: "2026-06-30T09:55:31Z"
---

<summary>

- After the controller writes a new recurring-task instance, it now automatically transitions the prior period's instance from `in_progress` to `aborted` — closing the manual close-out tax for inbox-style recurring tasks.
- The supersede runs only for instances the recurring-task publisher created and only when the new instance is NOT flagged audit-style; audit-style instances (`check-prometheus-alerts`, `ibkr-swing-trading`) are deliberately left untouched so a missed firing stays visible.
- The prior file is stamped `status: aborted`, `phase: done`, a completion timestamp, and a `superseded_by` back-pointer to the new instance — all other frontmatter and the body are preserved.
- The behavior is safe under Kafka redelivery (a prior already aborted/completed is a no-op) and never fails the create flow: any read/parse/write problem is logged at WARNING and swallowed because the new instance is already on disk.
- Operators can confirm activity by grepping git log for `auto-supersede prior recurring task` and pod logs for `auto-supersede:`.
- A crafted title that would produce a path-separator-bearing prior path is rejected (no-op + WARNING), preventing path traversal.

</summary>

<objective>

Extend the `create-task` executor so that, immediately after a successful new-instance write, it supersedes the prior-period instance for the same Schedule: read the prior file, and if it exists and is still `in_progress` and the new instance is not audit-style and was created by the recurring-task publisher, transition it to `aborted` via a read-modify-write that preserves all other content. Wire the new dependency (clock) through the factory. The supersede must never cause the handler to return an error.

</objective>

<context>

Read `/workspace/CLAUDE.md` for project conventions.
Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-patterns.md`, `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`, `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`, and `/home/node/.claude/plugins/marketplaces/coding/docs/go-time-injection.md` (clock injection via `libtime.CurrentDateTimeGetter`, `SetNow()` in tests).
Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-factory-pattern.md` for zero-logic factory wiring.

Read `/workspace/docs/period-token-semantics.md` — the contract prompt 1 implements.

Read `/workspace/pkg/command/task_create_task_executor.go` IN FULL. The frozen insertion point is inside `NewCreateTaskExecutor`'s handler func, immediately after the successful `gitClient.AtomicWriteAndCommitPush(...)` call (currently lines 100-112) and BEFORE the final `return nil, nil, nil` (line 115). Note these existing helpers in the same file you will reuse:
- `resolveCreateTaskRelPath(ctx, taskDir, cmd)` (line 126) — already rejects titles containing `/` or `\`.
- `isNotFoundReadError(err)` (line 160) — returns true when a `gitClient.ReadFile` error embeds `returned 404`. Reuse this verbatim for the prior-file not-found check.
- `marshalFileContent(ctx, fm, body)` (in `task_increment_frontmatter_executor.go` line 145) — marshals `lib.TaskFrontmatter` + body back to file bytes.
- `parseTaskFrontmatter(frontmatterStr)` (in `task_increment_frontmatter_executor.go` line 119) — YAML-unmarshals a frontmatter string into `lib.TaskFrontmatter`.

Read `/workspace/pkg/command/task_update_frontmatter_executor.go` IN FULL — this is the canonical read-modify-write pattern. `buildUpdateModifyFn` (lines 95-125) shows the exact `AtomicReadModifyWriteAndCommitPush` modify-closure shape: extract frontmatter via `result.ExtractFrontmatter(ctx, current)`, extract body via `result.ExtractBody(ctx, current)`, parse via `parseTaskFrontmatter`, mutate the `lib.TaskFrontmatter` map, return `marshalFileContent(ctx, fm, body)`. Mirror this for the supersede write.

Read `/workspace/pkg/gitrestclient/git_rest_client.go` lines 298-345 for the `GitClient` interface. The two methods you call:
- `ReadFile(ctx context.Context, relPath string) ([]byte, error)` — relPath is repo-root-relative (e.g. `tasks/foo.md`).
- `AtomicReadModifyWriteAndCommitPush(ctx context.Context, absPath string, modify func(current []byte) ([]byte, error), message string) error` — absPath is `filepath.Join(gitClient.Path(), relPath)`.

Read `/workspace/pkg/result/result_writer.go` lines 352-390 — `ExtractFrontmatter(ctx, content) (string, error)` and `ExtractBody(ctx, content) (string, error)` (package `result`, import alias already used as `"github.com/bborbe/agent-task-controller/pkg/result"`).

Read `/workspace/pkg/factory/factory.go` IN FULL. `CreateCommandConsumer` (line 21) wires `command.NewCreateTaskExecutor(gitClient, taskDir, vaultName)` at line 35. You will add a `currentDateTime libtime.CurrentDateTimeGetter` parameter to both `NewCreateTaskExecutor` and `CreateCommandConsumer` and thread it through.

Read `/workspace/main.go` lines 110-160. `currentDateTime := libtime.NewCurrentDateTime()` is created at line 112 and already passed to `publisher.NewTaskPublisher(...)` (line 124) and `result.NewResultWriter(...)` (line 151). The `factory.CreateCommandConsumer(...)` call is at line 152 — you will pass `currentDateTime` into it.

Clock contract (verified in repo): `libtime "github.com/bborbe/time"`; the getter type is `libtime.CurrentDateTimeGetter`; usage is `currentDateTime.Now()` returning a value whose `.UTC().Format(time.RFC3339)` yields an RFC 3339 timestamp (see `result_writer.go` line 304: `r.currentDateTime.Now().UTC().Format(time.RFC3339)`). Follow this exact idiom for `completed_date` — it satisfies the spec's "full RFC 3339 timestamp" Constraint.

Read `/workspace/pkg/command/task_create_task_executor_test.go` IN FULL for the Ginkgo test harness: `package command_test`, `mocks.GitClient` with `PathReturns`, `AtomicWriteAndCommitPushStub`, `ReadFileReturns`, `ReadFileReturnsOnCall`, and the `buildCmdObj(task.CreateCommand)` helper. New tests extend this same `Describe`. NOTE: this file constructs `command.NewCreateTaskExecutor` at FIVE places — line 57 (the `BeforeEach`), and lines 352, 371, 390, 409 (per-test local executors). ALL FIVE must gain the new clock argument (Requirement 8).

Read `/workspace/mocks/git_client.go` to confirm the counterfeiter mock exposes `ReadFileReturns`, `ReadFileReturnsOnCall`, `ReadFileCallCount`, `AtomicReadModifyWriteAndCommitPushStub`, `AtomicReadModifyWriteAndCommitPushReturns`, `AtomicReadModifyWriteAndCommitPushCallCount`, and `AtomicReadModifyWriteAndCommitPushArgsForCall` — you rely on these in tests. (The mock already exists; the `GitClient` interface is unchanged by this prompt, so NO counterfeiter regen is needed.)

DEPENDENCY: prompt 1 must already have shipped `decrementRecurringTaskTitle` and the `DecrementRecurringTaskTitle` test seam in `pkg/command/`. If `grep -rn "func decrementRecurringTaskTitle" /workspace/pkg/command/` returns no match, STOP and report `status: failed` with message `"period-token decrementor not yet deployed (prompt 1)"` — do NOT re-implement it here.

</context>

<requirements>

1. **Add the clock parameter to `NewCreateTaskExecutor` in `/workspace/pkg/command/task_create_task_executor.go`.**
   - Add the import `libtime "github.com/bborbe/time"` to the import block.
   - Change the signature from:
     ```go
     func NewCreateTaskExecutor(
         gitClient gitclient.GitClient,
         taskDir string,
         vaultName string,
     ) cdb.CommandObjectExecutorTx {
     ```
     to:
     ```go
     func NewCreateTaskExecutor(
         gitClient gitclient.GitClient,
         taskDir string,
         vaultName string,
         currentDateTime libtime.CurrentDateTimeGetter,
     ) cdb.CommandObjectExecutorTx {
     ```
   - This is the ONLY new constructor parameter (spec Constraint: "no new constructor parameter beyond what is needed to inject the supersede helper... a clock for `completed_date`").

2. **Insert the supersede call at the frozen insertion point.** In the handler func, immediately after the successful `AtomicWriteAndCommitPush` block (after line 112's closing brace, after the existing `glog.V(2).Infof("create-task: created task file...")` at lines 113-114) and BEFORE `return nil, nil, nil` (line 115), add:
   ```go
   supersedePriorRecurringTask(ctx, gitClient, taskDir, currentDateTime, cmd, relPath)
   ```
   The function returns nothing (errors are swallowed internally) so the handler's existing `return nil, nil, nil` is unchanged. The handler MUST still return `nil, nil, nil` on the success path (spec DB8).

3. **Implement `supersedePriorRecurringTask` in `/workspace/pkg/command/task_create_task_executor.go`** (or a new file `/workspace/pkg/command/task_supersede.go` in `package command` if the executor file would exceed ~250 lines — your choice, same package). Signature:
   ```go
   // supersedePriorRecurringTask transitions the prior-period recurring-task instance
   // to status: aborted after a new instance is materialized. It is a best-effort
   // hook: every failure path (ineligible, no prior, read/parse/write error) is logged
   // and swallowed so the already-written new instance is never rolled back.
   // newRelPath is the repo-root-relative path of the just-written new instance
   // (used as the superseded_by back-pointer).
   func supersedePriorRecurringTask(
       ctx context.Context,
       gitClient gitclient.GitClient,
       taskDir string,
       currentDateTime libtime.CurrentDateTimeGetter,
       cmd task.CreateCommand,
       newRelPath string,
   )
   ```
   Body logic, in order:

   a. **Eligibility gate (spec DB1).** Read `created_by` and `audit_style` from `cmd.Frontmatter` (type `lib.TaskFrontmatter`, a `map[string]any`):
      - `createdBy, _ := cmd.Frontmatter.String("created_by")` — if `createdBy != "recurring-task-creator"`, return (no-op, no log needed beyond V(3)).
      - For `audit_style`: read it tolerantly. The project's idiom for a bool frontmatter field is a dedicated accessor that does `v, _ := f["<key>"].(bool)` (see `TaskFrontmatter.SpawnNotification()` at `agent_task-frontmatter.go:114-117`). `audit_style` has no dedicated accessor yet, so mirror that idiom inline: `if b, _ := cmd.Frontmatter["audit_style"].(bool); b { return }`. Also tolerate the string `"true"` (YAML may decode bools as strings): `if s, _ := cmd.Frontmatter["audit_style"].(string); s == "true" { return }`. Absent or any other value → treat as `false` (eligible). Spec Constraint: "absence is treated as `false`".

   b. **Compute prior title.** Use `cmd.Title` directly as the new instance's title (the `task.CreateCommand` carries `Title`; confirmed by `resolveCreateTaskRelPath` line 143 which references `cmd.Title`). Call:
      ```go
      priorTitle, err := decrementRecurringTaskTitle(ctx, cmd.Title)
      if err != nil {
          glog.Warningf("auto-supersede: cannot decrement title %q for %s: %v", cmd.Title, cmd.TaskIdentifier, err)
          return
      }
      ```
      Note: the WARNING for an unrecognized period-token shape (spec Failure Mode "Period-token shape unrecognized") is emitted here.

   c. **Path-traversal defense (spec Security).** Reject a computed prior title that contains a path separator:
      ```go
      if strings.ContainsAny(priorTitle, "/\\") {
          glog.Warningf("auto-supersede: computed prior title %q contains path separator; skipping for %s", priorTitle, cmd.TaskIdentifier)
          return
      }
      ```
      Then build the prior relative path: `priorRelPath := filepath.Join(taskDir, priorTitle+".md")`.

   d. **Read the prior file (spec DB3).**
      ```go
      content, err := gitClient.ReadFile(ctx, priorRelPath)
      if err != nil {
          if isNotFoundReadError(err) {
              glog.V(3).Infof("auto-supersede: no prior instance at %s for %s (first-ever instance)", priorRelPath, cmd.TaskIdentifier)
              return
          }
          glog.Warningf("auto-supersede: read prior %s failed for %s: %v", priorRelPath, cmd.TaskIdentifier, err)
          return
      }
      ```
      (Reuse the existing `isNotFoundReadError` helper. Transient read error → WARNING + swallow, per spec Failure Mode.)

   e. **Parse prior frontmatter and check status (spec DB4).**
      ```go
      frontmatterStr, err := result.ExtractFrontmatter(ctx, content)
      if err != nil {
          glog.Warningf("auto-supersede: extract frontmatter from prior %s failed for %s: %v", priorRelPath, cmd.TaskIdentifier, err)
          return
      }
      priorFm, err := parseTaskFrontmatter(frontmatterStr)
      if err != nil {
          glog.Warningf("auto-supersede: parse prior frontmatter %s failed for %s: %v", priorRelPath, cmd.TaskIdentifier, err)
          return
      }
      status, _ := priorFm.String("status")
      if status != "in_progress" {
          glog.V(3).Infof("auto-supersede: prior %s status=%q (not in_progress), no-op for %s", priorRelPath, status, cmd.TaskIdentifier)
          return
      }
      ```
      (`status != "in_progress"` covers already-aborted/completed/done AND the Kafka-redelivery second pass — idempotent no-op.)

   f. **Transition the prior file (spec DB5, DB6).** Build the abs path and call `AtomicReadModifyWriteAndCommitPush` with a modify closure that mirrors `buildUpdateModifyFn` in `task_update_frontmatter_executor.go`:
      ```go
      ts := currentDateTime.Now().UTC().Format(time.RFC3339)
      priorAbsPath := filepath.Join(gitClient.Path(), priorRelPath)
      modify := func(current []byte) ([]byte, error) {
          fmStr, err := result.ExtractFrontmatter(ctx, current)
          if err != nil {
              return nil, errors.Wrapf(ctx, err, "extract frontmatter")
          }
          body, err := result.ExtractBody(ctx, current)
          if err != nil {
              return nil, errors.Wrapf(ctx, err, "extract body")
          }
          fm, err := parseTaskFrontmatter(fmStr)
          if err != nil {
              return nil, errors.Wrapf(ctx, err, "parse frontmatter")
          }
          fm["status"] = "aborted"
          fm["phase"] = "done"
          fm["completed_date"] = ts
          fm["superseded_by"] = newRelPath
          fm["created_by"] = "recurring-task-creator"
          return marshalFileContent(ctx, fm, body)
      }
      msg := "[agent-task-controller] auto-supersede prior recurring task " + priorTitle
      if err := gitClient.AtomicReadModifyWriteAndCommitPush(ctx, priorAbsPath, modify, msg); err != nil {
          glog.Warningf("auto-supersede: write prior %s failed for %s: %v", priorRelPath, cmd.TaskIdentifier, err)
          return
      }
      glog.Infof("auto-supersede: %s -> %s (prior superseded by new instance)", priorRelPath, newRelPath)
      ```
      - The commit message MUST contain the frozen substring `auto-supersede prior recurring task` (spec Constraint + AC). The INFO log MUST contain the frozen substring `auto-supersede:` (spec DB7).
      - The five frozen frontmatter field names — `status`, `phase`, `completed_date`, `superseded_by`, `created_by` — must be exactly these strings (spec Constraint).
      - `superseded_by` value is `newRelPath` (the new instance's repo-root-relative path), matching the AC `<taskDir>/<new title>.md`.

   g. **Note on `newRelPath`.** The handler passes `relPath` (the resolved write path) as `newRelPath`. This is `<taskDir>/<title>.md` for the title-path case, satisfying the `superseded_by` AC.

4. **Add the required imports** to whichever file holds `supersedePriorRecurringTask`: `"path/filepath"`, `"strings"`, `"time"`, `libtime "github.com/bborbe/time"`, `"github.com/bborbe/errors"`, `"github.com/golang/glog"`, `result "github.com/bborbe/agent-task-controller/pkg/result"`, `task "github.com/bborbe/agent/command/task"`. Several are already imported in `task_create_task_executor.go` — reuse, do not duplicate. If you create `task_supersede.go`, add only the imports that file actually uses.

5. **Wire the clock through the factory.** In `/workspace/pkg/factory/factory.go`:
   - Add import `libtime "github.com/bborbe/time"`.
   - Add a `currentDateTime libtime.CurrentDateTimeGetter` parameter to `CreateCommandConsumer` (append it after `vaultName string`).
   - Change the `command.NewCreateTaskExecutor(gitClient, taskDir, vaultName)` call at line 35 to `command.NewCreateTaskExecutor(gitClient, taskDir, vaultName, currentDateTime)`.

6. **Update the `CreateCommandConsumer` call site in `/workspace/main.go`** (line 152). The local `currentDateTime` already exists at line 112. Append `currentDateTime` as the new trailing argument to the `factory.CreateCommandConsumer(...)` call. Read lines 152-162 to see the exact argument list and insert `currentDateTime` as the final argument, preserving the others in order.

7. **Sibling entry-point check.** Run `grep -rn "factory.CreateCommandConsumer\|command.NewCreateTaskExecutor" /workspace/ --include="*.go"` to confirm ALL call sites are updated. As verified at prompt-authoring time the complete set is: production wiring in `main.go` (line 152, one `CreateCommandConsumer` caller), the factory itself, and FIVE `NewCreateTaskExecutor` constructions in `pkg/command/task_create_task_executor_test.go` (lines 57, 352, 371, 390, 409). There is a single `main.go` (no `cmd/` variant). If `grep` reveals any NEW call site not in this list, update it too OR document in `## Improvements` why it is out of scope.

8. **Update ALL FIVE existing `NewCreateTaskExecutor` test constructions** in `/workspace/pkg/command/task_create_task_executor_test.go`. Run `grep -n "command.NewCreateTaskExecutor" /workspace/pkg/command/task_create_task_executor_test.go` to find every site; as of authoring they are at lines 57 (the `BeforeEach`), 352, 371, 390, 409. Each must gain the clock as the new 4th argument.
   - Import `libtime "github.com/bborbe/time"` in the test file.
   - In `BeforeEach`, create a clock and store it in a suite-level `var`. Use the repo's time-injection test pattern (read `go-time-injection.md`): check `grep -rn "SetNow\|NewCurrentDateTimeGetter\|ConstantCurrentDateTime\|CurrentDateTimeGetter\|libtime\." /workspace/pkg/ /workspace/mocks/` to find the existing settable-clock helper the codebase uses. If a settable getter exists (a counterfeiter mock or a getter with a `SetNow`/`Set` method), use it and set a fixed instant so the happy-path test can assert an exact RFC 3339 `completed_date`. If none exists, use `libtime.NewCurrentDateTime()` and assert `completed_date` is a non-empty string that parses via `time.Parse(time.RFC3339, ...)` without error (spec AC: "non-empty RFC 3339 timestamp").
   - The `BeforeEach` line 57 becomes `command.NewCreateTaskExecutor(fakeGit, taskDir, "openclaw", clock)`.
   - The four per-test constructions (352, 371, 390, 409) gain the same `clock` as the 4th arg; preserve their existing `vaultName` strings ("personal" / "openclaw").
   - The existing tests (malformed payload, empty TaskIdentifier, missing assignee/status, title collision, the four vault-routing tests) must still pass — they do not set `created_by`/`audit_style`, so the supersede hook is a no-op for them (or, for the collision case, never reached because the path returns `ErrTaskAlreadyExists` before the write).

9. **Add the supersede unit tests** to `/workspace/pkg/command/task_create_task_executor_test.go` (new `Context` blocks inside the existing `Describe("NewCreateTaskExecutor")`). The mock `fakeGit.ReadFileReturns` default is a 404 (see `BeforeEach`); override per-test with `ReadFileReturnsOnCall` to distinguish the title-collision read (call 0) from the prior-file read (call 1). The happy path issues: (read 0 = title-collision check → 404 free) then (write new via `AtomicWriteAndCommitPushStub`) then (read 1 = prior-file read). Cover these ACs:

   - **AC supersede happy path:** new instance frontmatter `created_by: recurring-task-creator`, no `audit_style` (or `false`), prior file read (call 1) returns content with `status: in_progress`. Assert `AtomicReadModifyWriteAndCommitPushCallCount() == 1`, capture args via `AtomicReadModifyWriteAndCommitPushArgsForCall(0)`, run the captured `modify` closure against the prior content, and assert the resulting bytes parse to frontmatter with `status: aborted`, `phase: done`, a non-empty RFC 3339 `completed_date`, `superseded_by == <taskDir>/<new title>.md`, `created_by: recurring-task-creator`. Also assert the captured commit message (3rd captured arg) contains `auto-supersede prior recurring task`.
   - **AC audit_style true:** new instance frontmatter `created_by: recurring-task-creator`, `audit_style: true`. Assert the prior-file `ReadFile` is never issued (only the title-collision `ReadFile` at call 0 occurs → `ReadFileCallCount() == 1`) and `AtomicReadModifyWriteAndCommitPushCallCount() == 0`.
   - **AC not created by publisher:** `created_by` absent (or `created_by: someone-else`). Assert `ReadFileCallCount() == 1` (only the collision check) and `AtomicReadModifyWriteAndCommitPushCallCount() == 0`.
   - **AC prior not found:** eligible new instance; prior-file read (call 1) returns a 404 error (`errors.New("GET ... returned 404: not found")`). Assert `AtomicReadModifyWriteAndCommitPushCallCount() == 0`.
   - **AC prior status completed:** eligible; prior read returns `status: completed`. Assert no write.
   - **AC prior status aborted (redelivery 2nd pass):** eligible; prior read returns `status: aborted`. Assert no write.
   - **AC prior read non-404 error → swallowed:** eligible; prior read (call 1) returns `errors.New("GET ... returned 500: server error")`. Assert `AtomicReadModifyWriteAndCommitPushCallCount() == 0` AND the handler return value is `(nil, nil, nil)` (no error returned to the caller).
   - **AC unrecognized title → no-op:** eligible new instance whose `Title` has no recognizable period token (e.g. `My Random Task`). Assert no prior `ReadFile` beyond the collision check (`ReadFileCallCount() == 1`) and `AtomicReadModifyWriteAndCommitPushCallCount() == 0`. This covers the decrement-error branch in step (b).
   - **AC supersede write failure swallowed:** eligible; prior read returns `status: in_progress`; set `fakeGit.AtomicReadModifyWriteAndCommitPushReturns(errors.New("git-rest 503"))`. Assert the handler return value is `(nil, nil, nil)` (the write error does NOT propagate).
   - **AC superseded_by value:** assert the captured modify output's `superseded_by` equals the new instance relative path `<taskDir>/<new title>.md`.

   The `cmd.Title` for happy-path tests should be a real recurring title with a period token, e.g. `Aquascape PWC - 2026W27`, so the decremented prior path is `<taskDir>/Aquascape PWC - 2026W26.md`. Build the command via `buildCmdObj(task.CreateCommand{...})` setting `Title`, `TaskIdentifier`, and `Frontmatter` (include `assignee`, `status`, `created_by`, optionally `audit_style`). For the prior-file read, return raw markdown bytes:
   ```
   ---
   task_identifier: prior-id
   assignee: claude
   status: in_progress
   ---
   body text
   ```

10. **Path-separator guard test (spec AC).** The decrementor in prompt 1 only emits canonical tokens (no separators), so the `strings.ContainsAny(priorTitle, "/\\")` guard cannot be reached through `decrementRecurringTaskTitle` with a normal title. Cover the guard at the unit level by adding a focused test on the decrementor's slug behavior: a `cmd.Title` whose SLUG portion already contains a separator, e.g. `Reports/Weekly - 2026W27`. With this title the slug is `Reports/Weekly`, the decremented prior title is `Reports/Weekly - 2026W26`, which contains `/` → the guard fires, no write. Assert `AtomicReadModifyWriteAndCommitPushCallCount() == 0` and `ReadFileCallCount() == 1` (no prior read). NOTE: such a title would also have been rejected by `resolveCreateTaskRelPath` for the NEW write (it contains `/`), so in production this case falls back to the UUID path and the supersede still no-ops safely; the test exercises the guard in isolation. Document this reasoning in a test comment.

11. **Frozen-substring grep AC.** After implementation, `grep -n 'auto-supersede prior recurring task' /workspace/pkg/command/task_create_task_executor.go` (or `task_supersede.go` if you split it) must return ≥1 line. If you split the helper into `task_supersede.go`, the frozen commit-message substring lives there — update the AC grep target accordingly and note it in `## Improvements`.

</requirements>

<constraints>

- Frozen insertion point: inside `NewCreateTaskExecutor`'s handler func, immediately after the successful `AtomicWriteAndCommitPush` and before `return nil, nil, nil`.
- Frozen git-rest client reuse: use the same `gitClient` already passed to `NewCreateTaskExecutor`. No new client, no new env var. The ONLY new constructor parameter is the clock (`libtime.CurrentDateTimeGetter`).
- Frozen frontmatter field names on the prior transition: `status`, `phase`, `completed_date`, `superseded_by`, `created_by` — do not rename.
- Frozen log substring `auto-supersede:` and frozen commit-message substring `auto-supersede prior recurring task`.
- The new instance's `audit_style` read is the sole opt-out; absence is treated as `false`. Spec Non-goal (verbatim): *"Do NOT add a per-feature opt-out flag, config field, or tunable threshold on the controller side."* Do NOT add any controller-side config knob.
- The hook NEVER causes the handler to return an error — every supersede failure (decrement error, separator, read error, parse error, write error) is logged at WARNING (or V(3) for benign not-found / wrong-status) and swallowed. Handler success path stays `return nil, nil, nil`.
- The hook runs ONLY after a successful new-instance write (it is called after the `AtomicWriteAndCommitPush` block, so a failed new-instance write returns early before reaching it).
- One read, one write, no retry loop — the hook inherits the handler's context deadline (spec Security: "must not add its own retry loop").
- `completed_date` uses `currentDateTime.Now().UTC().Format(time.RFC3339)` (matching `result_writer.go`).
- Do NOT add a `CHANGELOG.md` entry. Spec Non-goal (verbatim): *"Do NOT add a `CHANGELOG.md` entry — this repo has no `CHANGELOG.md`."*
- Do NOT regenerate counterfeiter mocks — the `GitClient` interface is unchanged.
- Do NOT add a new E2E scenario — spec states the behavior is fully reachable via unit tests with mock `GitClient`.
- Existing tests in `/workspace/pkg/command/` and `/workspace/pkg/gitrestclient/` must still pass.
- Per `go-error-wrapping-guide.md`: `errors.Wrapf(ctx, ...)` / `errors.Errorf(ctx, ...)` only — never `fmt.Errorf`, never `context.Background()` in `pkg/`.
- Per `go-precommit.md`: funlen 80 / nestif 4 / golines 100. If `supersedePriorRecurringTask` exceeds 80 lines, extract the modify-closure builder into a helper (e.g. `buildSupersedeModifyFn`).
- Do NOT commit — dark-factory handles git.

</constraints>

<verification>

Run iteratively while implementing:

```
cd /workspace && go build ./...
cd /workspace && go test ./pkg/command/... -v
cd /workspace && go test ./pkg/factory/... -v
```

Frozen-substring check (spec AC):

```
cd /workspace && grep -rn 'auto-supersede prior recurring task' pkg/command/
```
Expect ≥1 line.

Run ONCE at the end:

```
cd /workspace && make precommit
```

Expected: exit 0; all new supersede Context specs pass (happy path, audit_style true, not-created-by-publisher, prior-not-found, prior-completed, prior-aborted, non-404-read-error swallowed, write-failure swallowed, unrecognized-title no-op, path-separator guard, superseded_by value); existing create-task specs still pass; handler returns `(nil, nil, nil)` on every supersede failure path.

</verification>
