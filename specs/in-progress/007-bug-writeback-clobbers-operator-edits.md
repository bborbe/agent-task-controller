---
status: prompted
tags:
    - dark-factory
    - spec
approved: "2026-09-03T18:09:23Z"
generating: "2026-09-03T18:09:44Z"
prompted: "2026-09-03T18:18:35Z"
branch: dark-factory/bug-writeback-clobbers-operator-edits
---

## Summary

- The agent result write-back path clobbers operator edits two ways. The frontmatter merge gives the agent's spawn-time snapshot blanket precedence on `assignee` / `previous_assignee`, so a stale snapshot re-assigns a task the operator parked mid-run. And the body is replaced wholesale, so operator-authored body sections are deleted with no trace except git history.
- Observed live on 2026-08-19: an operator park (`assignee: ""` + `previous_assignee` + a `## Parked` section with resume options) applied to a looping `bborbe/backup` task was destroyed by the next result write within 2m42s — the assignee was restored, the counters reset, and the `## Parked` section deleted. The task kept looping.
- This spec makes `assignee` and `previous_assignee` **operator-owned** in the frontmatter merge: when the key exists on disk, the on-disk value always wins; a spawn/claim may still introduce the key when it is absent on disk; and an incoming `assignee: ""` is always honored — that is the deliverer's deliberate Failed/needs_input clear, not a stale snapshot.
- The body write becomes a **section-level merge** instead of full-body replacement: on-disk headings absent from the incoming body are preserved (an operator `## Parked` section survives), same-named headings are replaced by the incoming content (the agent's fresh `## Result` lands), and the on-disk preamble survives when the incoming body starts with a heading.
- No new write site, no agent-side change, no change to the escalation or assignee-clear machinery. This is the direct follow-on to spec 006, which fixed the counters and the terminal `status` and explicitly left these two halves open.
- Verified by unit tests through the existing mock git harness, then end-to-end on the nuke dev and prod clusters against a real looping task.

## Problem

The controller merges the task file's on-disk frontmatter with the frontmatter carried in the agent's result. The agent does not author that frontmatter from live state: it parses it out of the task-content snapshot injected at spawn time, so the published map is a photograph of the file as it looked before the run started. Merging it with blanket precedence turns every result publish into a lost update that reverts anything written to the file during the run. Two things an operator writes during a run matter and are unprotected: a park (`assignee: ""` — the documented mechanism for stopping a task, per `docs/controller-design.md` § "Assignee-Clear on Escalation") and body sections recording why the task was parked and how to resume it. A task that needs parking is almost always stuck in a retry loop, which means a job is essentially always in flight — so the documented park mechanism fails in exactly the situation it exists to handle: the park lands on disk, and the next in-flight publish restores the old assignee and deletes the operator's notes within minutes. The measured cost: 2026-08-19, `bborbe/backup` spawned 5 times in ~58 minutes, monopolizing the agent's single concurrency slot while 18-19 other tasks queued behind it, and the park intended to stop it was destroyed in 2m42s.

## Reproduction

### Live (prod, 2026-08-19, all timestamps UTC)

`24 Tasks/Update Go bborbe-backup fa19848 redrive.md` was parked by the operator per the documented escalation rule — `assignee: ""` + `previous_assignee: github-update-go-agent`, leaving `status`/`phase` untouched, with a `## Parked` body section recording the reason and three concrete resume options:

1. obsidian-git committed the park at 11:21:59Z (commit `14ad8c008`).
2. The pipeline's git-rest result write at 11:24:41Z (commit `579e23415`) restored `assignee: github-update-go-agent`, reset `retry_count`/`trigger_count` to 0, and DELETED the entire `## Parked` section. Diff stat: 2 insertions, 15 deletions. Survival time: 2m42s.
3. Further git-rest writes followed at 11:47:22, 11:47:26, 11:47:31Z — the task kept looping.

### Related harm (prod, 2026-08-30) — same clobber class, separate write site

A controller restart republished `CreateTaskCommand`s; the reopen-of-terminal-task behavior then reopened already-completed `Analyze Sentry issue NUKE-*` tasks and overwrote their bodies with the freshly-rendered create-template instead of merging — commit `a1e2db729` deleted `## Failure`, `## Analysis`, `## Verdict` from a completed analysis; 14 completed analyses were destroyed and `phase: done` fell 19→5. This harm lives in the create-task reopen write site (`pkg/command/task_create_task_executor.go`'s `writeTaskFile`, a full-replacement `AtomicWriteAndCommitPush` that bypasses `MergeFrontmatter` entirely). It is related context for the body-merge doctrine, NOT fixed by this spec — see Non-goals.

### Deterministic (unit-level, reproducible today)

With a task file on disk containing `status: in_progress`, `phase: ai_review`, `assignee: ""`, `previous_assignee: github-update-go-agent`, and body

```
## Parked

Operator park reason, resume options.

## Result
Status: failed
```

call `WriteResult` with an incoming frontmatter of `assignee: github-update-go-agent` and body `## Result\nStatus: failed\n`.

Observed today: the written file contains `assignee: github-update-go-agent` and the written body is exactly the incoming body — the park is reverted and `## Parked` is gone.

## Expected vs Actual

| | Expected | Actual today |
|---|---|---|
| On-disk `assignee: ""` (park), incoming `assignee: github-update-go-agent` | file keeps `assignee: ""`; guard logs `field assignee` | file becomes `github-update-go-agent` — the park is destroyed (2026-08-19, survival 2m42s) |
| On-disk `previous_assignee: github-update-go-agent`, incoming snapshot carries a different value | file keeps `github-update-go-agent` | file takes the incoming value |
| On-disk no `assignee` key, incoming `assignee: backtest-agent` (spawn/claim) | file becomes `backtest-agent` | file becomes `backtest-agent` — same, must not regress |
| On-disk `assignee: claude`, incoming `assignee: ""` (deliverer Failed/needs_input clear, spec 039) | file becomes `assignee: ""` | file becomes `assignee: ""` — same, must not regress |
| On-disk body has `## Parked` (+ reason), incoming body has only `## Result` | `## Parked` + reason preserved, `## Result` lands | `## Parked` deleted wholesale (2026-08-19) |
| On-disk body has `## Result` (old content), incoming has `## Result` (new content) | `## Result` replaced by new content | `## Result` replaced — same, must not regress |
| On-disk preamble (task description), incoming body starts with `## Result` (no preamble) | on-disk preamble preserved | on-disk preamble deleted |
| On-disk preamble-only `First content`, incoming preamble-only `Second result` | body becomes `Second result` | body becomes `Second result` — same, must not regress |
| At-cap task already parked (`assignee: ""`, escalation section present), stale result arrives | assignee stays `""`, escalation section count stays 1, phase restored | same today — must not regress |

## Why this is a bug

`docs/controller-design.md` § "Assignee-Clear on Escalation" documents `assignee: ""` as THE mechanism an operator uses to park a task, and the § "Escalation rule" in the Agent Task File Contract says the same. A task that needs parking is almost always stuck in a retry loop with a job in flight, so the park is written to disk at the same moment a result publish is about to overwrite it with the spawn-time snapshot. The documented park mechanism therefore fails under exactly the conditions it is reached for. The body is the same story: `## Parked` is the operator's durable record of why the task was stopped and how to resume it, and wholesale replacement deletes it with no trace except git history — the operator's park work is silently undone, and the resume context vanishes.

## Workaround

Until the fix lands, stopping a looping task requires removing the agent's ability to be re-dispatched rather than parking it: clear `assignee` and delete the in-flight Job (per spec 006's workaround). Any body note must be re-applied after every result write. Neither action is durable against a stale snapshot, and an operator cannot know a note was deleted without diffing git history.

## Goal

An operator park (`assignee: ""` + `previous_assignee` + a `## Parked` body section) applied to a task with a job in flight survives the next agent write-back, and operator-authored body sections survive every write-back — merged, not replaced. The agent's own result still lands exactly as it does today, through the same single write-site chokepoint; the deliverer's deliberate Failed/needs_input assignee-clear keeps working; and the escalation parks and `human_review` assignee-clears are untouched.

## Non-goals

- Do NOT change the create-task reopen write site (`pkg/command/task_create_task_executor.go` `writeTaskFile`). The 2026-08-30 harm (14 destroyed `Analyze Sentry issue NUKE-*` analyses) happens there, and it bypasses `MergeFrontmatter` and the body merge entirely — fixing it (merge-at-reopen) is a separate spec. This spec's body merge covers only the result write-back chokepoint (`WriteResult`).
- Do NOT change the agent side or the deliverer (`agent` repo `result-deliverer.go`). Its Failed/needs_input `assignee: ""` clear is already the contract; the empty-clear exception in DB 1 exists precisely so no agent change is needed.
- Do NOT change `clearAssignee` / `ClearAssigneeIfHumanReview` — signatures, call sites, and effects are frozen; they already run post-merge and are unaffected by the guard.
- Do NOT change phase routing, `restoreExistingPhase`, the escalation-section writers, the terminal status pin, or the controller-owned counter rules from spec 006.
- Do NOT add a config flag, env var, or per-task opt-out for the ownership rules or the empty-clear exception — invariant; if a future consumer demands variation, that is a separate spec.
- Do NOT extend ownership to `stage`, `tags`, or any other operator-backfilled key — a distinct case, separate spec.
- Do NOT update the [[Agent Task File Contract]] vault page in this work. Documenting what an operator edit is guaranteed to survive is a follow-up task outside this spec's code scope.
- Do NOT change the "preserve bare `---` lines in the body" behavior or introduce escaping.

## Acceptance Criteria

- [ ] Unit spec (operator park survives — task Success Criterion 1): on-disk `assignee: ""` + `previous_assignee: github-update-go-agent`, incoming `assignee: github-update-go-agent` → the written file contains `assignee: ""` and `previous_assignee: github-update-go-agent` and does NOT contain `assignee: github-update-go-agent` — evidence: positive + negative file-content assertions in `pkg/result/result_writer_test.go`.
- [ ] Unit spec (empty-clear exception): on-disk `assignee: claude`, incoming `assignee: ""` → the written file contains `assignee: ""` and does NOT contain `\nassignee: claude` — evidence: positive + negative file-content assertions. The existing `It("preserves phase and keeps assignee cleared when deliverer published needs_input (post-spec-039 shape)")` (`pkg/result/result_writer_test.go:939`) must pass with unmodified `Expect(` lines.
- [ ] Unit spec (introducibility — the fix is not "existing always wins"): on-disk has no `assignee` key, incoming `assignee: backtest-agent` → the written file contains `assignee: backtest-agent` — evidence: file-content assertion.
- [ ] Unit spec: on-disk `previous_assignee: A`, incoming `previous_assignee: B` → the written file contains `previous_assignee: A` and does NOT contain `previous_assignee: B` — evidence: positive + negative file-content assertions.
- [ ] Unit spec (guard decision): `MergeFrontmatter` with on-disk `assignee: claude` vs incoming `assignee: other` returns exactly one decision naming `assignee` with kept `claude` / rejected `other`; the same fixture with equal values returns zero decisions — evidence: decision-count and field/value assertions in `pkg/result/result_writer_guard_test.go` (mirrors the existing `trigger_count` decision specs).
- [ ] Unit spec (operator body section survives — task Success Criterion 2): on-disk body `## Parked\n\n<operator reason>\n\n## Result\nStatus: failed\n`, incoming body `## Result\nStatus: failed\n` → the written body contains `## Parked` and `<operator reason>` and the incoming `## Result` content — evidence: file-content assertions.
- [ ] Unit spec (same-heading replacement): on-disk body `## Result\nOld content\n`, incoming body `## Result\nNew content\n` → the written body contains `New content` and does NOT contain `Old content` — evidence: positive + negative file-content assertions.
- [ ] Unit spec (preamble preservation): on-disk body `Tags: [[Task]]\n\n---\n\ndescription\n\n## Details\n- x\n`, incoming body `## Result\nStatus: failed\n` (no preamble) → the written body contains `Tags: [[Task]]`, `description`, and `## Result` — evidence: file-content assertions.
- [ ] Unit spec (preamble replacement — heading-less bodies keep today's behavior): on-disk body `First content\n`, incoming body `Second result\n` → the written body contains `Second result\n` and does NOT contain `First content` — evidence: positive + negative; the existing `It("fully replaces content on second call")` (`pkg/result/result_writer_test.go:221`) passes with unmodified `Expect(` lines.
- [ ] Escalation parks unchanged: the existing `It("writes assignee: empty and preserves phase: <phase> at retry cap")` and `It("writes assignee: empty and preserves phase: <phase> at trigger cap")` specs pass with unmodified `Expect(` lines — evidence: the specs green in `make precommit`.
- [ ] Carve-out (named in Constraints): the existing `It("keeps assignee empty and phase unchanged when stale result arrives at already-parked task")` (`pkg/result/result_writer_test.go:1287`) passes after its on-disk fixture string gains `previous_assignee: claude` (the realistic parked-task state); all its `Expect(` lines are unmodified — evidence: the spec green plus the fixture diff confined to that one on-disk fixture string.
- [ ] PIt fate: `grep -c 'PIt(' pkg/result/result_writer_test.go` returns `0` — the pending `PIt("preserves prior ## Review content when writing a new result")` (`pkg/result/result_writer_test.go:1852`) is un-pended and rewritten as a passing `It` asserting the section-merge doctrine: a same-named `## Review` heading is replaced by the incoming content, the on-disk preamble (`# Body`) survives, and the write commits exactly once — evidence: grep count `0` plus the block's assertions.
- [ ] Test-suite integrity: `git diff -U0 pkg/result/result_writer_test.go | grep -c '^-[^-]' || true` — every deleted line falls inside the two named carve-out blocks (the line-1287 fixture and the PIt rewrite); `git diff -U0 pkg/result/result_writer_test.go | grep '^-[^-]' | grep -c 'Expect(' || true` returns `0` for every deleted `Expect(` line outside those blocks — evidence: machine-decidable diff greps.
- [ ] `make precommit` exits 0 with the full Ginkgo suite green — evidence: exit code.
- [ ] `CHANGELOG.md` gains a `fix:` bullet under `## Unreleased` naming the assignee ownership guard and the body section merge — evidence: `awk '/^## v/{exit} /assignee/{f=1} END{exit !f}' CHANGELOG.md` exits `0` (position-aware: the match must precede the first released `## vX.Y.Z` heading).
- [ ] `docs/controller-design.md` § "Frontmatter Merge" documents the operator-owned row and the body merge: `sed -n '/^## Frontmatter Merge/,/^## Terminal Task Status/p' docs/controller-design.md | grep -c 'Operator-owned' || true` returns ≥1 AND `sed -n '/^## Frontmatter Merge/,/^## Terminal Task Status/p' docs/controller-design.md | grep -c 'assignee' || true` returns ≥1 — evidence: two section-scoped grep counts, both `0` today.
- [ ] **Post-Deploy (Rung-2):** on the nuke dev cluster, an operator park (`assignee: ""` + `previous_assignee` + a `## Parked` body section) applied mid-loop to a real looping task survives the next agent write-back — evidence: `kubectlnukedev -n dev logs agent-task-controller-openclaw-0 --since=2h | grep -c 'ownership guard kept on-disk' || true` returns ≥1 with a line naming `field assignee`, and `git log -p -- "24 Tasks/<title>.md"` shows no later commit re-assigning the task or deleting the `## Parked` section.
  - `deploy_check:` `kubectlnukedev -n dev get pod agent-task-controller-openclaw-0 -o jsonpath='{.spec.containers[0].image}' | awk -F: '{print $NF}'`
  - `deploy_target:` `$(sed -n 's/^## \(v[0-9][^ ]*\)$/\1/p' CHANGELOG.md | head -1)`
- [ ] **Post-Deploy (Rung-3):** on the nuke prod cluster, the same park survives the next agent write-back on a real looping task (e.g. a task in the state `Update Go bborbe-backup fa19848 redrive.md` was in) — evidence: `kubectlnukeprod -n prod logs agent-task-controller-openclaw-0 --since=2h | grep -c 'ownership guard kept on-disk' || true` returns ≥1 with a line naming `field assignee`, and the task file's git history shows the park commit surviving (no re-assign commit, no `## Parked` deletion).
  - `deploy_check:` `kubectlnukeprod -n prod get pod agent-task-controller-openclaw-0 -o jsonpath='{.spec.containers[0].image}' | awk -F: '{print $NF}'`
  - `deploy_target:` `$(sed -n 's/^## \(v[0-9][^ ]*\)$/\1/p' CHANGELOG.md | head -1)`

Scenario coverage: **NO new E2E scenario.** Every branch is reachable through the existing `mocks.GitClient` harness in `pkg/result/result_writer_test.go`, which already stages on-disk file content and captures the written bytes — no real Docker, no real cluster, no real `gh`. The runtime path is additionally covered by the Rung-2 and Rung-3 post-deploy ACs.

Test style: `pkg/result/result_writer_test.go` is written as `Context` / `It` blocks with `writeTaskFile(...)` fixtures and `ContainSubstring` assertions on the written file. Use `DescribeTable` for the operator-owned-field rows and plain `It` blocks for the empty-clear, introducibility, body-merge, preamble, and guard-decision cases, matching the surrounding file.

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

```
make precommit
grep -c 'PIt(' pkg/result/result_writer_test.go || true
git diff -U0 pkg/result/result_writer_test.go | grep -c '^-[^-]' || true
sed -n '/^## Frontmatter Merge/,/^## Terminal Task Status/p' docs/controller-design.md | grep -c 'Operator-owned' || true
sed -n '/^## Frontmatter Merge/,/^## Terminal Task Status/p' docs/controller-design.md | grep -c 'assignee' || true
awk '/^## v/{exit} /assignee/{f=1} END{exit !f}' CHANGELOG.md
```

Expected: `make precommit` exits 0 with all specs green; the `PIt(` grep returns 0; the deleted-line count is accounted for entirely by the two named carve-out blocks, with no deleted `Expect(` line outside them; both section-scoped doc greps return ≥1; the CHANGELOG `awk` exits 0. Every `grep -c` is wrapped in `|| true` because `grep -c` exits 1 on a zero count and would abort a `set -e` verification block on exactly the outcome being measured.

### Operator-executable (runs on the host after PR merge)

1. Deploy the merged fix to dev, then prod, and confirm the image tag matches the release (see the Rung-2 / Rung-3 `deploy_check` lines above).
2. On dev (then repeat on prod): pick a real looping task (e.g. one whose `retry_count` or `trigger_count` is visibly climbing). While a job is in flight, apply the park per the documented escalation rule — `assignee: ""`, `previous_assignee` preserved, `status`/`phase` untouched — and add a `## Parked` body section with a reason.
3. Wait for the next result publish. Confirm in the vault: `git log -p -- "24 Tasks/<title>.md"` shows the park commit and NO later commit re-assigning the task or deleting `## Parked`; `head -20 "24 Tasks/<title>.md"` shows `assignee: ""` and the `## Parked` section.
4. Confirm the guard fired: `kubectlnukedev -n dev logs agent-task-controller-openclaw-0 --since=2h | grep 'ownership guard kept on-disk'` — expect a line naming `field assignee` with the kept and rejected values.
5. Confirm no regression in the agent's result delivery: the same task file shows the agent's `## Result` content present after the write, and the agent is no longer re-dispatched (the park held).

## Desired Behavior

1. **Operator-owned routing fields, with an empty-clear exception.** `assignee` and `previous_assignee` become operator-owned in `MergeFrontmatter`: when the key exists on disk, the on-disk value always wins over a differing incoming value; when the key is absent on disk, an incoming value may introduce it (unlike controller-owned counters, absence on disk does NOT delete the incoming value — a spawn/claim legitimately names an assignee on a task that never carried one). One exception, for `assignee` only: an incoming empty string (`assignee: ""`) is always applied even when the on-disk value is non-empty — this is the deliverer's deliberate Failed/needs_input clear (agent lib `result-deliverer.go`), never a stale snapshot. A discarded differing value flows through the existing `GuardDecision` + `ownership guard kept on-disk` INFO-log path naming `field assignee`; the empty-clear exception produces no decision and no log line.
2. **Deliberate writer assignee mutations are unchanged and keep working.** `clearAssignee` (in `applyTriggerCap` / `applyRetryCap`) and `ClearAssigneeIfHumanReview` keep their signatures, call sites, and post-merge execution inside `applyRetryCounter`: they still set `assignee: ""` and `previous_assignee` on the merged map after the guard has run, so cap parks and `human_review` handoffs are unaffected by the new ownership rule. No special-casing, no new code path.
3. **Body sections merge instead of full replacement.** `buildResultModifyFn` merges the on-disk body with the incoming body by heading: an on-disk `## <heading>` absent from the incoming body is preserved with its content in its on-disk position; a heading present in both is replaced in place by the incoming content; a heading present only in the incoming body is appended after the last on-disk section. Headings are matched by exact heading-line text, and preserved on-disk sections keep their on-disk relative order.
4. **Preamble rule.** Text before the first `## ` heading: the on-disk preamble is preserved only when the incoming body has no preamble (the incoming body starts with a heading); when the incoming body carries its own preamble, the incoming preamble replaces the on-disk preamble. A body with no `## ` heading at all is preamble-only on both sides, so an incoming preamble-only body still replaces an on-disk preamble-only body — today's full-replacement behavior for heading-less bodies.
5. **Escalation sections still append exactly once.** `applyTriggerCap` / `applyRetryCap` append `## Trigger Cap Escalation` / `## Retry Escalation` to the merged body exactly as today; the `containsEscalationSection` dedup keeps working because an on-disk escalation section survives the merge as a preserved heading, and an incoming copy, when present, replaces it rather than duplicating it.
6. **Delimiter validation unchanged.** The on-disk body is still parsed to validate well-formed frontmatter delimiters before any merge; a file with missing or corrupt delimiters still refuses the write (extraction error surfaces, the Kafka offset is not advanced, the write is retried) instead of being overwritten.

## Constraints

- The change stays inside `pkg/result`: `MergeFrontmatter` plus a new package-level declaration of the operator-owned field names (distinct from `controllerOwnedFields`), `buildResultModifyFn` (body merge), and the test files. No new write site, no new Kafka operation, no command-executor change — see `docs/controller-design.md` § "Assignee-Clear on Escalation" and spec 042.
- The test suite must pass with `Expect(` lines unmodified, with exactly two carve-outs: (1) the on-disk fixture string of `It("keeps assignee empty and phase unchanged when stale result arrives at already-parked task")` (`pkg/result/result_writer_test.go:1287`) gains `previous_assignee: claude` — its `previous_assignee: claude` assertion currently depends on `clearAssignee` capturing a stale revived assignee, which the guard makes impossible, and a real parked task carries `previous_assignee` on disk (the 2026-08-19 evidence); (2) the pending `PIt("preserves prior ## Review content when writing a new result")` (`pkg/result/result_writer_test.go:1852`) is un-pended and its expectations rewritten to the section-merge doctrine — a same-named heading is replaced, so its current `SatisfyAny(Prior review content, ## Outdated by)` cannot pass, and it is skipped today so no assertion is lost. No other spec may lose or weaken an assertion.
- The guard is unconditional: no config flag, env var, or per-task opt-out. The empty-clear exception applies to `assignee` only, never to `previous_assignee` — the deliverer never clears `previous_assignee`, and `clearAssignee` remains the sole writer of it, post-merge.
- Heading matching is an exact string comparison on the heading line; the body merge must tolerate both `\n` and `\r\n` line endings and must NOT treat a bare `---` line (horizontal rule) as a heading delimiter.
- The terminal `status` pin and the `controllerOwnedFields` counter rules from spec 006 are frozen; the `operatorOwnedFields` rule is additive and must not disturb the decision list for counters or status.
- The incoming payload is never a valid re-assignment channel for a task that already has an `assignee` on disk; the empty-clear exception is the only agent-controlled assignee write and it is the deliverer's documented contract, unchanged from today.
- `applyRetryCounter` keeps its existing signature; the merged body is passed to it after the body merge, and it continues to append escalation sections and restore the on-disk phase on repeated parks.

## Failure Modes

| Trigger | Expected behavior | Detection | Concurrency | Recovery |
|---|---|---|---|---|
| Operator parks a task mid-run (`assignee: ""` + `previous_assignee` + `## Parked`) | The in-flight result's stale assignee is discarded; on-disk `assignee: ""` and `previous_assignee` win; the guard logs `field assignee` | Task file shows `assignee: ""` and `## Parked` after the next publish; `git log -p -- <file>` shows no re-assign commit | Atomic RMW re-reads on every git retry and the on-disk value wins — this is the concurrent case the spec exists for | none needed |
| Deliverer clears assignee on a Failed/needs_input result | The incoming `assignee: ""` is applied despite a non-empty on-disk value (exception); the task surfaces in the operator inbox | File shows `assignee: ""` after the publish, phase unchanged | RMW per retry | none needed |
| Stale snapshot carries `assignee: ""` against a mid-run re-delegation (Empty-to-Named reset) | The incoming `""` is honored, reverting the re-delegation — identical to today's behavior, no regression | File shows `assignee: ""` after the publish | RMW per retry | Operator re-delegates once the stale job's results stop arriving; the scanner's Empty-to-Named Reset refills the counters |
| Operator adds a `## Parked` (or any on-disk-only heading) mid-run | The merge preserves the heading and its content in place | Heading still present after the write | n/a | none needed |
| Agent output carries a heading also present on disk (e.g. `## Result`) | The heading is replaced in place by the fresh incoming content; the old content is gone by design | Body shows the new content | n/a | Operator notes inside a same-named heading are superseded — put operator notes in a distinct heading or the preamble, which survive |
| On-disk body is preamble-only and incoming starts with `## ` | On-disk preamble is preserved and the incoming sections are appended | Stale preamble text (e.g. a task description) survives the write | n/a | Operator deletes the preamble manually if it is stale |
| Body contains bare `---` lines | The merge never treats `---` as a heading; body `---` is preserved unescaped (existing spec `preserves bare --- lines in body without escaping` stays green) | That existing spec passes unmodified | n/a | none needed |
| File has missing or corrupt frontmatter delimiters | Unchanged: extraction errors and the write is refused rather than overwriting a corrupted file | Error metric + error log naming the task | n/a | Operator repairs the file in the vault; the next publish succeeds |
| git-rest unavailable or the file read fails mid-write | Unchanged: the modify function errors, `ResultsWrittenTotal("error")` increments, the Kafka offset is not advanced, the write is retried | `agent_controller_results_written_total{outcome="error"}` increments; `/readiness` reports 503 while git-rest is stuck | Partial progress is impossible — git-rest commits per write | Automatic on git-rest recovery; operator confirms `ResultsWrittenTotal("success")` resumes |
| Task file uses CRLF line endings | The section split tolerates both line-ending styles and preserves the file's style | File remains parseable after the write | n/a | none needed |

## Security / Abuse Cases

- **What the agent controls:** the entire incoming frontmatter map and the entire body. Before this fix, a looping, buggy, or compromised agent could re-assign itself (stale `assignee` restoring over an operator park) and delete operator body sections — the 2026-08-19 incident is the benign version: 2m42s park survival and a deleted `## Parked` section with no automated stop.
- **Trust boundary moved:** after this fix, `assignee` and `previous_assignee` are outside the result payload's reach except for one documented lever — an incoming `assignee: ""` from the deliverer's Failed/needs_input branch. That lever is the spec-039 contract, unchanged from today, not a new exposure: it is the deliverer, not a stale snapshot, and it can only park a task, never assign one.
- **Still agent-controlled and out of scope:** `phase`, arbitrary additional frontmatter keys, and the same-named body headings the agent owns (`## Result`, `## Review`, …). An agent can still overwrite the content of those headings — unchanged from today, no new exposure, separate concern.
- **Input validation:** the guard compares parsed YAML values and body heading lines by exact string comparison; it never evaluates agent-supplied strings as paths, commands, or field names. The operator-owned field list is a fixed package-level declaration, never derived from the incoming payload.
- **Log safety:** the guard log line contains a task identifier, a field name, and two frontmatter values. It must not log the body or the full frontmatter, so a large agent payload cannot flood the controller log through this path.

## Design Decisions

Three decisions are settled here so the implementer does not re-litigate them:

1. **The empty-clear exception is load-bearing, not a loophole.** The adopted policy ("on-disk assignee always wins") would silently break the deliverer's Failed/needs_input handoff: the deliverer (`agent` repo) publishes `assignee: ""` in the result payload to park a task for operator review, and the existing spec at `result_writer_test.go:939` locks that behavior in. A strict on-disk-wins rule would revert that clear whenever the task has a named assignee on disk, re-assigning the agent that just asked for human input. The exception — incoming `assignee: ""` is always honored — keeps the deliverer contract intact, keeps the operator park intact (on-disk `""` beats any incoming name), and is never worse than today for the residual stale-`""` edge (identical to today's incoming-wins behavior). It applies to `assignee` only; `previous_assignee` has no empty-clear analogue.
2. **Same-named body headings are replaced, and the pending `## Review` spec is superseded.** The section-merge doctrine replaces a heading that exists in both bodies — that is what lets the agent's fresh `## Result` land without duplication. The pending test `PIt("preserves prior ## Review content when writing a new result")` documented a broader hope (operator annotations inside a same-named heading surviving); under the adopted doctrine those annotations do not survive, and the PIt is un-pended to assert the doctrine instead. Operator content survives in on-disk-only headings and the preamble, which is the durable surface this spec protects.
3. **The 2026-08-30 reopen harm is out of scope despite sharing the root cause's shape.** The 14 destroyed analyses were written by the create-task reopen upsert (`task_create_task_executor.go`), a full-replacement write that bypasses `MergeFrontmatter` and `WriteResult` entirely. This spec's body merge cannot reach that site, and claiming it does would be false. The reopen site needs its own merge-at-reopen spec; the section-merge doctrine defined here is the shape that spec will reuse.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Operator-owned `assignee`/`previous_assignee` guard + empty-clear exception in `MergeFrontmatter`, body section-merge in `buildResultModifyFn`, all unit specs (guard, merge, preamble, introducibility, empty-clear), the two carve-out fixture updates, the PIt rewrite, and the `CHANGELOG.md` entry | 1, 2, 3, 4, 5, 6 | 1-15 | — |
| 2 | `docs/controller-design.md` § "Frontmatter Merge" operator-owned row + body-merge paragraph | 1, 3 (doc halves) | 16 | prompt 1 |

AC 17 (Rung-2 dev) and AC 18 (Rung-3 prod) belong to the spec verification phase, not to a prompt.

Rationale: the whole behavior change is two function-sized edits in one package (`pkg/result`) plus their tests, so splitting further would create prompts that cannot be verified independently. The `CHANGELOG.md` entry ships in prompt 1 so a release cut can never happen with the fix merged and no changelog entry naming it. The docs prompt is separated only so the code prompt's diff stays reviewable and the doc text can describe what actually shipped.

## Do-Nothing Option

Doing nothing keeps the documented park mechanism broken under exactly the conditions it is reached for: a task that needs parking is almost always mid-run, so every operator park against a looping task is at risk of being silently undone within minutes, and any body content the operator wrote (reasoning, resume options) vanishes with no trace except git history. The measured cost of the current behavior is a park destroyed in 2m42s on 2026-08-19 with the task spawning five times in ~58 minutes, plus the operator time spent re-applying lost context. Spec 006 fixed the counters and the terminal `status`; this spec is the remaining half of the write-back clobber class, and the Agent Task File Contract cannot truthfully document a survival guarantee for operator edits until it lands.
