---
status: prompted
tags:
    - dark-factory
    - spec
approved: "2026-08-27T18:28:45Z"
generating: "2026-08-27T18:28:45Z"
prompted: "2026-08-27T18:42:40Z"
branch: dark-factory/bug-create-task-dedup-blocks-terminal-reopen
---

## Summary

- `create-task` treats "a file exists at the title path" as the whole dedup rule. It never looks at the existing task's `status`, so a title whose task was long ago closed is permanently burned.
- Producers that key on a stable, long-lived title — the Sentry collector keys on `Analyze Sentry issue <SHORT-ID> - <last-activity-date>` — can never re-open a topic once its first task went terminal.
- Live blast radius on 2026-08-27: all 68 currently-active Sentry alerts were published, all 68 were dropped, zero task files created; the vault already holds 72 per-alert files, all of them `aborted` or `completed`. Every active alert is invisible to its consumer, permanently.
- The fix narrows the dedup rule from "path occupied" to "path occupied by a **live** task". Terminal (`completed` / `aborted`) means the slot is free; every non-terminal status keeps dedup exactly as it is today.
- Anything the executor cannot positively prove is terminal — unparseable frontmatter, absent `status`, an unknown status value — holds the path. A parse failure must never become an overwrite.

## Problem

`checkTitlePathFree()` in `pkg/command/task_create_task_executor.go` reads the file at the resolved title path and, on a successful read, unconditionally returns `task.ErrTaskAlreadyExists` — no regard for the existing task's `status`. That is correct for replay suppression (the case it was written for) but wrong as a general contract, because it silently converts "this title was used once" into "this title is retired forever". Producers whose natural dedup key is stable across an entity's lifetime are the ones that break, and they break silently: the command is dropped as a benign Failure, nothing is written, nothing alerts. The Sentry pipeline is the live casualty — `sentry-collector-agent` publishes one `task.CreateCommand` per active unresolved alert on every sweep, and once an alert's task goes terminal (aborted by a zombie sweep, or completed by a prior triage that did not resolve the alert), that alert can never be re-triaged by `sentry-analyzer-agent` again.

## Reproduction

Environment: `agent-task-controller-personal-0`, prod, 2026-08-27. Personal vault, task dir `24 Tasks/`.

Version anchor — the build that exhibited the bug:

```
docker.prod.nuke.benjamin-borbe.de:443/bborbe/agent-task-controller:v0.5.0
docker.prod.nuke.benjamin-borbe.de:443/bborbe/agent-task-controller@sha256:4c03e7bd6d61f48bfd01bb9dc0f4188e933222230881a1d647f9dcc4b4d1137d
```

Read with the same command the Post-Deploy AC uses. Note prod runs `v0.5.0` while `origin/master` is at `v0.5.2` — the defect is present on both, since `checkTitlePathFree()` is unchanged across those tags.

Steps:

1. Publish a `task.CreateCommand` with `Title: "Analyze Sentry issue <SHORT-ID> - <date>"` for an alert whose task file already exists in the vault with `status: aborted` (or `status: completed`).
2. Observe the controller's log.
3. Observe the vault: no new file, no new commit.

Observed evidence (verbatim shape from the prod log):

```
create-task: title path 24 Tasks/Analyze Sentry issue <SHORT-ID> - <date>.md already occupied (<n> bytes), returning ErrTaskAlreadyExists for <task-identifier>
```

Scale of the observed incident:

- `sentry-collector-agent` published all 68 active unresolved alerts (Kafka partition 0, offset 114876).
- `agent-task-controller-personal-0` consumed all 68 and created **zero** task files — every one logged the line above.
- Personal vault holds 72 per-alert task files: 61 `status: aborted`, 11 `status: completed`, **zero** live.

Net effect: every currently-active Sentry alert is invisible to its consumer `sentry-analyzer-agent`, and no future sweep can change that.

## Expected vs Actual

| | Behavior |
|---|---|
| **Expected** | The dedup guard exists to suppress Kafka replay and genuine live-duplicate creates (`NewCreateTaskExecutor` doc comment: "If a file already exists at the resolved path the command returns ErrTaskAlreadyExists (a benign Failure — no overwrite, no git write)"). A task that has reached a terminal status is no longer a live duplicate; the title slot is free and a fresh non-terminal task must be materialized. |
| **Actual** | `checkTitlePathFree()` returns `ErrTaskAlreadyExists` on any successful read, regardless of status. The title slot is retired for the lifetime of the file. |

## Why this is a bug

The guard's stated purpose is replay suppression and duplicate-live-task avoidance, not permanent title retirement. Nothing in the design docs or the executor's own doc comment claims a title is single-use for all time. The observable consequence — a producer that correctly keeps publishing an unresolved condition, and a consumer that never sees it — is exactly the "config/command silently ignored" class in `docs/bug-workflow.md`. It is also a data-flow dead end with no operator signal: the drop is logged at V(2) and surfaces as a benign Failure, so nothing escalates.

## Workaround

Manually delete or manually flip the terminal task file's `status` back to a non-terminal value in the vault, then wait for the next collector sweep. This is per-alert manual toil and does not scale to 68 alerts; it also races the controller's own writes.

## Goal

`create-task`'s dedup rule is "the title path is occupied by a **live** task", not "the title path is occupied". A create command whose title path holds a terminal task (`completed` or `aborted`) materializes a fresh non-terminal task at that path. A create command whose title path holds a task in any non-terminal status, or whose frontmatter the executor cannot read a status from, is dropped with `ErrTaskAlreadyExists` exactly as it is today.

## Non-goals

- Do NOT touch the unrelated `assignee`-blanking-on-failure defect — in flight on branch `fix/assignee-preserve`.
- Do NOT backfill or repair the 72 existing terminal per-alert task files — the next collector sweep re-materializes the live ones once this fix ships; no migration script.
- Do NOT change the `<SHORT-ID> - <date>` title key scheme, or any producer's key scheme.
- Do NOT change `sentry-collector-agent`'s publishing cadence, filter, or payload.
- Do NOT merge, migrate, or carry forward any content from the terminal file into the new one (body, `trigger_count`, `assignee`, verdict sections) — the new task is built solely from the `CreateCommand`, exactly as a first-ever create is. Git history is the record of the prior instance.
- Do NOT add a config field, env var, CRD knob, or per-producer opt-out for terminal-reopen — invariant; if a future consumer demands variation, that's a separate spec.
- Do NOT add a tunable or extendable terminal-status list — `completed` and `aborted` are the vault's two terminal statuses and are frozen here.
- Do NOT add a new frontmatter marker on the reopened task (`reopened: true`, `reopen_count`, `supersedes`) — the git commit and the log line are the record.
- Do NOT change the `supersedePriorRecurringTask` hook, its opt-in gate, or its behavior.

## Acceptance Criteria

- [ ] `make precommit` exits 0 at repo root — evidence: exit code.
- [ ] `grep -n 'reopen terminal task' pkg/command/task_create_task_executor.go` returns ≥1 line — evidence: grep line count (frozen commit-message substring).
- [ ] `grep -n 'create-task: reopening terminal task' pkg/command/task_create_task_executor.go` returns ≥1 line — evidence: grep line count (frozen log substring).
- [ ] Unit spec: existing file frontmatter `status: completed` → handler returns `(nil, nil, nil)` AND `fakeGit.AtomicWriteAndCommitPushCallCount() == 1` — evidence: counterfeiter call count + handler return value.
- [ ] Unit spec: existing file frontmatter `status: aborted` → handler returns `(nil, nil, nil)` AND `fakeGit.AtomicWriteAndCommitPushCallCount() == 1` — evidence: counterfeiter call count + handler return value.
- [ ] Unit spec: the content written on a reopen equals the content a first-ever create writes for the same command — evidence: the captured `AtomicWriteAndCommitPush` content argument contains `status: next` (from the command), and `grep`-equivalent assertion that it contains none of the terminal file's distinctive fields (`completed_date`, `phase: done`, and the terminal file's body marker string). Negative evidence: prior-file fields absent from the captured payload.
- [ ] Table-driven unit spec covering **each** non-terminal status `next`, `in_progress`, `backlog`, `hold` as the existing file's `status` → handler returns an error satisfying `errors.Is(err, task.ErrTaskAlreadyExists)` AND `fakeGit.AtomicWriteAndCommitPushCallCount() == 0` — evidence: four table rows, each asserting sentinel identity + write call count 0.
- [ ] Unit spec: existing file content has no frontmatter delimiters at all (e.g. `plain text, no frontmatter`) → sentinel `ErrTaskAlreadyExists`, write call count 0 — evidence: sentinel identity + call count.
- [ ] Unit spec: existing file frontmatter is syntactically invalid YAML → sentinel `ErrTaskAlreadyExists`, write call count 0 — evidence: sentinel identity + call count.
- [ ] Unit spec: existing file has valid frontmatter with **no** `status` key → sentinel `ErrTaskAlreadyExists`, write call count 0 — evidence: sentinel identity + call count.
- [ ] Unit spec: existing file frontmatter `status: ""` and `status: some-unknown-value` → sentinel `ErrTaskAlreadyExists`, write call count 0 — evidence: two rows, sentinel identity + call count.
- [ ] Unit spec: the reopen decision consumes the bytes from the collision read — `fakeGit.ReadFileCallCount()` after one handled reopen command is exactly 1 — evidence: counterfeiter call count (locks out a second git-rest round trip on the hot create path).
- [ ] Unit spec: on reopen, the commit message passed to `AtomicWriteAndCommitPush` contains `reopen terminal task`; on a first-ever create (read returns 404) it does NOT — evidence: captured commit-message argument, positive on one path and negative on the other.
- [ ] Unit spec (idempotency): a reopen followed by an immediate replay of the same command, where the second `ReadFile` returns the just-written non-terminal content, yields sentinel `ErrTaskAlreadyExists` and leaves `AtomicWriteAndCommitPushCallCount() == 1` — evidence: call count unchanged across the replay.
- [ ] The two pre-existing collision specs in `pkg/command/task_create_task_executor_test.go` (`status: next` at ~line 163, `status: todo` in "collision with a different task_identifier") still assert `ErrTaskAlreadyExists` with unmodified `Expect(...)` lines — evidence: `git diff pkg/command/task_create_task_executor_test.go` shows no change to any `Expect(` line inside those two `Context` blocks (comment-only edits permitted).
- [ ] **Post-Deploy (Rung-3):** after prod deploy, the next `sentry-collector-agent` sweep materializes live tasks for currently-active alerts — evidence: `kubectlnukeprod -n prod logs agent-task-controller-personal-0 --since=1h | grep -c 'create-task: reopening terminal task'` returns ≥1, and in the Personal vault `grep -l 'status: aborted' "24 Tasks/Analyze Sentry issue"*.md | wc -l` is strictly lower than the pre-deploy count of 61.
  - `deploy_check:` `kubectlnukeprod -n prod get pod agent-task-controller-personal-0 -o jsonpath='{.spec.containers[0].image}' | awk -F: '{print $NF}'`
  - `deploy_target:` `$(sed -n 's/^## \(v[0-9][^ ]*\)$/\1/p' CHANGELOG.md | head -1)`
- [ ] `CHANGELOG.md` gains a `fix:` bullet under a new version heading naming the terminal-reopen behavior change — evidence: `grep -n 'terminal' CHANGELOG.md` returns a line above the current top version heading.

Scenario coverage: **NO new E2E scenario.** Every branch of the decision is reachable through the existing counterfeiter `fakeGit` harness by staging `ReadFileReturnsOnCall` content — no real Docker, no real cluster, no real `gh` is needed to reach it. The runtime path is additionally covered by the Rung-3 post-deploy AC above, which replays the original reproduction against the deployed binary.

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

```
make precommit
grep -n 'reopen terminal task' pkg/command/task_create_task_executor.go
grep -n 'create-task: reopening terminal task' pkg/command/task_create_task_executor.go
git diff pkg/command/task_create_task_executor_test.go
```

Expected: `make precommit` exits 0 with all Ginkgo/Gomega specs green; both greps return ≥1 line; the test diff adds new `Context` blocks and touches no `Expect(` line inside the two pre-existing collision contexts.

### Operator-executable (runs on the host after PR merge)

Reproduction replay — the mandatory bug check:

1. Deploy the merged fix to prod and confirm the image tag matches the release (see the Rung-3 `deploy_check` above).
2. Pick one alert whose task file is `status: aborted` and whose Sentry alert is still active and unresolved. Record its title.
3. Wait for (or trigger) the next `sentry-collector-agent` sweep.
4. `kubectlnukeprod -n prod logs agent-task-controller-personal-0 --since=30m | grep 'create-task: reopening terminal task'` — expect ≥1 line naming that title's path.
5. In the Personal vault: `git log --oneline -5 -- "24 Tasks/<title>.md"` — expect a commit whose message contains `reopen terminal task`.
6. `head -20 "24 Tasks/<title>.md"` — expect a non-terminal `status`, and none of the prior instance's terminal fields (`completed_date`, `phase: done`).
7. `git show HEAD~1:"24 Tasks/<title>.md" | head -20` — expect the prior terminal instance intact in history (overwrite is recoverable).
8. Confirm dedup did not regress: re-run the sweep without resolving anything and confirm no second commit lands for that title — the file is now non-terminal, so the second sweep must be suppressed.

## Desired Behavior

1. When the resolved title path is occupied, the executor decides on the existing task's `status` rather than on mere existence: it extracts and parses the frontmatter from the bytes it already read.
2. When the existing `status` is exactly `completed` or exactly `aborted` (compared case-sensitively after whitespace trim), the executor treats the slot as free and continues to the normal write path — the new task file is written at that path, overwriting the terminal file in place, and the handler returns success as it does for a first-ever create.
3. When the existing `status` is any other value — including every non-terminal vault status (`next`, `in_progress`, `backlog`, `hold`, the legacy alias `todo`), the empty string, and any unrecognized value — the executor returns `ErrTaskAlreadyExists` and writes nothing, unchanged from today.
4. When the executor cannot positively establish a status — frontmatter delimiters missing, YAML unparseable, or the `status` key absent — it fails closed: `ErrTaskAlreadyExists`, no write, and a WARNING log naming the path so the operator can see why the slot is held.
5. A reopen is distinguishable from a first-ever create in two operator-greppable places: an unconditional INFO log line containing `create-task: reopening terminal task` with the path and the prior status, and a git commit message containing `reopen terminal task`. A first-ever create carries neither.

## Design Decision: overwrite in place

**Adopted: overwrite the terminal file in place at the same title path.**

Rationale: these alerts are still unresolved, so the prior terminal transition was premature — the prior task's verdict is not a conclusion about the alert, it is a conclusion about a triage attempt that did not close it. The title key stays stable only for as long as the alert stays stale, so a reopen is a genuine continuation of the same slot, not a second concurrent concern. The prior instance's full content stays recoverable via `git show HEAD~1:<path>`, and the vault is a git repo precisely so that overwrite is not destruction. Downstream, `sentry-analyzer-agent` sees exactly one live task per alert, which preserves the existing one-live-task-per-key contract that the whole dedup guard exists to protect.

**Rejected: write a new, distinctly-keyed filename** (e.g. a `(2)` / reopen-counter suffix). Three reasons: (a) the alert's date component is stable while the alert is stale, so a distinct key needs a monotonic counter, which needs a directory scan (`ListFiles`) on the hot create path for every collision — a new git-rest round trip on the highest-frequency command in the system; (b) it produces a growing family of near-identical filenames per alert, and the consumer would then see N live tasks for one alert, which breaks the very one-live-task-per-key contract this guard protects; (c) it makes the vault's per-alert history unreadable through the filename, since the meaningful history is already the git log of a single path.

**Rejected: fix it producer-side** (have `sentry-collector-agent` vary its key when the prior task went terminal). This pushes vault-state knowledge into every producer, requires every future producer to re-implement it, and races the controller's own writes. The controller is the single writer for the vault and is the correct owner of the dedup contract.

## Constraints

- Frozen change site: `checkTitlePathFree()` in `pkg/command/task_create_task_executor.go`. The call order in `NewCreateTaskExecutor`'s handler func (validate → route → validate frontmatter → resolve path → collision check → write → supersede hook) does not change.
- Frozen sentinel: the non-terminal / unreadable path continues to return an error satisfying `errors.Is(err, task.ErrTaskAlreadyExists)`, wrapped with the same `"title path %s occupied"` message shape. Downstream result-topic consumers treat this sentinel as a benign Failure and must keep doing so.
- Frozen terminal set: exactly `completed` and `aborted`. Not `done` (that is a `phase` value, not a `status`). The `status` vs `phase` distinction this relies on is documented in `docs/controller-design.md` (status/phase merge semantics); once this fix lands, add the terminal-status definition there so the rule outlives this spec.
- Fail-closed is the invariant: any read/parse ambiguity holds the path. There is no code path where a parse failure results in a write.
- Exactly one `ReadFile` per handled create command — the status decision reuses the bytes already read by the collision check. No second git-rest round trip.
- Reuse the existing in-package helper pair `result.ExtractFrontmatter(ctx, content)` + `parseTaskFrontmatter(fmStr)`, as `buildSupersedeModifyFn()` already does. No new YAML dependency, no new parser.
- No change to `NewCreateTaskExecutor`'s signature: no new constructor parameters, no new env vars, no new command-line flags, no new config.
- `validateCreateTaskFrontmatter` still requires `status` on the incoming command; a reopen does not relax it.
- Existing tests in `pkg/command/task_create_task_executor_test.go` (including the two collision contexts) and `pkg/gitrestclient/*_test.go` must still pass. The comment in the "collision with a different task_identifier" context claiming the executor "must not consult frontmatter" is now stale — it may be updated, but that context's assertions must not be.
- `supersedePriorRecurringTask` still runs after a successful write on the reopen path, gated by its existing opt-in `created_by` + `auto_abort_prior` check. Sentry-collector commands do not satisfy that gate, so it stays a no-op for them.
- The reopen INFO log is unconditional (`glog.Infof`, not `glog.V(n).Infof`) — an in-place overwrite of vault state is rare and consequential enough to be visible at default verbosity, and the Rung-3 AC greps for it in prod logs.

## Failure Modes

| Trigger | Expected behavior | Detection | Reversibility | Recovery |
|---|---|---|---|---|
| git-rest read of the title path returns a transient non-404 error | Unchanged from today: error is propagated, command is retried by cqrs, no write. | Error surfaces on the result topic; controller log carries the wrapped read error. | N/A — no state written. | Automatic on cqrs retry. |
| Existing file frontmatter unparseable / missing `status` | Fail closed: `ErrTaskAlreadyExists`, no write, WARNING log naming the path. | `kubectlnukeprod -n prod logs agent-task-controller-personal-0 \| grep 'create-task'` shows the WARNING for that path. | N/A — no state written. | Operator repairs the file's frontmatter in the vault; next sweep proceeds normally. |
| Terminal file's body held operator notes / a triage verdict | Overwritten by the fresh task content. | The reopen commit in the vault git log. | Reversible — `git show <sha>~1:"<path>"` returns the prior content verbatim. | `git show <sha>~1:"<path>" > <path>` restores it; or read it in place from history. |
| Kafka redelivery of the same `CreateCommand` after a reopen already landed | Second pass reads the just-written non-terminal task → `ErrTaskAlreadyExists`, no second write. Idempotent. | Result topic shows one Success then benign Failures. | N/A — no second write. | None needed. |
| Two create commands for the same title, different `task_identifier`, land concurrently on different Kafka partitions | Both may observe the terminal file and both may write. Last write wins at a single path — the outcome is one file with one command's content plus one redundant commit, never two files and never a lost live task. | Two adjacent commits touching the same path in the vault git log. | Reversible via git history. | None needed — the surviving file is a valid live task; the next sweep suppresses further writes because the file is now non-terminal. |
| Burst of N terminal-path commands after a long outage (the observed 68-alert case) | N reopens ⇒ N git commits + N git-rest pushes, serialized by the controller's single-writer discipline. No batching. | Vault git log shows N commits in one window; controller log shows N reopen lines. | Reversible via git history. | None needed. Bounded by the collector's alert count, which is bounded by Sentry's active-alert set. |
| git-rest write fails mid-burst (5xx, push rejected, disk full) | The failing command returns a wrapped write error and is retried by cqrs; earlier reopens in the burst are already committed. Partial progress, no corruption — each write is one atomic commit. | Error on the result topic + controller log; vault git log shows a partial burst. | Partial — completed commits stand, the failed one did not land. | cqrs retry re-drives the failed command; on a persistent failure the operator restores git-rest and the next collector sweep re-drives it. |
| Alert's last-activity date advances between sweeps, changing the title | New title ⇒ free path ⇒ ordinary first-ever create; no reopen, no interaction with this fix. | Two differently-dated files for one alert in the vault. | N/A. | Out of scope — the key scheme is explicitly a Non-goal. |

## Security / Abuse Cases

- **What can an attacker control?** The `task.CreateCommand` Kafka payload — `Title`, `TaskIdentifier`, `Frontmatter`, `Body`. Anyone able to publish to the command topic can already create arbitrary vault files under `taskDir`.
- **What crosses a trust boundary?** This fix widens the controller's write capability: before it, a crafted `Title` could only create a *new* file; after it, a crafted `Title` matching an existing **terminal** task's filename overwrites that file's content. This is a real, deliberate widening and is the price of the fix.
- **Bounding the blast radius** — three properties hold and must be preserved: (a) writes remain confined to `taskDir` by the existing path-separator rejection in `resolveCreateTaskRelPath`, so no traversal; (b) only terminal files are overwritable — no live task can be clobbered by a crafted title, which is the property that matters operationally; (c) the vault is a git repo and every overwrite is a commit, so the prior content is always recoverable and the action is always attributable.
- **What can hang, retry forever, or race?** No new I/O is introduced — the status decision reuses the bytes from the read the executor already performs, so there is no new hang surface and no new retry loop. Concurrency is bounded as described in the Failure Modes concurrency row.
- **What must be validated?** The existing file's frontmatter is untrusted vault content: it must be parsed defensively, and any parse failure must hold the path rather than free it. This is the fail-closed invariant, and it is what stops a corrupted or attacker-planted unparseable file from becoming an overwrite vector.

## Suggested Decomposition

Two prompts. The behavior change lands first and is independently correct; the operator-facing observability rides on top of it.

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Terminal-status decision in `checkTitlePathFree`: parse the already-read bytes, free the slot on `completed`/`aborted`, fail closed on everything else, plumb the reopen fact out to the write path. Full Ginkgo table (terminal pair, four non-terminal statuses, four unreadable/ambiguous cases, single-ReadFile lock, replay idempotency, prior-content-not-carried-forward). | 1, 2, 3, 4 | precommit; terminal-completed; terminal-aborted; content-equals-first-create; non-terminal table; no-delimiters; invalid-YAML; no-status-key; empty/unknown-status; single-ReadFile; replay-idempotency; pre-existing-specs-unmodified | — |
| 2 | Reopen observability: unconditional INFO log with the frozen substring `create-task: reopening terminal task` (path + prior status), distinct commit message containing `reopen terminal task` on the reopen path only, `CHANGELOG.md` fix entry. Unit spec asserting the commit-message substring is present on reopen and absent on first-ever create. | 5 | both grep ACs; commit-message positive/negative AC; CHANGELOG AC; Rung-3 post-deploy AC | prompt 1 |

Rationale: prompt 1 is the whole defect fix and carries the entire regression lock — if only prompt 1 ships, the bug is gone. Prompt 2 makes the reopen greppable in prod logs and in vault git history, which is what the post-deploy verification step needs; it depends on prompt 1 having plumbed the reopen signal to the write call.

## Do-Nothing Option

If we don't fix this: every currently-active Sentry alert stays invisible to `sentry-analyzer-agent`, and the 61 aborted + 11 completed per-alert files permanently retire their titles. The Sentry triage pipeline degrades from "automated" to "manually unblock one title at a time", and the same trap silently waits for every future producer that keys on a stable identifier — the failure mode is a silent drop with no alert, so the next occurrence will also be found by accident. The current behavior is not acceptable.
