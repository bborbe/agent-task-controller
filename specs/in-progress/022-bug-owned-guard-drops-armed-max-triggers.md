---
status: draft
---

## Summary

- `max_triggers` sits in `controllerOwnedFields`, whose merge rule deletes the key from the incoming payload whenever it is absent on disk. An incoming value can therefore never introduce the cap.
- That was correct while nothing but a task's own create set it. It is now wrong: the executor's arm gate is the intended setter, and it writes `max_triggers: 3` into a record that by definition has none — which is exactly the record shape the arm exists for.
- The arm decision is logged, the rest of the same command lands (`current_job`, the `## Failure` body), and the one key that stops the loop is silently discarded. The task keeps re-queueing.
- The fix keeps "on-disk wins" — an agent snapshot can never raise or lower an operator budget — and allows the key to be **introduced** when the record carries none.

## Problem

The executor's arm gate (`crashFailureUpdates` in `bborbe/agent-task-executor`) writes `max_triggers: 3` on a failure it classifies as armable. The controller merges that frontmatter update through `MergeFrontmatter` (`pkg/result/result_writer.go`), which for every key in `controllerOwnedFields` does:

```go
if !onDisk {
    if inIncoming {
        delete(merged, field)
    }
    continue
}
```

`controllerOwnedFields` is `{trigger_count, trigger_scope, max_triggers}`. For `trigger_count` and `trigger_scope` the rule is right — they are measurements, and a stale snapshot must not reintroduce them. For `max_triggers` it is a definition the executor is now supposed to set, so the guard makes the arm inert in exactly the case it was built for.

The operator-visible effect is the original defect unchanged: a Job whose pod can never be scheduled re-queues until a human clears the assignee, while the executor log says the cap was armed.

## Reproduction

Live, nuke dev, executor `v0.56.2`, 2026-11-04. A replay task with no `max_triggers` and no `ref`, assigned to a Config requesting `memory: 500Gi` against a 28 Gi node, so its pod stays `Pending`/`Unschedulable`.

```
$ kubectlnukedev -n agent logs deploy/agent-task-executor-personal --since=20m | grep crash_arm_decision
event=crash_arm_decision task=626f12a9-… reason="pod_not_scheduled" parsed="pod_not_scheduled"
  parsed_ok=true decision=arm cached_max_triggers=<absent> max_triggers=3
```

The same command's other effects reached the record — `current_job: ""` and a `## Failure` section stamped `2026-11-04T15:49:37Z` — so this is not a lost write. The frontmatter of the file at that commit:

```
assignee: replay-probe-agent
trigger_count: 2
current_job: ""
```

No `max_triggers`. The sweeper re-published every 60 s afterwards, each suppressed by the executor's per-job dedupe (1 h TTL), so the gate never ran again and the arm was never retried.

Unit-level, deterministic: `MergeFrontmatter(existing{"status":"in_progress"}, incoming{"max_triggers":3})` returns a map with no `max_triggers`.

## Expected vs Actual

**Expected** — the executor's documented contract, and the premise its own acceptance criteria are written on: an armable failure writes `max_triggers: 3` into the record, `TriggerCapEngaged` becomes true, and the controller's escalation clears the assignee on the third trigger.

**Actual** — `TriggerCapEngaged` stays false, `trigger_count` accrues without bound, and the escalation never fires.

## Goal

A frontmatter update that carries `max_triggers` introduces it when the record has none, and is still refused when the record already carries a value. Every other controller-owned key keeps its current behaviour exactly.

## Non-goals

- **Removing `max_triggers` from the guard entirely.** The "on-disk wins" half is load-bearing: a stale agent snapshot that raises the cap would suppress the escalation the cap exists to produce. Only the introduction half changes.
- **Changing `trigger_count` or `trigger_scope`.** Both stay controller-owned with both halves of the rule intact.
- **A new command type.** The executor already publishes `UpdateFrontmatterCommand`; a second command shape for one key would duplicate the write path for no benefit.
- **Fixing the executor side.** That half is shipped and verified: it arms and logs correctly on dev `v0.56.2`.

## Assumptions

- The executor is the only writer that carries `max_triggers` on a `UpdateFrontmatterCommand`. Verified in `bborbe/agent-task-executor`: `crashFailureUpdates` is the sole producer, and it only writes when the key is absent.
- `operatorOwnedFields` already implements exactly the semantics needed here — on-disk wins when present, incoming may introduce when absent — so this is a reclassification, not new machinery.

## Acceptance Criteria

- [ ] An update carrying `max_triggers` introduces it when the record has none — evidence: a `MergeFrontmatter` spec with `existing` lacking the key and `incoming{"max_triggers": 3}` asserts the merged map carries `max_triggers: 3` and returns no guard decision; `go test ./pkg/...` exits 0
- [ ] An existing value is never overwritten — evidence: `existing{"max_triggers": 5}` with `incoming{"max_triggers": 3}` asserts the merged value is `5` and a `GuardDecision` names the field; `go test ./pkg/...` exits 0
- [ ] `trigger_count` and `trigger_scope` keep both halves of the controller-owned rule — evidence: the existing specs for those keys pass unchanged, including the "absent on disk stays absent even when incoming carries it" case; `go test ./pkg/...` exits 0
- [ ] The behaviour is reachable end to end — evidence: a spec that runs the frontmatter-update path with an arm-shaped payload asserts the resulting record carries `max_triggers`; `go test ./pkg/...` exits 0
- [ ] `make precommit` exits 0
- [ ] CHANGELOG carries the fix — evidence: `grep -A2 '## Unreleased' CHANGELOG.md | grep -i max_triggers` returns ≥1 line
- [ ] **Post-Deploy (Rung-2):** on nuke dev, a replay task with no `max_triggers` whose pod can never schedule carries `max_triggers: 3` after the executor's arm decision — evidence: the executor logs `decision=arm max_triggers=3` and `vault-cli --vault private-personal task get "<replay task>" max_triggers --output json` reports `3`

## Verification

### Container-executable

- `make precommit` — exits 0
- `go test ./pkg/result/...` — exits 0; the new introduction spec and the unchanged refusal specs are green

### Operator-executable

- `kubectlnukedev -n agent logs deploy/agent-task-executor-personal --since=30m | grep 'decision=arm'` — names the replay task
- `vault-cli --vault private-personal task get "<replay task>" max_triggers --output json` — reports `3`

## Desired Behavior

1. `max_triggers` is no longer in `controllerOwnedFields`.
2. When the record already carries `max_triggers`, the on-disk value wins and a differing incoming value is reported as a `GuardDecision` — unchanged from today.
3. When the record carries no `max_triggers` and the incoming update carries one, the merged frontmatter carries the incoming value.
4. When neither side carries it, the key stays absent.
5. `trigger_count` and `trigger_scope` keep both halves of the controller-owned rule: on-disk wins when present, and a key absent on disk stays absent even when the incoming payload carries it.

## Constraints

- The guard lives in `MergeFrontmatter` (`pkg/result/result_writer.go`), which is shared by the result-write path and the frontmatter-command path. Changing it changes both, so the refusal half must stay provably intact.
- Repo conventions are frozen: Ginkgo/Gomega v2, `github.com/bborbe/errors` wrapping, counterfeiter mocks, glog `V(n)` gating.
- The change must not alter how `TriggerCapEngaged` reads the key — it stays a presence check.
- CHANGELOG: add an `## Unreleased` bullet.

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---|---|---|
| A stale agent snapshot carries `max_triggers` while the record has a different value | On-disk value kept, decision reported | None needed — this is the guard working |
| A stale snapshot carries `max_triggers` while the record has none | Introduced | Acceptable: the only producer writes it when absent, and a later operator value still wins |
| The key is introduced on a record that already reached a terminal status | The terminal-status pin and `restoreExistingPhase` are unaffected — they do not read `max_triggers` | None needed |

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Reclassify `max_triggers` out of `controllerOwnedFields`, add the introduction and refusal specs, add the CHANGELOG bullet | 1-5 | 1-4, 6 | — |

## Do-Nothing Option

Not acceptable. The executor half is shipped and verified on dev; without this the arm is inert and the defect it was built to close stays open, with an added cost — an operator reading the executor log is told the cap was armed when it was not.
