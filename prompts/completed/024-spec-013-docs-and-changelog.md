---
status: completed
spec: [013-coalesce-escalation-notifications]
summary: 'Documented the shipped escalation-coalescing rule in docs/controller-design.md § Assignee-Clear on Escalation and added the `## Unreleased` fix: bullet to CHANGELOG.md, then ran the full build gate green'
execution_id: agent-task-controller-escalation-dedup-exec-024-spec-013-docs-and-changelog
dark-factory-version: v0.193.0
created: "2026-09-16T22:08:04Z"
queued: "2026-09-17T05:23:40Z"
started: "2026-09-17T05:31:42Z"
completed: "2026-09-17T05:37:23Z"
branch: dark-factory/coalesce-escalation-notifications
---

# Record the escalation-coalescing rule in the doctrine and the changelog

<summary>

- The controller design doc's escalation section now states the coalescing rule as shipped behaviour: the key, the window, the fail-safe and the reason the suppression sits at the publish point rather than in the write.
- The paragraph names the coalescing key shape, the 30-minute window and the anchored task-name parse, so a reader can tell which repeat pings are dropped and which are not.
- It records that distinct repositories and distinct PR numbers never coalesce, that an unrecognised task name publishes uncoalesced, and that a suppressed escalation does not extend the window.
- It records that a failed publish releases the claim, so a broker outage cannot suppress the escalation that follows.
- The changelog gains an `## Unreleased` section whose bullet names the coalescing and the duplicate-ping volume it removes, above the newest released version heading.
- Documentation only: no Go file is authored here, and no existing doc section, changelog version section or bullet is modified.
- The full build gate runs once at the end as the final acceptance check for the whole spec change.

</summary>

<objective>

Make the documented contract match the shipped behaviour — `docs/controller-design.md` § "Assignee-Clear on Escalation" gains the escalation-coalescing rule and `CHANGELOG.md` gains an `## Unreleased` bullet naming the change — then run the full build gate as the final acceptance check for spec 013 (spec Desired Behavior 8, AC 12 and AC 13).

</objective>

<context>

This repo has no root `CLAUDE.md`; the global YOLO container CLAUDE.md (already in your context) governs project conventions.

Read the spec: `specs/in-progress/013-coalesce-escalation-notifications.md` — Goal, Desired Behavior 1-8, Constraints, Failure Modes, Acceptance Criteria (AC 12 and AC 13), Non-goals, and the "Suggested Decomposition" table (this prompt is row 2).

DEPENDENCY — prompt 1 must have shipped before this prompt runs, because this prompt documents behaviour rather than intent. Verify first:

```
cd /workspace && grep -n 'escalation notification coalesced' pkg/result/result_writer.go || true
cd /workspace && grep -n 'escalationCoalescingWindow' pkg/result/result_writer.go || true
```

Expect the V(1) coalesce log line and the frozen-window constant. If either returns nothing, STOP and report `status: failed` with message `"escalation coalescing not yet deployed (prompt 1)"` — do not document behaviour that did not ship.

Read before writing:

- `docs/controller-design.md` — the `## Assignee-Clear on Escalation (spec 021, refined by spec 039, completed by spec 042)` section in full (it runs from that heading to the `## Empty-to-Named Reset (spec 021)` heading). Note its shape: an intro sentence, the four-row escalation table, the "Once a task is parked …" paragraph, then the `phase == "human_review"` ordering paragraph that ends with "…on 2026-04-24." Your new paragraph goes after that last paragraph and before `## Empty-to-Named Reset (spec 021)`. Note also the bold-lead-in paragraph style used elsewhere in this doc (e.g. `**Accumulated counters.**` in § "Frontmatter Merge").
- `pkg/result/result_writer.go` — `escalationCoalescingWindow`, `escalationTaskNamePattern`, `escalationCoalescingKey`, `claimEscalationCoalescingSlot`, `releaseEscalationCoalescingSlot` and `publishEscalation` (prompt 1), so the prose describes what the code does and not more. Read the code, not this prompt, as the source of truth for the mechanism.
- `CHANGELOG.md` — there is currently NO `## Unreleased` section; the newest section is `## v0.10.0`, directly below the frozen preamble (the `# Changelog` title, the "All notable changes…" line and the two SemVer lines — this repo's preamble carries **no** MAJOR/MINOR/PATCH bullets, and they must not be added: leave the preamble byte-for-byte unchanged).
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — `## Unreleased` placement (immediately after the last preamble line, directly above the highest `## vX.Y.Z`), the required conventional prefix, bullet style, and the anti-patterns (the preamble is frozen).
- `/home/node/.claude/plugins/marketplaces/coding/docs/documentation-guide.md` — prose style for the doc edit.

Environment facts that shape this prompt:

- The execution container's `.git` is masked (the daemon runs with `hideGit=true`; `git status` fails with `fatal: not a git repository`). Do NOT run any `git` command — a failed git command in `<verification>` reads as a pass.
- Do NOT run `kubectl*`, `docker`, `make build`, `make buca`, `gh`, or any deploy command — the spec's Post-Deploy ACs (14, 15), the image build and the dev/prod deploys are operator-side.

</context>

<requirements>

## 1. Add the coalescing paragraph to `docs/controller-design.md`

Insert the paragraph below into the `## Assignee-Clear on Escalation (spec 021, refined by spec 039, completed by spec 042)` section, immediately AFTER the paragraph that ends "…and prompt 075 for the same reorder pattern applied to `applyTriggerCap` on 2026-04-24." and immediately BEFORE the `## Empty-to-Named Reset (spec 021)` heading.

Write it as **one single unwrapped line** — do not re-wrap it at 100 characters, and do not introduce a line break anywhere inside `(repo, PR number)`, `30-minute` or `^PR \S+ \S+ - (\S+) - ([0-9]+) - \S+`. The section-scoped greps in `<verification>` count matching *lines*, and a line break inside one of those literals would break the evidence the spec's AC 12 asks for.

```
**Coalescing the repeat escalation ping (spec 013).** The publish is keyed on the underlying PR issue, not on the task file. `resultWriter.publishEscalation` derives the coalescing key `(repo, PR number)` from the task name with the anchored pattern `^PR \S+ \S+ - (\S+) - ([0-9]+) - \S+` — only the owner-qualified repo token and the PR number are captured, because everything after the short SHA (the title slug, the ` - retry-<taskid[:8]>` suffix, the task kind word and the provider word) varies between retries of one PR while those two do not. Inside a **30-minute** window measured on the injected `libtime.CurrentDateTimeGetter` from the last *published* ping for that key, a further escalation of the same key is written, committed and parked exactly as before but publishes nothing, with one `glog.V(1)` line carrying `escalation notification coalesced` and naming the key and the task name. The record holds the time of the last published ping, so a suppressed escalation does not extend the window and a PR that escalates every few minutes is re-announced once per window rather than silenced for as long as it keeps escalating. The check and the record are one atomic step under a mutex, so two concurrent escalations of one key cannot both publish, and the claim is released when the sender rejects the command, so a broker outage is followed by the next escalation for that key publishing normally. Distinct keys never coalesce: the key is repo-qualified, so the same PR number in two repositories, two numbers in one repository and two tasks carrying the same head SHA in different repositories each publish their own notification. A task name the parse does not recognise publishes uncoalesced and creates no window state, so a format change degrades to one ping per file and never to silence. The suppression sits at the publish point rather than in the write because the modify closure re-runs on every git retry and the four escalation rows share one write path that must never be suppressed — the park, the `previous_assignee` write, the escalation section, the message, the metadata and the deeplink are unchanged, and only the repeat ping is dropped.
```

Requirements for this paragraph:

- The word `coalesc` (in `Coalescing`, `coalescing`, `coalesced`, `uncoalesced` or any other form) MUST appear in the section, the literal `30-minute` MUST appear, and the literal `(repo, PR number)` MUST appear — spec AC 12 greps the section for each.
- State the key, the window, the fail-safe and the reason the suppression sits at the publish point rather than in the write — a one-word insertion must not satisfy the AC.
- Do not modify any other paragraph of this section, the escalation table, the `## Empty-to-Named Reset (spec 021)` heading, or any other section of the doc. Do not add a knob/flag/opt-out sentence (the spec's Non-goals forbid one), do not claim a metric, and do not describe a configurable or persisted window.

## 2. Add the changelog entry

Insert a new `## Unreleased` section into `CHANGELOG.md` immediately after the frozen preamble (after the "and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html)." line) and immediately above the `## v0.10.0` heading, containing exactly one bullet:

```markdown
## Unreleased

- fix: coalesce repeat escalation notifications so one PR reaches the operator once per 30-minute window instead of once per task file. A retried PR review mints a new task file — `--force` appends ` - retry-<taskid[:8]>` to the title, and a new head SHA produces its own file — so each retry re-announced an escalation the operator had already been told about: one measured PR produced six task files and six pings, and the vault gained 534 same-key repeat escalations inside 30-minute windows since 2026-08-01 (11.6 a day, matching the operator's reported ~8-11 duplicate pings a day). `resultWriter.publishEscalation` now derives the coalescing key `(repo, PR number)` from the task name with the anchored pattern `^PR \S+ \S+ - (\S+) - ([0-9]+) - \S+` and publishes only when no ping for that key was published in the last 30 minutes, measured on the `libtime.CurrentDateTimeGetter` the writer already holds; the record holds the last *published* ping, so a suppressed escalation does not extend the window, the check-and-record is one atomic step under a mutex so two concurrent escalations of one key publish once, and the claim is released when the sender rejects the command so a broker outage cannot suppress the escalation that follows. Distinct repositories, distinct PR numbers and two tasks carrying the same head SHA in different repositories each publish their own notification; a task name the parse does not recognise publishes uncoalesced, so a format change degrades to today's behaviour and never to silence; and the write, the park, the message, the metadata, the notification type and the deeplink are unchanged — only the repeat ping is dropped, with one `glog.V(1)` line carrying `escalation notification coalesced` naming the key and the task name (spec 013)
```

Requirements for this entry:

- The `fix:` prefix is required and is the only prefix used here: the spec frames this as a defect in the dedup's coverage — a retry re-announced an escalation the operator had already been told about — so it ships a patch bump, not a new capability.
- One logical change, one bullet. Do not split it, do not add further bullets, and do not touch any existing version section, heading or bullet.
- The word `coalesc` (lowercase-insensitively) MUST appear in the text above the first `## vX.Y.Z` heading, and the literal `30-minute` MUST appear.
- The `# Changelog` title and the SemVer preamble stay exactly where they are — the new section goes after them, never above or inside them.

## 3. Run the final build gate

Run `make precommit` once, at repo root, as the final acceptance check for the whole spec change (spec AC 1). If a target fails, fix only what is needed to make it green and never by deleting or weakening an assertion; re-run the failing target first, then the full `make precommit`. Note that `make precommit` runs `make format`, which rewrites `.go` files in place on every run — that is expected, not a failure.

## 4. Self-check before finishing

Re-run `<verification>` and confirm all four section-scoped doc greps print `≥1`, the changelog placement `awk` exits `0`, the `## Unreleased` count prints `1`, and `make precommit` exits `0`. Then re-read the new paragraph against `pkg/result/result_writer.go` and confirm every claim is true of the shipped code — in particular that the window is 30 minutes and not configurable, that the key is `(repo, PR number)` and not the short SHA or the task identifier, that the log line carries `escalation notification coalesced`, and that no metric, no persistence and no knob is claimed.

</requirements>

<constraints>

- **Documentation only.** The only files this prompt AUTHORS are `docs/controller-design.md` and `CHANGELOG.md`. `make precommit`'s `make format` step rewrites `.go` files in place on every run (expected, allowed); if `make precommit` reports a real lint/vet/test failure originating in prompt 1's `pkg/result/` change, STOP and report `status: failed` naming the failing target and the file — a docs prompt must not patch production code, because that is exactly how an assertion gets softened.
- **Frozen key: `(repoToken, prNumber)`** — the prose must state this key and must not describe the head SHA, the title or the `task_identifier` as part of it.
- **Frozen parse: `^PR \S+ \S+ - (\S+) - ([0-9]+) - \S+`**, anchored at the start of the task name, owned by `github-pr-watcher` (`computeTaskTitle`, `appendRetryToken`) — the controller matches the prefix it needs and tolerates any suffix.
- **Frozen window: 30 minutes**, a fixed value with no config surface.
- **Frozen clock:** the window is measured on the injected `libtime.CurrentDateTimeGetter`, never `time.Now()`.
- **Frozen publish point:** `writeAndPublish` → `publishEscalation`, after the commit succeeds — not in the modify closure and not in any of the four escalation rows. The prose must give the reason the suppression sits there rather than in the write.
- **Frozen delivery:** `agent-escalation` is reused; no new notification type, no `Target`, the three existing metadata keys, the message text and the deeplink unchanged. The routing table is not touched. Do not describe a new type, target or metadata key.
- **In-process state only:** the prose must not claim persistence, a shared cache or cross-process coordination, and must not claim a restart keeps the window.
- **No new config field, env var, CLI flag, per-repo allowlist, tunable window, or metric** (spec Non-goal, invariant). Do not document one and do not add one — the V(1) `escalation notification coalesced` log line is the observable, not a counter.
- **No backfill:** do not claim the historical escalated task files are re-processed or cleaned up.
- Do NOT create a second `## Unreleased` section, do NOT modify any existing version section or bullet, and do NOT move or edit the frozen `# Changelog` preamble.
- Do NOT commit — dark-factory handles git.
- Do NOT run any `git` command — the container's `.git` is masked; a failed git command would read as a pass.
- Do NOT run `kubectl*`, `docker`, `make build`, `make buca`, `gh`, or any operator/deploy command — the spec's Post-Deploy ACs (14, 15), the image build and the dev/prod deploys are operator-side.

</constraints>

<verification>

Spec AC 12 — the section-scoped doc greps (from the `## Assignee-Clear on Escalation` heading to the `## Empty-to-Named Reset` heading; every `grep -c` is wrapped in `|| true` because it exits 1 on a zero count). All three print `0` today:

```
cd /workspace && sed -n '/^## Assignee-Clear on Escalation/,/^## Empty-to-Named Reset/p' docs/controller-design.md | grep -ci 'coalesc' || true
cd /workspace && sed -n '/^## Assignee-Clear on Escalation/,/^## Empty-to-Named Reset/p' docs/controller-design.md | grep -cE '30[ -]min' || true
cd /workspace && sed -n '/^## Assignee-Clear on Escalation/,/^## Empty-to-Named Reset/p' docs/controller-design.md | grep -cE '\(repo, (PR )?number\)' || true
```

Expect `≥1` for each. The three together are what a one-word insertion cannot satisfy: the first proves the rule is named, the second proves the window is stated, the third proves the key shape is stated. Confirm the section boundaries are unchanged: `## Assignee-Clear on Escalation` still starts the section and `## Empty-to-Named Reset` still ends it.

Spec AC 13 — the changelog entry is present and positioned above the first released version heading (non-git, position-aware):

```
cd /workspace && awk '/^## v/{exit} tolower($0) ~ /coalesc/{f=1} END{exit !f}' CHANGELOG.md
cd /workspace && grep -c '^## Unreleased' CHANGELOG.md || true
cd /workspace && awk '/^## v/{exit} /30-minute/{f=1} END{exit !f}' CHANGELOG.md
cd /workspace && awk '/^## v/{exit} /escalation notification coalesced/{f=1} END{exit !f}' CHANGELOG.md
```

Expect exit `0` for the first (it exits `1` today), a printed count of `1` for the second, and exit `0` for the last two.

Spec AC 1 — the full gate, run ONCE at the end, at repo root:

```
cd /workspace && make precommit
```

Expect exit `0` with the full Ginkgo suite green — prompt 1's eight coalescing specs and every pre-existing spec, all passing unmodified. If it fails, iterate on the specific failing target (`make test`, `make check`, `make lint`) rather than re-running the whole chain, then re-run the full `make precommit` once the individual targets pass.

</verification>
