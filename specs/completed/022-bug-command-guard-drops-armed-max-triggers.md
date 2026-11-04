---
status: completed
tags:
    - dark-factory
    - spec
approved: "2026-11-04T16:16:28Z"
generating: "2026-11-04T16:22:04Z"
prompted: "2026-11-04T16:29:59Z"
verifying: "2026-11-04T16:45:22Z"
completed: "2026-11-04T16:45:30Z"
branch: dark-factory/bug-command-guard-drops-armed-max-triggers
---

## Summary

- The frontmatter-command guard deletes `max_triggers` from an update whenever the record does not already carry the key, so the executor's arm can never introduce the cap.
- That rule was written when nothing but a task's own create set the key. The executor's arm gate is now the intended setter, and it writes `max_triggers: 3` into a record that by definition has none — exactly the shape the arm exists for.
- The arm decision is logged and the rest of the same command lands (`current_job`, the `## Failure` body); the one key that stops the loop is silently discarded and the task keeps re-queueing.
- The fix lets the **command** path introduce the key when the record carries none, while the **result** path keeps its refusal — so an agent result payload still cannot introduce or change the cap.

## Problem

The executor arms the trigger cap by publishing an `UpdateFrontmatterCommand` whose `Updates` carry `max_triggers` (`crashFailureUpdates` in `bborbe/agent-task-executor@v0.56.2`: it sets `updates["max_triggers"] = defaultMaxTriggers` only when the task's frontmatter has no such key). The controller applies that command through `applyControllerOwnedGuard` (`pkg/command/task_update_frontmatter_executor.go:309`), which for every key in `capDefinitionFields` (`:291`, `["max_triggers"]`) does:

```go
for _, field := range capDefinitionFields {
    if _, named := updates[field]; !named {
        continue
    }
    if onDisk, ok := frontmatter[field]; ok {
        merged[field] = onDisk
        continue
    }
    delete(merged, field)     // ← :322, the branch this spec removes
}
```

The doc above the guard says it covers "what an agent has no business writing at all — the cap's definition". That is right for an agent result payload and wrong for the executor's arm, which is the controller's own fleet component publishing a deliberate decision.

The operator-visible effect is the original defect unchanged: a Job whose pod can never be scheduled re-queues until a human clears the assignee, while the executor log says the cap was armed.

**There are two guards, and they are not the same code.** `MergeFrontmatter` (`pkg/result/result_writer.go:1307`) has exactly one call site — the result write-back at `:729` — and owns `controllerOwnedFields` (`:1198`). The frontmatter-command path does **not** go through it; `buildUpdatePatch` (`:255`, sole caller `:172`) calls `applyControllerOwnedGuard`, which has its own list. This spec changes **only** the command-path guard.

⚠️ `docs/controller-design.md:107` currently states that the typed frontmatter commands "are therefore *not* subject to the guard". **That sentence is wrong** — there is a guard on this path, and it is the one that dropped the arm. Correcting that sentence is part of this work (AC 5), not optional documentation polish: leaving it will re-create this same bug report.

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

Unit-level, deterministic: `pkg/command/task_update_frontmatter_executor_test.go:264` — "does not let a snapshot introduce max_triggers on a ref-only task", a task with `ref: abc123`, `trigger_count: 3` and `Updates{"max_triggers": 99}`, asserting `Expect(fm).NotTo(HaveKey("max_triggers"))` — reproduces the defect directly. **That spec inverts, and its payload stays `99`** so the widening below is pinned rather than hidden.

## Goal

After this work an armable failure actually arms the cap: the command path introduces `max_triggers`, `TriggerCapEngaged` turns true, and the controller's escalation clears the assignee on the third trigger — while an agent result payload still cannot introduce or change the key.

## Expected vs Actual

**Expected** — the executor's documented contract: an armable failure writes `max_triggers: 3` into the record, `TriggerCapEngaged` becomes true, and the escalation clears the assignee on the third trigger.

**Actual** — `TriggerCapEngaged` stays false, `trigger_count` accrues without bound, and the escalation never fires.

## Desired Behavior

> **Amendment 2026-11-04 — the introduction is bounded.** As shipped (`801ce1b`), item 1 below is **narrowed**: a command may introduce `max_triggers` only at or below the library default (`TaskFrontmatter.MaxTriggers()`, 3). A higher value is refused and the key stays absent.
>
> The reason is the premise § Security / Abuse Cases rests on. This spec asserted "the command topic's sole publisher is the controller's own executor, and an agent cannot reach it" — but `docs/controller-design.md` records the opposite: an agent **does** emit `UpdateFrontmatterCommand` (the `Agent emits UpdateFrontmatterCommand` row of the assignee table). Unbounded introduction on that path lets an agent raise its own cap and escape the escalation the cap exists to produce. The bound is the minimum that closes that hole while leaving the arm working.
>
> Consequences for the sections below: item 1 and the § Non-goals bullet *Constraining the introduced value* are superseded by this amendment; the second and third rows of the § Security table are now "yes, at or below the default" and "yes, at the value in `Updates` (always 3 for the arm)"; AC1 now asserts refusal of `99` rather than its landing. The default is **derived** from the library rather than restated as a literal, which retires this spec's own objection to keying the rule to the constant 3 — the rule follows the library default instead of duplicating it.

1. An `UpdateFrontmatterCommand` that names `max_triggers` introduces **the value it names** when the record has no such key. The guard no longer constrains the introduced value; the executor's arm is the only producer and it writes `defaultMaxTriggers` (3). *(Narrowed by the amendment above: only values at or below the library default land.)*
2. When the record already carries `max_triggers`, the on-disk value wins over a differing incoming value in both directions — a command can neither raise nor lower an operator's budget.
3. An agent result payload can neither introduce `max_triggers` nor change an existing one; the result-path guard is unchanged.
4. `trigger_count` and `trigger_scope` keep their current command-path handling, including the deliberate non-guarding of the counter.
5. `TriggerCapEngaged` still reads the key as a presence check. The cap engages on **this** write or the next controller pass — `applyUpdateTriggerCap` runs after the guard on the same merged map, so a record already at `trigger_count >= 3` escalates in the same write.

## Non-goals

- **Changing the result-path guard.** `controllerOwnedFields` in `pkg/result/result_writer.go` keeps `max_triggers` with both halves of its rule. An agent result payload is built from a spawn-time snapshot and must never introduce or change the cap.
- **Removing the command-path guard.** The "on-disk wins" half is load-bearing. Only the introduction branch changes.
- **Constraining the introduced value.** Rejected: a rule keyed to the constant 3 would break the moment `defaultMaxTriggers` changes or another controller component legitimately arms a different cap. The widening is stated in § Security / Abuse Cases instead. *(Superseded by the 2026-11-04 amendment under § Desired Behavior: the value IS constrained, to the library default, which is derived rather than restated so the stated objection does not apply. See also § Security / Abuse Cases.)*
- **`trigger_count` / `trigger_scope`.** Both keep their current command-path handling, including the deliberate refusal to guard the counter (`triggerBudgetWrite`'s `scopeChanged` arm writes `trigger_count: 1` on purpose).
- **A new command type.** The executor already publishes `UpdateFrontmatterCommand`; a second shape for one key duplicates the write path for no benefit.
- **Fixing the executor side.** That half is shipped and verified on dev `v0.56.2`.

## Assumptions

- The only producer that carries `max_triggers` on an `UpdateFrontmatterCommand` is the executor's `crashFailureUpdates`, and it writes only when the key is absent, with `defaultMaxTriggers` = 3. Source cited in § Problem; the executor repo is not mounted in this container.
- `applyControllerOwnedGuard` is the sole command-path guard; `MergeFrontmatter` is not reachable from it (one call site, `result_writer.go:729`).
- `TriggerCapEngaged` reads the key as a presence check (`result_writer.go:848`), so introduction is sufficient — no value semantics change.

## Security / Abuse Cases

The introduction branch is a widening, so the residual risk is stated rather than assumed away:

| Actor | Can it now introduce `max_triggers`? | At what value | Detection |
|---|---|---|---|
| An agent **result payload** | **No** | — | `GuardDecision` on the result path (unchanged) |
| An agent-authored `UpdateFrontmatterCommand` naming the key | **Yes** | **Whatever value the command names** — the guard no longer constrains it | None; this path logs only discards |
| A stale executor snapshot | **Yes** | The value in `Updates`, which for the arm is always 3 | None |

Accepted risk, named: the guard's protection on this path reduces to "an existing value is never changed". Introduction is unconstrained in value. That is acceptable **only** because the command topic's sole publisher is the controller's own executor, and an agent cannot reach it; the blast radius if that premise breaks is a task whose cap is set arbitrarily high, which suppresses the escalation the cap exists to produce, with no log line. If a second producer ever appears on this topic, this decision must be revisited.

> **Superseded 2026-11-04.** The premise above is false: `docs/controller-design.md` records that an agent **does** emit `UpdateFrontmatterCommand` (the `Agent emits UpdateFrontmatterCommand` row of the assignee table), so the blast radius this paragraph accepted is reachable. Introduction is therefore bounded at the library default — see the amendment under § Desired Behavior. The table rows above now read: agent-authored command — **yes, at or below the default**; stale executor snapshot — **yes, at the value in `Updates`, always 3 for the arm**.

## Acceptance Criteria

- [ ] The command path introduces a value at or below the library default, and refuses one above it — evidence: three specs at `pkg/command/task_update_frontmatter_executor_test.go` — `Updates{"max_triggers": 99}` on a task with none asserts `NotTo(HaveKey("max_triggers"))` (the amendment above; originally this spec asserted `99` landed); `Updates{"max_triggers": 4}` on a task with none asserts the key stays absent, pinning the 3/4 boundary an off-by-one would widen; and `Updates{"max_triggers": 2}` on a task with none asserts the key lands as `2` with `assignee` unchanged, pinning that a below-default value is introduced rather than refused. The arm shape is separately pinned by the `max_triggers: 3` spec, which also asserts `assignee` cleared and `## Trigger Cap Escalation` appended — that is the evidence the introduced value governs the predicate rather than merely landing; `go test ./pkg/command/...` exits 0
- [ ] The inverted spec's name and the guard's doc comment stop asserting the old rule — evidence: `grep -c 'does not let a snapshot introduce' pkg/command/task_update_frontmatter_executor_test.go` returns `0` (it returns `1` today) and `grep -c 'may neither raise one nor introduce one' pkg/command/task_update_frontmatter_executor.go` returns `0` (it returns `1` today); the replacement comment states on-disk-wins-plus-introduction
- [ ] The command path still refuses an existing value — evidence: a spec with on-disk `max_triggers: 3` and `Updates{"max_triggers": 99}` asserts the merged value is `3`; `go test ./pkg/command/...` exits 0
- [ ] The arm shape works end to end — evidence: a spec driving `HandleCommand` with `Updates{"max_triggers": 3}` on a task with no `max_triggers` asserts the file carries `max_triggers: 3`; `go test ./pkg/command/...` exits 0
- [ ] The result path is unchanged — evidence: the existing `pkg/result` `MergeFrontmatter` specs for `max_triggers`, `trigger_count` and `trigger_scope` pass, and `git diff --stat pkg/result/` is empty; `go test ./pkg/result/...` exits 0
- [ ] `docs/controller-design.md` records the two-guard split — evidence: `grep -cF 'are therefore *not* subject to the guard' docs/controller-design.md` returns `0` (it returns `1` today; the asterisks make a plain grep vacuous), the field-ownership table gains a **dedicated** `max_triggers` row whose rule text names **both** paths (result: absent stays absent; command: absent is introduced) so that

```
grep -c '^| .*`max_triggers`' docs/controller-design.md
```

returns `1` (it returns `0` today), and the dependent claims at `:109` ("deliberate rather than an oversight") and `:111` ("outside the guard") are reconciled so the page does not contradict itself
- [ ] `make precommit` exits 0
- [ ] CHANGELOG carries the fix — evidence: an `## Unreleased` section is created above the newest version heading and carries a `max_triggers` bullet; `grep -A2 '## Unreleased' CHANGELOG.md | grep -i max_triggers` returns ≥1 line
- [ ] **Post-Deploy (Rung-2):** on nuke dev, a replay task with no `max_triggers` whose pod can never schedule carries `max_triggers: 3` after the executor's arm decision — evidence: the executor logs `decision=arm max_triggers=3` and the record's frontmatter shows `max_triggers: 3`
  - `deploy_check:` `kubectlnukedev -n agent get sts agent-task-controller-personal -o jsonpath='{.spec.template.spec.containers[0].image}' | awk -F: '{print $NF}'` — the workload is a **StatefulSet**, confirmed on nuke dev: `get sts` returns it, `get deploy` returns NotFound.
  - `deploy_target:` `v0.26.4` — the tag this change's release cuts; confirm with `git tag --contains <merge-sha>` before reading the deploy as correct.

## Verification

### Container-executable

- `make precommit` — exits 0
- `go test ./pkg/command/... ./pkg/result/...` — exits 0; the inverted ref-only spec and the untouched result-path specs are green
- `grep -c 'delete(merged, field)' pkg/command/task_update_frontmatter_executor.go` — returns `0` (the `trigger_scope` branch deletes a literal key and is unaffected)

### Operator-executable

- `kubectlnukedev -n agent logs deploy/agent-task-executor-personal --since=30m | grep 'decision=arm'` — names the replay task
- the replay record's frontmatter carries `max_triggers: 3`

## Constraints

- **Two guards, one change.** `applyControllerOwnedGuard` (`pkg/command/task_update_frontmatter_executor.go`) owns the command path and is the only thing this spec changes. `MergeFrontmatter` (`pkg/result/result_writer.go`) owns the result path and keeps `max_triggers` in `controllerOwnedFields`.
- The command-path guard emits **no** `GuardDecision` and logs only discards, so introduction is silent. Pre-existing for this path; recorded in § Security / Abuse Cases rather than changed here.
- **Concurrency.** The command path applies a field-level PATCH of only the keys the command names; the result path's `delete(merged, field)` (`result_writer.go:1327`) fires only when the incoming payload names the key, and an agent result payload never carries `max_triggers`. A concurrent result write therefore cannot drop a freshly introduced cap.
- Repo conventions are frozen: Ginkgo/Gomega v2, `github.com/bborbe/errors` wrapping, counterfeiter mocks, glog `V(n)` gating.
- CHANGELOG: the repo's newest section is a released version, so create `## Unreleased` above it rather than editing a released section.

## Failure Modes

| Trigger | Expected behavior | Recovery | Detection |
|---|---|---|---|
| A stale executor snapshot names `max_triggers` on a record with none | Introduced at the named value (the arm writes 3) | Operator sets `max_triggers: <N>` in the task frontmatter; the next command write keeps it (on-disk wins), verified with `grep '^max_triggers:'` on the file | None — silent on this path |
| A command names a different value than the record carries | On-disk value kept, incoming discarded | None needed | None on this path (result path reports a `GuardDecision`) |
| An agent result payload names `max_triggers` | Refused, key stays absent | None needed | `GuardDecision` on the result path |
| The record already sits at `trigger_count >= 3` when the arm introduces the cap | Escalation fires in the same write — assignee cleared, `## Trigger Cap Escalation` appended | None — this is the intended end state | The escalation section appears |
| The key is introduced on a terminal record | The terminal-status pin and `restoreExistingPhase` do not read `max_triggers` | None needed | None |

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Drop the introduction branch from `applyControllerOwnedGuard`, rewrite the guard's doc comment (`:286-303`) to state on-disk-wins-plus-introduction, invert the ref-only spec including its trailing assignee/escalation assertions, add the refusal and arm-shape specs, correct `docs/controller-design.md:107-111` and add the ownership row, create `## Unreleased` with the bullet | 1-5 | 1-7 | — |

## Do-Nothing Option

Not acceptable. The executor half is shipped and verified on dev; without this the arm is inert and the defect it was built to close stays open, with an added cost — an operator reading the executor log is told the cap was armed when it was not.
