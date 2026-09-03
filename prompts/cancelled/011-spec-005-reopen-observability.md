---
status: cancelled
spec: [005-bug-create-task-dedup-blocks-terminal-reopen]
execution_id: agent-task-controller-target-vault-echo-exec-011-spec-005-reopen-observability
dark-factory-version: dev
created: "2026-08-27T20:30:00Z"
queued: "2026-08-27T18:44:32Z"
started: "2026-09-03T17:33:06Z"
branch: dark-factory/bug-create-task-dedup-blocks-terminal-reopen
cancelled: "2026-09-03T17:38:11Z"
---

# Reopen observability for create-task terminal reopen

<summary>

- A terminal reopen is now visible at default log verbosity: an unconditional INFO line names the reopened path and the prior status, so operators can see in prod logs (without raising the log level) which terminal files were re-materialized.
- The git commit message for a reopened task is distinct from a first-ever create's, so vault history makes reopens greppable at a glance.
- A first-ever create emits neither the reopen log line nor the reopen commit message — the two paths remain distinguishable in exactly the two greppable places.
- The changelog records the behavior change as a fix entry under a new Unreleased section above the current top version.
- The design doc gains the frozen terminal-status definition, so the `completed`/`aborted` rule outlives this spec.
- New unit specs assert the commit-message substring is present on the reopen path and absent on the first-ever-create path.

</summary>

<objective>

Make a terminal reopen operator-greppable in exactly the two places the spec freezes: an unconditional INFO log line containing `create-task: reopening terminal task` (with the path and prior status) and a git commit message containing `reopen terminal task`, both emitted only on the reopen path that prompt 1 plumbed into the write call. Record the behavior change in the changelog and document the frozen terminal-status rule in `docs/controller-design.md`.

</objective>

<context>

There is no CLAUDE.md in this repo; the global YOLO container CLAUDE.md (already in your context) governs project conventions. Read the repo's own code as the source of truth for style.

Read the coding-plugin docs that apply to this change:
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` (entry format, `## Unreleased` placement, prefix rules)
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-glog-guide.md` (unconditional `glog.Infof` vs `glog.V(n).Infof`)
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/definition-of-done.md`

DEPENDENCY — verify prompt 1 shipped before changing anything:

```
cd /workspace && grep -n 'reopened bool' pkg/command/task_create_task_executor.go
```

The `writeTaskFile` signature MUST now include `reopened bool, priorStatus string` (threaded from `checkTitlePathFree` by the previous prompt). If the grep returns no match, STOP and report `status: failed` with message `"terminal-reopen plumbing not yet deployed (prompt 1)"` — do NOT re-implement the decision or change signatures here.

Read `pkg/command/task_create_task_executor.go` IN FULL, focusing on:
- `writeTaskFile(ctx, gitClient, relPath, cmd, vaultName, reopened bool, priorStatus string) error` (the function you change — after prompt 1 it already declares `reopened`/`priorStatus` but does not consume them). Current body: build content via `buildCreateTaskContent`, then `gitClient.AtomicWriteAndCommitPush(ctx, absPath, content, "[agent-task-controller] create task "+string(cmd.TaskIdentifier))`, then `glog.V(2).Infof("create-task: created task file at %s for %s", relPath, cmd.TaskIdentifier)`.
- The handler in `NewCreateTaskExecutor` already captures `reopened, priorStatus, err := checkTitlePathFree(...)` and passes both into `writeTaskFile`.

Read `pkg/command/task_create_task_executor_test.go` IN FULL for the Ginkgo harness (`package command_test`, `mocks.GitClient`, `buildCmdObj`, `AtomicWriteAndCommitPushArgsForCall(i int) (context.Context, string, []byte, string)` where the 4th value is the commit message, default `ReadFileReturns(nil, errors.New("GET file returned 404: not found"))`).

Read `CHANGELOG.md` — the current top version heading is `## v0.5.2`; there is no `## Unreleased` section yet. The AC evidence is a `terminal` line above the top version heading.

Read `docs/controller-design.md` — the `## Frontmatter Merge` section (around lines 52-60) documents the status/phase merge semantics that the terminal-status definition relies on; the new terminal-status section belongs near it.

<!-- OPEN QUESTION for the human reviewer: the spec freezes only the substrings `create-task: reopening terminal task` (log) and `reopen terminal task` (commit message). This prompt picks the full commit-message form `"[agent-task-controller] reopen terminal task <id>"` to mirror the existing `"[agent-task-controller] create task <id>"` form — confirm the prefix/suffix choice at audit. -->

</context>

<requirements>

1. **Consume the reopen signal in `writeTaskFile` in `pkg/command/task_create_task_executor.go`.** The function already has the `reopened bool` and `priorStatus string` parameters (from the previous prompt). Change the commit message passed to `AtomicWriteAndCommitPush` from the unconditional form to a branch on `reopened`:
   ```go
   msg := "[agent-task-controller] create task " + string(cmd.TaskIdentifier)
   if reopened {
       msg = "[agent-task-controller] reopen terminal task " + string(cmd.TaskIdentifier)
   }
   ```
   The reopen message MUST contain the frozen substring `reopen terminal task` (spec Desired Behavior 5 + AC). The first-create message is unchanged.

2. **Emit the unconditional reopen INFO log in `writeTaskFile`** after the successful `AtomicWriteAndCommitPush` and after the existing `glog.V(2).Infof("create-task: created task file at %s for %s", ...)` line, gated on `reopened`:
   ```go
   if reopened {
       glog.Infof(
           "create-task: reopening terminal task at %s (prior status %s)",
           relPath,
           priorStatus,
       )
   }
   ```
   - The log MUST be unconditional `glog.Infof`, NOT `glog.V(n).Infof` — an in-place overwrite of vault state is rare and consequential enough to be visible at default verbosity, and the spec's Rung-3 AC greps for it in prod logs at default level (spec Constraint). It MUST contain the frozen substring `create-task: reopening terminal task` and MUST name the path and the prior status (spec DB5).
   - A first-ever create (`reopened == false`) MUST NOT emit this line.
   - `glog.Infof` is the standard glog API from `github.com/golang/glog` (already imported); `func Infof(format string, args ...any)` is exported at `glog.go:520` in glog v1.2.5 — this is this repo's first use of unconditional `glog.Infof` (the rest of the file uses `glog.V(n).Infof`), which is intentional and required.

3. **Update the `writeTaskFile` doc comment** to state that on `reopened == true` the write emits the unconditional INFO log `create-task: reopening terminal task` and commits with a `reopen terminal task` message, and that a first-ever create carries neither.

4. **Add the commit-message unit specs** to `pkg/command/task_create_task_executor_test.go` as a new `Context` block inside `Describe("NewCreateTaskExecutor")`. Assert both directions (spec AC):
   - **Positive (reopen):** stage `fakeGit.ReadFileReturns([]byte("---\ntask_identifier: old\nassignee: alice\nstatus: completed\nphase: done\n---\nprior verdict\n"), nil)`; handle once; capture `_, _, _, message := fakeGit.AtomicWriteAndCommitPushArgsForCall(0)`; assert `Expect(message).To(ContainSubstring("reopen terminal task"))` and `Expect(err).NotTo(HaveOccurred())`.
   - **Negative (first-ever create):** keep the `BeforeEach` default 404 read (title path free); handle once; capture the message; assert `Expect(message).NotTo(ContainSubstring("reopen terminal task"))` and `Expect(message).To(ContainSubstring("create task"))`.
   Do NOT assert the INFO log output — the repo has no glog-capture harness; the log is verified by the frozen-substring grep in `<verification>`.

5. **Add the CHANGELOG entry to `CHANGELOG.md`.** Per `changelog-guide.md`: add a `## Unreleased` section immediately above the current top version heading `## v0.5.2` (never above the `# Changelog` header block), with a single `fix:` bullet that names the terminal-reopen behavior change specifically (no `- fix: fix bug`). The word `terminal` MUST appear in the bullet (spec AC). Example shape:
   ```
   ## Unreleased

   - fix: reopen terminal task title slots on create — a create-task command whose title path holds a task with status `completed` or `aborted` now materializes a fresh task at that path instead of being dropped with `ErrTaskAlreadyExists` (terminal-task reopen, spec 005)
   ```
   The `fix:` prefix drives a patch version bump on release; the release agent renames `## Unreleased` to the next version heading after merge.

6. **Add the terminal-status definition to `docs/controller-design.md`.** Insert a new section `## Terminal Task Status (create-task dedup)` immediately after the `## Frontmatter Merge` section (around line 60). The section must state, in prose (markdown file, no Go):
   - The vault has exactly two terminal task statuses: `completed` and `aborted`.
   - `done` is a `phase` value, not a `status`, and is NOT terminal for the dedup rule (status/phase merge semantics documented in `## Frontmatter Merge`).
   - `create-task`'s dedup rule is "the title path is occupied by a live task": a create command whose title path holds a terminal task frees the slot and materializes a fresh non-terminal task at that path (overwrite in place; the prior instance remains recoverable via `git show <sha>~1:<path>`), while any non-terminal status or any existing file whose status cannot be read holds the path and is dropped with `ErrTaskAlreadyExists`.
   - The word `terminal` MUST appear (consistency with the changelog AC's grep target; the spec Constraint requires the definition to outlive the spec).

7. **Self-check before finishing:** re-run the `<verification>` block and confirm it passes; walk each acceptance criterion from the spec against the change — both frozen-substring greps ≥1 line in `pkg/command/task_create_task_executor.go`, commit-message positive/negative unit specs, CHANGELOG AC (`grep -n 'terminal' CHANGELOG.md` returns a line above `## v0.5.2`), and `make precommit` exit 0 at repo root. The Rung-3 post-deploy AC is operator-side and is covered by the spec's Verification ladder — do NOT attempt kubectl/cluster steps in this container.

</requirements>

<constraints>

- Frozen log substring: `create-task: reopening terminal task` — must appear verbatim in `pkg/command/task_create_task_executor.go`.
- Frozen commit-message substring: `reopen terminal task` — must appear verbatim in `pkg/command/task_create_task_executor.go`, only on the reopen path.
- The reopen INFO log is unconditional (`glog.Infof`, not `glog.V(n).Infof`).
- Do NOT change `checkTitlePathFree`, `NewCreateTaskExecutor`'s signature, or any other function's signature — prompt 1 already plumbed the reopen signal; this prompt only consumes it in `writeTaskFile`.
- Do NOT add a config field, env var, CRD knob, or per-producer opt-out for terminal-reopen — invariant.
- Do NOT add a new frontmatter marker on the reopened task (`reopened: true`, `reopen_count`, `supersedes`) — the git commit and the log line are the record.
- Do NOT touch the unrelated `assignee`-blanking-on-failure defect.
- Existing tests in `pkg/command/` and `pkg/gitrestclient/` must still pass; do NOT modify any `Expect(` line in the two pre-existing collision contexts.
- Per `go-error-wrapping-guide.md`: `errors.Wrapf(ctx, ...)` only — never `fmt.Errorf`, never `context.Background()` in `pkg/`.
- Do NOT commit — dark-factory handles git.
- Do NOT run kubectl/cluster/operator commands — the container has no cluster access; the Rung-3 post-deploy AC lives in the spec's operator-executable Verification rung.

</constraints>

<verification>

Run iteratively while implementing:

```
cd /workspace && make test
```

Frozen-substring checks (spec ACs):

```
cd /workspace && grep -n 'reopen terminal task' pkg/command/task_create_task_executor.go
cd /workspace && grep -n 'create-task: reopening terminal task' pkg/command/task_create_task_executor.go
```
Expect ≥1 line from each.

CHANGELOG AC check:

```
cd /workspace && grep -n 'terminal' CHANGELOG.md
```
Expect ≥1 line, positioned ABOVE the `## v0.5.2` heading.

Design-doc check:

```
cd /workspace && grep -n 'terminal' docs/controller-design.md
```
Expect ≥1 line.

Run ONCE at the end:

```
cd /workspace && make precommit
```

Expected: exit 0; the two commit-message unit specs pass (positive reopen, negative first-create); all prompt-1 terminal-reopen specs still pass; existing create-task and supersede specs still pass.

</verification>
