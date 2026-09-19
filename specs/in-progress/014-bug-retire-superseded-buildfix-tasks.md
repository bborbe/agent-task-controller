---
status: verifying
approved: "2026-09-17T21:44:39Z"
generating: "2026-09-18T05:12:40Z"
prompted: "2026-09-18T05:23:47Z"
verifying: "2026-09-19T14:50:35Z"
branch: dark-factory/bug-retire-superseded-buildfix-tasks
---

## Summary

- The `google-cloud-build-watcher` stamps `supersedes_task_id` and `supersedes_build_id` on every re-emission of a failed-build task, so a build that is re-classified — or whose log fetch recovers — leaves the earlier task file in place alongside the new one.
- **Nothing consumes those markers.** `grep -rn 'supersedes' --include='*.go' .` returns zero hits across every Go file in this repo, and the same grep in `bborbe/agent-task-executor` returns zero. The names do appear in this repo's `docs/`, `prompts/` and `specs/` — prose, not consumption.
- The controller *does* have an auto-supersede, but it is scoped to **recurring-schedule instances** — same-slug candidates ranked by **period-token**. A `Fix Build - <dashboard>` task has neither, so that path never applied to it. Its contract is written down in this repo, twice: the frozen transition set it writes, and the rule that its prior-file write does not heal `target_vault`. Neither document names a build-scoped consumer, because none was built.
- This was inert while the watcher emitted every task at `status: todo`, because the executor dispatches only on `status: in_progress`. `Seibert-Data/google-cloud-build-watcher#8` changes the emission to `status: in_progress` / `phase: execution`, which makes both tasks live.
- The consequence is two concurrent build-fixer runs for one build, against the same repo and commit, and two task files where the build-fix agent's own criteria require exactly one.

## Problem

A task file is created, referenced as superseded by a later one, and never retired — so the fleet runs two agents on one build. The marker is written by a producer in one repo and documented as the consumer's job in a second, and the consumer was never built. Nothing detects the omission: both tasks look healthy, and the duplicate only shows up as a doubled run.

## Goal

A build re-emitted **with supersede markers** leaves exactly one live task. The superseded task carries the same frozen transition set the recurring supersede writes, and the replacement is the only one the executor can dispatch.

## Non-goals

- Do NOT handle a re-emission that carries **no** marker — the controller has nothing to read. The `unknown/log_fetch_failed=true → unknown/log_fetch_failed=false` flip is exactly that case on the producer's released code, whose `supersedes_build_id` assignment is gated behind `if bucket != BucketUnknown`. The producer-side fix is committed but not yet merged (`Seibert-Data/google-cloud-build-watcher#8`, which emits the marker on every path); until it ships that path is unhandled, and it is **not** covered by this spec's acceptance criteria.
- Do NOT add a periodic sweep for tasks missed by the crash window (see Failure Modes). The retirement is a create-time hook, not a reconciler.
- Do NOT change the producer's marker semantics, the watcher's emission, or `Seibert-Data/google-cloud-build-watcher#8`.
- Do NOT replace or widen `supersedePriorRecurringTask` — this is a sibling mechanism on the same code path, not a generalisation of it.
- Do NOT add a config knob, an opt-out flag, or a tunable bound. The retirement is unconditional when a marker is present.
- Do NOT retro-close the historical `Fix Build` tasks already sitting in the vault — those are operator cleanup, tracked by this task's own vault entry.

## Assumptions

- `build_id` is present and identical on both T1 and T2. The watcher stamps it from the same `cdb.Build.ID` on every emission, including re-emissions.
- `task_identifier` is the identity used for `supersedes_task_id`, and `build_id` is the identity used for `supersedes_build_id`. Neither is re-derived from the other.
- The retirement never needs to run on the `ErrTaskAlreadyExists` branch — that branch is the deterministic-ID dedup for a byte-identical re-emission, which by construction carries no new task to point `superseded_by` at.
- The producer's marker semantics do not change under this spec; the watcher keeps emitting `supersedes_task_id` on the bucket-gated path and `supersedes_build_id` on every path.
- The `agent` vault is served by exactly one controller. On dev this assumption is currently **false** — see the Post-Deploy precondition.
- Kafka redelivery of the same command is absorbed by the deterministic task ID before the retirement is reached, so the retirement does not need its own idempotency guard beyond excluding the created task.

## Reproduction

Environment: any stage with the watcher and controller running against the `agent` vault.

1. A master build fails, and the log fetch also fails. The watcher emits `Fix Build - <dashboard>` with `failure_bucket: unknown`, `log_fetch_failed: true`, and no supersede markers. **T1** exists at `Fix Build - <dashboard>.md`.
2. A later pass recovers the fetch and classifies the build (e.g. `trivy`). The watcher emits a **second** task, **T2**, carrying `supersedes_task_id` = T1's deterministic ID and `supersedes_build_id` = the build ID.
3. Observe: two files exist — T1 at `Fix Build - <dashboard>.md` and T2 at `Fix Build - <dashboard> - <T2 taskID[:8]>.md`. The second path is not the bare title: T1 holds that title path live under a *different* `task_identifier`, so `checkTitlePathFree` returns `errTitlePathOccupiedByOtherIdentifier` and `disambiguateCreateTaskRelPath` (`pkg/command/task_create_task_executor.go:417`) suffixes the incoming identifier rather than dropping it. `grep supersedes` finds the markers on T2 only. Nothing reads them. Both files are non-terminal, so both are dispatchable.

**Verbatim evidence, host, 2026-09-17** — the consumer is absent from every Go file in this repo, not merely from `pkg/`:

```
$ grep -rn 'supersedes_build_id\|supersedes_task_id' pkg/
$ echo $?
1
$ grep -rn 'supersedes' --include='*.go' . | grep -v vendor
$ echo $?
1
```

Exit 1 with no output is zero matches in both cases — the marker names appear in this repo's `docs/`, `prompts/` and `specs/` (which is prose, not consumption) and in no compiled code path. The producer-side comment in `Seibert-Data/google-cloud-build-watcher` `internal/watcher/emitter.go` promises "one build never has two live tasks"; this repo has no code that could make that true.

**Scope of the evidence.** The two greps above reproduce from a clean checkout of this repo. The `bborbe/agent-task-executor` grep, the producer-side comment, and the two-controller race in the Post-Deploy precondition are **cross-repo and cluster observations made on 2026-09-17** — they are stated with their timestamps and digests so they can be re-checked, but they are not reproducible from this checkout alone. Re-verify them against the live cluster before relying on them in a later session.

A second path reaches the same state without re-classification: `unknown/log_fetch_failed=true` → `unknown/log_fetch_failed=false` derives a new task ID too, and the watcher emitted **no** marker on that path before its own fix — so T2 carried nothing pointing at T1 at all.

## Expected vs Actual

**Expected**, per the watcher emitter's own documented invariant ("supersedes_build_id lets the consumer retire ANY prior build-fix task for this build … so one build never has two live tasks") and per the build-fix agent's criteria: after a re-emission, exactly one task for that build is live; the superseded one is terminal.

**Actual**: both are live and both are dispatchable.

## Why this is a bug

The producer writes a marker whose entire purpose is to be acted on, and documents the consumer as existing. It does not. The gap was harmless only for as long as nothing dispatched; the moment dispatch is enabled the missing consumer becomes duplicate work. This is a cross-repo contract with one end unimplemented.

## Acceptance Criteria

- [ ] `make precommit` exits 0 — evidence: exit code 0.
- [ ] A CreateCommand carrying `supersedes_build_id` retires any **other** live task for the same build — evidence: a test that creates a task for build B, then a second task for build B carrying `supersedes_build_id: B`, asserting the first task's file carries the **frozen transition set** the sibling mechanism already writes (`docs/period-token-semantics.md:44-53`) — `status: aborted`, `phase: done`, a `completed_date`, and a `superseded_by` equal to the new task's relPath — and that the second task is the only one non-terminal. A bare `status: aborted` write does **not** satisfy this.
- [ ] A CreateCommand carrying `supersedes_task_id` retires the task with that `task_identifier` — evidence: a test asserting the named task carries the same frozen transition set, while an unrelated task is byte-identical.
- [ ] A CreateCommand carrying **neither** marker changes nothing — evidence: a spec asserting the pre-existing task's status is byte-identical before and after, and `git diff` over the task directory is empty.
- [ ] The existing same-state idempotency still holds — evidence: a spec asserting that re-emitting the *same* build with the same bucket and the same log-fetch state creates no second file and retires nothing (the deterministic UUID5 already collapses it, and the retirement must not fire on the producer's own first filing, where `supersedes_build_id` names the task itself).
- [ ] The retirement is best-effort and never blocks creation — evidence: a spec where the supersede target is unreadable or unparseable; the new task is still created and no error is returned.
- [ ] The retirement's record is greppable and mechanism-specific — evidence: a spec asserting that the commit message passed to `AtomicReadModifyWriteAndCommitPush` for a retired task contains `auto-supersede build-fix:`, mirroring the sibling assertion at `pkg/command/task_create_task_executor_test.go:932` (which asserts `ContainSubstring("auto-supersede prior recurring task")` on that same call argument). Asserting on the commit message rather than on `glog` output is deliberate — the message is a return value the existing fake already exposes, so the assertion is mechanical, whereas a `glog` assertion would need a log sink. The four failure-path `glog` lines are therefore asserted by **nothing automated** — the source grep in Verification proves only that the strings exist, and the Post-Deploy log grep matches the success line just as well as a failure line, so it cannot confirm them either. That is stated here rather than glossed: the failure-path prefix is a frozen contract verified by reading the source, and it stays unverified until a real failure emits one.
- [ ] The contract outlives this spec — evidence: three doc artifacts carry it, so the knowledge does not die with a completed spec.
  - `docs/period-token-semantics.md` qualifies the `created_by` line with the frozen literal `written only by the recurring mechanism`, placed within two lines either side of the field — evidence: `grep -C2 'created_by: recurring-task-creator' docs/period-token-semantics.md | grep -q 'written only by the recurring mechanism'`. A frozen literal rather than a prose judgment, for the same reason the log prefix is frozen: a looser probe such as `grep -q 'recurring'` over that output passes trivially, because the matched line itself already contains `recurring-task-creator`. `-C2` rather than `-A2` because the field is the last line of a fenced block, so a qualifier written as a preceding sentence must also satisfy the check.
  - `docs/controller-design.md` documents the build-fix supersede's frozen `auto-supersede build-fix:` prefix beside the existing supersede paragraph — evidence: `grep -n 'auto-supersede build-fix:' docs/controller-design.md` returns ≥1 line.
  - `CHANGELOG.md` carries the change under an `## Unreleased` **or next-version** section — evidence: `grep -n 'retire' CHANGELOG.md` returns a line under the top heading. Not "under `## Unreleased`" alone: this repo's top heading is currently `## v0.10.1`, there is no `## Unreleased`, and `.maintainer.yaml` sets `autoRelease: true`, so the section is renamed on the next release cut. The wording mirrors the precedent — `specs/in-progress/004-recurring-task-supersede-scan-collapse.md:90`, which has an AC amending this same doc.
- [ ] The recurring-schedule supersede is unaffected — evidence: negative evidence rather than "the tests pass". `git diff origin/master -- pkg/command/task_create_task_executor_test.go` shows no deletion or weakening of the existing supersede assertions — the `AtomicReadModifyWriteAndCommitPushCallCount` checks and the `ContainSubstring("auto-supersede prior recurring task")` assertion at `:927-939` survive verbatim — and `make test` exits 0.
- [ ] **Post-Deploy (Rung-2):** on dev, a re-emitted build leaves one live task — evidence: after emitting a task for build B and then a superseding task for build B, `grep -l 'build_id: B' tasks/` lists two files but exactly one is non-terminal.
  - `deploy_check:` `kubectldev -n agent rollout status statefulset/agent-task-controller >/dev/null && kubectldev -n agent get pod agent-task-controller-0 -o jsonpath='{.status.containerStatuses[0].imageID}' | sed 's|.*@||'`
  - `deploy_target:` `$(gcloud artifacts docker images describe europe-west3-docker.pkg.dev/smedia-octopus-dev/octopus/agent-task-controller:dev --format='value(image_summary.digest)')`

  The controller is a **StatefulSet**, not a Deployment: `get deploy` returns `NotFound` with exit 1, which Phase 0.5 reads as an execution error rather than a staleness verdict. The check reads the **running pod's** `imageID`, not the pod template's `image` — the template is *desired* state, so a stalled rollout shows the new tag while the old binary serves, and it renders a mutable `:dev` tag that can never compare cleanly against a digest.

  **Precondition, verified against the live dev cluster 2026-09-17 — this rung's evidence is flaky until it is cleared.** The `agent` namespace runs a *second* controller, `statefulset/agent-task-controller-agent`, pinned at `europe-west3-docker.pkg.dev/smedia-octopus-dev/octopus/bborbe/agent-task-controller:v0.3.1` (digest `sha256:25b0a66…`) — a GAR path whose newest tag is `v0.3.1` while the current mirror path `octopus/agent-task-controller` carries `dev`. It declares the same `VAULT_NAME=agent` and consumes the same `agent-task-v1-request` topic: at `21:19:18.636` on 2026-09-17 both pods logged the same command ID (`b5094c76-…`), one creating the file and the other returning `ErrTaskAlreadyExists`. Both write — over the last 2000 log lines the `:dev` pod logged 2 created / 7 occupied, the stale pod 5 created / 4 occupied. When the stale pod wins, the create path returns at the `ErrTaskAlreadyExists` branch *before* `writeTaskFile` and before the retirement, so no retirement fires and two live tasks remain. Whichever pod is the intended one, the other must stop consuming this topic before the rung is run — otherwise the rung reports a race, not a defect.

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

- `make precommit` — fmt, generate, test, lint, vet, vuln, license all clean
- `make test` — the full suite passes, including the new specs
- `test "$(grep -rn 'supersedes_build_id\|supersedes_task_id' pkg/ | wc -l | tr -d ' ')" -gt 0` — the markers are read somewhere in `pkg/`, where this grep previously returned zero hits and exit 1. Stated as a count assertion rather than a bare grep so the check fails on an empty result instead of passing on a command that printed nothing. Like the log-prefix grep below, this proves the strings appear in the source — a comment or a string literal satisfies it — so it is a presence check, not evidence that the retirement runs. The behavioural evidence is the ACs, not this line.
- Each of the four **failure** discriminators the Failure Modes recovery column names is present in the source — one assertion per row, so a missing discriminator fails rather than passing on the success line alone:

  ```bash
  for op in list read parse write; do
    grep -q "auto-supersede build-fix: $op" pkg/command/task_create_task_executor.go || exit 1
  done
  ```

  This grep proves the strings **exist in the source**, not that they are ever emitted — a comment or an unreachable branch would satisfy it. Only the success path has an emission assertion (the commit-message check in AC7). The four failure-path lines have none, and the Post-Deploy log grep does not close that gap because a success line satisfies it equally. Stated here so the grep is not mistaken for emission evidence.

### Operator-executable (runs on the host after PR merge)

Deploy verification — read the **running pod's** digest (the pod template renders the mutable `:dev` tag and is desired state):

```bash
kubectldev -n agent rollout status statefulset/agent-task-controller >/dev/null \
  && kubectldev -n agent get pod agent-task-controller-0 \
       -o jsonpath='{.status.containerStatuses[0].imageID}' | sed 's|.*@||'
```

The dev pod does not track a release tag: it runs `octopus/agent-task-controller:dev` while Docker Hub carries only semver tags (`v0.10.1` newest, no `dev`), and this repo carries `dev` and `prod` branches — so the dev redeploy is a branch merge plus the mirror rebuild, not a release. Confirm which branch the pipeline builds before relying on it; the digest comparison above is what actually proves the new binary is serving.

It must equal the digest the mirror published:

```bash
gcloud artifacts docker images describe \
  europe-west3-docker.pkg.dev/smedia-octopus-dev/octopus/agent-task-controller:dev \
  --format='value(image_summary.digest)'
```

Clear the precondition above first — scale the stale `agent-task-controller-agent` StatefulSet to zero, or confirm it is no longer consuming `agent-task-v1-request` — then re-run the reproduction on dev and read the outcome off the task files, not off the log:

```bash
grep -rl 'build_id: <B>' tasks/ | wc -l                                      # 2 files
grep -rl 'build_id: <B>' tasks/ | xargs grep -l '^status: aborted' | wc -l    # 1 retired
```

The retirement firing is confirmed by the frozen prefix:

```bash
kubectldev -n agent logs agent-task-controller-0 | grep 'auto-supersede build-fix:'
```

## Desired Behavior

1. On create, the controller reads `supersedes_task_id` and `supersedes_build_id` from the incoming frontmatter.
2. When either is present, it retires the referenced task(s) — any live task for the same `build_id`, and the task whose `task_identifier` matches `supersedes_task_id` — leaving the new task as the only live one.
3. "Retire" writes the **same frozen transition set** the recurring supersede writes — `status: aborted`, `phase: done`, `completed_date`, and `superseded_by` naming the new task's relPath — frozen at `docs/period-token-semantics.md:44-53` and asserted by that mechanism's own specs. The vault must read the same way under both mechanisms; a bare status flip is not sufficient.
   - **Four fields, not five.** The cited range lists a fifth, `created_by: recurring-task-creator`, which this mechanism does **not** write. That value names the recurring publisher and would be false on a build-fix task, whose `created_by` is whatever the watcher's create command carried. The house reading is the four-field one — `scenarios/004-recurring-task-supersede-collapse.md` enumerates exactly those four. An implementer copying the whole block will stamp a wrong provenance and fail this criterion.
4. A create carrying neither marker behaves exactly as today.
5. The retirement is best-effort, matching the existing supersede: a list/read/parse/write failure on any single file is logged and swallowed, and the already-created new task is never rolled back.
6. The retirement never retires the task just created. `supersedes_build_id` always equals the creating task's own `build_id`, so a naive "retire every task whose `build_id` matches" retires the task it has just written — the candidate set must exclude it explicitly.
7. Every line the retirement emits carries the literal prefix `auto-supersede build-fix:` — one frozen, greppable substring covering the success line, every per-file failure line, **and the commit message** it passes to the git client. An operator must be able to separate this mechanism's output from the recurring mechanism's (`auto-supersede:`); a bare `auto-supersede:` line does not satisfy this, because the two mechanisms retire different things and the record must say which one fired.
   - **The operator-facing record is the log lines, not the commit message.** Verified 2026-09-18: `gitRestGitClientAdapter.AtomicReadModifyWriteAndCommitPush` (`pkg/gitrestclient/git_client_adapter.go:123`) passes `message` only to a `glog.V(3)` line and then calls `a.client.Post(ctx, rel, updated)`, which carries path and content only — so the message never reaches git and is **absent from the vault's history**. It is absent from the pod log in practice too: both deployed controllers run `-v=2` and V(3) is above that. The four failure lines are `glog.Warningf` (always emitted) and the success line is `glog.V(2)` (emitted at the deployed verbosity), so those are what an operator actually greps. The commit-message prefix is required because it is the **mechanically assertable** half — the sibling mechanism's spec asserts its message at `pkg/command/task_create_task_executor_test.go:932` — not because it is readable after the fact.

## Constraints

- The existing `supersedePriorRecurringTask` behaviour and its specs must not change; this is a sibling mechanism, not a replacement.
- The retirement is invoked from the same create callback as `supersedePriorRecurringTask`, immediately after `writeTaskFile`, and receives the already-resolved `relPath` as its `newRelPath`. It must not re-derive the path: the path may have been disambiguated (see Reproduction step 3), and `superseded_by` has to name the file that was actually written.
- The prior-file write does not heal `target_vault`, matching `docs/controller-design.md:54`. Prior files go to terminal `aborted` and create commands already route through `ShouldProcess` into the owning vault, so stamping `target_vault` onto a file nobody will re-create is a write with no reader. Adding the heal would be a behaviour change to a write path this spec is not chartered to alter.
- No new write path is introduced: the retirement writes through the same `gitClient.AtomicReadModifyWriteAndCommitPush` the recurring supersede uses, so the vault's write serialization is unchanged.
- The `task_identifier` is the identity used for matching `supersedes_task_id`; the build identity for `supersedes_build_id` is the `build_id` frontmatter field.
- No new configuration knob — the retirement is unconditional when a marker is present.
- The build-fix agent's own criteria require exactly one task file per failed build; this spec is what makes that true.

## Failure Modes

Every line the mechanism emits carries the frozen prefix `auto-supersede build-fix:`; the recovery column names the discriminating substring to grep for. The prefix is asserted, not illustrative — an operator must be able to separate this mechanism's output from the recurring one's.

| Trigger | Expected behavior | Recovery (frozen greppable substring) |
|---|---|---|
| Candidate list fails | The new task is created; the failure is logged and swallowed | `auto-supersede build-fix: list` |
| Supersede target missing or unreadable | The new task is created; the failure is logged and swallowed | `auto-supersede build-fix: read` — then close the stale task by hand |
| Target present but unparseable | The new task is created; the failure is logged and swallowed | `auto-supersede build-fix: parse` — then close the stale task by hand |
| Target write rejected | The new task is created; the failure is logged and swallowed | `auto-supersede build-fix: write` — the target stays live; close it by hand |
| Retirement succeeds | The target is terminal; the new task is the only live one | `auto-supersede build-fix:` … `(build <id> retired)` confirms it fired |
| `supersedes_build_id` equals the created task's own `build_id` | No-op on the created task — this is always true, so the candidate set must exclude it explicitly | None required |
| Both markers present and pointing at different tasks | Both are retired; the new task remains the only live one | None required |
| Two creates for the same build race | Last write wins; at most one live task remains | None required when one controller serves the vault — its read-modify-write serializes. On a stage running two controllers for one vault the loser returns `ErrTaskAlreadyExists` *before* the retirement and no retirement runs; see the Post-Deploy precondition |
| A task carries a marker but its target is already terminal | No-op on that target | None required |
| Process dies after `writeTaskFile` but before the retirement | Two live tasks remain, and **no log line** — the process that would have written it is gone | Operator closes the stale file by hand: write `status: aborted` on it, confirmed by `grep -l '^status: aborted' tasks/<file>`. Nothing re-reads the marker: it lives on the consumed Kafka command, not on either task file, so a redelivery of the same command hits the deterministic-ID dedup at `ErrTaskAlreadyExists` and returns before the retirement. This window has no automatic recovery, and a periodic sweep is an explicit non-goal. |

## Security / Abuse Cases

- **What can an attacker control?** The `task.CreateCommand` Kafka payload — the two marker values — plus the `build_id` of every task file already in the vault. `supersedes_task_id` matches on `task_identifier` **alone, with no ownership check**, so a producer that names an identifier can retire any task in that vault. That is the same authority the recurring supersede already holds over its own slug, and it is bounded deliberately: the retire set is only the tasks matching the named `task_identifier` or the named `build_id`. There is no wildcard form and no "retire all" form — a marker that matches nothing retires nothing.
- **An empty or malformed marker means absent.** `supersedes_task_id: ""` must be treated exactly like an absent key, never as "match every task". The same holds for a non-string frontmatter value, which the existing `TaskFrontmatter.String` accessor already reports as empty. A guard that reads the key's presence rather than its value turns a malformed producer payload into a vault-wide retirement.
- **The marker value must never be joined into a path.** A `supersedes_task_id` of `../../etc/passwd` must be harmless. Candidate paths come from listing the task directory and from each matched file's own path; matching is by comparing the parsed `task_identifier` as a **value**. The marker is never interpolated into `filepath.Join` — the same path-separator rule the sibling mechanism applies to its computed prior token, and for the same reason.
- **What crosses trust boundaries?** The Kafka command topic (already authenticated in-cluster) → git-rest HTTP (gateway-secret auth, already wired). No new trust boundary is introduced, and no credential is read.
- **What can hang, retry forever, or race?** git-rest read/write can hang. The retirement inherits the handler's context deadline, performs one read and one write per candidate, and adds no retry loop of its own. It runs inside the same callback as `writeTaskFile`, so an unbounded wait would delay the create result — the context deadline is the bound, and a failure is swallowed rather than propagated.
- **What data must be validated?** Both marker values must be non-empty strings; a candidate's `build_id` must be present and a string before it is compared; a candidate's `status` must be readable before it is treated as live. An unreadable or unparseable candidate is skipped, not fatal.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Read the markers on create and retire the referenced task(s), best-effort, beside the existing recurring supersede | 1–7 | 1–9 | — |

One prompt: the change is a single behaviour on one code path, and splitting it would leave a window where the marker is read but nothing acts on it.

**AC10 is outside prompt scope by design** — it is the operator-executable Post-Deploy rung, run on the host after the merge, not by the container. AC1–AC9 are the container-executable set.

**Size, stated rather than hidden:** 7 Desired Behaviors × 10 Acceptance Criteria = 70, above the 50 threshold, across 2 code layers (the create-executor hook and its specs) — under the 3-layer threshold. Pruning does not clear it: dropping both AC1 and AC9 still leaves 7 × 8 = 56. AC1 (`make precommit`) has a direct precedent (`specs/in-progress/002-…:102`, `004-…:92`). AC9 (the recurring specs still pass) has **no** direct precedent — 002 states the equivalent as a Constraint and 004 as a behavioural regression AC — and is kept deliberately as the stricter form, because the "what must not regress" preflight answer made verifiable is worth more than the arithmetic. The single-prompt decomposition above is the required mitigation, and the reviewer sees the count rather than discovering it.

## Do-Nothing Option

**If we don't do this, the answer is time-sensitive, and that is the whole argument.** On the producer's *released* code the marker is harmless: the watcher emits every task at `status: todo`, the executor dispatches nothing, and both the original and the re-emission sit inert in the vault. That is exactly why the gap survived. `Seibert-Data/google-cloud-build-watcher#8` changes the emission to `status: in_progress` / `phase: execution`, and the moment it ships, every re-classified build runs **two** build-fix agents against the same repo and the same commit, leaving two task files where this task's own criteria require exactly one.

**The tax is real but not a safety one.** The build-fix agent is analysis-only — it writes `## Failure Analysis` and clears the assignee; it never clones, edits, or pushes — so a duplicate run costs tokens and operator attention rather than corrupting anything. The operator also cannot tell which of the two enriched tasks to trust, and the second one's analysis is written against a task the first already closed.

**Alternatives considered:**

- **Retire by hand, with a sweep script.** Rejected as the primary fix: the marker lives on the consumed Kafka command, not on either task file, so nothing in the vault re-reads it. A hand sweep would be a permanent manual tax on an event that fires on *every* re-classification, and it would leave the producer's documented invariant false.
- **Stop the watcher re-emitting.** Rejected: re-emission is the mechanism that corrects a build first filed with an unreadable log. Removing it trades a duplicate task for a permanently wrong route, which is the worse failure.
- **Leave the emission at `status: todo`.** Rejected: that is the defect `#8` exists to fix, and it disables the entire chain — no build-fix task would ever dispatch.
- **Accept the duplicate and document it.** This was the interim decision taken on 2026-09-17 so the chain could dispatch at all. It is a deliberate, recorded debt, and this spec is its repayment.

**Why now rather than later:** the change is one hook on one code path, and the alternative is leaving a cross-repo contract with one end unimplemented — the state that produced this task in the first place.
