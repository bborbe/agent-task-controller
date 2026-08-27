---
status: cancelled
spec: [005-bug-create-task-dedup-blocks-terminal-reopen]
execution_id: agent-task-controller-dedup-terminal-exec-010-spec-005-terminal-reopen-decision
dark-factory-version: dev
created: "2026-08-27T20:30:00Z"
queued: "2026-08-27T18:44:32Z"
started: "2026-08-27T18:44:34Z"
branch: dark-factory/bug-create-task-dedup-blocks-terminal-reopen
cancelled: "2026-08-27T18:53:12Z"
---

# Terminal-status dedup decision in create-task

<summary>

- The create-task dedup guard now decides on the existing task's `status` instead of on mere file existence: a title path holding a task whose status is exactly `completed` or exactly `aborted` is treated as free, and a fresh non-terminal task is materialized at that path.
- Every non-terminal status — including the legacy alias `todo` — still holds the title path and suppresses the create with `ErrTaskAlreadyExists`, exactly as today.
- Fail-closed is preserved: an existing file whose status the executor cannot positively read (no frontmatter delimiters, invalid YAML, absent status key, empty status, unknown status value) still holds the path and is dropped with `ErrTaskAlreadyExists` plus a WARNING log naming the path.
- The status decision consumes the bytes already read by the collision check, so the create path still performs exactly one `ReadFile` — no second git-rest round trip.
- The reopen signal (and the prior status) is threaded out of the collision check into the write path so a follow-up prompt can make reopens greppable in logs and git history.
- A full regression table locks the behavior: both terminal statuses, four non-terminal statuses, five unreadable/ambiguous cases, byte-identical content with a first-ever create, and reopen-then-replay idempotency.
- The two pre-existing collision specs keep their assertions unmodified.

</summary>

<objective>

Fix the defect where `create-task` treats a title path occupied by a terminal task (`completed` / `aborted`) as permanently retired: change the dedup rule from "the path is occupied" to "the path is occupied by a live task", so a create command whose title path holds a terminal task materializes a fresh non-terminal task at that path, while every non-terminal or unreadable existing file is still dropped with `ErrTaskAlreadyExists`. The decision must reuse the bytes from the single collision read, and the reopen fact must be plumbed out to the write path for the observability that lands in the follow-up prompt.

</objective>

<context>

There is no CLAUDE.md in this repo; the global YOLO container CLAUDE.md (already in your context) governs project conventions. Read the repo's own code as the source of truth for style.

Read the coding-plugin docs that apply to this change:
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-patterns.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` (`errors.Wrapf(ctx, err, ...)` only, never `fmt.Errorf`, never `context.Background()` in `pkg/`)
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` (Ginkgo/Gomega, counterfeiter mocks, ≥80% coverage for changed code)
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-glog-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/definition-of-done.md`

Read `pkg/command/task_create_task_executor.go` IN FULL. The frozen change site is `checkTitlePathFree` (currently lines 95-123). The handler's call sequence in `NewCreateTaskExecutor` (validate → route → validate frontmatter → resolve path → collision check → write → supersede hook) MUST NOT change order. Note the existing helpers in this file you will reuse:
- `isNotFoundReadError(err error) bool` (line 192) — true when a `gitClient.ReadFile` error embeds `returned 404`. A nil error is NOT a not-found error.
- `writeTaskFile(ctx, gitClient, relPath, cmd, vaultName) error` (line 128) — builds content via `buildCreateTaskContent(ctx, cmd, vaultName)` and writes it atomically. This is the function you will extend with the reopen signal.
- Imports already present and used in this file: `strings`, `errors "github.com/bborbe/errors"`, `glog "github.com/golang/glog"`, `result "github.com/bborbe/agent-task-controller/pkg/result"`, `task "github.com/bborbe/agent/command/task"`.

Read `pkg/command/task_increment_frontmatter_executor.go` lines 126-158 for the in-package helpers you will reuse verbatim (they are the pattern `buildSupersedeModifyFn` in `task_create_task_executor.go` already uses):
```go
func parseTaskFrontmatter(frontmatterStr string) (lib.TaskFrontmatter, error)
func marshalFileContent(ctx context.Context, fm lib.TaskFrontmatter, body string) ([]byte, error)
```

Read `pkg/result/result_writer.go` lines 366-385 for the frontmatter extractor (package `result`, import alias `result "github.com/bborbe/agent-task-controller/pkg/result"`):
```go
func ExtractFrontmatter(ctx context.Context, content []byte) (string, error)
```
It returns an error when the content has no opening `---` delimiter or no closing `\n---`.

Verified library types (module `github.com/bborbe/agent v0.83.0`, at `$GOPATH/pkg/mod/github.com/bborbe/agent@v0.83.0/`):
```go
// agent_task-frontmatter.go:16 — a generic map, YAML-serializable in vault files
type TaskFrontmatter map[string]interface{}
// agent_task-frontmatter.go:144 — returns ("", false) when the key is absent or non-string
func (f TaskFrontmatter) String(key string) (string, bool)
```
```go
// command/task/errors.go:18
var ErrTaskAlreadyExists = stderrors.New("task file already exists at title path")
```
The sentinel is imported in `task_create_task_executor.go` via `task "github.com/bborbe/agent/command/task"` and compared with `errors.Is(err, task.ErrTaskAlreadyExists)`.

Read `pkg/command/task_create_task_executor_test.go` IN FULL for the Ginkgo harness: `package command_test`, `mocks.GitClient` with `PathReturns`, `AtomicWriteAndCommitPushStub` (writes the file to a temp dir), `ReadFileReturns`, `ReadFileReturnsOnCall`, `ReadFileCallCount`, `AtomicWriteAndCommitPushCallCount`, `AtomicWriteAndCommitPushArgsForCall`, and the `buildCmdObj(task.CreateCommand)` helper. The `BeforeEach` default is `fakeGit.ReadFileReturns(nil, errors.New("GET file returned 404: not found"))` — every title path is free unless a test overrides it. The two pre-existing collision contexts you must NOT change are `Context("title path already occupied (collision)")` (~line 163, stages `status: next` on the replay read) and `Context("collision with a different task_identifier")` (~line 212, stages `status: todo`). Both remain valid under the new rule because `next` and `todo` are non-terminal — the only permitted edit to either context is the stale comment in the second one (Requirement 6).

Read `mocks/git_client.go` to confirm the counterfeiter mock method shapes you rely on: `ReadFileReturnsOnCall(i int, result1 []byte, result2 error)`, `ReadFileCallCount() int`, `AtomicWriteAndCommitPushCallCount() int`, `AtomicWriteAndCommitPushArgsForCall(i int) (context.Context, string, []byte, string)` — the 4-tuple is `(ctx, absPath, content, message)`.

Read `docs/controller-design.md` for the status/phase merge semantics background (the `status` vs `phase` distinction the terminal set relies on). You are NOT updating this doc — that is the follow-up prompt.

<!-- OPEN QUESTION for the human reviewer: per the spec's Suggested Decomposition, this prompt plumbs the reopen signal (bool + prior status) into `writeTaskFile` but does NOT yet consume it — the unconditional INFO log and distinct commit message land in the follow-up prompt (spec Desired Behavior 5). The two new `writeTaskFile` parameters are therefore declared-but-unused here. If you prefer the observability to land atomically with the decision, merge the two prompts. -->

</context>

<requirements>

1. **Change the `checkTitlePathFree` signature and body in `pkg/command/task_create_task_executor.go`.** This is the frozen change site (spec Constraint). Replace the current function:

   ```go
   // checkTitlePathFree returns ErrTaskAlreadyExists when the title path is
   // already occupied (benign Failure on the result topic — no overwrite, no
   // git write). A transient git-rest read error is propagated. A "not found"
   // read error is swallowed (title path is free).
   func checkTitlePathFree(
       ctx context.Context,
       gitClient gitclient.GitClient,
       relPath string,
       taskIdentifier lib.TaskIdentifier,
   ) error {
   ```

   with a version that returns the reopen signal and prior status:

   ```go
   // checkTitlePathFree reports whether the title path is free for a new task.
   // The slot is free when the path is unoccupied (git-rest 404) OR when the
   // existing file's frontmatter status is a terminal status ("completed" or
   // "aborted", compared case-sensitively after whitespace trim) — a terminal
   // task is no longer a live duplicate, so the slot is reusable. On a
   // terminal-status free it returns reopened=true and the prior status so the
   // caller can distinguish a reopen from a first-ever create. Every other
   // occupied state — any non-terminal status, an absent/empty/unknown status,
   // missing frontmatter delimiters, or unparseable YAML — returns
   // ErrTaskAlreadyExists (a benign Failure on the result topic — no overwrite,
   // no git write) wrapped with the "title path %s occupied" message shape. A
   // transient git-rest read error is propagated. The decision consumes the
   // bytes already read by this function; the caller must not issue a second
   // ReadFile.
   func checkTitlePathFree(
       ctx context.Context,
       gitClient gitclient.GitClient,
       relPath string,
       taskIdentifier lib.TaskIdentifier,
   ) (reopened bool, priorStatus string, err error) {
   ```

   Body logic, in order:

   a. **Read.** `existing, err := gitClient.ReadFile(ctx, relPath)`. If `err != nil`:
      - if `!isNotFoundReadError(err)` → `return false, "", errors.Wrapf(ctx, err, "check existing task file at %s for %s", relPath, taskIdentifier)` (transient error propagated, unchanged from today — spec Failure Mode row 1).
      - else (404) → `return false, "", nil` — the title path is free, first-ever create.
   b. **Extract frontmatter from the already-read bytes.** `frontmatterStr, parseErr := result.ExtractFrontmatter(ctx, existing)`. On `parseErr` (no delimiters), fail closed:
      ```go
      glog.Warningf(
          "create-task: cannot read status from existing file at %s for %s; holding title path: %v",
          relPath, taskIdentifier, parseErr,
      )
      return false, "", errors.Wrapf(ctx, task.ErrTaskAlreadyExists, "title path %s occupied", relPath)
      ```
   c. **Parse frontmatter.** `existingFm, parseErr := parseTaskFrontmatter(frontmatterStr)`. On `parseErr` (invalid YAML), fail closed with the same WARNING shape naming the path:
      ```go
      glog.Warningf(
          "create-task: cannot parse frontmatter of existing file at %s for %s; holding title path: %v",
          relPath, taskIdentifier, parseErr,
      )
      return false, "", errors.Wrapf(ctx, task.ErrTaskAlreadyExists, "title path %s occupied", relPath)
      ```
   d. **Read and normalize status.** `status, _ := existingFm.String("status")` then `status = strings.TrimSpace(status)`.
   e. **Terminal decision (spec DB2).** If `status == "completed" || status == "aborted"` → `return true, status, nil` (slot freed; `reopened=true`). The comparison is case-sensitive AFTER the whitespace trim, so `"Completed"` or `" completed"` do NOT free the slot. `strings` is already imported in this file.
   f. **Every other status holds the path.** Keep the existing occupied log line verbatim and return the frozen sentinel:
      ```go
      glog.V(2).Infof(
          "create-task: title path %s already occupied (%d bytes), returning ErrTaskAlreadyExists for %s",
          relPath,
          len(existing),
          taskIdentifier,
      )
      return false, "", errors.Wrapf(ctx, task.ErrTaskAlreadyExists, "title path %s occupied", relPath)
      ```
      This branch covers all non-terminal statuses (`next`, `in_progress`, `backlog`, `hold`, legacy `todo`), the empty string, and any unknown value (spec DB3). The sentinel is wrapped with the SAME `"title path %s occupied"` message shape as today (spec Constraint).

   The frozen sentinel message shape, the frozen terminal set (exactly `completed` and `aborted`), and the fail-closed invariant (no code path where a parse failure results in a write) are hard constraints — do not deviate.

2. **Update the handler call site in `NewCreateTaskExecutor`** (currently lines 82-85). Change:
   ```go
   relPath := resolveCreateTaskRelPath(ctx, taskDir, cmd)
   if err := checkTitlePathFree(ctx, gitClient, relPath, cmd.TaskIdentifier); err != nil {
       return nil, nil, err
   }
   ```
   to:
   ```go
   relPath := resolveCreateTaskRelPath(ctx, taskDir, cmd)
   reopened, priorStatus, err := checkTitlePathFree(ctx, gitClient, relPath, cmd.TaskIdentifier)
   if err != nil {
       return nil, nil, err
   }
   ```
   The call order (validate → route → validate frontmatter → resolve path → collision check → write → supersede hook) MUST NOT change (spec Constraint).

3. **Thread the reopen signal into the write path.** Change the `writeTaskFile` signature in `pkg/command/task_create_task_executor.go`:
   ```go
   func writeTaskFile(
       ctx context.Context,
       gitClient gitclient.GitClient,
       relPath string,
       cmd task.CreateCommand,
       vaultName string,
       reopened bool,
       priorStatus string,
   ) error {
   ```
   Update its doc comment to note the two new parameters carry the reopen signal for the write path. Update the handler's call:
   ```go
   if err := writeTaskFile(ctx, gitClient, relPath, cmd, vaultName, reopened, priorStatus); err != nil {
       return nil, nil, err
   }
   ```
   **Do NOT change `writeTaskFile`'s behavior in this prompt**: the content must still be built solely from the command via `buildCreateTaskContent(ctx, cmd, vaultName)` (spec Non-goal: "Do NOT merge, migrate, or carry forward any content from the terminal file"), the commit message must stay `"[agent-task-controller] create task "+string(cmd.TaskIdentifier)`, and the existing `glog.V(2).Infof("create-task: created task file at %s for %s", ...)` must stay. The two new parameters are declared but not yet consumed — the observability that distinguishes a reopen (unconditional INFO log + distinct commit message) is specified by the follow-up prompt in this spec (Desired Behavior 5). Go permits unused function parameters; the linter does not flag them.

4. **Update the `NewCreateTaskExecutor` doc comment** (lines 29-36). The sentence "If a file already exists at the resolved path the command returns ErrTaskAlreadyExists (a benign Failure on the result topic — no overwrite, no git write)" is now stale for terminal tasks. Rewrite it to state the new contract: a file that already exists at the resolved path causes `ErrTaskAlreadyExists` (a benign Failure — no overwrite, no git write) UNLESS the existing file's status is terminal (`completed`/`aborted`), in which case a fresh non-terminal task is materialized at that path. Keep the rest of the comment.

5. **No new imports.** `strings`, `errors`, `glog`, `result`, `task` are all already imported in `pkg/command/task_create_task_executor.go`. If any import becomes unused after your edit, remove it — but `strings` must remain (used by `TrimSpace`, `ContainsAny`, `Contains`, `TrimSuffix`).

6. **Update the stale comment in the "collision with a different task_identifier" context** in `pkg/command/task_create_task_executor_test.go` (~lines 214-215). The comment currently reads:
   ```go
   // Existing file at the title path belongs to a DIFFERENT task — filename owns the
   // slot; the executor must not consult frontmatter, must not write.
   ```
   This is stale: the executor DOES consult frontmatter. Replace the two comment lines with a comment stating the slot is held because the existing file's `status: todo` is non-terminal (e.g. `// Existing file at the title path belongs to a DIFFERENT task and its status "todo" is non-terminal — filename owns the slot, no overwrite.`). **Comment-only edit**: do NOT touch any `Expect(...)` line or any other statement inside this context or inside `Context("title path already occupied (collision)")`. The AC evidence for "pre-existing collision specs unmodified" is that no `Expect(` line inside either context changes.

7. **Add the regression tests** to `pkg/command/task_create_task_executor_test.go` as new `Context` blocks inside the existing `Describe("NewCreateTaskExecutor")`. All existing imports (`errors`, `strings`, `task`, `lib`) are already present. The `BeforeEach` default 404 read makes first-ever creates free; override per test with `ReadFileReturns`/`ReadFileReturnsOnCall`. For every test, `cmdObj := buildCmdObj(task.CreateCommand{...})` with `TaskIdentifier`, a `Title`, and `Frontmatter` (include `assignee: claude`, `status: next`). The reopen tests must use `ReadFileReturns` (not `ReadFileReturnsOnCall`) so the single-read guarantee is observable. Cover, at minimum:

   - **completed → reopen:** `fakeGit.ReadFileReturns([]byte("---\ntask_identifier: old-id\nassignee: alice\nstatus: completed\nphase: done\ncompleted_date: 2026-06-01T10:00:00Z\n---\npremature triage verdict for this alert\n"), nil)`. Handle once. Assert: `err` is nil (handler returns `(nil, nil, nil)`); `fakeGit.AtomicWriteAndCommitPushCallCount() == 1`; `fakeGit.ReadFileCallCount() == 1` (single-ReadFile lock — spec AC). Capture `_, _, content, _ := fakeGit.AtomicWriteAndCommitPushArgsForCall(0)` and assert `string(content)` contains `status: next` (from the command) and does NOT contain `completed_date`, does NOT contain `phase: done`, and does NOT contain `premature triage verdict` (the terminal file's body marker) — spec AC "content equals first-ever create".
   - **aborted → reopen:** same shape with `status: aborted`. Assert `(nil, nil, nil)` and `AtomicWriteAndCommitPushCallCount() == 1`.
   - **non-terminal table (spec AC):** `DescribeTable` over existing-status values `[]string{"next", "in_progress", "backlog", "hold"}` (use the `DescribeTable`/`Entry` style already used in `pkg/command/period_token_ranking_test.go`). For each, stage `fakeGit.ReadFileReturns([]byte("---\ntask_identifier: x\nassignee: alice\nstatus: "+status+"\n---\nbody\n"), nil)`, handle once, assert `errors.Is(err, task.ErrTaskAlreadyExists)` is true and `fakeGit.AtomicWriteAndCommitPushCallCount() == 0`. Exactly four rows (`todo` is already covered by the pre-existing "collision with a different task_identifier" context).
   - **ambiguous/unreadable table (spec AC):** `DescribeTable` over five entries, each asserting `errors.Is(err, task.ErrTaskAlreadyExists)` and `AtomicWriteAndCommitPushCallCount() == 0`:
     1. no frontmatter delimiters: `[]byte("plain text, no frontmatter\n")`
     2. syntactically invalid YAML: `[]byte("---\nstatus: [\n---\n")` (delimiters present so `ExtractFrontmatter` succeeds, `yaml.Unmarshal` fails)
     3. valid frontmatter with no `status` key: `[]byte("---\ntask_identifier: x\nassignee: alice\n---\n")`
     4. empty status: `[]byte("---\nstatus: \"\"\n---\n")`
     5. unknown status value: `[]byte("---\nstatus: some-unknown-value\n---\n")`
   - **reopen content byte-identical to first-ever create (strongest form of the content AC):** run the SAME `cmdObj` twice: first with the default 404 read (first-ever create) — capture `content0 := ...ArgsForCall(0)`. Then `fakeGit.ReadFileReturns([]byte("---\nstatus: aborted\n---\nprior verdict body\n"), nil)` and handle the same command again — capture `content1`. Assert `Expect(content1).To(Equal(content0))`. (Use a fresh `executor` or the same one — `buildCreateTaskContent` has no time/request-id dependency, so the bytes are deterministic.)
   - **reopen then replay is idempotent (spec AC):** stage `ReadFileReturnsOnCall(0, []byte(completed content), nil)` and `ReadFileReturnsOnCall(1, []byte("---\nstatus: next\n---\n"), nil)` (the second read returns the just-written non-terminal content). Handle once → assert nil error and `AtomicWriteAndCommitPushCallCount() == 1`. Handle the same command again → assert `errors.Is(err, task.ErrTaskAlreadyExists)` and `AtomicWriteAndCommitPushCallCount() == 1` (unchanged across the replay).

   Do NOT add a test that asserts the reopen INFO log line or the reopen commit message — those are the follow-up prompt's scope, and asserting them here would freeze behavior this prompt deliberately does not ship.

8. **Self-check before finishing:** re-run the `<verification>` block and confirm it passes; walk each acceptance criterion from the spec's Acceptance Criteria list against the change (terminal-completed, terminal-aborted, content-equals-first-create, non-terminal table, no-delimiters, invalid-YAML, no-status-key, empty/unknown-status, single-ReadFile, replay-idempotency, pre-existing-specs-unmodified). The spec's `make precommit` at repo root AC must hold.

</requirements>

<constraints>

- Frozen change site: `checkTitlePathFree()` in `pkg/command/task_create_task_executor.go`. The call order in `NewCreateTaskExecutor`'s handler func (validate → route → validate frontmatter → resolve path → collision check → write → supersede hook) does not change.
- Frozen sentinel: the non-terminal / unreadable path continues to return an error satisfying `errors.Is(err, task.ErrTaskAlreadyExists)`, wrapped with the same `"title path %s occupied"` message shape. Downstream result-topic consumers treat this sentinel as a benign Failure and must keep doing so.
- Frozen terminal set: exactly `completed` and `aborted`. Not `done` (that is a `phase` value, not a `status`). Comparison is case-sensitive after whitespace trim.
- Fail-closed is the invariant: any read/parse ambiguity holds the path. There is no code path where a parse failure results in a write.
- Exactly one `ReadFile` per handled create command — the status decision reuses the bytes already read by the collision check. No second git-rest round trip.
- Reuse the existing in-package helper pair `result.ExtractFrontmatter(ctx, content)` + `parseTaskFrontmatter(fmStr)`, as `buildSupersedeModifyFn()` already does. No new YAML dependency, no new parser.
- No change to `NewCreateTaskExecutor`'s signature: no new constructor parameters, no new env vars, no new command-line flags, no new config.
- `validateCreateTaskFrontmatter` still requires `status` on the incoming command; a reopen does not relax it.
- `supersedePriorRecurringTask` still runs after a successful write on the reopen path, gated by its existing opt-in `created_by` + `auto_abort_prior` check — do NOT change it.
- Do NOT touch the unrelated `assignee`-blanking-on-failure defect (in flight on `fix/assignee-preserve`).
- Do NOT add a config field, env var, CRD knob, or per-producer opt-out for terminal-reopen — invariant.
- Do NOT add a tunable or extendable terminal-status list — `completed` and `aborted` are frozen.
- Do NOT add a new frontmatter marker on the reopened task (`reopened: true`, `reopen_count`, `supersedes`) — the git commit and log line are the record (and the log line lands in the follow-up prompt).
- Do NOT merge, migrate, or carry forward any content from the terminal file (body, `trigger_count`, `assignee`, verdict sections) — the new task is built solely from the `CreateCommand`, exactly as a first-ever create is.
- Existing tests in `pkg/command/task_create_task_executor_test.go` (including the two collision contexts) and `pkg/gitrestclient/*_test.go` must still pass. The comment in the "collision with a different task_identifier" context may be updated, but that context's assertions must not be.
- Per `go-error-wrapping-guide.md`: `errors.Wrapf(ctx, ...)` / `errors.Errorf(ctx, ...)` only — never `fmt.Errorf`, never `context.Background()` in `pkg/`.
- Per `go-precommit.md`: funlen 80 / nestif 4 / golines 100 — `checkTitlePathFree` is small enough that this should not be a problem; do not inline the helper logic into the handler.
- Do NOT commit — dark-factory handles git.

</constraints>

<verification>

Run iteratively while implementing:

```
cd /workspace && make test
```

Run the new-spec regression checks (non-git filesystem checks — the container's `.git` is masked, so the AC's `git diff` evidence is confirmed operator-side at merge):

```
cd /workspace && grep -c 'Expect(errors.Is(err, task.ErrTaskAlreadyExists)).To(BeTrue())' pkg/command/task_create_task_executor_test.go
```
Expect exactly `2` (the two pre-existing collision contexts' sentinel assertions, unmodified).

```
cd /workspace && grep -n 'must not consult frontmatter' pkg/command/task_create_task_executor_test.go
```
Expect no match (the stale comment was replaced).

Run ONCE at the end:

```
cd /workspace && make precommit
```

Expected: exit 0; all new terminal-reopen Context specs pass (completed, aborted, non-terminal table of four, ambiguous/unreadable table of five, content byte-equality, replay idempotency); the two pre-existing collision contexts still pass; existing create-task and supersede specs still pass; `pkg/gitrestclient/*` tests still pass.

</verification>
