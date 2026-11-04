---
status: draft
tags:
  - dark-factory
  - spec
---

## Summary

- The frontmatter-command guard deletes `max_triggers` from an update whenever the record does not already carry the key, so the executor's arm can never introduce the cap.
- That rule was written when nothing but a task's own create set the key. The executor's arm gate is now the intended setter, and it writes `max_triggers: 3` into a record that by definition has none — exactly the shape the arm exists for.
- The arm decision is logged and the rest of the same command lands (`current_job`, the `## Failure` body); the one key that stops the loop is silently discarded and the task keeps re-queueing.
- The fix allows the **command** path to introduce the key when the record carries none, while the **result** path keeps its refusal — so an agent result payload still cannot introduce or raise the cap.

## Problem

The executor arms the trigger cap by publishing an `UpdateFrontmatterCommand` whose `Updates` carry `max_triggers: 3` (`crashFailureUpdates` in `bborbe/agent-task-executor`). The controller applies that command through `applyControllerOwnedGuard` (`pkg/command/task_update_frontmatter_executor.go`), which for every key in `capDefinitionFields` does:

```go
if onDisk, ok := frontmatter[field]; ok {
    merged[field] = onDisk
    continue
}
delete(merged, field)
```

`capDefinitionFields` is `["max_triggers"]`. The doc above it says the guard covers "what an agent has no business writing at all — the cap's definition". That is right for an agent result payload and wrong for the executor's arm, which is the controller's own fleet component publishing a deliberate decision.

The operator-visible effect is the original defect unchanged: a Job whose pod can never be scheduled re-queues until a human clears the assignee, while the executor log says the cap was armed.

**There are two guards, and they are not the same code.** `MergeFrontmatter` (`pkg/result/result_writer.go`) has exactly one call site — the result write-back at `:729` — and owns `controllerOwnedFields` = `{trigger_count, trigger_scope, max_triggers}`. The frontmatter-command path does **not** go through it; it has its own guard, `applyControllerOwnedGuard`, with its own list. `docs/controller-design.md:107` states this correctly. This spec changes **only** the command-path guard.

## Reproduction

Live, nuke dev, executor `v0.56.2`, 2026-11-04. A replay task with no `max_triggers` and no `ref`, assigned to a Config requesting `memory: 500Gi` against a 28 Gi node, so its pod stays `Pending`/`Unschedulable`.

```
$ kubectlnukedev -n agent logs deploy/agent-task-executor-personal --since=20m | grep crash_arm_decision
event=crash_arm_decision task=626f12a9-… reason="pod_not_scheduled" parsed="pod_not_scheduled"
  parsed_ok=true decision=arm cached_max_triggers=<absent> max_triggers=3
```

The same command's other effects reached the record — `current_job: ""` and a `## Failure` section stamped `2026-11-04T15:49:37Z` — so this is not a lost write. The frontmatter at that commit:

```
assignee: replay-probe-agent
trigger_count: 2
current_job: ""
```

No `max_triggers`. The sweeper re-published every 60 s afterwards, each suppressed by the executor's per-job dedupe (1 h TTL), so the gate never ran again and the arm was never retried.

Unit-level, deterministic: the existing spec `pkg/command/task_update_frontmatter_executor_test.go:264` — "does not let a snapshot introduce max_triggers on a ref-only task", asserting `Expect(fm).NotTo(HaveKey("max_triggers"))` — reproduces the defect directly. **That spec is expected to invert.**

## Expected vs Actual

**Expected** — the executor's documented contract: an armable failure writes `max_triggers: 3` into the record, `TriggerCapEngaged` becomes true, and the escalation clears the assignee on the third trigger.

**Actual** — `TriggerCapEngaged` stays false, `trigger_count` accrues without bound, and the escalation never fires.

## Goal

An `UpdateFrontmatterCommand` that names `max_triggers` introduces it when the record has none, and is still refused when the record already carries a value. The result path is unchanged: an agent result payload can neither introduce the key nor change it.

## Non-goals

- **Changing the result-path guard.** `controllerOwnedFields` in `pkg/result/result_writer.go` keeps `max_triggers` with both halves of its rule. An agent result payload is built from a spawn-time snapshot and must never introduce or raise the cap — that guard stays exactly as it is.
- **Removing the command-path guard.** The "on-disk wins" half is load-bearing: a stale snapshot must not lower an operator's budget of 5 to 3, and must not raise it either. Only the introduction half changes.
- **`trigger_count` / `trigger_scope`.** Both keep their current command-path handling, including the deliberate refusal to guard the counter (`triggerBudgetWrite`'s `scopeChanged` arm writes `trigger_count: 1` on purpose).
- **A new command type.** The executor already publishes `UpdateFrontmatterCommand`; a second shape for one key duplicates the write path for no benefit.
- **Fixing the executor side.** That half is shipped and verified on dev `v0.56.2`.

## Assumptions

- The only producer that carries `max_triggers` on an `UpdateFrontmatterCommand` is the executor's `crashFailureUpdates`, and it writes only when the key is absent. Verified in `bborbe/agent-task-executor`.
- `applyControllerOwnedGuard` is the sole command-path guard; `MergeFrontmatter` is not reachable from it (one call site, `result_writer.go:729`).
- `TriggerCapEngaged` reads the key as a presence check, so introduction is sufficient — no value semantics change.

## Security / Abuse Cases

The introduction path is a widening, so the threat is stated rather than assumed away:

| Actor | Can it now introduce `max_triggers`? | Why |
|---|---|---|
| An agent **result payload** | **No** | It merges through `MergeFrontmatter`, whose `controllerOwnedFields` still deletes an absent key. Unchanged by this spec. |
| An agent-authored `UpdateFrontmatterCommand` naming the key | **Yes** | This is the residual widening. Bounded by the fact that only the executor publishes this command shape; an agent cannot reach the command topic. Detection is nil — introduction emits no `GuardDecision` and no log line on either path, which is pre-existing behaviour for this guard. |
| A stale executor snapshot | **Yes** | Introduces the default 3, never a value of the snapshot's choosing; a later operator value still wins because on-disk beats incoming. |

Accepted risk: the widening is confined to the command path, whose only legitimate publisher is the controller's own executor. The alternative — a dedicated command type — was rejected as duplicating the write path for one key.

## Acceptance Criteria

- [ ] The command path introduces `max_triggers` when the record has none — evidence: a `pkg/command` spec drives the update executor with `Updates{"max_triggers": 3}` against a frontmatter carrying no `max_triggers`, and asserts the merged frontmatter carries `max_triggers: 3`; `go test ./pkg/command/...` exits 0
- [ ] The command path still refuses an existing value — evidence: a spec with on-disk `max_triggers: 3` and `Updates{"max_triggers": 99}` asserts the merged value is `3`; `go test ./pkg/command/...` exits 0
- [ ] The ref-only regression spec is inverted, not deleted — evidence: `task_update_frontmatter_executor_test.go:264` now asserts `Expect(fm).To(HaveKeyWithValue("max_triggers", 3))` for an arm-shaped payload and is renamed to say so; `go test ./pkg/command/...` exits 0
- [ ] The result path is unchanged — evidence: the existing `pkg/result` `MergeFrontmatter` specs for `max_triggers`, `trigger_count` and `trigger_scope` all pass with no edit; `go test ./pkg/result/...` exits 0
- [ ] `docs/controller-design.md` § field ownership lists `max_triggers` with the command-path rule — evidence: `grep -n 'max_triggers' docs/controller-design.md` returns ≥1 line and the section states the two-guard split
- [ ] `make precommit` exits 0
- [ ] CHANGELOG carries the fix — evidence: `grep -A2 '## Unreleased' CHANGELOG.md | grep -i max_triggers` returns ≥1 line
- [ ] **Post-Deploy (Rung-2):** on nuke dev, a replay task with no `max_triggers` whose pod can never schedule carries `max_triggers: 3` after the executor's arm decision — evidence: the executor logs `decision=arm max_triggers=3` and the record's frontmatter shows `max_triggers: 3`
  - `deploy_check:` `kubectlnukedev -n agent get deploy agent-task-controller-personal -o jsonpath='{.spec.template.spec.containers[0].image}' | awk -F: '{print $NF}'`
  - `deploy_target:` `$(git describe --tags --abbrev=0 origin/master)` — resolved at verification time to the tag the release cut produced.

## Verification

### Container-executable

- `make precommit` — exits 0
- `go test ./pkg/command/... ./pkg/result/...` — exits 0; the inverted ref-only spec and the untouched result-path specs are green
- `grep -n 'capDefinitionFields' pkg/command/task_update_frontmatter_executor.go` — returns ≥1 line

### Operator-executable

- `kubectlnukedev -n agent logs deploy/agent-task-executor-personal --since=30m | grep 'decision=arm'` — names the replay task
- the replay record's frontmatter carries `max_triggers: 3`

## Desired Behavior

1. An `UpdateFrontmatterCommand` that names `max_triggers` is never discarded merely because the record lacks the key; it is discarded only when the record already carries one.
2. When the record carries `max_triggers`, the on-disk value wins over a differing incoming value, in both directions — a stale snapshot can neither raise nor lower an operator's budget.
3. An agent result payload can neither introduce `max_triggers` nor change an existing one; the result-path guard is unchanged.
4. `trigger_count` and `trigger_scope` keep their current command-path handling, including the deliberate non-guarding of the counter.
5. `TriggerCapEngaged` still reads the key as a presence check, so an introduced `max_triggers: 3` engages the cap on the next controller pass.

## Constraints

- **Two guards, one change.** `applyControllerOwnedGuard` (`pkg/command/task_update_frontmatter_executor.go`) owns the command path and is the only thing this spec changes. `MergeFrontmatter` (`pkg/result/result_writer.go`) owns the result path and keeps `max_triggers` in `controllerOwnedFields`. `docs/controller-design.md:107` already documents the split correctly; the doc's field-ownership table needs `max_triggers` added with the new rule.
- The command-path guard emits **no** `GuardDecision` and logs only discards, so introduction is silent. That is pre-existing for this path and is recorded in § Security / Abuse Cases rather than changed here.
- Repo conventions are frozen: Ginkgo/Gomega v2, `github.com/bborbe/errors` wrapping, counterfeiter mocks, glog `V(n)` gating.
- CHANGELOG: add an `## Unreleased` bullet.

## Failure Modes

| Trigger | Expected behavior | Detection |
|---|---|---|
| A stale executor snapshot names `max_triggers` on a record with none | Introduced as the default 3 | None — silent by design on this path |
| A stale snapshot names a different value than on disk | On-disk value kept, incoming discarded | None on this path (the result path reports a `GuardDecision`) |
| An agent result payload names `max_triggers` | Refused, key stays absent | `GuardDecision` on the result path |
| A legitimate budget reset to a lower value | Operator sets it directly; the guard does not block an on-disk value | None needed |
| The key is introduced on a terminal record | The terminal-status pin and `restoreExistingPhase` do not read `max_triggers` | None needed |

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Drop the introduction-refusal from `applyControllerOwnedGuard`, invert the ref-only spec and add the refusal spec, add the doc row, add the CHANGELOG bullet | 1-5 | 1-7 | — |

## Do-Nothing Option

Not acceptable. The executor half is shipped and verified on dev; without this the arm is inert and the defect it was built to close stays open, with an added cost — an operator reading the executor log is told the cap was armed when it was not.
