---
status: completed
spec: [022-bug-command-guard-drops-armed-max-triggers]
summary: Let the command-path guard introduce max_triggers when absent while keeping on-disk-wins, inverted and added specs, corrected the two-guard docs, and recorded the fix in an Unreleased changelog section
execution_id: a6e3b8f7-2ceb-56a5-8923-bced1ad79d48-exec-048-bug-command-guard-introduces-armed-max-triggers
dark-factory-version: dev
created: "2026-11-04T16:22:53Z"
queued: "2026-11-04T16:33:30Z"
started: "2026-11-04T16:33:31Z"
completed: "2026-11-04T16:45:22Z"
branch: dark-factory/bug-command-guard-drops-armed-max-triggers
---

<summary>
- A task whose pod can never be scheduled now actually gets its trigger cap armed: the executor's arm decision writes `max_triggers: 3` into a record that had no such key.
- The command path stops deleting an incoming `max_triggers` when the task file does not already carry one; it introduces the value the command names.
- An existing operator budget is still protected: when the task already carries `max_triggers`, the on-disk value wins over any differing incoming value in both directions.
- An agent result payload still cannot introduce or change the cap; the result-path guard is untouched.
- The trigger-cap escalation now fires on the same write when the introduced cap is at or below the accrued trigger count.
- The design document stops claiming that typed frontmatter commands bypass the guard; it records the two distinct guards and their differing `max_triggers` rules.
- The changelog records the fix under a new Unreleased section.
- The inverted unit spec proves the introduced value governs the cap predicate, not merely that the key lands.
- No behavior change for `trigger_count` or `trigger_scope`.
</summary>

<objective>
Make the executor's arm effective: let the command path introduce `max_triggers` when the task carries none, while keeping the on-disk value over a differing incoming value and keeping the result path's refusal intact. Update the guard comments, invert the ref-only unit spec, add the arm-shape spec, correct `docs/controller-design.md`, and record the fix in `CHANGELOG.md`.
</objective>

<context>
Read these files before changing anything:

- `pkg/command/task_update_frontmatter_executor.go` — `buildUpdatePatch` (the sole caller of the guard), `capDefinitionFields` (the comment to rewrite), `applyControllerOwnedGuard` (the guard to change; its `delete(merged, field)` branch is the defect), `applyUpdateTriggerCap` (runs after the guard on the same merged map, so a record already at or above the cap escalates in this write).
- `pkg/command/task_update_frontmatter_executor_test.go` — `Describe("NewUpdateFrontmatterExecutor")` / `Describe("HandleCommand")` / `Context("controller-owned frontmatter")`. The spec `"does not let a snapshot introduce max_triggers on a ref-only task"` (currently around line 264) is the one that inverts; the spec `"does not let a snapshot raise max_triggers out from under the cap"` (currently around line 241) is the on-disk-wins control and keeps its assertions. Reuse the existing helpers `writeTaskFile`, `parseFrontmatter`, `buildCmdObj`, and the `fakeVault` harness.
- `pkg/result/result_writer.go` — read only. `controllerOwnedFields` (includes `max_triggers`), `MergeFrontmatter`, `TriggerCapEngaged` (presence check on `max_triggers`, plus the ref arm), and `MaxTriggers` usage. Do NOT modify this file or anything under `pkg/result/`.
- `docs/controller-design.md` — the field-ownership table (rows around lines 78-85), and the paragraphs `**What the guard does and does not cover.**` (line 107), `That separation is deliberate...` (line 109), and `Two mechanisms may therefore legitimately lower...` (line 111).
- `CHANGELOG.md` — it currently opens with `## v0.26.3`; there is no `## Unreleased` section.

Relevant coding guides (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-doc-best-practices.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-precommit.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/documentation-guide.md`
</context>

<requirements>
## 1. Drop only the introduction branch from the command-path guard

In `pkg/command/task_update_frontmatter_executor.go`, inside `applyControllerOwnedGuard`, replace the `capDefinitionFields` loop:

```go
for _, field := range capDefinitionFields {
	if _, named := updates[field]; !named {
		continue
	}
	if onDisk, ok := frontmatter[field]; ok {
		merged[field] = onDisk
		continue
	}
	delete(merged, field)
}
```

with:

```go
for _, field := range capDefinitionFields {
	if _, named := updates[field]; !named {
		continue
	}
	if onDisk, ok := frontmatter[field]; ok {
		merged[field] = onDisk
	}
}
```

Resulting semantics, exactly:
- command names `max_triggers`, task carries an on-disk value → the on-disk value stays in `merged` (a command can neither raise nor lower an operator's budget);
- command names `max_triggers`, task carries none → the command's value stays in `merged` (introduced);
- command does not name `max_triggers` → nothing changes.

Do NOT touch the `trigger_scope` block below it (`delete(merged, "trigger_scope")` is a literal key and stays), and do NOT change `applyUpdateTriggerCap`. Because the guard runs before `applyUpdateTriggerCap` on the same `merged` map, a record already at `trigger_count >= 3` with an introduced cap escalates in this same write.

## 2. Rewrite the guard comments to state on-disk-wins-plus-introduction

2a. Replace the `capDefinitionFields` comment (currently: "An agent snapshot may neither raise one nor introduce one where the task has none... Only a task's own create sets it.") so the phrase `may neither raise one nor introduce one` is gone. State: the command-path guard keeps an existing on-disk value over a differing incoming value in both directions, but an incoming value MAY introduce the key when the task carries none; the executor's arm is the producer and writes `max_triggers: 3`; the result path keeps its own refusal in `result.controllerOwnedFields`. Suggested wording:

```go
// capDefinitionFields are the cap's definition. The command-path guard keeps an
// existing on-disk value over a differing incoming value in both directions, so
// a command can neither raise nor lower an operator's budget. An incoming value
// MAY introduce the key when the task carries none: the executor's arm publishes
// `max_triggers: 3` exactly that way, and refusing the introduction left the cap
// permanently disarmed. The result path keeps its own refusal (see
// result.controllerOwnedFields): an agent result payload never introduces the key.
var capDefinitionFields = []string{"max_triggers"}
```

2b. In the `applyControllerOwnedGuard` doc comment, replace the sentence fragment that says the guard covers "what an agent has no business writing at all — the cap's definition, and a scope without the count that always travels with it." with wording that matches the new rule: the guard covers an existing cap value (a differing incoming value loses) and a scope without the count that always travels with it; it deliberately does not refuse the introduction of an absent `max_triggers` on the command path. Keep the existing paragraphs about the deliberately unguarded `trigger_count` counter unchanged.

## 3. Invert the ref-only spec

In `pkg/command/task_update_frontmatter_executor_test.go`, replace the spec `It("does not let a snapshot introduce max_triggers on a ref-only task", ...)` (currently around line 264) with a spec that asserts the opposite. The spec name must not contain the substring `does not let a snapshot introduce`. Keep the same fixture shape (no `max_triggers` on disk, `ref: abc123`, `trigger_count: 3`, `assignee: pr-reviewer-agent`) and the same payload `Updates: lib.TaskFrontmatter{"max_triggers": 99}` so the introduced value is pinned rather than hidden. The trailing assertions must flip with the behavior: with the cap now 99 and `trigger_count: 3`, `applyUpdateTriggerCap` returns nil, so the assignee is NOT cleared and no escalation section is appended.

```go
It("introduces max_triggers when the command names it and the task has none", func() {
	// The executor's arm writes max_triggers: 3 into a record that by
	// definition carries none. The introduced value governs the predicate:
	// 99 is above trigger_count 3, so nothing escalates on this write.
	taskFile := writeTaskFile(
		"task.md",
		"---\ntask_identifier: intro-cap-uuid\nstatus: in_progress\nphase: planning\nassignee: pr-reviewer-agent\nref: abc123\ntrigger_count: 3\n---\nbody\n",
	)
	cmd := buildCmdObj(task.UpdateFrontmatterCommand{
		TaskIdentifier: lib.TaskIdentifier("intro-cap-uuid"),
		Updates:        lib.TaskFrontmatter{"max_triggers": 99},
	})
	_, _, err := executor.HandleCommand(ctx, nil, cmd)
	Expect(err).NotTo(HaveOccurred())

	fm := parseFrontmatter(taskFile)
	Expect(fm["max_triggers"]).To(BeEquivalentTo(99))
	Expect(fm["assignee"]).To(Equal("pr-reviewer-agent"))
	content, readErr := os.ReadFile(taskFile) // #nosec G304 -- test helper
	Expect(readErr).NotTo(HaveOccurred())
	Expect(string(content)).NotTo(ContainSubstring("## Trigger Cap Escalation"))
})
```

## 4. Keep the on-disk-wins control spec, fix its comment

In the same file, the spec `It("does not let a snapshot raise max_triggers out from under the cap", ...)` (currently around line 241) keeps ALL of its assertions unchanged: on-disk `max_triggers: 3` beats incoming `99`, the assignee is cleared, and `## Trigger Cap Escalation` is appended. Change only its leading comment so it no longer claims that an absent key stays absent — the second half of that sentence is now false. For example:

```go
// max_triggers is the cap's definition. A command that raises it in
// the same write would suppress the escalation, so the on-disk value
// wins over a differing incoming value.
```

Do not change the fixture, the payload, or any `Expect(` line in this spec.

## 5. Add the arm-shape spec

In the same `Context("controller-owned frontmatter")`, after the inverted spec, add a spec that drives the exact arm shape end to end through `HandleCommand`:

```go
It("introduces max_triggers: 3 when the executor arms a task that has none", func() {
	// The arm decision publishes Updates{"max_triggers": 3} for a task with
	// no max_triggers and no ref. The command path must land the key; the
	// executor's arm is the only producer of this shape.
	taskFile := writeTaskFile(
		"task.md",
		"---\ntask_identifier: arm-cap-uuid\nstatus: in_progress\nphase: planning\nassignee: replay-probe-agent\ntrigger_count: 2\n---\nbody\n",
	)
	cmd := buildCmdObj(task.UpdateFrontmatterCommand{
		TaskIdentifier: lib.TaskIdentifier("arm-cap-uuid"),
		Updates:        lib.TaskFrontmatter{"max_triggers": 3},
	})
	_, _, err := executor.HandleCommand(ctx, nil, cmd)
	Expect(err).NotTo(HaveOccurred())

	fm := parseFrontmatter(taskFile)
	Expect(fm["max_triggers"]).To(BeEquivalentTo(3))
})
```

## 6. Correct the two-guard split in `docs/controller-design.md`

6a. Rewrite the `**What the guard does and does not cover.**` paragraph (line 107) so it no longer says the typed commands are `*not* subject to the guard`. It must record two guards and their differing `max_triggers` rules. Suggested replacement:

```markdown
**What each guard covers.** Two guards exist and they are not the same code. The result-path guard is applied by `MergeFrontmatter`, which has exactly one call site: the result write-back path in `resultWriter`, and owns `controllerOwnedFields`. The command-path guard is `applyControllerOwnedGuard`, reached from `buildUpdatePatch` on the typed frontmatter commands (`## Typed Frontmatter Commands`); it owns `capDefinitionFields` and covers `max_triggers` only. For `max_triggers` the two paths differ by design: the result path never lets an agent payload introduce the key when the task carries none, while the command path lets a command that names the key introduce it when the task carries none — that is how the executor's arm writes `max_triggers: 3`. Both paths keep an existing on-disk value over a differing incoming value.
```

6b. Rewrite line 109 (`That separation is deliberate...`) so it no longer implies commands bypass any guard:

```markdown
The split is deliberate rather than an oversight. Both guards exist to reject a stale spawn-time snapshot that would silently roll back concurrent controller writes; neither is meant to freeze the file against the system's own deliberate, explicitly-addressed writes. An `UpdateFrontmatterCommand` naming a field is an intentional write; an agent result payload carrying that field is a side effect of when the job happened to start.
```

6c. Rewrite line 111 so "outside the guard" is not claimed for the command path:

```markdown
Two mechanisms may therefore legitimately lower a controller-owned counter, and both arrive as deliberate command-path writes that the command guard permits (the result-path guard still pins the on-disk value against any agent payload):
```

6d. Add a dedicated `max_triggers` row to the field-ownership table (the table whose header is `| Ownership | Fields | Rule |`, rows around lines 78-85). Place it after the `Controller-owned | trigger_count` row. The line must start with `| ` and contain a backticked `` `max_triggers` `` so the verification grep matches exactly one line. Use this row:

```markdown
| Controller-owned (result) / introduced by command | `max_triggers` | The result path keeps the on-disk value and an agent payload can neither raise, lower, nor introduce the key when the task carries none. The command path keeps an existing on-disk value over a differing incoming value in both directions, but an `UpdateFrontmatterCommand` that names the key may introduce it when the task carries none — the executor's arm writes `max_triggers: 3` this way, and the introduced value governs the cap predicate. |
```

## 7. Record the fix in `CHANGELOG.md`

Create a new `## Unreleased` section above the newest released heading (`## v0.26.3`), with a bullet that mentions `max_triggers`. The bullet must sit within two lines after the `## Unreleased` heading. For example:

```markdown
## Unreleased

- fix: let the command path introduce `max_triggers` when the task carries none, so the executor's arm actually arms the trigger cap; an existing value still wins and an agent result payload still cannot introduce or change it
```

## 8. Leave the result path untouched

Do not modify `pkg/result/result_writer.go` or any file under `pkg/result/`. `MergeFrontmatter` and `controllerOwnedFields` keep `max_triggers` with both halves of their rule (absent stays absent; existing value wins). The existing `pkg/result` specs for `max_triggers`, `trigger_count`, and `trigger_scope` must pass unchanged.
</requirements>

<constraints>
- **Two guards, one change.** `applyControllerOwnedGuard` (`pkg/command/task_update_frontmatter_executor.go`) owns the command path and is the only thing this change modifies. `MergeFrontmatter` (`pkg/result/result_writer.go`) owns the result path and keeps `max_triggers` in `controllerOwnedFields`; do not touch it.
- **Only the introduction branch changes.** The "on-disk wins" half of the command-path guard is load-bearing and stays.
- **Do not constrain the introduced value.** The guard must accept whatever value the command names; a rule keyed to the constant 3 would break when `defaultMaxTriggers` changes.
- **`trigger_count` / `trigger_scope` keep their current command-path handling**, including the deliberate non-guarding of the counter.
- The command-path guard emits no `GuardDecision` and logs only discards; do not add logging or a decision type on this path.
- **Concurrency:** the command path applies a field-level PATCH of only the keys the command names. Do not convert it to a whole-frontmatter write.
- Repo conventions are frozen: Ginkgo/Gomega v2, `github.com/bborbe/errors` wrapping, counterfeiter mocks, glog `V(n)` gating.
- CHANGELOG: create `## Unreleased` above the newest released section rather than editing a released section.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
</constraints>

<verification>
Run all of the following; each must hold.

- `make precommit` exits 0.
- `go test ./pkg/command/... ./pkg/result/...` exits 0.
- `grep -c 'delete(merged, field)' pkg/command/task_update_frontmatter_executor.go` returns `0` (the `trigger_scope` branch deletes a literal key and is unaffected).
- `grep -c 'does not let a snapshot introduce' pkg/command/task_update_frontmatter_executor_test.go` returns `0`.
- `grep -c 'may neither raise one nor introduce one' pkg/command/task_update_frontmatter_executor.go` returns `0`.
- `grep -cF 'are therefore *not* subject to the guard' docs/controller-design.md` returns `0`.
- ``grep -c '^| .*`max_triggers`' docs/controller-design.md`` returns `1`.
- `grep -A2 '## Unreleased' CHANGELOG.md | grep -i max_triggers` returns at least one line.
- `git diff --stat pkg/result/` prints nothing (the result path must be untouched).
</verification>
