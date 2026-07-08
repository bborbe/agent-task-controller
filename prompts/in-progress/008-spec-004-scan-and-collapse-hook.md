---
status: approved
spec: [004-recurring-task-supersede-scan-collapse]
created: "2026-07-08T09:11:00Z"
queued: "2026-07-08T11:18:41Z"
branch: dark-factory/recurring-task-supersede-scan-collapse
---

<summary>

- After the controller materializes a new recurring-task instance, it now closes EVERY earlier still-open instance of the same schedule — not just the single arithmetic predecessor.
- It finds same-schedule instances by listing the task directory and filtering on the schedule's slug (the title text before the final ` - `), so it works without knowing the recurrence kind or the schedule's weekday set.
- It inspects only the most-recent K prior instances (default look-back bound), so a deep history can never drive unbounded reads or writes.
- Missed-day gaps self-heal: if Monday and Tuesday were left open and Wednesday fires, both Monday and Tuesday are closed and Wednesday stays open.
- Each closed prior gets the same frozen transition as before (`aborted` / `phase: done` / completion timestamp / back-pointer to the new instance); already-closed priors are skipped, so Kafka redelivery is a safe no-op.
- The scan is best-effort: a list/read/parse/write error on any single file is logged and swallowed, the new instance is never rolled back, and remaining candidates are still processed.
- A slug containing glob-special characters falls back to listing all task files and filtering in memory, so a crafted title can neither widen the listing nor traverse paths.

</summary>

<objective>

Replace the single-prior supersede logic in the create-task executor with a bounded scan-and-collapse: after a new recurring-task instance is written, list same-slug candidate instances, exclude the new one, rank them most-recent-first, and transition every still-`in_progress` candidate whose period-token is strictly older than the new instance's token to `aborted` — capped at the K most-recent such candidates. The scan is glob-safe, path-traversal-safe, best-effort per file, and never fails the create flow. Consumes the ranking core from prompt 1.

</objective>

<context>

Read `/workspace/CLAUDE.md` for project conventions.
Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-patterns.md`, `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`, `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`, `/home/node/.claude/plugins/marketplaces/coding/docs/go-time-injection.md`, and `/home/node/.claude/plugins/marketplaces/coding/docs/go-precommit.md`.

Read `/workspace/docs/period-token-semantics.md` for the six token shapes (this doc is rewritten in prompt 3; do not edit it here).

Read `/workspace/pkg/command/task_create_task_executor.go` IN FULL. Anchors:
- `NewCreateTaskExecutor(gitClient, taskDir, vaultName, currentDateTime)` — the handler calls `supersedePriorRecurringTask(ctx, gitClient, taskDir, currentDateTime, cmd, relPath)` after the successful write (line 80). You will add a `k int` (look-back bound) parameter — see Requirement 1.
- `supersedePriorRecurringTask(...)` (line 211) — the hook rewritten by this prompt. Prompt 1 replaced its body with a no-op stub; this prompt implements the scan.
- `isEligibleForSupersede(cmd)` (line 264) — REUSE unchanged: returns true only when `created_by == recurring-task-creator` AND `auto_abort_prior` is bool `true` or string `"true"`. This is the frozen eligibility gate (spec Constraint).
- `readPriorForSupersede(...)` (line 285) — REUSE for per-candidate read (returns `(nil, nil)` on 404, `(nil, err)` on transient error, logging internally). Rename/generalize its log messages if needed but keep the `(content, err)` contract and the `isNotFoundReadError` reuse.
- `priorIsInProgress(ctx, content, relPath, taskIdentifier)` (line 312) — REUSE per candidate: parses frontmatter, returns true only when `status == "in_progress"`.
- `transitionPrior(ctx, gitClient, currentDateTime, priorRelPath, priorTitle, newRelPath, taskIdentifier)` (line 349) — REUSE per candidate: performs the atomic read-modify-write, frozen commit message `[agent-task-controller] auto-supersede prior recurring task <title>`, frozen INFO log `auto-supersede: <relPath> -> <newRelPath> ...`.
- `buildSupersedeModifyFn(ctx, newRelPath, ts)` (line 380) — REUSE unchanged: sets the five frozen frontmatter fields `status: aborted`, `phase: done`, `completed_date`, `superseded_by`, `created_by: recurring-task-creator`.
- `isNotFoundReadError(err)` (line 181) — REUSE for 404 detection.
- `resolveCreateTaskRelPath(...)` (line 147) — shows the `taskDir` + title path convention (`filepath.Join(taskDir, title+".md")`).

Read the ranking core from prompt 1 (`/workspace/pkg/command/period_token_ranking.go`). The symbols you consume:
- `func parsePeriodTokenOrdinal(ctx context.Context, token string) (int64, error)` — parses a token to a comparable ordinal.
- `func splitTitleToken(title string) (slug string, token string, ok bool)` — splits a title on the final ` - `.
- `func rankSameSlugCandidatesDescending(ctx context.Context, titles []string) []rankedCandidate` — ranks titles most-recent-first, dropping unrecognized ones; each element is `rankedCandidate{Title string; Ordinal int64}`.
DEPENDENCY GATE: if `grep -rn "func rankSameSlugCandidatesDescending" /workspace/pkg/command/` returns no match, STOP and report `status: failed` with message `"ranking core not yet deployed (spec-004 prompt 1)"` — do NOT re-implement it here.

Read `/workspace/pkg/gitrestclient/git_rest_client.go` lines 298-342 for the `GitClient` interface. The methods this prompt uses:
- `ListFiles(ctx context.Context, glob string) ([]string, error)` — single-level glob (e.g. `tasks/*.md`), returns repo-root-relative paths.
- `ReadFile(ctx context.Context, relPath string) ([]byte, error)`.
- `AtomicReadModifyWriteAndCommitPush(ctx, absPath, modify, message)` and `Path()` — already used by `transitionPrior`.

Read `/workspace/pkg/factory/factory.go` IN FULL. `CreateCommandConsumer(...)` (line 23) calls `command.NewCreateTaskExecutor(gitClient, taskDir, vaultName, currentDateTime)` at line 40. You add the `k int` argument here — see Requirement 6. It already receives `currentDateTime`; it does NOT yet receive `k`. Prompt 3 wires the env var from `main.go` through this factory; for THIS prompt add the `k int` parameter to both `NewCreateTaskExecutor` and `CreateCommandConsumer` and have `CreateCommandConsumer` pass it through — the `main.go` call site is updated in prompt 3, so to keep the build green in THIS prompt you must ALSO update the single `main.go` call site with a literal default (see Requirement 7).

Read `/workspace/pkg/command/task_create_task_executor_test.go` IN FULL for the Ginkgo harness: `package command_test`, `mocks.GitClient` with `PathReturns`, `AtomicWriteAndCommitPushStub`, `ReadFileReturns`, `ReadFileReturnsOnCall`, `ReadFileCallCount`, and the settable clock `clock = &libtimemocks.CurrentDateTimeGetter{}` with `clock.NowReturns(libtime.DateTime(...))` (lines 61-62). The `BeforeEach` constructs `command.NewCreateTaskExecutor(fakeGit, taskDir, "openclaw", clock)` (line 64) — this gains the new `k` arg. Prompt 1 deleted the old `Context("supersede prior recurring task", ...)` block; this prompt adds a NEW scan-based supersede Context.

Read `/workspace/mocks/git_client.go` and confirm it exposes `ListFilesReturns`, `ListFilesReturnsOnCall`, `ListFilesCallCount`, `ListFilesArgsForCall`, `ReadFileReturns`, `ReadFileReturnsOnCall`, `ReadFileCallCount`, `AtomicReadModifyWriteAndCommitPushReturns`, `AtomicReadModifyWriteAndCommitPushCallCount`, and `AtomicReadModifyWriteAndCommitPushArgsForCall` (all confirmed present at prompt-authoring time). The `GitClient` interface is UNCHANGED by this prompt, so NO counterfeiter regen is needed.

</context>

<requirements>

1. **Add the look-back bound `k int` parameter to `NewCreateTaskExecutor`** in `/workspace/pkg/command/task_create_task_executor.go`. Change the signature from:
   ```go
   func NewCreateTaskExecutor(
       gitClient gitclient.GitClient,
       taskDir string,
       vaultName string,
       currentDateTime libtime.CurrentDateTimeGetter,
   ) cdb.CommandObjectExecutorTx {
   ```
   to append `k int` as the final parameter:
   ```go
   func NewCreateTaskExecutor(
       gitClient gitclient.GitClient,
       taskDir string,
       vaultName string,
       currentDateTime libtime.CurrentDateTimeGetter,
       k int,
   ) cdb.CommandObjectExecutorTx {
   ```
   Update the handler call from `supersedePriorRecurringTask(ctx, gitClient, taskDir, currentDateTime, cmd, relPath)` to pass `k`:
   `supersedePriorRecurringTask(ctx, gitClient, taskDir, currentDateTime, k, cmd, relPath)`.

2. **Rewrite `supersedePriorRecurringTask`** in `/workspace/pkg/command/task_create_task_executor.go` (remove the prompt-1 transitional stub body). New signature:
   ```go
   // supersedePriorRecurringTask collapses a recurring schedule to a single open
   // instance: after a new instance is materialized it lists same-slug candidates,
   // excludes the new instance, ranks them most-recent-first, and transitions every
   // still-in_progress candidate whose period-token is strictly older than the new
   // instance's token to aborted — capped at the k most-recent such candidates.
   // Best-effort: list/read/parse/write errors on any single file are logged and
   // swallowed; the already-written new instance is never rolled back. newRelPath is
   // the repo-root-relative path of the new instance (the superseded_by back-pointer).
   func supersedePriorRecurringTask(
       ctx context.Context,
       gitClient gitclient.GitClient,
       taskDir string,
       currentDateTime libtime.CurrentDateTimeGetter,
       k int,
       cmd task.CreateCommand,
       newRelPath string,
   )
   ```
   Body logic, in order:

   a. **Eligibility gate.** `if !isEligibleForSupersede(cmd) { return }` (reuse unchanged).

   b. **Split the new instance's title.** `slug, newToken, ok := splitTitleToken(cmd.Title)`. If `!ok`, `glog.V(3).Infof("auto-supersede: new title %q has no period-token suffix, skipping for %s", cmd.Title, cmd.TaskIdentifier)` and return.

   c. **Parse the new instance's ordinal.** `newOrdinal, err := parsePeriodTokenOrdinal(ctx, newToken)`. If `err != nil`, `glog.Warningf("auto-supersede: new token %q unrecognized for %s: %v", newToken, cmd.TaskIdentifier, err)` and return.

   d. **List same-slug candidates.** Call a new helper `listSameSlugCandidateTitles(ctx, gitClient, taskDir, slug)` (Requirement 3) → `[]string` of candidate TITLES (not paths). On its error (already logged inside), return.

   e. **Exclude the new instance and rank.** Drop `cmd.Title` from the candidate titles (`for _, t := range titles { if t == cmd.Title { continue }; kept = append(...) }`). Then `ranked := rankSameSlugCandidatesDescending(ctx, kept)`.

   f. **Filter to not-newer, cap to K, transition each.** Iterate `ranked` in order (already most-recent-first). Maintain a counter `inspected := 0`. For each `rc`:
      - If `rc.Ordinal > newOrdinal`, `continue` (skip strictly-newer candidates only; a candidate with an EQUAL ordinal is a genuine same-week weekday sibling — e.g. `-mon` when the new instance is `-fri` — and MUST be collapsed. The new instance itself is already excluded from the candidate set in step (e), so the equal-ordinal branch never matches the new instance).
      - If `inspected >= k`, `break` (look-back bound reached — older candidates left open by design).
      - `inspected++`.
      - Build `candidateRelPath := filepath.Join(taskDir, rc.Title+".md")`.
      - Path-traversal guard: `if strings.ContainsAny(rc.Title, "/\\") { glog.Warningf("auto-supersede: candidate title %q contains path separator; skipping for %s", rc.Title, cmd.TaskIdentifier); continue }`. (Belt-and-suspenders — the listing already scopes to `taskDir`, but a crafted title in the listing must never escape.)
      - Read the candidate: `content, err := readPriorForSupersede(ctx, gitClient, candidateRelPath, cmd.TaskIdentifier)`. If `err != nil || content == nil`, `continue` (read error already logged; nil = 404 not-found, benign).
      - `if !priorIsInProgress(ctx, content, candidateRelPath, cmd.TaskIdentifier) { continue }` (already-closed / non-in_progress → skip; this is the Kafka-redelivery idempotency path).
      - `transitionPrior(ctx, gitClient, currentDateTime, candidateRelPath, rc.Title, newRelPath, cmd.TaskIdentifier)` (reuse unchanged; best-effort write, logs on failure).
   - NOTE on the cap: `inspected` counts candidates INSPECTED (read attempted), not only successfully-closed ones, so a huge same-slug backlog reads at most K files (spec Constraint: "Reads bounded to at most K candidate files per materialize"). Increment `inspected` BEFORE the read (as written above) so the read-count bound holds even when candidates 404 or are already aborted.

3. **Implement `listSameSlugCandidateTitles`** in `/workspace/pkg/command/task_create_task_executor.go` (or a new file `/workspace/pkg/command/task_supersede.go` in `package command` if the executor file would exceed ~430 lines — your choice; if you split, move `supersedePriorRecurringTask` and its private helpers there too and keep the frozen commit-message substring in that file). Signature:
   ```go
   // listSameSlugCandidateTitles lists task files scoped to a schedule's slug and
   // returns their TITLES (basename without ".md"), filtered to those whose title
   // starts with "<slug> - ". It uses a slug-scoped glob when the slug contains no
   // glob metacharacters; otherwise it lists all task files and filters in memory
   // (glob-injection defense). List errors are logged and returned.
   func listSameSlugCandidateTitles(
       ctx context.Context,
       gitClient gitclient.GitClient,
       taskDir string,
       slug string,
   ) ([]string, error)
   ```
   Logic:
   - Define the prefix once: `prefix := slug + " - "`.
   - Glob-safety: `if strings.ContainsAny(slug, "*?[]\\") { ... list-all fallback ... } else { ... slug-scoped glob ... }`.
     - Slug-scoped glob path: `glob := filepath.Join(taskDir, slug+" - *.md")`; `glog.V(3).Infof("auto-supersede: listing slug-scoped glob %q", glob)`.
     - List-all fallback path: `glob := filepath.Join(taskDir, "*.md")`; `glog.V(2).Infof("auto-supersede: slug %q contains glob metacharacters; falling back to list-all + in-memory filter", slug)`.
   - `relPaths, err := gitClient.ListFiles(ctx, glob)`. On error: `glog.Warningf("auto-supersede: list %q failed for slug %q: %v", glob, slug, err); return nil, err`. (Spec Failure Mode: git-rest ListFiles error → WARN log `auto-supersede: list`, swallow — caller returns.)
   - For each returned relPath: derive the title via `title := strings.TrimSuffix(filepath.Base(relPath), ".md")`. Keep only titles where `strings.HasPrefix(title, prefix)` (in-memory prefix filter applies to BOTH the glob and list-all paths, so glob-injection cannot widen the result and the fallback stays scoped). Collect the kept titles.
   - Return the kept titles, nil.
   - The WARN log substring `auto-supersede: list` MUST appear on the list-error path (spec Detection column).

4. **Do NOT change** `isEligibleForSupersede`, `readPriorForSupersede`, `priorIsInProgress`, `transitionPrior`, `buildSupersedeModifyFn`, or the five frozen frontmatter field names. They are reused verbatim. (If prompt 1's log messages inside `readPriorForSupersede`/`priorIsInProgress` say "prior" you may keep them; they still read correctly as per-candidate messages.)

5. **Imports.** Ensure the file holding the scan uses `"path/filepath"`, `"strings"`, `context`, `task "github.com/bborbe/agent/command/task"`, `libtime "github.com/bborbe/time"`, `gitclient "github.com/bborbe/agent-task-controller/pkg/gitrestclient"`, `"github.com/golang/glog"` — most already imported. If you split into `task_supersede.go`, import only what it uses and remove now-unused imports from `task_create_task_executor.go` (run `go build ./pkg/command/...` and fix what the compiler reports).

6. **Wire `k` through the factory.** In `/workspace/pkg/factory/factory.go`:
   - Add a `k int` parameter to `CreateCommandConsumer` (append it after `currentDateTime libtime.CurrentDateTimeGetter`, i.e. before `prCommenter prcomment.PRCommenter` to keep related args grouped — OR append it last; pick ONE and be consistent with the main.go update in Requirement 7). Chosen order: append `k int` immediately AFTER `currentDateTime` and BEFORE `prCommenter`.
   - Change the `command.NewCreateTaskExecutor(gitClient, taskDir, vaultName, currentDateTime)` call (line 40) to `command.NewCreateTaskExecutor(gitClient, taskDir, vaultName, currentDateTime, k)`.

7. **Update the single `main.go` call site to keep the build green.** In `/workspace/main.go` the call `factory.CreateCommandConsumer(...)` (line 163) passes, in order: `saramaClientProvider, syncProducer, db, a.TopicPrefix, resultWriter, gitClient, a.TaskDir, a.VaultName, currentDateTime, prCommenter`. Insert a literal `7` (the default look-back bound) as the new argument immediately AFTER `currentDateTime` and BEFORE `prCommenter`, matching the factory parameter order chosen in Requirement 6. Add a `// TODO(spec-004 prompt 3): replace literal 7 with configurable SUPERSEDE_LOOKBACK env var` comment on that argument line. Prompt 3 replaces the literal with the config field. Do NOT add the env-var struct field or CHANGELOG entry in this prompt.

8. **Sibling entry-point check.** Run `grep -rn "factory.CreateCommandConsumer\|command.NewCreateTaskExecutor" /workspace/ --include="*.go"`. As verified at authoring time the complete set is: one production caller in `main.go` (line 163, `CreateCommandConsumer`), the factory itself, and the `NewCreateTaskExecutor` construction in the test `BeforeEach` (line 64). There is a single `main.go` — no `cmd/` variant, no `run-once` binary. If grep reveals any NEW call site, update it too or document in `## Improvements` why it is out of scope.

9. **Update ALL FIVE existing `command.NewCreateTaskExecutor` constructions** in `/workspace/pkg/command/task_create_task_executor_test.go`. Run `grep -n "command.NewCreateTaskExecutor" /workspace/pkg/command/task_create_task_executor_test.go` to confirm; as verified at authoring time they are at line 64 (the `BeforeEach`) and lines 359, 378, 397, 416 (the four vault-routing Contexts, each building a local executor with vault `"personal"` or `"openclaw"`). Declare a package-level `const testK = 7` near the top of the file. Append `testK` as the new final argument to EACH of the five constructions, preserving each site's existing `vaultName` string:
   - line 64: `command.NewCreateTaskExecutor(fakeGit, taskDir, "openclaw", clock, testK)`.
   - line 359: `command.NewCreateTaskExecutor(fakeGit, taskDir, "personal", clock, testK)`.
   - line 378: `command.NewCreateTaskExecutor(fakeGit, taskDir, "openclaw", clock, testK)`.
   - line 397: `command.NewCreateTaskExecutor(fakeGit, taskDir, "openclaw", clock, testK)`.
   - line 416: `command.NewCreateTaskExecutor(fakeGit, taskDir, "personal", clock, testK)`.
   Line numbers are hints; anchor on the `command.NewCreateTaskExecutor(...)` calls. If grep finds MORE than five, update those too. All existing non-supersede Contexts (malformed payload, collision, vault routing, etc.) must still pass — they don't set `created_by`/`auto_abort_prior`, so the hook is a no-op (and by default `fakeGit.ListFiles` returns zero-value `(nil, nil)`, so even an eligible-but-empty listing is a safe no-op).

10. **Add the scan-based supersede tests** as a new `Context("scan-and-collapse supersede", func() {...})` inside the existing `Describe("NewCreateTaskExecutor")` in `/workspace/pkg/command/task_create_task_executor_test.go`. Test-harness mechanics:
    - The handler issues, in order: `ReadFile(relPath)` for the title-collision check (call 0), then `AtomicWriteAndCommitPush` for the new instance, then — inside the hook — `ListFiles(glob)`, then `ReadFile` per inspected candidate, then `AtomicReadModifyWriteAndCommitPush` per in_progress candidate.
    - Default the collision check to free: the `BeforeEach` already sets `fakeGit.ReadFileReturns(nil, errors.New("... returned 404 ..."))`. For per-candidate reads, use `fakeGit.ReadFileStub` keyed on the relPath argument so you can return distinct content per candidate path (the collision check reads the NEW instance's relPath, which won't exist in your candidate map → return 404). Example stub:
      ```go
      fakeGit.ReadFileStub = func(_ context.Context, relPath string) ([]byte, error) {
          if content, ok := fileContents[relPath]; ok {
              return content, nil
          }
          return nil, errors.New("GET " + relPath + " returned 404: not found")
      }
      ```
      where `fileContents` maps `tasks/<title>.md` → prior-file bytes.
    - `fakeGit.ListFilesReturns(candidateRelPaths, nil)` supplies the candidate listing (repo-root-relative paths like `tasks/IBKR Swing Trading - 2026W28-mon.md`).
    - Build the new-instance command via `buildCmdObj(task.CreateCommand{Title: ..., TaskIdentifier: ..., Frontmatter: lib.TaskFrontmatter{"assignee":"claude","status":"next","created_by":"recurring-task-creator","auto_abort_prior": true}})`.
    - Prior-file content helper (in_progress):
      ```go
      inProgress := func(id string) []byte {
          return []byte("---\ntask_identifier: " + id + "\nassignee: claude\nstatus: in_progress\n---\nbody\n")
      }
      ```
    Cover these ACs (each an `It`):

    - **AC N-collapse (all priors in window closed):** new instance `IBKR Swing Trading - 2026W28-wed`. `ListFiles` returns the new instance plus TWO older in_progress priors `... - 2026W28-mon.md` and `... - 2026W28-tue.md` (all same slug `IBKR Swing Trading`). `fileContents` maps both mon+tue to `inProgress(...)`. Assert `AtomicReadModifyWriteAndCommitPushCallCount() == 2`; capture both calls via `AtomicReadModifyWriteAndCommitPushArgsForCall(0/1)`; run each captured `modify` on the corresponding prior content and assert the output contains `status: aborted`, `phase: done`, `completed_date:`, `superseded_by: tasks/IBKR Swing Trading - 2026W28-wed.md`, `created_by: recurring-task-creator`; assert both commit messages contain `auto-supersede prior recurring task`. This is the missed-day-gap AC (Mon+Tue open, Wed fires → both close, Wed stays open — assert `AtomicWriteAndCommitPushCallCount() == 1` for the new instance and that no write targeted the Wed instance's abort).

    - **AC weekday-set-agnostic (sparse set):** new instance `Sched - 2026W28-fri`; `ListFiles` returns older in_progress `Sched - 2026W28-mon` and `Sched - 2026W28-wed` (a sparse mon/wed/fri set). All three weekdays of W28 share the same week-Monday ordinal, so mon and wed are EQUAL-ordinal same-week siblings of fri; the step-2f filter (`rc.Ordinal > newOrdinal` → skip; equal-ordinal siblings pass through) collapses them. Assert BOTH mon and wed transition (`AtomicReadModifyWriteAndCommitPushCallCount() == 2`) using slug + open-status only, with no weekday-set knowledge encoded.

    - **AC look-back honored (> K priors, small K):** set `testK` small for this test by constructing a LOCAL executor with `k = 2`: `smallK := command.NewCreateTaskExecutor(fakeGit, taskDir, "openclaw", clock, 2)`. New instance `Weekly Sched - 2026W30`. `ListFiles` returns FOUR older in_progress priors `2026W29, 2026W28, 2026W27, 2026W26` (all `Weekly Sched - ...`). Assert the fake records at most K=2 candidate `ReadFile` calls beyond the collision check — i.e. total `ReadFileCallCount()` == 1 (collision) + 2 (the two most-recent candidates W29, W28) == 3 — and `AtomicReadModifyWriteAndCommitPushCallCount() == 2` (only W29, W28 closed; W27, W26 left open). Assert the two captured abort target paths end with `2026W29.md` and `2026W28.md` (the two MOST-RECENT), proving ranking + cap.

    - **AC ranking across ISO-week + year boundary:** new instance `Weekly Sched - 2026W01`; `ListFiles` returns older in_progress `Weekly Sched - 2025W52` and `Weekly Sched - 2025W51`. With `k >= 2`, assert both close and the ordering places `2025W52` before `2025W51` (assert first captured abort target ends with `2025W52.md`). Proves the cross-year ordinal ordering end-to-end.

    - **AC Daily regression (collapse to one):** new instance `Cleanup - 2026-06-15`; `ListFiles` returns older in_progress `Cleanup - 2026-06-14`. Assert exactly one abort (`AtomicReadModifyWriteAndCommitPushCallCount() == 1`) targeting `2026-06-14.md`, new stays written.

    - **AC Weekly regression (collapse to one):** new instance `Aquascape PWC - 2026W27`; `ListFiles` returns older in_progress `Aquascape PWC - 2026W26`. Assert exactly one abort targeting `2026W26.md`.

    - **AC idempotency (redelivery — prior already aborted):** new instance `Weekly Sched - 2026W27`; `ListFiles` returns `Weekly Sched - 2026W26` but its `fileContents` returns `status: aborted`. Assert `AtomicReadModifyWriteAndCommitPushCallCount() == 0` (no rewrite of an already-closed prior).

    - **AC not eligible (auto_abort_prior absent):** new instance with `created_by: recurring-task-creator` but NO `auto_abort_prior`. Assert `ListFilesCallCount() == 0` (the hook returns before listing) and `AtomicReadModifyWriteAndCommitPushCallCount() == 0`.

    - **AC ListFiles error swallowed:** eligible new instance; `fakeGit.ListFilesReturns(nil, errors.New("git-rest 503"))`. Assert the handler return value is `(nil, nil, nil)` (no error propagates) and `AtomicReadModifyWriteAndCommitPushCallCount() == 0`.

    - **AC per-candidate read error swallowed, others processed:** eligible new instance `Weekly Sched - 2026W28`; `ListFiles` returns `2026W27` and `2026W26` (both older). `ReadFileStub` returns a NON-404 error (`errors.New("GET tasks/... returned 500")`) for the `2026W27` path but `inProgress(...)` for `2026W26`. Assert the handler still returns `(nil, nil, nil)` and `2026W26` is still closed (`AtomicReadModifyWriteAndCommitPushCallCount() == 1`, target ends `2026W26.md`). Proves per-file best-effort.

    - **AC write error swallowed:** eligible; one older in_progress prior; `fakeGit.AtomicReadModifyWriteAndCommitPushReturns(errors.New("git-rest 503"))`. Assert handler returns `(nil, nil, nil)`.

    - **AC glob-safety fallback:** eligible new instance whose SLUG contains a glob metacharacter, e.g. `Report [draft] - 2026W28` (slug `Report [draft]`). `ListFiles` returns `Report [draft] - 2026W27` (in_progress). Assert `ListFilesCallCount() == 1` and capture `ListFilesArgsForCall(0)` — the glob argument MUST be the list-all glob `tasks/*.md` (NOT a glob embedding the `[draft]` metacharacters), and the prior `Report [draft] - 2026W27` still closes (`AtomicReadModifyWriteAndCommitPushCallCount() == 1`) via the in-memory prefix filter. Proves glob-injection defense + fallback still collapses.

    - **AC unrelated-slug filtered out:** eligible new instance `Weekly Sched - 2026W28`; `ListFiles` returns `Weekly Sched - 2026W27` (in_progress) AND `Other Sched - 2026W27` (in_progress). Assert only `Weekly Sched - 2026W27` is read/closed (`AtomicReadModifyWriteAndCommitPushCallCount() == 1`, target ends `Weekly Sched - 2026W27.md`); `Other Sched` is never read (the `<slug> - ` prefix filter excludes it). Proves slug scoping.

11. **Frozen-substring grep AC.** After implementation, `grep -rn 'auto-supersede prior recurring task' /workspace/pkg/command/` must return ≥1 line (the commit message in `transitionPrior`). If you moved the helpers into `task_supersede.go`, the grep still matches there.

</requirements>

<constraints>

- Frozen supersede output contract — byte-identical to today's single-prior transition: `status: aborted`, `phase: done`, `completed_date` in RFC3339, `superseded_by` = new instance relPath, `created_by = recurring-task-creator`. Do NOT rename these five fields. (Reused via `buildSupersedeModifyFn` — do not alter it.)
- Frozen eligibility gate: `created_by == recurring-task-creator` AND `auto_abort_prior` is bool `true` or string `"true"`. Reuse `isEligibleForSupersede` unchanged. Absence of `auto_abort_prior` → ineligible.
- All timestamps via the injected `libtime.CurrentDateTimeGetter` — no stdlib `time.Now()`. (Reused via `transitionPrior`.)
- Listing uses the existing `GitClient.ListFiles(ctx, glob)` (single-level glob, repo-root-relative) — no new git-rest endpoint, no new GitClient method, no counterfeiter regen.
- Idempotent on Kafka redelivery: candidates already `aborted` (status != in_progress) are skipped, no rewrite. (Reused via `priorIsInProgress`.)
- Reads bounded to at most K candidate files per materialize regardless of history depth — increment the inspected-counter BEFORE the read so the bound holds across 404s and already-aborted candidates.
- The hook NEVER causes the handler to return an error — every failure (ineligible, unrecognized token, list error, per-candidate read/parse/write error, path-separator) is logged and swallowed. Handler success path stays `return nil, nil, nil`.
- The hook runs ONLY after a successful new-instance write (it is called after the write in the handler).
- Glob-injection defense: a slug containing any of `* ? [ ] \` falls back to `tasks/*.md` + in-memory `<slug> - ` prefix filter; the prefix filter also applies on the slug-scoped path so the result is always exactly `<slug> - `-prefixed.
- Path-traversal defense: candidate titles containing `/` or `\` are skipped (never joined into a read/write path).
- Do NOT add a per-schedule opt-out, extra config knob, or Prometheus metric. Spec Non-goals (verbatim): *"Do NOT add a per-schedule opt-out of the collapse behavior"* and *"Do NOT add any UI / dashboard surface for supersede."* Observability is the existing `auto-supersede:` / `auto-supersede prior recurring task` log lines only.
- Do NOT change `bborbe/recurring-task-creator`, the Schedule CRD, or `docs/period-token-semantics.md` (the doc rewrite is prompt 3).
- Do NOT add the `SUPERSEDE_LOOKBACK` env-var struct field or a CHANGELOG entry in this prompt — that is prompt 3. Use the literal `7` in `main.go` with the TODO comment.
- Per `go-error-wrapping-guide.md`: `errors.Wrapf(ctx, ...)` / `errors.Errorf(ctx, ...)` only.
- Per `go-precommit.md`: funlen 80 / nestif 4 / golines 100. `supersedePriorRecurringTask` will approach funlen — extract the per-candidate transition loop into a helper (e.g. `collapseCandidates(ctx, gitClient, taskDir, currentDateTime, k, ranked, newOrdinal, newRelPath, cmd)`) if it exceeds 80 lines.
- Existing non-supersede tests in `/workspace/pkg/command/` and `/workspace/pkg/factory/` must still pass.
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

Look-back / listing wired (no new git-rest endpoint):

```
cd /workspace && grep -rn "ListFiles" pkg/command/
```
Expect the scan helper's `gitClient.ListFiles(ctx, glob)` call.

Run ONCE at the end:

```
cd /workspace && make precommit
```

Expected: exit 0; the scan-and-collapse Context specs pass (N-collapse / missed-day, sparse weekday set, look-back cap with small K, cross-year ranking, Daily regression, Weekly regression, idempotency, ineligible, ListFiles error swallowed, per-candidate read error swallowed, write error swallowed, glob-safety fallback, unrelated-slug filtered); all existing create-task and factory specs still pass; handler returns `(nil, nil, nil)` on every failure path.

</verification>
