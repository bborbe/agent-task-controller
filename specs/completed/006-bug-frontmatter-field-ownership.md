---
status: completed
tags:
    - dark-factory
    - spec
approved: "2026-08-31T11:48:48Z"
generating: "2026-08-31T11:48:48Z"
prompted: "2026-08-31T12:10:29Z"
verifying: "2026-08-31T12:32:26Z"
completed: "2026-08-31T13:48:55Z"
branch: dark-factory/bug-frontmatter-field-ownership
---

## Summary

- Every agent result publish currently rewrites the *whole* task frontmatter with values captured at spawn time, because the merge gives the agent-supplied map blanket precedence on every key.
- Two consequences, one defect: the retry/trigger caps can never fire (the counters the executor bumped on disk are rolled back to their pre-run values), and an operator's terminal `status` (`aborted` / `completed`) is silently reverted while its companion fields linger as orphaned residue.
- Observed live on 2026-08-30: two tasks spawned ~13 Jobs each (26 Jobs in ~30 minutes) while their `trigger_count` read `1` and `3`; the loop was unbounded by construction and was stopped by a human, not by the cap. An operator `abort` during that window was reverted by the next publish.
- This spec introduces **field-level ownership** at the single existing result-write chokepoint: the counters `trigger_count` / `retry_count` are controller-owned (on-disk value always wins), a terminal on-disk `status` is operator-owned (never reverted by a stale publish), and everything else — `phase`, the agent's result fields, the body — keeps today's agent-wins behavior.
- Pinning a terminal status also short-circuits the escalation machinery for that task, by design: a task an operator has already marked terminal must not accrue escalation sections or be re-parked by machinery whose purpose is parking a *live* task. Both terminal statuses take that short-circuit uniformly.
- No new write site, no change to the executor's increment command, no change to assignee semantics, phase routing, or the escalation-section writers.

## Problem

The controller merges the task file's on-disk frontmatter with the frontmatter carried in the agent's result. The agent does not author that frontmatter from live state: it parses it out of the task-content snapshot injected into the agent at **spawn** time, so the published map is a photograph of the file as it looked before the run started. Merging it with blanket precedence turns every result publish into a lost update that reverts anything written to the file *during* the run. Two things are written during a run and matter: the spawn counters the executor increments before each Job, and any status change an operator makes to stop a task. The first makes the runaway-loop caps decorative — they compare a counter against a maximum that the counter can never reach — and the second makes `abort` an unreliable way to stop a running loop, which is precisely the situation in which an operator reaches for it.

## Reproduction

### Live (prod, 2026-08-30)

Two tasks against `bborbe/kafka-maxscale-cdc-connector` in the openclaw vault:

1. The executor increments `trigger_count` on disk before each spawn (`agent-task-executor/pkg/result_publisher.go:211` → `IncrementFrontmatterCommand` → `buildIncrementModifyFn`, a correct read-modify-write).
2. The agent runs, then publishes its result; the published frontmatter carries the pre-increment `trigger_count` from the spawn-time snapshot.
3. `mergeFrontmatter` lets that stale value win; the file's counter is rolled back.
4. Observed: `trigger_count` read **1** and **3** in the task files after roughly **13 spawns each** — 26 Jobs in ~30 minutes. The trigger cap never engaged. A human stopped it.
5. During the same window an operator ran `vault-cli task set <name> status aborted`. The next in-flight publish restored `status: in_progress`, `phase: human_review`, and left `aborted_reason` in the file as orphaned residue (the abort's companion field survived; the abort itself did not).

Prior art confirming the class was known but not root-caused: vault page `Check the PR Before Fixing a Looping Agent Task` (2026-08-19) — "the retry cap never engages when each agent write-back resets `retry_count`/`trigger_count` to 0 … Same write-back also reverts an operator park."

### Deterministic (unit-level, reproducible today)

With a task file on disk containing `status: aborted`, `trigger_count: 5`, `retry_count: 4`, `max_triggers: 3`, `phase: ai_review`, call `WriteResult` with an incoming frontmatter of `status: in_progress`, `trigger_count: 1`, `retry_count: 0`, `phase: execution`.

Observed today: the written file contains `status: in_progress`, `trigger_count: 1`, `retry_count: 0` — the on-disk abort and both counters are gone, and no `## Trigger Cap Escalation` section is appended because the merged `trigger_count` (1) is now below `max_triggers` (3).

## Expected vs Actual

| | Expected | Actual |
|---|---|---|
| On-disk `trigger_count: 5`, incoming `1` | file keeps `5`; cap comparison uses `5` | file becomes `1`; cap comparison uses `1` and never trips |
| On-disk `retry_count: 4`, incoming `0` | file keeps `4` | file becomes `0` |
| On-disk `status: aborted`, incoming `status: in_progress` | file keeps `aborted`; the agent's result body is still recorded | file becomes `in_progress`, with `aborted_reason` orphaned beside it |
| On-disk `status: completed`, incoming `status: in_progress` | file keeps `completed` | file becomes `in_progress` |
| On-disk terminal status, cap already reached | no new escalation section, no re-park — the task is already terminal | the reverted status makes the task look live, and the cap/assignee machinery runs against it |
| On-disk `phase: ai_review`, incoming `phase: execution` | file becomes `execution` (agent owns `phase`) | file becomes `execution` — correct, must not regress |

## Why this is a bug

`docs/controller-design.md` § "Frontmatter Merge" documents the merge as a mechanism for making fields the agent doesn't know about survive a writeback. It does not document — and the caps documented in § "Assignee-Clear on Escalation" cannot work under — a rule where a spawn-time snapshot outranks state written after the spawn. The documented cap behavior and the implemented merge behavior are mutually exclusive; the caps lose.

## Workaround

Until the fix lands, stopping a looping task requires removing the agent's ability to be re-dispatched rather than aborting it: clear `assignee` (empty assignee is not carried back as a routing value by the escalation paths) and delete the in-flight Job. Setting `status: aborted` alone is not sufficient and will be reverted.

## Goal

A result publish can no longer roll back state that was written to the task file after the agent was spawned. Specifically: the spawn counters the controller and executor own are authoritative on disk and survive every publish, so the trigger and retry caps compare against real spawn counts and can fire; and a terminal status set by an operator survives every publish and takes the task out of the escalation machinery, so `abort` is a reliable way to stop a running task. Everything the agent legitimately authors — `phase`, its result fields, the body — still lands exactly as it does today, through the same single write-site chokepoint.

## Non-goals

- Do NOT change `pkg/command/task_increment_frontmatter_executor.go`. Its read-modify-write (`buildIncrementModifyFn`) is correct and is the reason the on-disk counter is trustworthy.
- Do NOT change the agent side to stop publishing a full frontmatter snapshot. Removing the class rather than policing it is the better long-term fix, but it touches every agent — separate spec.
- Do NOT add the `phase`+`ref`-scoped counter, and do NOT flip the trigger cap from opt-in to default-on. That is the follow-up work, and it is worthless before this spec lands.
- Do NOT change `assignee` semantics. `clearAssignee` / `ClearAssigneeIfHumanReview` keep their current signatures, call sites, and effects; the only change is that a pinned-terminal task no longer reaches them (DB 4).
- Do NOT change phase routing, `restoreExistingPhase`, or the `## Retry Escalation` / `## Trigger Cap Escalation` section writers themselves.
- Do NOT change the scanner's Empty-to-Named Reset (`docs/controller-design.md` § "Empty-to-Named Reset (spec 021)"). It is untouched by this spec and becomes, post-fix, the only mechanism that may lower a counter.
- Do NOT add a config flag, env var, or per-task opt-out for the ownership rules — invariant; if a future consumer demands variation, that is a separate spec.
- Do NOT extend ownership to `max_triggers` / `max_retries` in this spec. A stale snapshot can still revert an operator's mid-run edit of those limits; that is a distinct (and rarer) case — separate spec.
- Do NOT add a new write site. `docs/controller-design.md` and spec 042 make the result writer the single chokepoint; the sixth uncoordinated write site is what caused the stuck-assignee prod incident.

## Desired Behavior

1. **Controller-owned counters are read only from disk.** When the result writer builds the merged frontmatter, `trigger_count` and `retry_count` take the on-disk value whenever the key exists on disk, regardless of what the incoming frontmatter carries; when the key does not exist on disk, it is absent from the merged result even if the incoming frontmatter carries it. The incoming values for these two keys are never written.
2. **Cap enforcement therefore sees real spawn counts.** `applyTriggerCap` and `applyRetryCap` read the merged (= on-disk) counters, so a live task whose executor bumped `trigger_count` to `max_triggers` is escalated and parked on the very next result publish, instead of being reset below the cap first.
3. **A terminal on-disk status is pinned.** When the on-disk `status` is terminal, the merged `status` is the on-disk value and the incoming `status` is discarded. Terminal is decided by the normalizing `lib.TaskFrontmatter.Status()` accessor (which canonicalises aliases — e.g. `done` → `completed`) compared against the `domain.TaskStatusCompleted` and `domain.TaskStatusAborted` constants, never by a raw string equality on the unparsed value. All other keys follow their normal ownership rules, and the body is written exactly as it is today (full replacement with the agent's content), so the agent's `## Result` is still recorded on an aborted or completed task. The write is a pin, not a freeze.
4. **A pinned-terminal task short-circuits the escalation machinery, and both terminal statuses do so uniformly.** The pin happens during the merge, i.e. before `applyRetryCounter` runs, so `applyRetryCounter`'s early return sees the terminal value and returns immediately. That early return is widened from `completed` only to *both* terminal statuses. Consequence, and it is the intended one: on a task whose on-disk status is `aborted` or `completed`, no `## Trigger Cap Escalation` or `## Retry Escalation` section is appended, `assignee` is not cleared, `previous_assignee` is not written, `phase` is not restored by `restoreExistingPhase`, and a `spawn_notification: true` key is left in place rather than deleted. Machinery whose entire purpose is parking a live task must not run against a task an operator has already ended.
5. **Agent-owned fields keep today's behavior.** `phase`, the agent's result fields, any key the agent introduces, and the body keep incoming-wins semantics on a non-terminal task. On a terminal task they also keep incoming-wins semantics — only `status` and the two counters are guarded.
6. **Every guard decision is logged once.** When — and only when — the guard discards an incoming value that *differs* from the value kept on disk, the writer emits one unconditional INFO line per affected field containing the frozen substring `ownership guard kept on-disk`, the task identifier, the field name, the kept value and the rejected value. Equal values produce no log line, so a steady-state publish is silent.
7. **The ownership rules live in one declaration.** The set of controller-owned field names and the set of terminal statuses are each declared once at package level in `pkg/result`, so adding a field later is a one-line change and the rules are greppable in a single place; `docs/controller-design.md` § "Frontmatter Merge" documents them as the contract.

## Constraints

- The change stays inside the existing `pkg/result` result-write chokepoint (`mergeFrontmatter` or a helper it calls, invoked from `buildResultModifyFn`) plus the terminal-status condition in `applyRetryCounter`. No new write site, no new Kafka operation, no new command executor — see `docs/controller-design.md` § "Assignee-Clear on Escalation" and spec 042.
- `pkg/command/task_increment_frontmatter_executor.go` is frozen for this spec.
- Terminal detection depends on `github.com/bborbe/agent v0.83.0`'s normalizing `TaskFrontmatter.Status()` accessor (`go.mod:6`), which routes through `vault-cli`'s `domain.NormalizeTaskStatus` (`done` → `completed`). A dependency bump that changes `NormalizeTaskStatus` silently alters which statuses are terminal — such a bump must re-verify ACs 5-9.
- `ClearAssigneeIfHumanReview` keeps its exported signature and semantics — `pkg/command/task_update_frontmatter_executor.go` calls it and must be unaffected. Only the set of tasks that reach it changes (terminal tasks no longer do).
- The `WriteResult` body semantics are frozen: the on-disk body is discarded and fully replaced by the agent's content, exactly as today.
- **The incoming payload is never a valid counter-reset channel** — that is the entire point of the fix. The one legitimate reset path is the scanner's Empty-to-Named Reset (`docs/controller-design.md` § "Empty-to-Named Reset (spec 021)"), which writes `trigger_count: 0` / `retry_count: 0` **to disk** on an empty→named `assignee` transition. Post-fix it is the only mechanism that can lower a counter, which makes it safety-critical for operator re-delegation; it is not modified by this spec and must keep working.
- Every existing spec in `pkg/result/result_writer_test.go` must pass with its `Expect(` lines unmodified, **with exactly two carve-outs**. (1) `It("previous_assignee persists when operator re-delegates by setting a non-empty assignee")` (currently at `pkg/result/result_writer_test.go:1351`) models the operator reset by sending `trigger_count: 0 // operator reset` in the *incoming* payload against an on-disk `trigger_count: 3` / `max_triggers: 3`. That channel is exactly what this spec closes, so the spec must be rewritten to model the reset the way the system actually performs it — the on-disk file is updated to `trigger_count: 0` with the new assignee (the scanner's Empty-to-Named Reset) before the second publish. Its intent is preserved and must still be asserted: after the second write, `previous_assignee: claude` persists and `assignee: backtest-agent` is set. (2) `It("fully replaces content on second call")` (`pkg/result/result_writer_test.go:215`) writes twice with `"status": "done"` in the first write's payload; `done` normalizes to `completed`, which is terminal, so the pin correctly discards the second write's `"status": "closed"` and its assertion fails. Its intent is body replacement across two writes, not terminal-status semantics, so the FIRST write's fixture status changes to a non-terminal value. Only that one fixture value changes — no `Expect(` line is touched. Found by applying the generated prompt to a scratch copy and running `go test ./...`, 2026-08-31; the earlier one-carve-out claim was wrong. No other spec may lose or weaken an assertion.
- Terminal statuses are exactly `aborted` and `completed` (plus whatever the normalizing accessor canonicalises into them), matching `docs/controller-design.md` § "Terminal Task Status". `phase: done` is not a status and plays no part.
- The guard is unconditional: no config flag, env var, or per-task opt-out.

## Acceptance Criteria

- [ ] Unit spec: on-disk `trigger_count: 5`, incoming `trigger_count: 1` → written file contains `trigger_count: 5` and does NOT contain `trigger_count: 1` — evidence: file-content assertion (positive `ContainSubstring` + negative `NotTo(ContainSubstring)`) in `pkg/result/result_writer_test.go`.
- [ ] Unit spec: on-disk `retry_count: 4`, incoming `retry_count: 0` → written file contains `retry_count: 4` and does NOT contain `retry_count: 0` — evidence: file-content assertion, positive + negative.
- [ ] Unit spec: `trigger_count` / `retry_count` absent on disk but present in incoming → the written file contains neither key (incoming can never introduce a controller-owned counter) — evidence: negative file-content assertion, `grep`-equivalent for both key names returns no match in the written frontmatter.
- [ ] Unit spec: on-disk `status: in_progress`, `trigger_count: 3`, `max_triggers: 3`, incoming `trigger_count: 1` → the written body contains `## Trigger Cap Escalation` and the written frontmatter contains `assignee: ""` — evidence: body-substring assertion + frontmatter assertion. This is the load-bearing case: the cap fires *because* the on-disk counter won.
- [ ] Unit spec: on-disk `status: aborted`, incoming `status: in_progress` → written file contains `status: aborted` and does NOT contain `status: in_progress` — evidence: file-content assertion, positive + negative.
- [ ] Unit spec: on-disk `status: completed`, incoming `status: in_progress` → written file contains `status: completed` and does NOT contain `status: in_progress` — evidence: file-content assertion, positive + negative.
- [ ] Unit spec (terminal write is a pin, not a freeze): on-disk `status: aborted`, incoming `status: in_progress`, `phase: execution`, `Content: "## Result\nStatus: failed\n"` → the written file contains `status: aborted` AND `phase: execution` AND the body substring `## Result` AND does NOT contain `status: in_progress` — evidence: four assertions in one spec; proves the guard pins only `status` and still records the agent's payload.
- [ ] Unit spec (terminal short-circuit, DB 4): on-disk `status: aborted`, `phase: ai_review`, `trigger_count: 3`, `max_triggers: 3`, `assignee: claude`, `spawn_notification: true`, incoming `status: in_progress` → the written body does NOT contain `## Trigger Cap Escalation`, the frontmatter still contains `assignee: claude`, does NOT contain `previous_assignee`, and still contains `spawn_notification: true` — evidence: four assertions (one positive, three negative) proving the escalation machinery did not run against a terminal task.
- [ ] Unit spec: the same fixture with on-disk `status: completed` instead of `aborted` produces the same four outcomes — evidence: a second row/spec with identical assertions, proving both terminal statuses take one uniform control flow.
- [ ] Unit spec (negative control — the fix is not "existing always wins"): on-disk `phase: ai_review`, `status: in_progress`, plus a key present only in incoming (e.g. `custom_field`) → written file contains the incoming `phase`, the incoming `status`, and the incoming `custom_field` value — evidence: file-content assertions on all three; the pre-existing spec `agent keys override existing keys` (`pkg/result/result_writer_test.go:186`) must still pass with unmodified `Expect(` lines.
- [ ] Unit spec (DB 6, no log on equal values): the merge helper reports its guard decisions to the caller alongside the merged map, and the caller emits one log line per reported decision. Spec asserts: on-disk `trigger_count: 3` with incoming `trigger_count: 3` reports **zero** decisions; the same fixture with incoming `trigger_count: 1` reports **exactly one** decision naming `trigger_count`, the kept value `3` and the rejected value `1` — evidence: assertion on the reported decision count (0 vs 1) and on the decision's field name and both values.
- [ ] Single declaration (DB 7): `grep -c '"trigger_count"' pkg/result/result_writer.go` returns ≥`1` — the string literal must appear in the package-level owned-field declaration; `≥` rather than `==` because a guard log format string may legitimately carry it too — and `grep -n 'controllerOwnedFields\|terminalStatuses' pkg/result/result_writer.go` returns ≥2 lines (declaration + use) — evidence: grep counts. Both are `0` today.
- [ ] The guard is observable in logs: `grep -n 'ownership guard kept on-disk' pkg/result/result_writer.go` returns ≥1 line, and the matched call is an unconditional `glog.Infof` (default verbosity, not `glog.V(n)`) naming the task identifier, the field, the kept on-disk value and the rejected incoming value — evidence: grep line count + the matched line contains `glog.Infof`.
- [ ] Test-suite integrity: `git diff -U0 pkg/result/result_writer_test.go | grep -c '^-[^-]' || true` — every deleted line falls inside the `It("previous_assignee persists when operator re-delegates by setting a non-empty assignee")` block named in Constraints; no other spec loses or weakens an `Expect(` line — evidence: `git diff -U0 pkg/result/result_writer_test.go | grep '^-[^-]' | grep -c 'Expect(' || true` returns `0` for every deleted `Expect(` line outside the carved block (machine-decidable; a weakened assertion elsewhere is exactly the regression this guards), plus the diff's hunk ranges compared against that block's range.
- [ ] The carved-out spec keeps its intent: `grep -c 'previous_assignee persists when operator re-delegates' pkg/result/result_writer_test.go` returns `1`, and that block still asserts `previous_assignee: claude` and `assignee: backtest-agent` after the second write, with the counter reset applied by a second `writeTaskFile` (on-disk reset, per the scanner's Empty-to-Named Reset) rather than by the incoming payload, and that rewritten on-disk content must itself retain `previous_assignee: claude` so the persistence assertion tests persistence rather than re-creation — evidence: grep count + the two `ContainSubstring` assertions + the added `writeTaskFile` call inside the block.
- [ ] `make precommit` exits 0 at repo root with the full Ginkgo suite green — evidence: exit code.
- [ ] `docs/controller-design.md` § "Frontmatter Merge" documents the ownership rules as a table: `sed -n '/^## Frontmatter Merge/,/^## Terminal Task Status/p' docs/controller-design.md | grep -c 'trigger_count' || true` returns ≥1 and `sed -n '/^## Frontmatter Merge/,/^## Terminal Task Status/p' docs/controller-design.md | grep -c 'Controller-owned' || true` returns ≥1 — evidence: two section-scoped grep counts, both `0` today. The section's illustrative merge example must no longer imply blanket agent precedence.
- [ ] `docs/controller-design.md` documents the terminal short-circuit: the § "Frontmatter Merge" section states that a terminal on-disk status skips the cap/assignee machinery — evidence: `sed -n '/^## Frontmatter Merge/,/^## Terminal Task Status/p' docs/controller-design.md | grep -c 'terminal' || true` returns ≥1 (`0` today).
- [ ] `CHANGELOG.md` gains a `fix:` bullet under `## Unreleased` naming the counter rollback and the reverted terminal status — evidence: `awk '/^## v/{exit} /trigger_count/{f=1} END{exit !f}' CHANGELOG.md` exits `0` (it exits `1` today, so the check is position-aware and change-sensitive).
- [ ] **Post-Deploy (Rung-3):** after the prod deploy, a task that spawns repeatedly shows a monotonically non-decreasing `trigger_count` across consecutive result publishes — evidence: `kubectlnukeprod -n prod logs agent-task-controller-openclaw-0 --since=2h | grep -c 'ownership guard kept on-disk' || true` returns ≥1, and for one task named in those lines, `git log -p -- "24 Tasks/<title>.md" | grep '^[+-]trigger_count'` shows no publish-commit lowering the value.
  - `deploy_check:` `kubectlnukeprod -n prod get pod agent-task-controller-openclaw-0 -o jsonpath='{.spec.containers[0].image}' | awk -F: '{print $NF}'`
  - `deploy_target:` `$(sed -n 's/^## \(v[0-9][^ ]*\)$/\1/p' CHANGELOG.md | head -1)`

Scenario coverage: **NO new E2E scenario.** Every branch is reachable through the existing `mocks.GitClient` harness in `pkg/result/result_writer_test.go`, which already stages on-disk file content and captures the written bytes — no real Docker, no real cluster, no real `gh`. The runtime path is additionally covered by the Rung-3 post-deploy AC.

Test style: `pkg/result/result_writer_test.go` is written as `Context` / `It` blocks with `writeTaskFile(...)` fixtures and `ContainSubstring` assertions on the written file; `DescribeTable` is used elsewhere in the repo (`pkg/command/`). Use `DescribeTable` for the counter rows and for the two terminal-status rows (same fixture shape, differing values) and plain `It` blocks for the pin-not-freeze, short-circuit, log, and negative-control cases, matching the surrounding file.

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

```
make precommit
grep -n 'ownership guard kept on-disk' pkg/result/result_writer.go || true
grep -c '"trigger_count"' pkg/result/result_writer.go || true
grep -n 'controllerOwnedFields\|terminalStatuses' pkg/result/result_writer.go || true
git diff -U0 pkg/result/result_writer_test.go | grep -c '^-[^-]' || true
grep -c 'previous_assignee persists when operator re-delegates' pkg/result/result_writer_test.go || true
sed -n '/^## Frontmatter Merge/,/^## Terminal Task Status/p' docs/controller-design.md | grep -c 'trigger_count' || true
sed -n '/^## Frontmatter Merge/,/^## Terminal Task Status/p' docs/controller-design.md | grep -c 'Controller-owned' || true
sed -n '/^## Frontmatter Merge/,/^## Terminal Task Status/p' docs/controller-design.md | grep -c 'terminal' || true
awk '/^## v/{exit} /trigger_count/{f=1} END{exit !f}' CHANGELOG.md
```

Expected: `make precommit` exits 0 with all specs green; the log grep returns ≥1 line containing `glog.Infof`; `"trigger_count"` appears at least once as a string literal; the declaration grep returns ≥2 lines; the deleted-line count is accounted for entirely by the one carved-out block, which still exists exactly once; all three section-scoped doc greps return ≥1; the CHANGELOG `awk` exits 0. Every `grep -c` and `grep -n` is wrapped in `|| true` because `grep -c` exits 1 on a zero count and would abort a `set -e` verification block on exactly the outcome being measured.

### Operator-executable (runs on the host after PR merge)

1. Deploy the merged fix to prod and confirm the image tag matches the release (see the Rung-3 `deploy_check` above).
2. `kubectlnukeprod -n prod logs agent-task-controller-openclaw-0 --since=2h | grep 'ownership guard kept on-disk'` — expect ≥1 line naming a task identifier, a field, and both values once tasks start publishing results.
3. Pick a task from those lines. In the openclaw vault: `git log -p -- "24 Tasks/<title>.md" | grep '^[+-]trigger_count'` — expect no commit authored by the controller's result write that lowers the value.
4. Abort replay: while a task has an in-flight run, run `vault-cli task set <name> status aborted`; after the run publishes, `head -20 "24 Tasks/<name>.md"` — expect `status: aborted` retained, the agent's `## Result` section present in the body, and no new escalation section appended.
5. Re-delegation replay (the reset path this spec makes safety-critical): clear `assignee`, then set it to a new agent, and confirm the scanner's Empty-to-Named Reset lands — `git log -p -- "24 Tasks/<name>.md" | head -40` shows a scanner commit writing `trigger_count: 0` / `retry_count: 0`, and the task is dispatched again.
6. Confirm no regression in parking for live tasks: for a non-terminal task at cap, `head -20` shows `assignee: ""` and the body still carries exactly one `## Trigger Cap Escalation` section.

## Failure Modes

| Trigger | Expected behavior | Detection | Concurrency | Recovery |
|---|---|---|---|---|
| On-disk `status` absent, empty, or an unknown value | Treated as non-terminal; the incoming `status` applies (today's behavior) | none needed | n/a | none needed |
| On-disk counter is not an integer (`"3"`, `null`, a map) | The on-disk value is kept verbatim; the cap arithmetic reads it through the existing accessor, which yields `0` for a non-integer, so no escalation fires — unchanged from today | Cap never fires while `trigger_count` visibly rises in the task file | n/a | Operator rewrites the field as an integer (`vault-cli task set <name> trigger_count <n>`) and confirms `head -20 <file>` shows an unquoted integer |
| Executor increments `trigger_count` between the controller's read and its write | The atomic read-modify-write re-reads on every git retry and the on-disk value wins, so the increment survives the publish | `git log -p -- <task file> \| grep '^[+-]trigger_count'` shows no lowering commit | This is the concurrent case the spec exists for; the guard is what makes the RMW retry meaningful | none needed |
| Scanner's Empty-to-Named Reset fails, is skipped, or regresses | Counters are never lowered by any other path, so a re-delegated agent inherits the previous agent's spent budget and is parked at cap on its first publish | Task parks immediately after re-delegation: `assignee: ""` plus a fresh escalation section within one publish of the hand-off | Reset write and result write are serialized by the same per-file mutex + git-rest commit-per-write | Operator lowers the counter on disk (`vault-cli task set <name> trigger_count 0`) and confirms the task dispatches again; post-fix this is the only supported way to lower a counter |
| Operator sets `status: aborted` while a result publish is in flight | The retry re-reads the file, sees `aborted`, pins it, and short-circuits the escalation machinery; the agent's body still lands | Task file shows `status: aborted` with the agent's `## Result` present and no new escalation section | Two writers on one file, serialized by the existing per-file mutex + git-rest commit-per-write | Reversible: `vault-cli task set <name> status in_progress` restores the live control flow on the next publish |
| A task is aborted while already at cap and carrying `spawn_notification: true` | Nothing is appended, `assignee` is left as-is, and `spawn_notification` survives the write (DB 4) | `head -20 <file>` still shows `spawn_notification: true` after the publish | n/a | Operator clears `spawn_notification` manually if the task is later revived; a non-terminal publish deletes it as today |
| Agent needs to move a task out of `aborted` / `completed` | Not possible via a result publish, by design | Task stays terminal while publishes keep arriving; each logs `ownership guard kept on-disk status` | n/a | Operator sets a non-terminal status, or the `create-task` terminal-reopen path (v0.6.0) materializes a fresh instance |
| git-rest unavailable or the file read fails mid-write | Unchanged: the modify function errors, `ResultsWrittenTotal("error")` increments, the Kafka offset is not advanced, the write is retried | `agent_controller_results_written_total{outcome="error"}` increments; `/readiness` reports 503 while git-rest is stuck | Partial progress is impossible — git-rest commits per write | Automatic on git-rest recovery; operator confirms `ResultsWrittenTotal("success")` resumes |
| Task file has corrupt or missing frontmatter delimiters | Unchanged: extraction errors and the write is refused rather than overwriting a corrupted file | Error metric + error log naming the task | n/a | Operator repairs the file in the vault; the next publish succeeds |

## Security / Abuse Cases

- **What the agent controls:** the entire incoming frontmatter map and the entire body. Before this fix, a looping, buggy, or compromised agent could zero its own spawn counters and un-abort itself, giving it unbounded Job spawns — resource exhaustion of the cluster and unbounded model spend. The 2026-08-30 incident is the benign version of that abuse case: 26 Jobs in ~30 minutes with no automated stop.
- **Trust boundary moved:** after this fix, the spawn counters and a terminal status are outside agent control; only the controller/executor and the scanner's Empty-to-Named Reset write the former, and only an operator (or the create-task reopen path) leaves the latter.
- **Still agent-controlled and out of scope:** `phase`, arbitrary additional frontmatter keys, and the body. An agent can still write junk keys into the file — unchanged from today, no new exposure, separate concern.
- **Input validation:** the guard compares parsed YAML values through the typed accessors and never evaluates agent-supplied strings as paths, commands, or field names; the owned-field list and the terminal-status set are fixed package-level declarations, never derived from the incoming payload.
- **Log safety:** the guard log line contains a task identifier, a field name, and two frontmatter values. It must not log the body or the full frontmatter, so a large agent payload cannot flood the controller log through this path.

## Design Decisions

Two decisions are settled here so the implementer does not re-litigate them:

1. **The terminal pin short-circuits escalation, and that is intended.** Pinning `status` during the merge means `applyRetryCounter`'s existing terminal early return now fires on a task whose *on-disk* status is terminal, where today the reverted status kept the task looking live and let the cap/assignee/`spawn_notification` block run. This is a control-flow change, not merely a value change, and it is the desired one: escalation exists to park a live runaway task, and a task an operator already aborted is not that.
2. **The early return covers both terminal statuses.** It matches only `completed` today (`pkg/result/result_writer.go:189`). Leaving it that way would give the single "terminal" class two different control flows — `completed` short-circuits, `aborted` falls through into the cap machinery. An earlier draft of this spec listed "do not extend the early return to `aborted`" as a scope-limiting non-goal; that removal was right in isolation (it looked like unrequested scope) but wrong once decision 1 is on the table, because it manufactures exactly the asymmetry decision 1 exists to remove. The reversal is deliberate and recorded here so it stays legible.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Field-ownership guard, terminal pin + widened terminal early return, guard log, single package-level declaration, all unit specs, and the `CHANGELOG.md` entry | 1, 2, 3, 4, 5, 6, 7 | 1-16, 19 | — |
| 2 | `docs/controller-design.md` § "Frontmatter Merge" ownership table + terminal short-circuit note | 7 (doc half) | 17, 18 | prompt 1 |

AC 20 (Post-Deploy Rung-3) belongs to the spec verification phase, not to a prompt.

Rationale: the whole behavior change is one function's worth of code in one package, so splitting it further would create prompts that cannot be verified independently. The `CHANGELOG.md` entry ships in prompt 1 rather than with the docs prompt so a release cut can never happen with the fix merged and no changelog entry naming it. The docs prompt is separated only so the code prompt's diff stays reviewable and the doc text can describe what actually shipped.

## Do-Nothing Option

Doing nothing keeps both caps decorative: a task that loops keeps looping until a human notices, and the documented `max_triggers` / `max_retries` limits are unreachable by construction. It also keeps `abort` unreliable, which means the one operator lever for stopping a runaway loop cannot be trusted under exactly the conditions it is reached for. The measured cost of the current behavior is 26 Jobs in ~30 minutes on two tasks, stopped manually, with an abort that did not take. The follow-up work (`phase`+`ref`-scoped counters, default-on trigger cap) has no value until this lands, because those refinements all sit on top of a counter that a stale snapshot can reset.
