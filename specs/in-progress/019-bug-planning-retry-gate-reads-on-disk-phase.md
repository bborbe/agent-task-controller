---
status: prompted
tags:
    - dark-factory
    - spec
approved: "2026-11-01T10:21:26Z"
generating: "2026-11-01T10:23:39Z"
prompted: "2026-11-01T10:37:12Z"
branch: dark-factory/bug-planning-retry-gate-reads-on-disk-phase
---

## Summary

- The planning-retry gate decides whether to retry from the **published payload**, not from the task file on disk. `matchesRetryGate` reads `req.Frontmatter.Phase()`, and `Handle` calls it **before** `FindTaskFilePath` fetches the on-disk frontmatter.
- `bborbe/agent` **v0.136.0** stopped publishing `phase` on a `failed` result that advanced nothing (the fix for the 2026-10-30 phase regression, PR #172, merge `3023fb2`). Every other non-advancing branch already omitted it.
- Those two changes collide. A `pr-review` task that fails while parked in `planning` now publishes a payload with **no** `phase`, so `req.Frontmatter.Phase()` is `nil`, `matchesRetryGate` returns false, and the retry never fires: `planning_retry_count` never increments, no PR comment is posted, and the task stalls in `planning` instead of retrying up to 3 times.
- `Handle` already holds the on-disk frontmatter — `FindTaskFilePath` returns it. The phase predicate simply runs one call too early.
- Fix: drop the phase check from `matchesRetryGate`, and check the **on-disk** phase in `Handle` after `FindTaskFilePath` succeeds and before the `planning_retry_count` handling.

## Problem

The gate's job is to retry a `pr-review` task that failed during planning, at most 3 times, then escalate. Its pre-filters are cheap and payload-shaped: task type, assignee, failure marker. The phase check was grouped with them, but phase is the one field the deliverer is now entitled to omit — the whole point of the deliverer fix is that the **on-disk** phase is authoritative and the payload must not overwrite it.

So the gate reads a field that a correct deliverer no longer sends. It fails **closed**: the gate silently skips, and the observable is a task that sits in `planning` with a failure body and no retry, indistinguishable from a task the operator has not got to yet.

Scope of the affected field is bounded and verified: `grep -rn 'Frontmatter\.Phase()' --include='*.go' .` returns exactly one non-test match, in `matchesRetryGate`. Every other phase read goes through the merged or existing record (`result_writer.go` `merged.Phase()` / `existing.Phase()`), which the deliverer change does not affect. This spec therefore needs no other controller change.

## Reproduction

Environment: `agent-task-controller` v0.22.10 (`ede6d9d`), `bborbe/agent` v0.136.0.

1. A task carries `task_type: pr-review`, `assignee: pr-reviewer-agent`, on-disk `phase: planning`, and a body containing `Status: failed`.
2. Its Job fails without advancing a phase, so the deliverer publishes a payload with **no** `phase` key (v0.136.0 behaviour).
3. `NewTaskResultExecutor` passes that payload `req` to `retryGate.Handle` before any on-disk read.
4. `matchesRetryGate` evaluates `phase := req.Frontmatter.Phase()` → `nil` → `phase.String() != "planning"` → returns false.
5. `Handle` returns `(false, nil)`. No retry, no escalation, `planning_retry_count` untouched. The `glog.V(3)` line reads `phase is not planning`, which is **false** — the task is on disk in `planning`.

## Expected vs Actual

- **Expected:** the gate reads the task's actual lifecycle position. A task parked in `planning` that failed retries; a task that has moved past `planning` does not.
- **Actual:** the gate reads whatever the deliverer chose to publish. Since v0.136.0 a failed planning run publishes nothing, so the retry path is dead for exactly the tasks it exists to serve.

## Goal

Make the planning-retry gate's phase decision read the on-disk phase, so it is correct for both deliverer generations — one that publishes `phase` on a failed result (≤ v0.135.0) and one that omits it (≥ v0.136.0).

## Non-goals

- Changing the deliverer. That half is merged in `bborbe/agent` (#172, v0.136.0).
- Changing `MergeFrontmatter` or any other `result_writer.go` phase handling — verified unaffected.
- Changing the task-type / assignee / failure-marker pre-filters, the retry cap (`maxControllerPlanningRetries`), or the escalation shape.

## Desired Behavior

1. A failed `pr-review` task whose **on-disk** phase is `planning` retries, whether or not the published payload carries a `phase`.
2. A failed `pr-review` task whose **on-disk** phase is past `planning` never retries, whatever the payload's `phase` says.
3. An absent or unreadable on-disk phase skips the gate without writing, and logs the on-disk value it saw.
4. A task file that cannot be found, or whose read fails, skips the gate without writing — unchanged from today.

## Acceptance Criteria

- [ ] **AC1 — the payload phase read is gone and the on-disk read is present.** `grep -rn 'req\.Frontmatter\.Phase()' --include='*.go' pkg/` returns **zero** matches (today it returns one, in `matchesRetryGate`), **and** `grep -n 'existingFrontmatter\.Phase()' pkg/command/planning_retry.go` returns ≥ 1 line. Both halves are required: the pattern is anchored on the receiver because the correct fix — `existingFrontmatter.Phase()` — contains the substring `Frontmatter.Phase()` and would falsely satisfy an unanchored negative.
- [ ] **AC2 — the skip is observable.** A non-`planning` on-disk phase produces a `glog.V(3)` line whose format string is exactly `planning-retry: task %s on-disk phase=%s; skipping gate`, called with `phase.String()` (not the `*domain.TaskPhase` pointer — `%s` on the pointer renders an address, not the phase), and no task write occurs.
- [ ] **AC3 — the regression is locked (fails on `ede6d9d`).** A spec delivers a `pr-review` payload with **no** `phase` while the on-disk file reads `phase: planning`, and asserts the retry fires: `Handle` returns `(true, nil)` and `planning_retry_count` is incremented. On `ede6d9d` this spec fails at the `handled` assertion.
- [ ] **AC4 — the direction the deliverer fix protects is also locked.** A spec delivers a payload whose `phase` reads `planning` while the on-disk file reads `phase: execution`, and asserts the gate does **not** fire (`handled == false`, no write). This is the 2026-10-30 regression shape: a stale payload phase must never re-drive a task that has moved on. **AC4 is load-bearing** — it is the only criterion a laziest implementation (delete the phase check, add nothing) fails. Do not weaken or merge it.
- [ ] **AC5 — exactly one existing spec changes, and only its fixture.** Every existing `pkg/command/planning_retry*_test.go` spec passes unchanged **except** the `Context("non-planning phase")` block in `planning_retry_test.go`, whose payload carries `phase: execution` while its `onDiskFile(...)` fixture hardcodes `phase: planning`. Under this fix that fixture is the input that decides, so its on-disk phase becomes `execution`; the assertion (`handled == false`) stays. `onDiskFile` is a shared closure with 17 call sites, so either it gains a `phase` parameter defaulting to `planning`, or this block builds its fixture inline — the signature change is sanctioned and does **not** violate "no other spec may be edited", since no other spec's assertions move. No spec may be "fixed" by re-adding a payload phase read.
- [ ] **AC6 — the rule is documented.** `docs/controller-design.md` states, in the section that describes the retry gate, that the gate's phase predicate reads the on-disk phase and that the payload phase is not consulted.
- [ ] **AC7 — `make precommit` green**, including `go test ./...`.

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

```bash
go test ./pkg/command/ -count=1
# expect: ok  	github.com/bborbe/agent-task-controller/pkg/command

grep -rn 'req\.Frontmatter\.Phase()' --include='*.go' pkg/
# expect: no output (before the fix: one match in pkg/command/planning_retry.go)

grep -n 'existingFrontmatter\.Phase()' pkg/command/planning_retry.go
# expect: >= 1 line (AC1's positive half)

make precommit
# expect: exit 0 (AC7 — includes go test ./...)
```

The AC3 spec must be shown **failing before** the production change and passing after. Record both runs in the prompt's evidence block.

### Operator-executable (informational smoke check — not part of the verification gate)

On the dev controller, watch one planning-failing `pr-review` task through a delivery:

```bash
kubectlnukedev -n dev logs deploy/agent-task-controller --tail=200 \
  | grep 'planning-retry:'
```

Expect a `planning-retry: attempt 1/3 for task <id>` line, and the task file's `planning_retry_count` moving 0 → 1. On the pre-fix build the only line is `planning-retry: task <id> phase is not planning; skipping gate`.

## Constraints

- **The phase predicate moves; the pre-filters do not.** Task type, assignee and `hasFailureMarker` stay in `matchesRetryGate` and stay payload-driven — they are not fields the deliverer omits.
- **The phase check must follow the `relPath == ""` guard.** A missing task file must skip the gate before the phase is consulted; that ordering is the one implementation detail this spec freezes.
- **No second on-disk read.** `FindTaskFilePath` already returns the frontmatter the check consumes.
- **AC5 is the only sanctioned test edit.** A prompt that re-adds a payload phase read to keep that fixture passing has inverted the fix.
- **Deploy before or with `bborbe/agent` v0.136.0.** Until this ships, a cluster running the new deliverer has a dead planning-retry path. That is a stall, not data loss — the task stays visible in `planning` with its failure body — but it is why this change must not be deferred behind the deliverer rollout.
- **`autoRelease: false`** in this repo: no tag is cut on merge, so deploy is a separate operator step.

## Assumptions

- The frontmatter returned by `FindTaskFilePath` carries the task's lifecycle phase (it is the same value `result_writer.go` uses as `existing.Phase()` for cap stickiness).
- A task on disk always carries a `phase`; an absent on-disk phase means the gate should skip, same as a non-`planning` one.

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---|---|---|
| On-disk read fails | `errors.Wrapf` returns; no retry written | Unchanged from today; the wrapped error reaches the caller and the delivery fails loudly |
| Task file not found (`relPath == ""`) | `glog.Warningf`, returns `(false, nil)`; the phase check must sit **after** this guard | `go test ./pkg/command/ -run TestPlanningRetry` stays green with the not-found spec still asserting `handled == false` |
| On-disk phase absent | Skip with the AC2 log line | Same branch as non-`planning`; the AC2 grep returns ≥ 1 line naming an empty phase |
| **Deliverer ≥ v0.136.0 live while the controller is still pre-fix** | Planning retries silently stop on that cluster | `kubectl … get deploy/agent-task-controller -o jsonpath='{.spec.template.spec.containers[0].image}'` reads at or above this build **before** the deliverer tag lands; the ordering constraint above is the only mitigation, which is why the deploy is gated |

## Detection

The defect was found while landing `bborbe/agent` #172: the deliverer fix's own PR body names this gate as the required companion, and a grep of the controller confirmed `req.Frontmatter.Phase()` in `matchesRetryGate` is the only payload-phase reader. It would otherwise surface as `pr-review` tasks stalling in `planning` after a failed Job, with `planning_retry_count` stuck at 0 and no PR comment.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Move the phase predicate to the on-disk frontmatter; update the one fixture AC5 sanctions; document the rule | 1, 2, 3, 4, 5 | AC1, AC2, AC5, AC6 | — |
| 2 | Add the AC3 and AC4 regression specs; show AC3 failing on `ede6d9d` | 1, 2, 3, 4 | AC3, AC4, AC7 | 1 |

## Do-Nothing Option

Leave it. The cost is that every `pr-review` task whose Job fails during planning stalls silently on any cluster running `bborbe/agent` ≥ v0.136.0, with no retry and no escalation, until an operator notices. The deliverer fix cannot be reverted without restoring the phase regression it exists to stop, so "do nothing" means permanently losing the planning-retry safety net.
