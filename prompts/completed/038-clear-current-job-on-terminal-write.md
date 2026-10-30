---
status: completed
summary: Added result.ClearCurrentJobIfTerminal and invoked it on both controller write paths so terminal records no longer keep a stale current_job, with unit and write-path specs plus a changelog entry.
execution_id: agent-task-controller-clear-current-job-exec-038-clear-current-job-on-terminal-write
dark-factory-version: dev
created: "2026-10-30T00:00:00Z"
queued: "2026-10-30T07:17:44Z"
started: "2026-10-30T07:18:36Z"
completed: "2026-10-30T07:26:48Z"
---

<summary>
- A task record written to `completed` or `aborted` by the controller no longer keeps a stale `current_job`
- The clear happens on both controller write paths: an agent result write and a partial frontmatter update
- A record that is not terminal keeps its `current_job` exactly as before, so a running job is never erased
- A record whose `current_job` is already empty or absent is not touched
- New tests prove each case on each write path
- The changelog records the fix
</summary>

<objective>
Stop finished agent runs from keeping a stale `current_job`. The executor clears the field only from process memory (`pkg/job_watcher.go` in `bborbe/agent-task-executor` gates the clear on `taskStore.Load`), so a run that finishes across an executor restart or after store eviction is never cleared: on 2026-10-29, 5175 closed records in `private-agent` carried a non-empty `current_job` (5063 completed, 112 aborted). After this change the controller, which writes the terminal status itself, empties `current_job` in the same write. A terminal record cannot own a live job, so this clear can never erase a newer job's name.
</objective>

<context>
Read CLAUDE.md for project conventions, if it exists. This repo has no root CLAUDE.md today, so do not treat its absence as an error.

Read these files before changing anything:
- `pkg/result/result_writer.go`:
  - `func (r *resultWriter) buildResultModifyFn(...)`: inside the returned closure, `merged, decisions := MergeFrontmatter(currentOnDisk, req.Frontmatter)`, then the line `HealTargetVault(merged, r.vaultName)`, then `mergedBody := mergeBody(...)`.
  - `func HealTargetVault(fm lib.TaskFrontmatter, vaultName string)`: exported, doc-commented, mutates `fm` in place. Copy its style.
  - `func isTerminalStatus(s domain.TaskStatus) bool` and `var terminalStatuses` (`TaskStatusCompleted`, `TaskStatusAborted`).
- `pkg/command/task_update_frontmatter_executor.go`: the function that builds `merged` from `frontmatter` + `updates`, then calls `result.ClearAssigneeIfHumanReview(merged, humans)` and `result.HealTargetVault(merged, vaultName)`, then diffs `merged` against `before` into `patch` with `result.FrontmatterValueEqual`. A key changed in `merged` lands in `patch`.
- `pkg/result/result_writer_test.go` and `pkg/command/task_update_frontmatter_executor_test.go`: follow existing specs that write a terminal status for fixture style.
- `CHANGELOG.md`: top section is `## v0.22.8`; there is no `## Unreleased` section. Copy the bullet style (`- fix: **bold lead.** ...`).
</context>

<requirements>
1. In `pkg/result/result_writer.go`, directly above `func isTerminalStatus`, add:
   ```go
   // ClearCurrentJobIfTerminal empties current_job when the merged status is
   // terminal (completed / aborted). The executor clears the field only from
   // process memory, so a run that finishes across an executor restart, or after
   // its task was evicted from the executor's store, kept a stale current_job.
   // A terminal record cannot own a live job, so clearing here cannot erase a
   // newer job's name. Writes "" (the executor's own clear value). Mutates fm in
   // place; no-op when the status is not terminal or the field is absent or empty.
   func ClearCurrentJobIfTerminal(fm lib.TaskFrontmatter) {
   	if !isTerminalStatus(fm.Status()) {
   		return
   	}
   	if v, ok := fm["current_job"]; ok && v != nil && v != "" {
   		fm["current_job"] = ""
   	}
   }
   ```
2. In `buildResultModifyFn`, call `ClearCurrentJobIfTerminal(merged)` on the line directly after `HealTargetVault(merged, r.vaultName)`.
3. In `pkg/command/task_update_frontmatter_executor.go`, directly after `result.HealTargetVault(merged, vaultName)`, add:
   ```go
   // A terminal record owns no live job; see result.ClearCurrentJobIfTerminal.
   result.ClearCurrentJobIfTerminal(merged)
   ```
4. Tests (Ginkgo/Gomega, existing suites):
   - Unit specs for `result.ClearCurrentJobIfTerminal`: `completed` + non-empty → `""`; `aborted` + non-empty → `""`; `in_progress` + non-empty → unchanged; `completed` + key absent → key still absent; `completed` + `""` → `""`.
   - A `WriteResult` spec: on-disk record has `current_job: some-job`, incoming result sets `status: completed`; the written frontmatter has `current_job: ""`. A second spec with incoming `status: in_progress` keeps `current_job: some-job`.
   - An update-frontmatter executor spec: on-disk `current_job: some-job`, update `status: completed` → the patch carries `current_job: ""`. With update `phase: execution` on an `in_progress` record → the patch has no `current_job` key.
5. Add a `## Unreleased` section above `## v0.22.8` in `CHANGELOG.md` with one bullet:
   `- fix: **a terminal write clears \`current_job\`.** The executor cleared the field only for tasks still in its process memory, so runs finishing across an executor restart or after store eviction kept a stale job name — 5175 closed records on 2026-10-29. Both controller write paths (result write, update-frontmatter) now empty \`current_job\` when the merged status is \`completed\` or \`aborted\`; non-terminal writes are unchanged, so a running job is never erased.`
</requirements>

<constraints>
- Do not change any other field, any non-terminal behaviour, or any wire format.
- Do not touch `MergeFrontmatter`, `controllerOwnedFields`, or the ownership guard.
- Do not commit; dark-factory handles git.
- Existing tests must still pass.
</constraints>

<verification>
Run `make precommit` -- must pass.
</verification>
