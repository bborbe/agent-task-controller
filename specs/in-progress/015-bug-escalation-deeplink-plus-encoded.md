---
status: approved
tags:
    - dark-factory
    - spec
approved: "2026-09-19T14:20:36Z"
branch: dark-factory/bug-escalation-deeplink-plus-encoded
---

## Summary

- Every `agent-escalation` notification carries an Obsidian deeplink meant to take the operator straight to the parked task file. The link is inert: clicking it opens nothing.
- The cause is one encoder call. The deeplink is built with form encoding, which renders a space as `+`. Obsidian's URI handler does not decode `+` as a space, so the path it resolves contains literal `+` characters and matches no file.
- This affects effectively every escalation ever sent: task filenames routinely contain spaces — every `PR Review github - <repo> - <n> - <sha> - <slug>` name does.
- The fix is to emit `%20` for spaces, the encoding the vault's own convention documents and every other `obsidian://` link in the vault already uses.
- Measured against a live Obsidian instance: the delivered `+` form leaves the resolved path unchanged; the `%20` form moves it to the front of the recently-opened list.

## Problem

Escalation exists so a parked task reaches the operator, and the deeplink is what makes that message one click from the notification into the task file. Today that click does nothing: the operator reads the ping, taps the link, and lands nowhere — so every escalation costs a manual search through the vault at exactly the moment they are triaging. The defect is total rather than occasional, and not only because the task names that escalate most often contain spaces: the `personal` controller runs with `taskDir: "25 Tasks"` (`nuke/agent/values-dev.yaml:129`, `nuke/agent/values-prod.yaml:95`), so its vault-relative path **always** contains a space and every one of its escalations was broken whatever the filename was. The `openclaw` controller (`taskDir: tasks`) breaks whenever the task name carries a space — which, for `PR Review github - …` names, is most of them.

## Reproduction

Environment: OpenClaw vault `tasks/`, read 2026-09-18 from the host; controller `agent-task-controller` on dev and prod; macOS Obsidian, workspace state at `<vault>/.obsidian/workspace.json`.

1. An agent escalates a parked task — it clears its own assignee — and the controller publishes one `agent-escalation` notification carrying an `obsidian://open?vault=…&file=…` deeplink as the last line of the message.
2. Open the delivered deeplink. Obsidian resolves no file and opens nothing.
3. Repeat with the same path re-encoded as `%20`. The note opens.

The delivered form, copied verbatim from the operator's Telegram:

```
obsidian://open?vault=openclaw&file=tasks%2FPR+Review+github+-+bborbe-pr-review-fixtures+-+2+-+03448af5+-+fixture-real-defect-inverted-health-check+-+retry-288998c8
```

**A/B against a live Obsidian instance**, using the vault's own `.obsidian/workspace.json`, whose `lastOpenFiles` records resolved paths with real spaces — so a successful open moves the entry to index 0:

| form | before | after | verdict |
|---|---|---|---|
| `%20` — `…&file=tasks%2FPR%20Review%20github%20-…` | index 22 | **index 0** | resolves |
| `+` — `…&file=tasks%2FPR+Review+github+-…` (as delivered) | index 4 | **index 4** | **inert** |

The vault's own convention agrees with the `%20` column — `50 Knowledge Base/Deep Link URL Schemes` in the Personal vault documents *"URL-encode special characters: spaces → `%20`"*, and every `obsidian://` link in the vault uses `%20`.

## Expected vs Actual

| | Behavior |
|---|---|
| **Expected** | The `file=` value of the deeplink percent-encodes a space as `%20`, so Obsidian resolves the path and opens the note. This is what the vault's documented convention specifies, and what the link is for: the `publishEscalation` doc comment states the message is one click from the notification into the parked task. |
| **Actual** | The `file=` value encodes a space as `+`. Obsidian does not decode `+` as a space, so the resolved path carries literal `+` characters, matches no file, and the click opens nothing. |

## Why this is a bug

The deeplink's whole purpose is to resolve. `docs/controller-design.md` § "Assignee-Clear on Escalation" lists the deeplink among the parts of the escalation message that are contractually unchanged, and spec 013's own regression lock asserts the message still carries `obsidian://open?vault=` — an assertion that passes on the broken form, because it checks for the scheme, not for a resolvable path. The link was therefore covered by a green test the whole time it was inert. The harm is the cost of a manual search on every escalation, paid at the moment the operator is deciding what needs attention.

## Workaround

Read the task name out of the message and search the vault for it by hand. There is no user-side fix for the link itself — the encoding is decided by the producer.

## Goal

An escalation notification's deeplink resolves when opened. The `file=` value encodes a space as `%20` and never as `+`, for task names with no spaces, with many spaces, with non-ASCII characters, and for a vault name containing a space. A literal `+` in a task name still round-trips as `%2B`, so the replacement cannot be confused with real data. The published command — its type, its target, its metadata, its message text and its routing — is otherwise byte-for-byte what it is today.

## Non-goals

- **The delivery core.** `bborbe/notification`, `notification-controller`, `notification-telegram` and `notification-discord` are out of scope and stay untouched. In particular, the separate defect that the delivered URL renders as **plain text** rather than a tappable Telegram `text_link` entity lives in `notification-telegram/pkg/message-sender.go` — a different repo with no dark-factory config. It is tracked in the Personal vault task `Fix the Plus-Encoded obsidian Deeplink in Escalation Notifications`; fixing the encoding alone leaves a correct target with nothing clickable, and that is a known, recorded gap rather than an oversight here.
- **The Discord leg.** `publishEscalation` sets no `Target`, so the routing table fans one publish out to every channel mapped to `agent-escalation`. The Discord sender passes a plain string with no link entity, the same shape as the Telegram defect. It was not reported by the operator and whether Discord's client can render a custom scheme is unverified; it is out of scope and recorded on the vault task.
- **Backfill.** Escalations already delivered stay inert. The fix applies from the first escalation after it deploys.
- **The message shape.** No change to the `escalation: …` prefix lines, the metadata keys, the notification type or the routing table.
- **Any knob.** No env var, config field, CLI flag or allowlist. The encoding is not configurable — invariant; a switch that restores `+` re-opens this bug.

## Acceptance Criteria

- [ ] `make precommit` exits 0 at the repo root, and `go test -count=1 ./pkg/result/...` exits 0 with the suite green — evidence: exit codes.
- [ ] Case-table unit spec — one rule, six inputs: for each input in {a name containing spaces, a name containing none, a name containing an em dash, a name containing a literal `+`, a nested vault-relative path, a vault name containing a space}, the published message's deeplink encodes a space as `%20` and carries no bare `+`, with every other byte unchanged — concretely: the no-space name emits neither `+` nor `%20`; the em dash keeps its percent-encoded UTF-8 bytes (`%E2%80%94`); the literal `+` survives as `%2B`; the path separator stays `%2F`; and the spaced vault name is `%20`-encoded in `vault=` — evidence: a `DescribeTable` over the six inputs in `pkg/result/result_writer_deeplink_test.go`, every row asserting on the **raw** captured `command.Message` via `ContainSubstring` / `NotTo(ContainSubstring)`. One AC rather than six because all six rows assert the same rule; the literal-`+` row is the falsifier that catches a replacement applied **before** escaping, and the spaced-vault-name row catches a fix applied only to `file=`.
- [ ] **Falsifier on the assertion shape:** the specs never decode the URI before asserting — evidence: `grep -cE 'ParseQuery|QueryUnescape|url\.Parse' pkg/result/result_writer_deeplink_test.go` returns 0. Go's `url.ParseQuery` decodes `+` as a space, so a round-trip through it passes on the **buggy** form; a spec written that way would be green today and prove nothing. The assertion must read the emitted string.
- [ ] Regression lock (the published command is otherwise unchanged): the published command still carries `Type: agent-escalation`, a nil `Target`, the `taskIdentifier` / `taskName` / `previousAssignee` metadata keys, and the `escalation: <agent> cleared its assignee — status <s> , phase <p>` prefix — evidence: the pre-existing escalation specs are neither weakened nor deleted, checked two ways because neither alone is sound in both runtimes. (a) `git diff HEAD -- pkg/result/result_writer_escalation_test.go` prints nothing — **inside the container this is a real comparison**, because the working tree carries the prompt's edits while `HEAD` is still the pre-change commit; it is the form that works at prompt time, where a range against `origin/master` is vacuous (`workflow: direct` / `pr: false` means `origin/master` equals `HEAD` once dark-factory has pushed) and a bare `git diff` only compares working tree to index. (b) `grep -c 'obsidian://open?vault=' pkg/result/result_writer_escalation_test.go` returns ≥1 — context-independent, and the check that still means something on the host, where (a) is vacuous on a clean tree. Both hold alongside `make precommit`.
- [ ] The V(1) publish line is extended to carry the emitted deeplink, so the deployed encoder is observable in the pod log — evidence: the log call's argument list names the deeplink, verified by `grep -A8 -n 'assignee cleared → notification published' pkg/result/result_writer.go | grep -cE 'vaultDeeplink\('` returning ≥1. The check accepts an inline call or a variable assigned from one, because the spec requires the helper to stay the single owner of the URI contract — an assertion on the literal `obsidian://open` near the log statement would fail the correct implementation, and could only pass by duplicating the URI format string. **The behavioural proof that the line really carries a resolvable URI is Post-Deploy AC 8/9**, which read it from a running pod; this AC is a shape check only.
- [ ] `docs/controller-design.md` § "Assignee-Clear on Escalation" records the encoding rule and the extended log line — evidence: `sed -n '/^## Assignee-Clear on Escalation/,/^## Empty-to-Named Reset/p' docs/controller-design.md | grep -cE '%20'` returns ≥1, so the rule is **stated** there and not merely referenced. The project convention is that a behavioural rule lands in this file — it already carries a per-spec paragraph for the spec-013 coalescing rule — and a completed spec is immutable, so the contract has to live somewhere that outlives it.
- [ ] `CHANGELOG.md` gains an `## Unreleased` section whose bullet names the deeplink encoding — evidence: `awk '/^## v/{exit} tolower($0) ~ /deeplink/{f=1} END{exit !f}' CHANGELOG.md` exits 0 (position-aware: the match must precede the first released `## vX.Y.Z` heading).
- [ ] **Post-Deploy (Rung-2):** on dev, a real escalation publishes a deeplink whose `file=` value contains `%20` and no `+` — evidence: `kubectlnukedev -n dev logs agent-task-controller-openclaw-0 --since=24h | grep -c 'obsidian://open' || true` returns ≥1 (the positive clause) **and** `kubectlnukedev -n dev logs agent-task-controller-openclaw-0 --since=24h | grep 'obsidian://open' | grep -cE 'file=[^&]*\+' || true` returns 0 (the absence clause). The pair is deliberate: the absence alone would pass on a window holding no escalations, and a positive alone would pass on a window holding one fixed line among many broken ones. **Precondition:** at least one escalation since the deploy; an unmet precondition blocks the AC — the spec stays in `verifying` and the walk resumes at the next escalation — it does not fail the spec.
  - `deploy_check:` `kubectlnukedev -n dev get statefulset agent-task-controller-openclaw -o jsonpath='{.spec.template.spec.containers[0].image}' | awk -F: '{print $NF}'`
  - `deploy_target:` `$(git ls-remote --tags --refs origin 'v*' | awk -F/ '{print $NF}' | sort -V | tail -1)`
- [ ] **Post-Deploy (Rung-3):** on prod the same holds — evidence: `kubectlnukeprod -n prod logs agent-task-controller-openclaw-0 --since=24h | grep -c 'obsidian://open' || true` returns ≥1 **and** `kubectlnukeprod -n prod logs agent-task-controller-openclaw-0 --since=24h | grep 'obsidian://open' | grep -cE 'file=[^&]*\+' || true` returns 0. **Precondition:** at least one escalation since the deploy; an unmet precondition blocks the AC — the spec stays in `verifying` and the walk resumes at the next escalation — it does not fail the spec.
  - `deploy_check:` `kubectlnukeprod -n prod get statefulset agent-task-controller-openclaw -o jsonpath='{.spec.template.spec.containers[0].image}' | awk -F: '{print $NF}'`
  - `deploy_target:` `$(git ls-remote --tags --refs origin 'v*' | awk -F/ '{print $NF}' | sort -V | tail -1)`

Scenario coverage: **NO new E2E scenario.** Every case — spaced, unspaced, non-ASCII, literal `+`, separators, spaced vault name — is reachable through the existing `mocks.GitClient` + `NotificationPublishCommandSenderFunc` harness that `pkg/result/result_writer_escalation_test.go` already uses, driving `WriteResult` with a fixture task file and reading the captured message. No real Docker, no real cluster, no real Obsidian. The deployed path is covered by the two Post-Deploy ACs, which read the real controller's log rather than a fake.

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

```
make precommit
go test -count=1 ./pkg/result/...
grep -cE 'ParseQuery|QueryUnescape|url\.Parse' pkg/result/result_writer_deeplink_test.go || true
grep -n 'assignee cleared → notification published' pkg/result/result_writer.go
grep -A8 -n 'assignee cleared → notification published' pkg/result/result_writer.go | grep -cE 'vaultDeeplink\(' || true
git diff HEAD -- pkg/result/result_writer_escalation_test.go
grep -c 'obsidian://open?vault=' pkg/result/result_writer_escalation_test.go || true
sed -n '/^## Assignee-Clear on Escalation/,/^## Empty-to-Named Reset/p' docs/controller-design.md | grep -cE '%20' || true
awk '/^## v/{exit} tolower($0) ~ /deeplink/{f=1} END{exit !f}' CHANGELOG.md
```

Expected: `make precommit` exits 0 with the full suite green; the focused `go test` exits 0; the assertion-shape grep returns **0** (the specs must not decode before asserting); the frozen log line exists and its call site names `vaultDeeplink(`; `git diff HEAD` prints nothing for the escalation spec file **and** the `obsidian://open?vault=` assertion is still present in it; the section-scoped doc grep returns ≥1; the CHANGELOG `awk` exits 0. Every `grep -c` is wrapped in `|| true` because `grep -c` exits 1 on a zero count and would abort a `set -e` block on exactly the outcome being measured.

`make precommit` runs from the **repo root**: this repo is a single Go module and `pkg/result/` carries no Makefile, so the per-directory invocation has no target to run — the focused equivalent for the changed package is `go test -count=1 ./pkg/result/...` above.

### Operator-executable (runs on the host after PR merge)

1. Confirm the merge and the tag: the PR is merged to master and the maintainer bot has cut the release (`.maintainer.yaml` → `release.autoRelease: true`; `.dark-factory.yaml`'s `autoRelease: false` is a different gate). A PR link proves the merge, not the release.
2. Build and publish the image by hand — the release tag alone does not produce it (CI runs `make precommit` only): `cd ~/Documents/workspaces/agent-task-controller && git checkout master && git pull && VERSION=vX.Y.Z make build upload`, then `docker manifest inspect docker.io/bborbe/agent-task-controller:vX.Y.Z >/dev/null && echo OK`. Pass `VERSION=` explicitly: the Makefile derives it from the newest tag in the repo, which a release cut mid-build silently re-resolves. `make build` refuses unless HEAD is exactly the release tag (`check-version-tag`), so a docs commit on master after the release makes the build fail by design — that is the guard working, not a broken build.
3. Bump the **five** pins in `nuke/agent/`: `MIRROR_IMAGES` in `Makefile`, the `image.tag` of both controller entries in `values-dev.yaml` (`openclaw`, `personal`), and both entries in `values-prod.yaml`. Confirm with `grep -rn 'v<old>' Makefile values-*.yaml` returning nothing for the stages being shipped. **The pins currently lag the running pods** — they read `v0.10.0` while dev and prod both run `v0.10.1` — so the tag this grep searches for is not the image the cluster is executing; expect the two to differ until the first apply after this fix.
4. Deploy dev, then prod, per `65 Runbooks/Deploy Mirrored Agent Service` in the Personal vault: `cd ~/Documents/workspaces/nuke/agent && BRANCH=dev make apply`, then `BRANCH=master make apply` (`BRANCH=prod` is rejected by `Makefile.env`). The Makefile sets `KUBECONFIG` itself; the mirror step pulls from `docker.io` and fails until step 2 has run. Never `BRANCH=dev make buca` from the service repo — it pushes to `docker.io`, applies nothing, and overwrites the immutable released tag.
5. Run the Rung-2 read-back on dev and the Rung-3 read-back on prod.
6. **Operator-side confirmation (not a spec AC — carried by the Personal vault task):** tap a freshly delivered deeplink and confirm the note opens. Freshness caveat: `deploy_check` compares the running image tag against the newest **remote** tag (`git ls-remote --tags --refs origin 'v*'`, version-sorted), not the local `CHANGELOG.md` — so Phase 0.5 refuses as soon as the release is cut remotely and the deployed image lags.

## Desired Behavior

1. **A space in the vault-relative path is emitted as `%20`.** The deeplink's `file=` value percent-encodes a space as `%20` and never as `+`, so Obsidian resolves the path and opens the note. This is the whole fix.
2. **The same holds for the vault name.** The `vault=` value is produced by the same encoding step, so a vault name containing a space is encoded the same way. A fix applied to only one of the two query values is incomplete.
3. **Real data is never mistaken for a space.** A literal `+` in a task name still round-trips as `%2B`, and the path separator still encodes as `%2F`. The replacement operates on the **already-escaped** result, where a space is the only thing that produced a `+` — escaping first turns a literal `+` into `%2B`, so no genuine character can be rewritten.
4. **Already-correct inputs are unchanged.** A name with no spaces emits no `%20` and no `+`; a non-ASCII character keeps its percent-encoded UTF-8 bytes. The change is confined to the space case.
5. **The published command is otherwise unchanged.** The message text and its prefix lines, the three metadata keys, `Type: agent-escalation`, the absent `Target`, the routing table, the write path, the park and the coalescing window from spec 013 are all untouched.
6. **The emitted deeplink is observable in the pod log.** The existing V(1) `assignee cleared → notification published for task …` line is extended to carry the deeplink, so a deployed encoder's output can be read from the controller log rather than inferred. The line's existing prefix is preserved, so the greps spec 013's Post-Deploy ACs run against it keep matching.
7. **The changelog records the fix.** `CHANGELOG.md` gains an `## Unreleased` bullet naming the deeplink encoding.

## Design Decisions

**Why the fix sits at the encoder rather than at the URI builder's caller.** `vaultDeeplink` has exactly one call site (`publishEscalation`), so a fix there and a fix at the call site are the same blast radius today — but the helper is the named owner of the URI contract, and a second caller added later inherits the fix rather than the bug. The task's own call-site sweep recorded the count as one, so there is no migration surface to weigh.

**Why the replacement runs on the escaped result, not on the input.** Running `strings.ReplaceAll(name, " ", "%20")` before escaping would leave every other character to `url.QueryEscape` and would miss nothing — but it would also have to be repeated for every escaped segment, and it silently changes behaviour if a future caller passes a pre-escaped value. Escaping first and then rewriting `+` → `%20` is a single post-step over the whole rendered string: it covers both query values at once (Desired Behavior 2) and cannot touch real data, because escaping has already turned any literal `+` into `%2B` (Desired Behavior 3).

**Why the log line is extended.** The dark-factory verification doctrine (`docs/spec-verification.md` in the dark-factory plugin) forbids completing a runtime-behaviour change on "tests pass" alone, and the deeplink is not otherwise present in any log: the existing publish line names the task and its identifier but not the message. Without extending it, the only deployed evidence would be the operator manually tapping a link — real, but not a command the `spec-verifier` can run, which would leave both Post-Deploy ACs unwritable. Extending the line is one format-string change, it is append-only so existing prefix greps keep working, and it matches spec 013, which added a V(1) line specifically so its own suppression was observable in the pod log.

**Why the specs assert on the raw string.** Go's `url.ParseQuery` decodes `+` as a space, so a spec that parsed the URI and compared the decoded value would pass against the **buggy** implementation. That is the failure mode this spec most needs to avoid, because the pre-existing regression lock in `result_writer_escalation_test.go` already asserts the message contains `obsidian://open?vault=` — an assertion that was green throughout the period the link was inert. AC 3 makes the assertion shape itself a checked property rather than a convention.

**Why this is a spec and not a prompt.** The production change is small — one helper body and one format string — and on size alone a prompt would do. It is a spec for two reasons, stated so the choice is reviewable rather than incidental. First, the **Post-Deploy gate**: ACs 8 and 9 read a running pod's log on dev and prod, and a prompt has no mechanism to gate on a deployed artifact — its `<verification>` block is container-only by contract. Second, the **falsifier surface**: AC 3 (never decode before asserting) and AC 2's literal-`+` row (a literal `+` must survive as `%2B`) encode the two ways a green-but-meaningless test could be written for this change, and the pre-existing regression lock in `result_writer_escalation_test.go` is live proof that such a test was written and stayed green while the link was inert. Those two properties are what a durable document buys here; without them, a prompt would be the right call.

## Constraints

- **Frozen public surface:** `vaultDeeplink(vaultName, relPath string) string` keeps its signature. It is unexported and the test package is external (`package result_test`), so the specs drive `WriteResult` and read the captured `NotificationPublishCommand.Message` — the production path, not the helper.
- **Frozen call site:** the single call site in `publishEscalation` is not restructured. No new caller, no signature change, no move to another package.
- **Frozen message contract:** the `escalation: %s cleared its assignee — status %s, phase %s\n%s` format and the position of the deeplink as its final line are unchanged. Only the encoding of the URI that fills `%s` changes.
- **Frozen delivery:** `Type: agent-escalation` is reused; no `Target` is set; the `taskIdentifier` / `taskName` / `previousAssignee` metadata keys are unchanged; the routing table is not touched.
- **Frozen write path:** the escalation recorder, the four escalation rows, the counter logic, the terminal-status short-circuit, the body merge and the spec-013 coalescing window are untouched.
- **Append-only log change:** the V(1) publish line keeps its existing prefix and gains the deeplink. No new log line, no level change.
- **No new config field, env var, CLI flag, or metric.**
- **`make precommit` runs from the repo root** (single Go module; `pkg/result/` carries no Makefile). The focused run for the changed package is `go test -count=1 ./pkg/result/...`.
- The existing specs in `pkg/result/result_writer_escalation_test.go` pass with unmodified `Expect(` lines.

## Assumptions

- Obsidian does not decode `+` as a space in an `obsidian://` URI's query. Measured 2026-09-18 against a live instance — the A/B table in the Reproduction section is the evidence, not an inference from the URI spec. If a future Obsidian release began decoding `+`, the `%20` form would remain correct and the fix would still be right.
- The vault-relative path is passed to `vaultDeeplink` already joined (`filepath.Join(taskDir, taskName+".md")`) and with the `.md` suffix stripped inside the helper. The specs use a spaced fixture name that reaches the helper through that same path.
- Both deployed controllers run `-v=2` (`logLevel: "2"` in `nuke/agent/values-{dev,prod}.yaml`), so the V(1) publish line is visible in the pod log the Post-Deploy ACs read.
- A task name may legitimately contain `+`, spaces, and non-ASCII characters; nothing in the vault's naming rules forbids them, and `PR Review github - …` names already carry spaces and hyphens.
- The operator's Obsidian instance is the one the deeplink targets, and the delivered notification reaches it — the operator's Telegram is the observed delivery channel.

## Failure Modes

| Trigger | Expected behavior | Recovery | Detection | Reversibility |
|---|---|---|---|---|
| A task name contains a literal `+` | It is emitted as `%2B`, because escaping runs first and the replacement runs on the escaped result | None needed — this is the correct path | The literal-`+` row of AC 2 | N/A |
| A future caller passes an already-escaped value to the helper | It is escaped a second time; `%` becomes `%25` and the link breaks for that caller | None needed today — the helper has one call site and both callers pass raw values; a second caller must pass raw input, which the doc comment states | Not detectable from the log; the unit specs pin the raw-input contract | Reversible (caller-side) |
| A vault name contains a space | It is emitted as `%20` by the same encoding step | None needed | The spaced-vault-name row of AC 2 | N/A |
| Obsidian changes its URI handling | The `%20` form is the documented convention and would remain correct; a change that broke it would break every existing `obsidian://` link in the vault equally | None needed | Not detectable from this repo — the A/B is the check | N/A |
| The pod log is not at `-v=2` on a given environment | The V(1) publish line is absent, so the Post-Deploy AC has no evidence to read | Set the log level for the environment, or read the delivered message instead | The Post-Deploy grep returning zero lines while escalations are demonstrably publishing | Reversible (log level) |
| The publish fails (broker unavailable) | The write commits and no deeplink is logged; the escalation is parked regardless | Automatic on the next escalation; the existing `publish agent-escalation notification … failed` warning line names the failure | The existing warning line | N/A — no state consumed |

## Security / Abuse Cases

- **What can an attacker control?** A PR title, which `github-pr-watcher` slugifies into the trailing part of the task name, and therefore into the `file=` value of the deeplink. The value is `url.QueryEscape`d, so `&`, `=`, `?`, `#`, `%` and newlines cannot break out of the query parameter and inject a second one — that property is unchanged by this fix and must stay.
- **What crosses a trust boundary?** Nothing new. The notification command, its type, its metadata keys and the routing table are unchanged; the deeplink is a value inside the message text.
- **Can the fix widen the injection surface?** No. The replacement rewrites `+` to `%20` on an already-escaped string; it introduces no character that was not already legal in a percent-encoded query value, and it cannot produce a bare `&`, `=` or `#`.
- **What can hang or retry forever?** Nothing: no loop, no new I/O, no new timeout. The publish stays fire-and-forget with its existing error swallow.
- **What must be validated?** That the replacement runs on the escaped result rather than the input — the literal-`+` row of AC 2 is the guard, because a replacement applied before escaping would turn real data into spaces.

## Suggested Decomposition

Prompts are generated in this order — each row is one prompt with a clear scope.

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | The encoding fix in `vaultDeeplink`, the append-only deeplink on the V(1) publish line, the new external-package case table in `pkg/result/result_writer_deeplink_test.go`, the regression lock on the existing escalation specs, the `docs/controller-design.md` contract paragraph and the `## Unreleased` CHANGELOG bullet | 1-7 | 1-7 | — |

**A single prompt, by design.** The table has one row because the change is one helper body, one format string and one test file — there is no seam to split on that would let two prompts be verified independently, which is the grouping test in `docs/rules/prompt-writing.md`. The row is written out anyway so the prompt-creator inherits the scope statement and does not derive a split of its own. The Post-Deploy ACs (8, 9) belong to spec verification, not to a prompt: they read the released image on dev and prod, and a prompt's `<verification>` block is container-only by contract.

Prompt-level test style (guidance for the prompt-creator, not a behavioral contract): `pkg/result/result_writer_deeplink_test.go` is `package result_test`, `Context` / `It` blocks, using the same `writeTaskFile(...)` fixture and `NotificationPublishCommandSenderFunc` recording seam as `pkg/result/result_writer_escalation_test.go`; the six encoding inputs are one `DescribeTable`, every row asserting on the **raw** captured message string with `ContainSubstring` / `NotTo(ContainSubstring)`, never through `url.ParseQuery`.

Rationale: one prompt carries the whole behaviour and every falsifier — an implementation that leaves `+` fails AC 2 and Post-Deploy ACs 8-9; one that replaces before escaping fails AC 2's literal-`+` row; one that touches only `file=` fails AC 2's spaced-vault-name row; one that decodes before asserting fails AC 3; one that deletes the pre-existing escalation assertions fails AC 4.

## Do-Nothing Option

The bug stays, and it is not marginal: every escalation ever sent carries an inert link, because the task names that escalate most often contain spaces. The cost is paid on every triage — the operator reads a ping, taps it, lands nowhere, and searches the vault by hand. The fix is one encoding step plus its specs, with the message, the metadata, the routing and the write path untouched, so there is no cheaper moment to do it than now. Leaving it also leaves the pre-existing regression lock asserting the broken form is present, which is worse than no assertion: it reports coverage over the exact defect.
