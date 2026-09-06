---
status: prompted
approved: "2026-09-06T11:26:18Z"
generating: "2026-09-06T11:28:40Z"
prompted: "2026-09-06T11:37:46Z"
branch: dark-factory/durable-commands-raise-expiry
---

## Summary

- The controller consumes commands from `<stage>.agent-task-v1-request` through `cdb.RunCommandConsumerTxDefault`, which hardcodes a **5-minute** `commandExpireDuration` (`cqrs/cdb/cdb_run-command-consumer-tx.go:19-33`).
- A command whose `RequestTime + 5min < now` at consume time is dropped: `kafka_message-handler-tx-skip.go` logs `command expired (expire <t> < now <t+3s>)`, commits the offset, and the frontmatter write is lost silently.
- Under queue backpressure (burst of agent results, slow git-rest write, controller restart/rebalance) the request topic can back up past 5 minutes and the tail commands die — the 2026-08-14 outage's 16 dropped commands.
- Fix: `pkg/factory/factory.go` calls `RunCommandConsumerTx` (the explicit form) with a **60-minute** expiry instead of `RunCommandConsumerTxDefault`'s 5 minutes. No `cqrs` change.
- Expiry stays observable via the existing kafka `failure_counter` on the request topic (already wraps the chain) plus the warning log — no new metric.

## Problem

The command consumer's expiry is not sized for queue residency. `RunCommandConsumerTxDefault` passes `5*time.Minute` to `RunCommandConsumerTx` (verified in the pinned `cqrs@v0.6.10`). The 5-minute window is fine when the controller is always caught up, but the request topic is the result path for every agent finish: a burst of completions, a slow git-rest round-trip, or a controller restart that resets the in-memory backlog all push command residency past 5 minutes, and the tail commands are dropped with only a sampled warning. The task's SC3 ("zero Kafka commands expire while queued during the verification run") is currently unachievable by construction — the window is smaller than a plausible worst-case residency.

## Goal

The command consumer processes every command that arrives within a 60-minute queue window. Commands older than that (truly stale — superseded by a newer write for the same task) still expire, but the window no longer intersects realistic queue residency, so no enqueue of 100+ tasks against a capped agent drops a result.

## Non-goals

- Remove expiry entirely — stale-command protection stays (a command that sat for hours must not clobber newer state).
- Change `cqrs` or `kafka` libs — `RunCommandConsumerTx` already accepts the duration.
- Add a new expiry metric — the existing kafka `failure_counter` on the request topic + `command expired` warning log already make expiry observable (verified: `NewMessageHandlerTxMetrics` wraps the whole chain, `kafka_message-handler-tx-metrics.go`).
- Touch the executor or the ResourceQuota (capacity decision, out of scope per the task).

## Acceptance Criteria

- [ ] `pkg/factory/factory.go` wires the command consumer via `RunCommandConsumerTx` (not `RunCommandConsumerTxDefault`) with `commandExpireDuration = 60 * time.Minute` — evidence: `grep -n 'RunCommandConsumerTx\|60 \* time.Minute' pkg/factory/factory.go` returns the explicit call with the 60-minute argument; `make precommit` exits 0
- [ ] No `cqrs` / `kafka` dependency change ships with this — evidence: `git diff origin/master -- go.mod go.sum` shows no cqrs/kafka line changes (negative evidence)
- [ ] The expiry duration is a named package constant, not a bare literal at the call site — evidence: `grep -n 'commandExpireDuration\|CommandExpireDuration' pkg/factory/factory.go` returns ≥1 line referencing the constant
- [ ] A command with `RequestTime` 30 minutes in the past is still executed (not dropped as expired) — evidence: a unit test runs the consumer's real message-handler path (the same `RunCommandConsumerTx` wiring the factory builds, with the 60-minute duration) and asserts the executor's `Handle` is called for a 30-minute-old command and NOT called for a 61-minute-old command; `go test ./pkg/factory/... -run <expiry-test>` exits 0
- [ ] **Post-Deploy (Rung-2):** on dev, after the deploy, a live 100+ task enqueue against `maxConcurrentJobs: 1` completes with zero `command expired` warnings in the controller log — evidence: `kubectlnukedev -n dev logs <controller-pod> --since=<run-window> | grep -c 'command expired'` returns `0`
  - `deploy_check:` `kubectlnukedev -n dev get pod -l app=agent-task-controller -o jsonpath='{.items[0].spec.containers[0].image}' | awk -F: '{print $NF}'`
  - `deploy_target:` to be filled with the exact tag at prompt time when the version is bumped

## Verification

### Container-executable (runs inside the dark-factory YOLO container at prompt time)

- `make precommit` — exits 0
- `go test ./pkg/...` — exits 0 (includes the expiry-window unit test)
- `grep -n '60 \* time.Minute' pkg/factory/factory.go` — returns ≥1 line
- `git diff origin/master -- go.mod go.sum` — empty for cqrs/kafka lines

### Operator-executable (runs on the host after PR merge, spec verification ladder)

- Release + deploy per [[Deploy Mirrored Agent Service]]: release tag → `make build && make upload` → bump the version in `nuke/agent/` (Makefile `MIRROR_IMAGES` + four `controllers[].image.tag` entries) → `cd ~/Documents/workspaces/nuke/agent && BRANCH=master make apply`
- Dev: run the task's verification subtask — enqueue 100+ tasks against a 1-concurrent-Job cap on dev, confirm `grep -c 'command expired'` = 0 in the controller log and the queue-depth observables from the task's SC

## Desired Behavior

1. The command consumer is wired with a 60-minute command expiry instead of the 5-minute default.
2. The 60-minute value is a named constant with a comment explaining why it is sized that way (worst-case queue residency of a 100+ enqueue at cap=1, measured job runtimes 11-15 min).
3. Commands arriving within the 60-minute window are executed regardless of how long they queued.
4. Commands older than 60 minutes still expire (stale-command protection unchanged) and remain visible via the warning log + kafka `failure_counter`.
5. No library dependency changes ship with this change.

## Constraints

- Repo conventions frozen: Ginkgo/Gomega v2 tests, `github.com/bborbe/errors` wrapping, counterfeiter mocks for new dependencies, glog `V(n)` gating.
- `cdb.RunCommandConsumerTx` requires the caller to also pass `batchSize` (`libkafka.BatchSize(1)`, matching what `RunCommandConsumerTxDefault` used) and `trigger` (`run.NewTrigger()`), plus `ignoreUnsupported` and `prefix` unchanged — mirror the Default's wiring exactly, only the duration changes.
- `cqrs` stays pinned at `v0.6.10` (its `RunCommandConsumerTx` already accepts the duration).
- CHANGELOG: add an `## Unreleased` bullet (section exists — HEAD is v0.8.1).

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---|---|---|
| Controller restarts mid-backlog | Kafka offsets persist; consumer resumes from last committed offset; 60-min window covers the rebalance | None needed |
| A command genuinely older than 60 min arrives | Expires as today — warning log + `failure_counter` increments | If this fires on a healthy run, the window is still too tight; revisit the constant |
| git-rest slow / unavailable during a burst | Commands queue in Kafka; up to 60 min of residency absorbed; `KafkaConsumePausedTotal` reflects pauses as today | git-rest recovers; backlog drains |
| Wrong duration value at the call site | AC 1 + AC 3 (grep + unit test) catch it pre-merge | Fix + re-run |

## Do-Nothing Option

Doing nothing keeps SC3 ("zero commands expire") unachievable: the 5-minute window is smaller than realistic queue residency under a 100+ enqueue, so the verification run is expected to drop commands and fail the task's own acceptance test. The 2026-08-14 outage (16 commands dropped in a 33-minute window) is the demonstrated cost of the current sizing.

## Suggested Decomposition

Single-layer single-behavior change — one prompt covers the factory wiring change, the constant, the unit test, and the CHANGELOG bullet.
