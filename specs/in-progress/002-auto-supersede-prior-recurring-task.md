---
status: generating
tags:
    - dark-factory
    - spec
approved: "2026-06-30T09:34:18Z"
generating: "2026-06-30T09:39:59Z"
branch: dark-factory/auto-supersede-prior-recurring-task
---

## Summary

- Inbox-style recurring tasks (`cleanup-obsidian-inbox`, `cleanup-omnifocus-inbox`, `aquascape-pwc`) accumulate stale `status: in_progress` instances when a day/week is skipped — the leftover is noise because today's instance covers yesterday's intent, and manual close-out is a tax on the operator.
- After the controller materializes a new recurring-task instance (via the existing `task.CreateCommand` handler + `AtomicWriteAndCommitPush`), it computes the prior-period token for the same Schedule, checks whether the prior instance file is still `status: in_progress`, and transitions it to `status: aborted` in the immediate next git-rest write.
- Audit-style recurring tasks (`check-prometheus-alerts`, `ibkr-swing-trading`) are the opposite: each missed firing IS the signal, so silently aborting destroys "we missed Tuesday." The publisher stamps `audit_style: true` on those instances' frontmatter (mirroring `spec.skipAutoCleanup: true` on the Schedule CRD); the controller reads that stamp at materialize time and skips the supersede entirely.
- Reuses the controller's existing git-rest client, `GitRestURL`, `GatewaySecret`, per-vault STS deployment, and Kafka partition serialization — zero new env vars, zero new pods, zero new network deps.
- This is v1 (controller-side). The abandoned v2 (a cleanup cron inside `recurring-task-creator`) is out of scope — it would have added a second git-rest client to a pod that didn't have one and introduced an hourly stale window this spec closes to zero.

## Problem

When a recurring-task instance's day/week is skipped (pod down, Kafka lag, manual pause), the prior-period instance file remains `status: in_progress` indefinitely. Operators must notice the stale instance and manually transition it to `aborted`. Across dozens of inbox-style schedules, this is a recurring tax and a source of "is this still real?" noise in vault task surfaces.

Audit-style schedules (`check-prometheus-alerts`, `ibkr-swing-trading`) deliberately want the stale instance to stay `in_progress` — a missed firing is itself the signal an operator must investigate. Auto-supersede would silently erase that signal.

This spec adds the supersede behavior at the materialize seam (the `task.CreateCommand` handler), the only layer that already owns a git-rest connection to the vault.

## Goal

After the controller writes a new recurring-task instance, the prior-period instance for the same Schedule — if it exists and is still `in_progress` and is not audit-style — is automatically transitioned to `aborted` in the same materialize flow. Audit-style instances are preserved. The transition is idempotent under Kafka redelivery.

## Non-goals

- Do NOT add a `CHANGELOG.md` entry — this repo has no `CHANGELOG.md` (verified in spec-001).
- Do NOT add a cron loop, a `CronToInterval` parser, a second git-rest client, new STS env vars, or a new pod — all dropped with the v2 pivot.
- Do NOT change the publisher's hourly tick or the `/trigger` handler in `bborbe/recurring-task-creator`.
- Do NOT retro-bulk-abort historical stale instances — separate cleanup script if needed; this spec only handles the forward-going materialize-time supersede.
- Do NOT handle cross-recurrence-kind changes (Daily → Weekly mid-flight) — not a real case.
- Do NOT add a notification / dashboard surface for what was superseded — the vault file frontmatter + the existing `task.TaskUpdated` event the controller already emits is enough.
- Do NOT add a per-feature opt-out flag, config field, or tunable threshold on the controller side — the only opt-out is `audit_style: true` stamped by the publisher (invariant; if a future consumer demands controller-side variation, that's a separate spec).
- Do NOT ship the `spec.skipAutoCleanup` CRD field or the `audit_style` frontmatter stamp here — those live in `bborbe/recurring-task-creator` and are a separate prerequisite PR (see Assumptions).

## Assumptions

- The publisher (`bborbe/recurring-task-creator`) stamps `audit_style: <bool>` onto every recurring-task instance frontmatter, shipped separately in `bborbe/recurring-task-creator`. **This PR ships FIRST** — otherwise the controller reads `audit_style` before the publisher stamps it. Until that ships, the controller treats absence-of-`audit_style` as `false` (inbox-style, eligible for supersede), which matches operator intent for pre-existing files.
- The new instance's frontmatter carries `created_by: recurring-task-creator` (already stamped by the publisher today).
- The new instance's title carries the slug + period token (e.g. `Aquascape PWC - 2026W27`), so the prior-period token is derivable by decrementing the period token suffix. The slug prefix is the part of the title before the final ` - <period-token>` separator.
- Period-token decrement follows [[Per-Kind Firing Semantics for Recurring Task Schedulers]] — the recurrence kind is NOT carried in the frontmatter, so decrement is inferred from the period-token shape (see Constraints).
- Kafka partitioning by `task_identifier` serializes writes per task; the controller is the single git writer per vault (see [[Task Controller]]). No new mutex is required.
- git-rest auto-commits + pushes on every POST (see [[Task Controller]] "git-rest integration"); the prior-file transition is a separate POST immediately after the new-file POST.

## Desired Behavior

1. Immediately after a successful `AtomicWriteAndCommitPush` of a new task instance, the handler inspects the new instance's frontmatter. If `created_by != "recurring-task-creator"` OR `audit_style == true`, the supersede hook is a no-op and the handler returns as it does today.
2. When the new instance is a recurring-task instance eligible for supersede, the hook computes the prior-period token by decrementing the period-token suffix of the new instance's title, per the period-token decrement table.
3. The hook constructs the prior instance's relative path (`<taskDir>/<slug> - <prior-period-token>.md`) and reads it via `gitClient.ReadFile`. If the read returns a not-found error (first-ever instance, no prior file), the hook is a no-op. If the read returns any other error, the hook logs a warning and returns without transitioning (transient git-rest failure must not block the already-written new instance).
4. If the prior file exists, the hook parses its frontmatter. If `status != "in_progress"`, the hook is a no-op (prior already completed/aborted — covers Kafka redelivery idempotency on the second pass).
5. If the prior file's `status == "in_progress"`, the hook transitions the prior file by writing back: `status: aborted`, `phase: done`, `completed_date: <now RFC 3339 timestamp>`, `superseded_by: <new instance relative path>`, and re-stamps `created_by: recurring-task-creator` (idempotent if already present). The write uses `AtomicReadModifyWriteAndCommitPush` to preserve all other frontmatter fields and the body.
6. The supersede transition writes a commit message that contains the frozen substring `auto-supersede prior recurring task` so operators can grep the git log for supersede activity.
7. The supersede transition is logged at INFO with the frozen substring `auto-supersede:` followed by the prior path and the new path, so operators can confirm via `kubectl logs | grep`.
8. The supersede hook never causes the `CreateCommand` handler to return an error — the new instance is already written; a supersede failure (read error, parse error, write error) is logged at WARNING and swallowed. The handler's existing return shape (`nil, nil, nil` on success) is unchanged.

## Period-Token Decrement Table

The recurrence kind is not carried in frontmatter; decrement is inferred from the period-token suffix shape. The decrement table, shape-recognition order, and boundary cases live in `docs/period-token-semantics.md` (durable contract between publisher and controller). Matches [[Per-Kind Firing Semantics for Recurring Task Schedulers]] for token shapes.

## Constraints

- Frozen insertion point: the supersede hook is called inside `NewCreateTaskExecutor`'s handler func, immediately after the successful `AtomicWriteAndCommitPush` call (currently around line 112 of `pkg/command/task_create_task_executor.go`) and before the `return nil, nil, nil`.
- Frozen git-rest client reuse: the hook uses the same `gitClient gitclient.GitClient` instance already passed to `NewCreateTaskExecutor`. No new client, no new env var, no new constructor parameter beyond what is needed to inject the supersede helper (the helper receives `gitClient`, `taskDir`, and a clock for `completed_date`).
- Frozen frontmatter field names on the prior transition: `status`, `phase`, `completed_date`, `superseded_by`, `created_by`. These names are the contract with the vault consumer surfaces and must not change without a separate spec.
- Frozen log substring `auto-supersede:` and frozen commit-message substring `auto-supersede prior recurring task` — load-bearing for operator greps and the verification ACs.
- The new instance's `audit_style` read is the sole opt-out; absence is treated as `false`.
- Existing tests in `pkg/command/task_create_task_executor_test.go` and `pkg/gitrestclient/*_test.go` must still pass.
- The hook must not write the prior file if the new instance write failed (the hook only runs after a successful new-instance write).
- `completed_date` uses a full RFC 3339 timestamp (e.g. `2026-06-30T11:26:21+02:00`), matching the existing `completed_date` convention in vault task frontmatter (verified against existing completed tasks: all carry RFC 3339, not date-only).

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---------|-------------------|----------|
| Prior file does not exist (first-ever instance) | Hook reads not-found, no-op, returns. | No recovery needed. |
| Prior file read returns transient git-rest error (5xx, network) | Hook logs WARNING with `auto-supersede:` substring + the error, swallows, returns. New instance already written. | The originally-failed stale instance is not retried automatically by this mechanism (the next materialize supersedes period N, not the still-stale period N-1). Operator manually aborts it, or the separate retro-bulk-abort script (Non-goals) handles it. |
| Prior file frontmatter unparseable / missing `status` | Hook logs WARNING, swallows, returns. | Operator manually inspects the prior file. |
| Prior `status` already `aborted` / `completed` / `done` | No-op, returns. | No recovery needed (idempotent under Kafka redelivery). |
| Kafka redelivery of the same `CreateCommand` after supersede succeeded | New-instance write path hits `ErrTaskAlreadyExists` (file present) and returns the sentinel BEFORE reaching the hook. Hook never runs on redelivery. | No recovery needed. |
| New-instance `audit_style: true` | Hook is a no-op (audit-style preserved). | No recovery needed. |
| Period-token shape unrecognized (non-recurring title) | Hook logs WARNING, no-op. | No recovery needed. |
| Two instances materialize near-simultaneously for adjacent periods | Kafka partitioning by `task_identifier` serializes per-task; cross-task races are bounded by git-rest's auto-commit-per-POST. Worst case: the earlier-period instance's supersede of its own prior races with the later-period instance's supersede of the earlier — both target the same prior file. The `AtomicReadModifyWriteAndCommitPush` read-modify-write serializes at git-rest; the second write sees the first's `status: aborted` and no-ops (status check). | No operator action. |
| Clock skew: `completed_date` off by a day | Acceptable — `completed_date` is informational, not load-bearing for ordering. | No recovery needed. |

## Security / Abuse Cases

- What can an attacker control? The `task.CreateCommand` Kafka payload: title, frontmatter. A crafted title with a path-separator-bearing "prior token" could attempt path traversal. **Mitigation:** the hook constructs the prior path via `filepath.Join(taskDir, slug+" - "+priorToken+".md")` and the existing `resolveCreateTaskRelPath` already rejects titles containing `/` or `\`; the prior-path construction must apply the same path-separator rejection to the computed prior token and slug. If either contains a separator, no-op + WARNING.
- What crosses trust boundaries? The Kafka command topic (already authenticated within the cluster) → git-rest HTTP (gateway-secret auth, already wired). No new trust boundary.
- What can hang, retry forever, or race? git-rest read/write can hang on a slow git operation. The hook inherits the handler's context deadline; it must not add its own retry loop — one read, one write, swallow on failure.
- What data must be validated? Prior file frontmatter `status` field must be present and a string; `audit_style` on the new instance must be read via the existing `TaskFrontmatter.String` accessor (treats absent/non-string as `false`).

## Acceptance Criteria

- [ ] `make precommit` exits 0 in the repo root — evidence: exit code.
- [ ] `grep -n 'auto-supersede prior recurring task' pkg/command/task_create_task_executor.go` returns ≥1 line — evidence: grep line count (frozen commit-message substring).
- [ ] A unit test asserts: new instance with `created_by: recurring-task-creator` + `audit_style: false` (or absent) + prior file `status: in_progress` → prior file written back with `status: aborted`, `phase: done`, `completed_date` set to a non-empty RFC 3339 timestamp, `superseded_by` equal to the new instance relative path, `created_by: recurring-task-creator` — evidence: mock `GitClient` `AtomicReadModifyWriteAndCommitPush` call captured with frontmatter asserting the five field values.
- [ ] A unit test asserts: new instance with `audit_style: true` → hook never calls `ReadFile` or `AtomicReadModifyWriteAndCommitPush` on the prior path — evidence: mock call count == 0 for prior-path reads/writes.
- [ ] A unit test asserts: new instance with `created_by` absent or != `recurring-task-creator` → hook never calls prior `ReadFile` — evidence: mock call count == 0 for prior-path reads.
- [ ] A unit test asserts: prior file read returns not-found error → hook no-ops, no write to prior path — evidence: mock `AtomicReadModifyWriteAndCommitPush` call count == 0.
- [ ] A unit test asserts: prior file `status: completed` → hook no-ops, no write to prior path — evidence: mock write call count == 0.
- [ ] A unit test asserts: prior file `status: aborted` (simulating Kafka redelivery second pass) → hook no-ops — evidence: mock write call count == 0.
- [ ] A unit test asserts: prior file read returns a non-not-found error → hook logs WARNING with `auto-supersede:` substring and swallows, no write, handler returns `nil, nil, nil` — evidence: mock write call count == 0 AND handler return value `(nil, nil, nil)`.
- [ ] A table-driven unit test covers all six period-token decrements (Daily, Weekday-list, Weekly, Monthly, Quarterly, Yearly): given a new-title and expected prior-title, the computed prior path matches — evidence: test cases pass with the six rows from the Period-Token Decrement Table. **Hardening**: each kind is exercised at ≥2 anchor dates including a boundary (e.g. Quarterly `2026Q1 → 2025Q4` year-boundary; Weekly `2026W01 → 2025W52` ISO-week-boundary; Monthly `2026-01 → 2025-12` year-boundary; Daily `2026-01-01 → 2025-12-31` year-boundary) so a hardcoded lookup table keyed on the exact test-row strings cannot satisfy the test.
- [ ] A unit test asserts: computed prior token containing `/` or `\` → no-op + WARNING, no write — evidence: mock write call count == 0.
- [ ] A unit test asserts: `superseded_by` value equals the new instance's relative path (`<taskDir>/<new title>.md`) — evidence: captured mock write frontmatter field.
- [ ] A unit test asserts: a supersede write failure (mock `AtomicReadModifyWriteAndCommitPush` returns error) does NOT cause the handler to return an error — evidence: handler return value `(nil, nil, nil)`.

Scenario coverage: NO new E2E scenario. The behavior is fully reachable via unit tests with mock `GitClient`; the real git-rest + Kafka path is already covered by the existing `CreateCommand` integration test. The operator-run E2E drill (deliberately-missed-day + `git diff`) is captured in the Verification section, not as an automated scenario.

## Verification

Unit + integration:

```
make precommit
```

Expected: exit 0, all Ginkgo/Gomega specs pass, counterfeiter mocks regenerated cleanly.

Operator E2E drill (post-deploy, on dev first, then prod):

1. Pick an inbox-style Schedule (e.g. `cleanup-obsidian-inbox`).
2. Temporarily disable firing for one cycle (pause the Schedule CR or scale the publisher to 0 for one tick window).
3. Re-enable; let the next instance materialize.
4. `cd ~/Documents/Obsidian/Personal && git log --oneline -5` — expect a commit message containing `auto-supersede prior recurring task`.
5. `git diff HEAD~1 -- "24 Tasks/<slug> - <prior-period>.md"` — expect frontmatter diff: `status: in_progress` → `status: aborted`, `phase` → `done`, `completed_date` set, `superseded_by: 24 Tasks/<slug> - <new-period>.md`.
6. Confirm the audit-style Schedule (`check-prometheus-alerts`) with `audit_style: true` was NOT transitioned: `git diff` shows no commit touching its prior instance.
7. Restore the Schedule to normal cadence.

Deploy:

```
# dev
cd ~/Documents/workspaces/agent-dev
git pull && git merge master
cd task/controller && BRANCH=dev make buca
# verify on dev, then prod
cd ~/Documents/workspaces/agent-prod
git pull && git merge master
cd task/controller && BRANCH=prod make buca
```

Rollout (happens in `bborbe/recurring-task-creator` Schedule CRs, not in this repo): default-on for inbox-style Schedules (`cleanup-obsidian-inbox`, `cleanup-omnifocus-inbox`, `aquascape-pwc`); set `spec.skipAutoCleanup: true` on audit-style Schedules (`check-prometheus-alerts`, `ibkr-swing-trading`).

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Period-token decrementor: a new `pkg/command/period_token_decrementor.go` (or similar) with the six-kind decrement logic + table-driven tests, no git-rest dependency. | DB2, DB6 (shape recognition) | period-token decrement AC | — |
| 2 | Supersede hook: extend `pkg/command/task_create_task_executor.go` with the `supersedePriorRecurringTask` helper (calls decrementor, reads prior, transitions via `AtomicReadModifyWriteAndCommitPush`), inject the helper + clock into `NewCreateTaskExecutor`, wire in `pkg/factory/factory.go`. Ginkgo tests with mock `GitClient`. | DB1, DB3, DB4, DB5, DB7, DB8 | all supersede / no-op / idempotency ACs | prompt 1 |

Rationale: prompt 1 is pure date arithmetic — testable in isolation with no mocks. Prompt 2 consumes prompt 1's decrementor as a pure function and adds the git-rest I/O + handler wiring. Splitting avoids one giant prompt that mixes date logic with mock-heavy I/O tests.

## Do-Nothing Option

If we don't do this: inbox-style stale instances continue accumulating. Operators manually abort them. The tax is low-frequency but real (a few per week across schedules), and the noise degrades trust in vault task surfaces. Audit-style instances are unaffected (no auto-supersede today). The current approach is acceptable but operationally noisy; this spec removes the tax for inbox-style without compromising audit-style signal.
