---
status: prompted
tags:
    - dark-factory
    - spec
approved: "2026-09-16T22:05:04Z"
generating: "2026-09-16T22:05:05Z"
prompted: "2026-09-16T22:25:52Z"
branch: dark-factory/coalesce-escalation-notifications
---

## Summary

- The controller publishes one `agent-escalation` notification per escalated **task file**, not per underlying PR issue. A retried PR review mints a new task file — `--force` appends ` - retry-<taskid[:8]>` to the title — so the retry gets its own filename, its own assignee and therefore its own publish.
- Measured on disk 2026-09-16: one PR produced six task files and six pings; the operator's Telegram carries ~8-11 duplicate escalation pings a day.
- The existing dedup is deliberately narrow and does not cover this: `publishEscalation` already suppresses a re-write of an already-parked task (it publishes only when a non-empty assignee was actually cleared), but a **new file for the same underlying issue** is a new escalation and pings again.
- The fix coalesces at the producer: one publish per `(repo, PR number)` per 30-minute window, measured on the clock the result writer already injects. The write, the park, the message, the metadata and the deeplink are unchanged — only the repeat ping is dropped.
- Fail-safe by construction: a task name that does not match the frozen format publishes **uncoalesced**, so a format change degrades to today's behaviour (one ping per file) and never to silence.

## Problem

Escalation exists so a parked task reaches the operator — `publishEscalation`'s contract is "the ping is what reaches the operator". A retried PR review produces a **new** task file rather than a re-write of the parked one, so each retry re-announces an escalation the operator has already been told about. Nothing about the second ping carries a new action: the task is already parked, already in the inbox, already linked. The cost is not the extra message; it is that the channel becomes noise, and a channel the operator learns to skim is a channel that misses the genuine escalation. The measured volume (2325 escalated task files in the OpenClaw vault, 1504 of them PR-shaped, ~11.6 repeat escalations a day inside a 30-minute window since 2026-08-01) is what makes it noise rather than an occasional duplicate.

## Reproduction

Environment: OpenClaw vault `tasks/`, read 2026-09-16 from the host; controller `agent-task-controller:v0.10.0` on dev and prod (`.maintainer.yaml` → `release.autoRelease: true`; the deployed pins are the `image.tag` entries in `nuke/agent/values-{dev,prod}.yaml`).

1. A PR gets a review task. The reviewer agent escalates — it clears its own assignee with `phase: human_review` — and the controller publishes one `agent-escalation`.
2. The watcher's `--force` retry mints a **new** task file: `appendRetryToken` appends ` - retry-<taskid[:8]>` to the title (`github-pr-watcher/pkg/filename.go`), so the retry carries a new filename, a new `task_identifier` and its own assignee.
3. The retry escalates the same way. The controller publishes a second `agent-escalation` for the same PR.
4. Repeat per retry, and per new head SHA on the same PR.

Observed evidence, verbatim:

```
$ ls ~/Documents/Obsidian/OpenClaw/tasks | grep 'bborbe-go-version-watcher - 15'
PR Review github - bborbe-go-version-watcher - 15 - 28915b31 - feat-publish-go-release-notification-alongside-the-go-version-update-task - retry-fb0b7cd0.md
PR Review github - bborbe-go-version-watcher - 15 - 28915b31 - feat-publish-go-release-notification-alongside-the-go-version-update-task.md
PR Review github - bborbe-go-version-watcher - 15 - 2d12d9f5 - feat-publish-go-release-notification-alongside-the-go-version-update-task.md
PR Review github - bborbe-go-version-watcher - 15 - 5e33e5ff - feat-publish-go-release-notification-alongside-the-go-version-update-task.md
PR Review github - bborbe-go-version-watcher - 15 - c49193d9 - feat-publish-go-release-notification-alongside-the-go-version-update-task.md
PR Review github - bborbe-go-version-watcher - 15 - e4410722 - feat-publish-go-release-notification-alongside-the-go-version-update-task.md
```

One PR, six task files, six distinct `task_identifier` values, five distinct `ref` values — and two of the files share `ref: 28915b31…` while carrying different `task_identifier`s (`e56bc4fe-ccc9-5f3d-a066-efa3d751a24a` vs `fb0b7cd0-11e1-54d3-8d9d-3dec49e362ce`). Scale of the observed condition, all measured 2026-09-16:

- `grep -rl --include='*.md' '^previous_assignee:' ~/Documents/Obsidian/OpenClaw/tasks | wc -l` → `2325` escalated task files.
- Of those names, `1504` match `^PR \S+ \S+ - (\S+) - ([0-9]+) - \S+` and `821` do not (676 `Update Go …`, 80 `Release …`, 31 `Dark Factory Implement github …`, 28 `Build Failure github …`, six one-offs).
- The 1504 matching files collapse to `1136` distinct `(repo, number)` keys; `238` keys hold ≥2 files, covering `606` files; the worst key, `bborbe-maintainer#17`, holds `8`.
- Since 2026-08-01 the OpenClaw vault `tasks/` gained `3174` PR-named task files across `568` keys with ≥2 creations; of the `877` consecutive same-key creation pairs, `399` are ≤15 minutes apart and `534` are ≤30 minutes apart (median gap 18.2 minutes) — `534` pairs over 46 days is `11.6` a day, the same order as the operator's reported `~8-11` duplicate pings a day.
- Operator report: `security-review-agent#10` pinged Telegram twice, 11 minutes apart.

## Expected vs Actual

| | Behavior |
|---|---|
| **Expected** | One escalation ping per underlying PR issue while the operator has not yet acted on it. A retry that mints a new task file for the same PR parks the new task without re-pinging: `docs/controller-design.md` § "Assignee-Clear on Escalation" states the intent — "Once a task is parked …, repeated stale agent result publishes are idempotent" — and a retry of the same PR is the same operator action, not a new one. |
| **Actual** | The publish is keyed on the escalation event of a task **file**. Every new file for the same PR clears its own assignee and publishes its own ping, so one PR escalated six times produced six notifications. |

## Why this is a bug

The escalation notification's documented purpose (`publishEscalation`'s doc comment: the ping is what reaches the operator; the message is one click from the notification into the parked task) is a single actionable signal per parked task. The retry path escapes the dedup doctrine in `docs/controller-design.md` § "Assignee-Clear on Escalation" not because the escalation is new but because the **file** is new. The observable harm is channel degradation — an operator who learns that escalation pings repeat stops reading them, and the next genuine escalation is missed. The condition is measured, widespread (238 keys, 606 files) and daily (11.6 repeats/day), so it is a defect in the dedup's coverage rather than an occasional duplicate.

## Workaround

Mute the notification channel and poll the vault inbox for tasks with an empty assignee. Per-escalation manual dedup on the operator side does not scale to ~11 repeats a day and loses the push delivery that the notification exists for.

## Goal

Repeat escalations of one PR inside a coalescing window publish **one** notification. The controller derives `(repo, PR number)` from the task name, and inside a 30-minute window of the last published ping for that key, a further escalation of the same key is parked and committed exactly as before but publishes nothing — with one log line recording the suppression. Distinct PRs always publish; a task name the parse does not recognise always publishes. The delivery path, the message, the metadata, the notification type and the routing table are unchanged.

## Non-goals

- **The delivery core.** `bborbe/notification`, `notification-controller` and the channel services are out of scope and stay untouched: they receive one command per published escalation and hold no repo identity to dedup on (the payload's `Metadata` carries only `taskIdentifier`, `taskName`, `previousAssignee`).
- **The task-name format.** The `PR <kind> <provider> - <owner>-<repo> - <number> - <shortSHA>[- <slug>][- <suffix>]` shape and the ` - retry-<taskid[:8]>` suffix are owned by `github-pr-watcher` (`computeTaskTitle`, `appendRetryToken`). The controller parses defensively and changes nothing there.
- **The write path.** No change to which task files are created, committed or parked; the assignee clear, the `previous_assignee` write, the escalation section and the deeplink are untouched. Only the notification is coalesced.
- **Any knob.** No env var, config field, CLI flag, per-repo allowlist or tunable window. A switch that disables the coalescing re-opens the flood this spec closes, and a tunable window has no named consumer. Do NOT add one — invariant; if a future consumer demands variation, that is a separate spec.
- **Persistence for the window.** No BoltDB, no file, no shared cache. The window is in-process; a restart clears it.
- **A new metric.** The V(1) coalesce log line is the observable. Do NOT add a coalesced-notifications counter — invariant; a dashboard that needs the rate is a separate spec.
- **Backfill.** The 2325 historical escalated task files and the 606 files inside the 238 duplicate keys are left exactly as they are; coalescing starts at the first escalation after this ships.
- **The four escalation rows.** Trigger-cap, retry-cap, `human_review` and the spec-042 partial-update path keep their behaviour; the coalescing sits at the single shared publish point.
- **A new notification type or message shape.** `agent-escalation` is reused; no new `Type`, no new `Target`, no new metadata key.

## Acceptance Criteria

- [ ] `make precommit` exits 0 at the repo root, and `go test -count=1 ./pkg/result/...` exits 0 with the suite green — evidence: exit codes.
- [ ] Unit spec (the coalesced repeat): two `WriteResult` calls inside the window, for two task files of the **same** PR carrying different filenames, different `ref` values and different `task_identifier` values, publish exactly one `agent-escalation`. **Neither task name carries a ` - retry-` suffix; the two names differ in their short SHA** — this pins the dominant measured case (5 of the 6 files in the Reproduction are a new head SHA with no retry token, not a `--force` retry), so a fixture where one name merely contains `retry-` cannot satisfy it — evidence: `Expect(published).To(HaveLen(1))` in `pkg/result/result_writer_escalation_test.go`.
- [ ] Unit spec (the park is unaffected): after those two calls both task files on disk carry `previous_assignee` and no non-empty `assignee`, and `fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()` is 2 — evidence: file-content assertions on both fixture files plus the counterfeiter call count. Coalescing drops the ping, never the write.
- [ ] Unit spec (distinct keys never coalesce): in one window, `repoA#10` then `repoB#10` (same number, different repo) publish 2; `repoA#10` then `repoA#11` (same repo, different number) publish 2; two task names carrying the same short SHA in different repos publish 2 — evidence: `HaveLen(2)` in each of the three cases.
- [ ] Unit spec (the window boundary on the injected clock): with a publish at T, the same key at T+29min is suppressed and the same key at T+30min publishes — evidence: `HaveLen(1)` after the T+29min escalation and `HaveLen(2)` after the T+30min one, driven by `fakeTime.NowReturns(...)`; `grep -c 'time.Sleep' pkg/result/result_writer_escalation_test.go` returns 0.
- [ ] Unit spec (a suppressed escalation does not extend the window): publish at T, suppress at T+29min, escalate the same key again at T+30min — three escalations, 2 publishes — evidence: `HaveLen(2)` after the third call.
- [ ] Unit spec (fail-safe): two escalations inside one window whose task names do not match the frozen format (a `Build Failure github - bborbe-agent - deadbeef` name and a `Update Go bborbe-vault-cli f9b19bd` name) publish 2 — evidence: `HaveLen(2)`.
- [ ] Unit spec (a failed publish does not consume the window): the first escalation's send returns an error and the same key escalated again inside the window publishes — evidence: `HaveLen(2)` with the first `SendPublishNotificationCommand` erroring, and both `WriteResult` calls returning nil.
- [ ] Unit spec (concurrent escalations of one key publish once): two goroutines escalating the same key inside one window yield exactly one publish — evidence: `Expect(published).To(HaveLen(1))` after both complete, and `go test -race -count=1 ./pkg/result/...` exits 0.
- [ ] Frozen log substring: `grep -n 'escalation notification coalesced' pkg/result/result_writer.go` returns ≥1 line — evidence: grep line count. The line names the coalescing key and the task name.
- [ ] Regression lock (the delivery path is unchanged): `git diff origin/master...HEAD -- pkg/result/result_writer_escalation_test.go | grep '^-[^-]' | grep -c 'Expect('` returns 0 — the range is pinned to `origin/master...HEAD` deliberately: a bare `git diff` compares working tree to index and reads empty on a clean tree, which is vacuously true for *any* implementation, including one that deleted every assertion, and the published command still carries `Type: agent-escalation`, a nil `Target`, the `taskIdentifier` / `taskName` / `previousAssignee` metadata keys, the escalating agent in the message and the `obsidian://open?vault=` deeplink — evidence: negative diff grep plus the existing assertions green under `make precommit`.
- [ ] `docs/controller-design.md` § "Assignee-Clear on Escalation" records the coalescing rule — evidence: `sed -n '/^## Assignee-Clear on Escalation/,/^## Empty-to-Named Reset/p' docs/controller-design.md | grep -ci 'coalesc' || true` returns ≥1, **and** the same range names the window and the key shape, so a one-word insertion cannot satisfy it — `… | grep -cE '30[ -]min' || true` returns ≥1 and `… | grep -cE '\(repo, (PR )?number\)' || true` returns ≥1.
- [ ] `CHANGELOG.md` gains an `## Unreleased` section whose bullet names the coalescing — evidence: `awk '/^## v/{exit} tolower($0) ~ /coalesc/{f=1} END{exit !f}' CHANGELOG.md` exits 0 (position-aware: the match must precede the first released `## vX.Y.Z` heading).
- [ ] **Post-Deploy (Rung-2):** on dev a repeat escalation of one PR is coalesced instead of pinged — the controller log carries ≥1 coalesce line, and for the key named on it the log shows exactly one publish line while ≥2 distinct task names sharing that PR's `PR <kind> <provider> - <repo> - <number>` prefix appear across the publish and coalesce lines. **Precondition:** at least one PR escalated twice inside a 30-minute window since the deploy; an unmet precondition blocks the AC — the spec stays in `verifying` and the walk resumes at the next repeat escalation — it does not fail the spec — evidence: `kubectlnukedev -n dev logs agent-task-controller-openclaw-0 --since=24h | grep -c 'escalation notification coalesced' || true` returns ≥1, and `kubectlnukedev -n dev logs agent-task-controller-openclaw-0 --since=24h | grep -c 'assignee cleared → notification published for task PR Review github - <repo> - <number>' || true` returns exactly 1.
  - `deploy_check:` `kubectlnukedev -n dev get statefulset agent-task-controller-openclaw -o jsonpath='{.spec.template.spec.containers[0].image}' | awk -F: '{print $NF}'`
  - `deploy_target:` `$(git ls-remote --tags --refs origin 'v*' | awk -F/ '{print $NF}' | sort -V | tail -1)`
- [ ] **Post-Deploy (Rung-3):** on prod the coalescing is live **and** the channel is not silenced — over one 24h window the controller log carries ≥1 coalesce line AND ≥2 publish lines naming two different `PR <kind> <provider> - <repo> - <number>` keys, and for a key whose ≥2 escalated task files were created **inside one 30-minute window** the publish line count is exactly 1 (the window scoping is load-bearing: a key escalated twice more than 30 minutes apart correctly publishes twice, so an unscoped per-key count would read a legitimate pass as a failure) — evidence: `kubectlnukeprod -n prod logs agent-task-controller-openclaw-0 --since=24h | grep -c 'escalation notification coalesced' || true` returns ≥1, `… | grep 'assignee cleared → notification published' | sed -E 's/.*task (PR [^ ]+ [^ ]+ - [^ ]+ - [0-9]+).*/\1/' | sort -u | wc -l` returns ≥2, and the per-key count returns exactly 1.
  - `deploy_check:` `kubectlnukeprod -n prod get statefulset agent-task-controller-openclaw -o jsonpath='{.spec.template.spec.containers[0].image}' | awk -F: '{print $NF}'`
  - `deploy_target:` `$(git ls-remote --tags --refs origin 'v*' | awk -F/ '{print $NF}' | sort -V | tail -1)`

Scenario coverage: **NO new E2E scenario.** Every branch — coalesced repeat, distinct keys, the window boundary, non-extension, fail-safe on an unmatched name, the failed publish, the concurrent pair — is reachable through the existing `mocks.GitClient` + `NotificationPublishCommandSenderFunc` harness in `pkg/result/result_writer_escalation_test.go`, with the window driven by the already-injected `libtimemocks.CurrentDateTimeGetter`: no real Docker, no real cluster, no real `gh`. The deployed path is covered by the Rung-2 and Rung-3 ACs, which read the real controller's log instead of a fake.

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

```
make precommit
go test -count=1 ./pkg/result/...
go test -race -count=1 ./pkg/result/...
grep -n 'escalation notification coalesced' pkg/result/result_writer.go
grep -c 'time.Sleep' pkg/result/result_writer_escalation_test.go || true
git diff -U0 pkg/result/result_writer_escalation_test.go | grep '^-[^-]' | grep -c 'Expect(' || true
sed -n '/^## Assignee-Clear on Escalation/,/^## Empty-to-Named Reset/p' docs/controller-design.md | grep -ci 'coalesc' || true
awk '/^## v/{exit} tolower($0) ~ /coalesc/{f=1} END{exit !f}' CHANGELOG.md
```

Expected: `make precommit` exits 0 with the full suite green; both focused `go test` runs exit 0, the race run with no data race on the window state; the frozen-substring grep returns ≥1 line; the new specs contain no `time.Sleep`; no deleted `Expect(` line appears in the escalation spec file; the section-scoped doc grep returns ≥1; the CHANGELOG `awk` exits 0. Every `grep -c` is wrapped in `|| true` because `grep -c` exits 1 on a zero count and would abort a `set -e` verification block on exactly the outcome being measured.

`make precommit` runs from the **repo root**: this repo is a single Go module and `pkg/result/` carries no Makefile, so the per-directory invocation has no target to run — the focused equivalent for the changed package is `go test -count=1 ./pkg/result/...` above.

### Operator-executable (runs on the host after PR merge)

1. Confirm the merge and the tag: the PR is merged to master and the maintainer bot has cut the release (`.maintainer.yaml` → `release.autoRelease: true`; `.dark-factory.yaml`'s `autoRelease: false` is a different gate). A PR link proves the merge, not the release.
2. Build and publish the image by hand — the release tag alone does not produce it (CI runs `make precommit` only): `cd ~/Documents/workspaces/agent-task-controller && git checkout master && git pull && VERSION=vX.Y.Z make build upload`, then `docker manifest inspect docker.io/bborbe/agent-task-controller:vX.Y.Z >/dev/null && echo OK`. Pass `VERSION=` explicitly: the Makefile derives it from the newest tag in the repo, which a release cut mid-build silently re-resolves.
3. Bump the **five** pins in `nuke/agent/`: `MIRROR_IMAGES` in `Makefile`, the `image.tag` of both controller entries in `values-dev.yaml` (`openclaw`, `personal`), and both entries in `values-prod.yaml`. Confirm with `grep -rn 'v<old>' Makefile values-*.yaml` returning nothing for the stages being shipped.
4. Deploy dev, then prod, per [[Deploy Mirrored Agent Service]]: `cd ~/Documents/workspaces/nuke/agent && BRANCH=dev make apply`, then `BRANCH=master make apply` (`BRANCH=prod` is rejected by `Makefile.env`). The Makefile sets `KUBECONFIG` itself; the mirror step pulls from `docker.io` and fails until step 2 has run.
5. Run the Rung-2 read-back on dev and the Rung-3 read-back on prod.
6. Freshness caveat: `deploy_check` compares the running image tag against the newest **remote** tag (`git ls-remote --tags --refs origin 'v*'`, version-sorted), not the local `CHANGELOG.md` — so Phase 0.5 refuses as soon as the release is cut remotely and the deployed image lags. A verification run that happens before the release is cut still sees the pre-fix tag on both sides; the coalesce log line and the per-key publish count are what prove the fix is live.

## Desired Behavior

1. **The coalescing key is `(repo, PR number)`, derived from the task name.** The controller parses the task name with the anchored pattern `^PR \S+ \S+ - (\S+) - ([0-9]+) - \S+` and uses the captured `owner-repo` token together with the PR number. Everything after the short SHA — the title slug, the ` - retry-<taskid[:8]>` suffix, the task kind word and the provider word — is ignored, because all of it varies between retries of one PR while the repo token and the number do not.
2. **One notification per key per 30-minute window, anchored to the last published ping.** An escalation of a key is published when no published ping for that key exists, or when the elapsed time since that ping is 30 minutes or more, and suppressed when the elapsed time is strictly less than 30 minutes. The record holds the time of the last **published** ping, not the time of the last escalation, so a suppressed escalation does not extend the window and a PR that escalates every few minutes is re-announced once per window rather than silenced for as long as it keeps escalating. At most one notification reaches the operator per key per 30 minutes.
3. **Distinct keys never coalesce.** The key is repo-qualified: the same PR number in two different repositories, two numbers in one repository, and two tasks carrying the same head SHA in different repositories each publish their own notification.
4. **An unrecognised task name publishes uncoalesced.** A name that does not match the frozen pattern — a `Build Failure …` name, a `Update Go …` name, a free-form title — publishes exactly as it does today, one ping per escalation, and creates no window state. A format change therefore degrades to today's behaviour, never to silence.
5. **A failed publish does not consume the window.** The send is fire-and-forget and its error is already swallowed; the key is recorded only for a publish the sender accepted — the claim taken under the mutex is released when the sender rejects the command — so a broker outage is followed by the next escalation for that key publishing normally. The write and its commit are unaffected either way.
6. **The park, the message and the delivery path are unchanged.** The task file is still written, committed and parked with `assignee: ""` and `previous_assignee` set, whether or not its ping was coalesced. A published command still carries `Type: agent-escalation`, no `Target`, the `taskIdentifier` / `taskName` / `previousAssignee` metadata, the escalating agent named in the message and a resolvable Obsidian deeplink. Exactly one new log line is added — at V(1), carrying the frozen substring `escalation notification coalesced`, the key and the task name — so a suppressed ping is distinguishable from a delivered one in the pod log.
7. **The window state is in-process, mutex-guarded, and measured on the injected clock.** The decision to publish and the recording of the key happen as one atomic step under the mutex, so two concurrent escalations of one key inside a window produce exactly one publish; the window is pruned on every write, so its size is bounded by the distinct keys seen in the last 30 minutes; and expiry is read from the `libtime.CurrentDateTimeGetter` already injected into the result writer, never from `time.Now()`, so the boundary is testable without sleeping.
8. **The doctrine doc and the changelog record the rule.** `docs/controller-design.md` § "Assignee-Clear on Escalation" gains the coalescing rule — the key, the window, the fail-safe and the reason the suppression sits at the publish point rather than in the write — and `CHANGELOG.md` gains an `## Unreleased` bullet naming the change.

## Constraints

- **Frozen key: `(repoToken, prNumber)`.** It must exclude `ref` (head SHA), `title` and `task_identifier`, because all three vary between retries of the same PR — verified on disk: two retries of one PR share `ref: 28915b31…` but carry different `task_identifier`s, and one PR's six files carry five distinct `ref` values. It must NOT merge two genuinely different issues: GitHub numbers PRs monotonically within a repository, so the owner-qualified repo token plus the number is unique per PR. A counter-example that rules out `(repo, title)`: `bborbe-agent-pi` PRs 9 and 12 both carry the title `update-go-module-dependencies`.
- **Frozen parse: `^PR \S+ \S+ - (\S+) - ([0-9]+) - \S+`**, anchored at the start of the task name. The format is owned by `github-pr-watcher` (`computeTaskTitle`, built at `pkg/filename.go:88`), so this is a cross-repo parse: the controller matches the prefix it needs and tolerates any suffix, including the slug, the retry token and the title truncation `computeTaskTitle` applies.
- **Frozen window: 30 minutes**, a fixed value with no config surface. It is derived from the measured same-key gap distribution (median 18.2 minutes; 534 of 877 consecutive same-key creations inside 30 minutes, matching the operator's reported 8-11 duplicate pings a day).
- **Frozen clock:** the window is measured on `r.currentDateTime`, the `libtime.CurrentDateTimeGetter` the result writer already holds. Never `time.Now()`.
- **Frozen publish point:** `writeAndPublish` → `publishEscalation` in `pkg/result/result_writer.go`, after the commit succeeds. NOT inside `buildResultModifyFn`'s modify closure, which re-runs on every git retry, and not in any of the four escalation rows.
- **Frozen delivery:** `agent-escalation` is reused; no new notification type, no `Target`, the three existing metadata keys, the message text and the deeplink unchanged. The routing table is not touched.
- **Frozen write path:** the escalation recorder, the four escalation rows, the counter logic, the terminal-status short-circuit and the body merge are untouched.
- **Atomic claim, released on failure:** the check-and-record is one step under the mutex, so two concurrent escalations of one key cannot both publish; the claim is released when the sender rejects the command, so a broker outage cannot suppress the escalation that follows. The two rules are one mechanism, not two.
- **In-process state only:** no persistence, no cross-process coordination, no shared cache.
- **No new config field, env var, CLI flag, or metric.**
- **`make precommit` runs from the repo root** (single Go module; `pkg/result/` carries no Makefile). The focused run for the changed package is `go test -count=1 ./pkg/result/...`.
- The existing specs in `pkg/result/result_writer_escalation_test.go` pass with unmodified `Expect(` lines.

## Assumptions

- The controller is the only component that can see the repo: the notification payload's `Metadata` carries only `taskIdentifier`, `taskName` and `previousAssignee`, so the delivery core has no key to coalesce on. This is why the gate is producer-side.
- The task-name format is `PR <kind> <provider> - <owner>-<repo> - <number> - <shortSHA>[- <slug>][- <suffix>]`, built by `github-pr-watcher`'s `computeTaskTitle`, with `appendRetryToken` folding ` - retry-<taskid[:8]>` into the suffix segment. Measured 2026-09-16: 1504 of 2325 escalated task names match the parse; the 821 misses carry no PR number at all and therefore publish uncoalesced.
- The coalescing scope is any task name resolving to the same `(repoToken, number)`, which includes a `PR Review …` and a `PR Override …` task for one PR inside one window: those are two task kinds of the same PR, so the operator gets one ping and both tasks are parked in the inbox. The key deliberately excludes the provider word for the same reason the caller froze the pattern — the deployed fleet publishes `github` names only, and a second provider carrying the same owner-repo token and number would coalesce with it.
- Both deployed controllers run `-v=2` (`logLevel: "2"` in `nuke/agent/values-{dev,prod}.yaml`), so a `glog.V(1)` line is visible in the pod log the Post-Deploy ACs read.
- The vault partition holds: a given PR's task files live in one vault, so one controller process owns a given key and a per-process window is sufficient. Re-read the live pins before asserting which controller serves which vault.

## Failure Modes

| Trigger | Expected behavior | Recovery | Detection | Reversibility | Concurrency |
|---|---|---|---|---|---|
| Notification broker or delivery core unavailable (the send returns an error) | The write commits, the ping is lost, and the key is **not** recorded — the next escalation of that key publishes | Automatic: the next escalation for the key publishes once the sender accepts. Nothing is suppressed while the sender is failing | The existing `publish agent-escalation notification … failed: …` warning line | N/A — no state consumed | N/A |
| The delivery core accepts the publish and never delivers it (throttling, a channel outage downstream of the send) | The window is consumed, because the send contract is "the sender accepted the command"; the operator gets no ping for that key inside the window | The task is parked in the inbox regardless; the next escalation after the window pings again | The missing ping, contrasted with the publish line in the controller log | Partial — the ping cannot be replayed | N/A |
| The task-name format drifts (`github-pr-watcher` changes the prefix, the separator or the field order) | Fail-open: nothing matches, so every escalation publishes — exactly today's behaviour. A format change never silences the channel | Update the anchored pattern in the controller to the new format; the change is one pattern in one file, and the format owner's change is the trigger | The `escalation notification coalesced` line disappears from the controller log while duplicate pings return | Reversible (one pattern change) | N/A |
| Two distinct repositories whose `owner-repo` tokens collide, or two providers sharing a token and a number | Two genuinely different PRs coalesce inside the window — the second PR's task is still written, committed and parked, only its ping is suppressed | None needed for the park: both tasks sit in the vault inbox. The next escalation of either key after the window pings | The coalesce line names both the key and the task name, so the mismatch is readable in the log | Partial — the suppressed ping cannot be replayed | N/A |
| Crash between recording the key and the send completing | The escalation's ping is lost for that key and the window stays claimed until it expires (or until the process restarts and clears the in-memory state) | The next escalation of that key after the window publishes; a restart clears the whole window | A vault commit for an escalated task with no matching publish line and no coalesce line in the log | Partial — one ping per key, bounded by the window | N/A |
| Controller restart (deploy, crash, OOM) | The in-memory window is cleared, so at most one extra ping per key per restart | None needed | Pod restart count; the `started version=` startup line | N/A | N/A |
| Two escalations of one key processed concurrently (two Kafka partitions, one consumer process) | Exactly one publish: the check and the record are one atomic step under the mutex, and the loser is suppressed and logged | None needed | The single publish line plus the coalesce line for the same key | N/A | This row is the concurrency case; the mutex is what makes the pair deterministic |
| A burst of distinct PRs inside one window (fleet-wide failure) | The window map holds one entry per distinct key seen in the last 30 minutes and is pruned on every write; memory is bounded by the escalation rate times the window | None needed | Process memory of the controller pod | N/A | Pruning under the same mutex as the check |
| Clock jump (NTP step, timezone change) | The window is measured on the process clock: a backwards step extends the suppression for that key, a forwards step ends it early. Both directions are bounded to one ping per key | None needed; a restart clears the window | n/a | N/A | N/A |

## Security / Abuse Cases

- **What can an attacker control?** A PR title (which `github-pr-watcher` slugifies into the trailing part of the task name), the task's frontmatter, and any command published to `agent-task-v1-request`. The coalescing key is parsed from the **fixed prefix** of the task name — built from GitHub's owner, repo and PR number — so a crafted title cannot move the key: the pattern is anchored at the start and every captured group is a space-delimited token, so neither ` - ` nor a newline in a title reaches a captured value.
- **What crosses a trust boundary?** Nothing new. The notification command, its `Type`, its `Target`, its metadata keys and the message are unchanged; the window state is in-process and is never serialized, published or written to the vault.
- **What can hang or retry forever?** Nothing new: no loop, no new I/O, no new timeout. The send stays fire-and-forget with its existing error swallow.
- **What can be suppressed?** Only a repeat escalation of the same `(repo, number)` inside 30 minutes, and only the notification — the task is still written, committed and parked, so the operator's inbox is complete. The key is repo-qualified, so a flood of escalations for one repository cannot silence another repository's pings; and a name that does not parse is never suppressed.
- **What must be validated?** The parse is anchored and the unmatched case is explicitly the publish path (fail-open); a matched name never yields an empty repo token or a non-numeric number; unmatched names create no state, so a flood of arbitrary names cannot grow the window map.

## Suggested Decomposition

Prompts are generated in this order — each row is one prompt with a clear scope.

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | The coalescing gate at the publish point: the anchored key parse, the 30-minute window on the injected clock with the atomic check-and-record and its release on send failure, the fail-open branch, the V(1) `escalation notification coalesced` line, plus the unit specs (coalesced repeat, park unaffected, distinct keys, window boundary, no extension, fail-safe, failed publish, concurrent pair) and the regression lock on the existing escalation specs | 1-7 | 1-11 | — |
| 2 | The doctrine and the release note: the coalescing rule in `docs/controller-design.md` § "Assignee-Clear on Escalation" and the `## Unreleased` CHANGELOG bullet, with the two greps that prove them | 8 | 12, 13 | prompt 1 (the doc describes shipped behaviour) |

Prompt-level test style (guidance for the prompt-creator, not a behavioral contract): `pkg/result/result_writer_escalation_test.go` is `Context` / `It` blocks with `writeTaskFile(...)` fixtures, the `NotificationPublishCommandSenderFunc` seam recording into `published`, and `libtimemocks.CurrentDateTimeGetter` for time; assert publish **counts** with `HaveLen`, not only content, and drive expiry by moving the fake clock — never by sleeping. The two Post-Deploy ACs (14, 15) belong to spec verification, not to a prompt: they read the released image on dev and prod.

Rationale: prompt 1 is the whole behaviour and carries every falsifier — a build that always publishes fails the count ACs, one that always suppresses fails the distinct-key, boundary, no-extension and fail-safe ACs. Prompt 2 is documentation and can only describe behaviour that already shipped, so it is sequenced second. There is no third prompt: the change is one gate at one publish point in one package.

## Do-Nothing Option

The current approach is not acceptable. The duplicates are measured, widespread and daily: 238 keys hold 606 escalated files, and since 2026-08-01 the vault gained 534 same-key repeat escalations inside 30-minute windows — 11.6 a day, matching the operator's own report. Each repeat carries no new action, and the cost compounds in the direction that matters: a channel that repeats is a channel that gets skimmed, and the first genuinely missed escalation costs more than every duplicate combined. The fix is one gate at the single publish point, with the write path and the delivery path untouched, so there is no cheaper moment to do it than now.
