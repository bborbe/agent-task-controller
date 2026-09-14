---
status: approved
spec: [012-accumulate-agent-counters-on-write-back]
created: "2026-09-14T20:40:02Z"
queued: "2026-09-14T20:57:47Z"
branch: dark-factory/accumulate-agent-counters-on-write-back
---

# Document the accumulated-counter merge rule and record it in the changelog

<summary>

- The controller design doc's Frontmatter Merge doctrine gains the third ownership row: the two cumulative agent counters, which accumulate rather than being overwritten or discarded.
- The row states the sum rule, the absent-incoming and absent-on-disk rules, the non-numeric keep-verbatim rule, and why these keys are neither controller-owned nor operator-owned.
- A short paragraph explains that accumulation is a transform, not a discard — a clean sum produces no guard decision and no log line, and the counter names are a frozen cross-repo contract with the agent-side emitter.
- The changelog records the change under a new `## Unreleased` section with one `feat:` bullet naming both counters and the silent reset they prevent.
- The full build gate runs once at the end as the final acceptance check for the whole spec change.
- Documentation only: no Go file is authored here, and the existing doc sections and changelog version sections are left intact.

</summary>

<objective>

Make the documented contract match the shipped behaviour — `docs/controller-design.md` § "Frontmatter Merge" gains the accumulated-counter ownership row and its rule, and `CHANGELOG.md` gains an `## Unreleased` bullet naming the accumulated counters — then run the full build gate as the final acceptance check (spec 012, Desired Behavior 7, AC 12-14).

</objective>

<context>

This repo has no root `CLAUDE.md`; the global YOLO container CLAUDE.md (already in your context) governs project conventions.

Read the spec: `specs/in-progress/012-accumulate-agent-counters-on-write-back.md` — Goal, Desired Behavior 1-7, Acceptance Criteria (AC 12, 13, 14), Non-goals, and the "Suggested Decomposition" table (this prompt is row 3).

DEPENDENCY — prompts 1 and 2 must have shipped before this prompt runs, because this prompt documents behaviour rather than intent. This gate is stricter than the spec's Suggested Decomposition table, whose row 3 lists "—" under "Depends on" and whose rationale allows prompt 3 to run in parallel; treat this gate as authoritative for this prompt. Verify first:

```
cd /workspace && grep -n 'applyAccumulatedCounters' pkg/result/result_writer.go || true
cd /workspace && grep -c 'accumulation across runs (spec 012)' pkg/result/result_writer_test.go || true
```

Expect the helper's doc comment, its `func` line and its call inside `MergeFrontmatter`, and `1` for the spec count. If either check fails, STOP and report `status: failed` with message `"accumulation not yet deployed (prompt 1)"` — do not document behaviour that did not ship.

Read before writing:

- `docs/controller-design.md` — the `## Frontmatter Merge` section in full (it runs from the `## Frontmatter Merge` heading to the `## Terminal Task Status (create-task dedup)` heading). Note its shape: an intro, the `| Ownership | Fields | Rule |` table whose rows are Controller-owned, Controller-owned (terminal pin), Operator-owned and Agent-owned; a merge example block and the sentence explaining it; then the "**Body merge.**", "**Terminal short-circuit.**", "**Guard logging.**" and "**What the guard does and does not cover.**" paragraphs.
- `pkg/result/result_writer.go` — `accumulatedCounters`, `applyAccumulatedCounters`, and `MergeFrontmatter`, so the prose describes what the code does (and not more).
- `CHANGELOG.md` — there is currently NO `## Unreleased` section; the newest section is `## v0.9.0`, directly below the frozen preamble.
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — `## Unreleased` placement, the required conventional prefix, bullet style, anti-patterns (the preamble is frozen; the entry goes immediately after the last preamble line).
- `/home/node/.claude/plugins/marketplaces/coding/docs/documentation-guide.md` — prose style for the doc edit.

Environment facts that shape this prompt:

- The execution container's `.git` is masked (the daemon runs with `hideGit=true`; `git status` fails with `fatal: not a git repository`). Do NOT run any `git` command — the spec's placement checks are expressed as non-git greps in `<verification>` below, and a failed git command would read as a pass.
- Do NOT run `kubectl*`, `docker`, `make build`, `make buca`, `gh`, or any deploy command. The spec's Post-Deploy ACs (15, 16) and the release/tag steps live on the spec's operator-executable rung.

</context>

<requirements>

## 1. Add the accumulated-counter ownership row to `docs/controller-design.md`

Inside the `## Frontmatter Merge` section, insert a new row into the ownership table BETWEEN the `Operator-owned` row and the `Agent-owned` row, using the table's existing three-column shape:

```
| Accumulated counters | `metrics_agent_turns`, `metrics_interaction_count` | The written value is the sum of the on-disk value and the incoming value when both carry a number — `int`, `int64` and `float64` are compared and added by value, so a JSON-decoded incoming `float64` adds correctly to a YAML-decoded on-disk `int`. An incoming key absent from the payload leaves the on-disk value untouched (an emitted `0` adds nothing), and an absent on-disk key takes the incoming value as the starting total. A non-numeric value on either side keeps the on-disk value verbatim and is reported as a guard decision when the two values differ. These keys are neither controller-owned (that guard discards the emitted value, so the counter would never move) nor operator-owned; adding either key to a guard list is the failure this row exists to prevent. |
```

The row's first column MUST begin with the word `Accumulated` (spec AC 13 greps the section for `Accumulat`), and both key spellings MUST appear in the row exactly as written above — lowercase, snake_case, no alias, no version suffix.

## 2. Add the rule paragraph

Insert a new paragraph immediately AFTER the sentence "The terminal `status` is pinned and the controller-owned counter keeps its on-disk value, while the agent-owned `phase` still lands." and immediately BEFORE the paragraph beginning "**Body merge.**", in the section's existing voice (bold lead-in, plain prose, no code fence):

```
**Accumulated counters.** `metrics_agent_turns` and `metrics_interaction_count` are cumulative: the agent's payload carries one run's contribution while the task file carries the lifetime total, so the merge adds rather than overwrites. After N runs the on-disk total is the sum of the N per-run emitted values, and an emitted `0` — an evidenced unattended run — adds nothing, so it can never wipe a total an earlier run built up. Accumulation is a transform, not a discard: a clean sum produces no guard decision and no log line, and for these two keys the `ownership guard kept on-disk` line appears only on the non-numeric branch, where the on-disk value is genuinely kept and an incoming value is rejected. The two key names are a frozen cross-repo contract with the agent-side emitter — the merge knows exactly these spellings, so a renamed emitter key silently stops accumulating instead of erroring.
```

Do not modify any other paragraph, the table's other rows, the example block, or any other section of the doc. In particular, do NOT extend the guard-logging paragraph's claim, do NOT add a knob/flag/opt-out sentence (the spec's Non-goals forbid one), and do NOT describe the emitter's side of the contract as shipped beyond what the paragraph above says.

## 3. Add the changelog entry

Insert a new `## Unreleased` section into `CHANGELOG.md` immediately after the frozen preamble (after the "and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html)." line) and immediately above the `## v0.9.0` heading, containing exactly one bullet:

```markdown
## Unreleased

- feat: the result write-back merge accumulates the two cumulative agent counters instead of overwriting them — `metrics_agent_turns` and `metrics_interaction_count` are added to the on-disk value when both the payload and the task file carry a number (`int`, `int64` and `float64` are compared and added by value, so a JSON-decoded incoming `float64` adds to a YAML-decoded on-disk `int`); an emitted `0` adds nothing and can no longer wipe a lifetime total; an absent incoming key leaves the on-disk value untouched; an absent on-disk key takes the emitted value as the starting total; and a non-numeric value on either side keeps the on-disk value verbatim with one `ownership guard kept on-disk` line when the two values differ. Accumulation is a third behaviour in `MergeFrontmatter` beside "keep the on-disk value" and "incoming wins": neither guard list gains a member — `controllerOwnedFields` would discard the emitted value and the default incoming-wins would reset a lifetime total to one run's value — and a clean sum produces no guard decision and no log line. Closes the silent-reset window opened by the agent-side emitter (spec 012)
```

Requirements for this entry:
- The `feat:` prefix is required and is the only prefix used here: this ships a new merge behaviour (minor bump), not a repair of the controller's own defect.
- One logical change, one bullet. Do not split it, do not add further bullets, and do not touch any existing version section, heading, or bullet.
- The word `accumulates` (or another `accumulat`-containing word) MUST appear, and both counter key spellings MUST appear.
- The `# Changelog` title and the SemVer preamble stay exactly where they are — the new section goes after them, never above or inside them.

## 4. Run the final build gate

Run `make precommit` once, at repo root, as the final acceptance check (spec AC 12). If a target fails, fix only what is needed to make it green and never by deleting or weakening an assertion; re-run the failing target first, then the full `make precommit`. Note that `make precommit` runs `make format`, which rewrites `.go` files in place on every run — that is expected, not a failure.

## 5. Self-check before finishing

Re-run `<verification>` and confirm all four doc greps pass (≥1 for the first three, exactly 1 for the row grep), the changelog placement check exits 0, and `make precommit` exits 0. Then re-read the new row and paragraph against `pkg/result/result_writer.go` and confirm every claim is true of the shipped code — no claim about a knob, a cap, a clamp, an emitter rollout, or a new writer.

</requirements>

<constraints>

- **Documentation only.** The only files this prompt AUTHORS are `docs/controller-design.md` and `CHANGELOG.md`. `make precommit`'s `make format` step rewrites `.go` files in place on every run (expected, allowed); if `make precommit` reports a real lint/vet/test failure originating in prompts 1-2's `pkg/result/` change, STOP and report `status: failed` naming the failing target and the file — a docs prompt must not patch production code, because that is exactly how an assertion gets softened.
- **Both counter keys accumulate, and only these two** (spec Desired Behavior 1): `metrics_agent_turns` and `metrics_interaction_count` — exactly these spellings, a frozen cross-repo contract with the agent-side emitter. Do not rename, alias, or add a third key.
- **The guard lists are frozen** at `controllerOwnedFields = {trigger_count, retry_count}` and `operatorOwnedFields = {assignee, previous_assignee}`, with the terminal-`status` pin unchanged. The prose must state that neither counter key is on either list.
- **No new knob** (spec Non-goal, invariant): no opt-out flag, no per-key allowlist, no configurable cap, no config field, no new metric, no new log line. Do not document one and do not add one.
- **No clamping or bounds-checking** of the counters — the merge adds what it is given and does not police the emitter's contract. Do not claim a bound exists.
- **Exactly-once accumulation under Kafka redelivery is a Non-goal** — a re-delivered result adds its value twice. Do not document an idempotency guarantee, and do not add one.
- **Nothing else in the merge moves** (spec Desired Behavior 6): the guard lists keep their members, the terminal pin keeps pinning, the body section merge, the preamble rules and the escalation dedup are untouched, and every non-counter key still takes the incoming value.
- Do NOT create a second `## Unreleased` section, do NOT modify any existing version section or bullet, and do NOT move or edit the frozen `# Changelog` preamble.
- Do NOT commit — dark-factory handles git.
- Do NOT run any `git` command — the container's `.git` is masked; a failed git command would read as a pass.
- Do NOT run `kubectl*`, `docker`, `make build`, `make buca`, `gh`, or any operator/deploy command — the spec's Post-Deploy ACs (15, 16), the merge/tag and deploy steps are operator-side.

</constraints>

<verification>

Spec AC 13 — the section-scoped doc greps (from the `## Frontmatter Merge` heading to the `## Terminal Task Status` heading; every `grep -c` is wrapped in `|| true` because it exits 1 on a zero count). All four print `0` today:

```
cd /workspace && sed -n '/^## Frontmatter Merge/,/^## Terminal Task Status/p' docs/controller-design.md | grep -c 'metrics_agent_turns' || true
cd /workspace && sed -n '/^## Frontmatter Merge/,/^## Terminal Task Status/p' docs/controller-design.md | grep -c 'metrics_interaction_count' || true
cd /workspace && sed -n '/^## Frontmatter Merge/,/^## Terminal Task Status/p' docs/controller-design.md | grep -c 'Accumulat' || true
cd /workspace && grep -c '^| Accumulated counters |' docs/controller-design.md || true
```

Expect `≥1` for the first three and exactly `1` for the fourth — the row grep is the only check that proves the table row exists, since the § 2 paragraph alone would satisfy the `Accumulat` grep. Confirm the section boundaries are unchanged: `## Frontmatter Merge` still starts the section and `## Terminal Task Status (create-task dedup)` still ends it.

Spec AC 14 — the changelog entry is present and positioned above the first released version heading (non-git, position-aware):

```
cd /workspace && awk '/^## v/{exit} tolower($0) ~ /accumulat/{f=1} END{exit !f}' CHANGELOG.md
cd /workspace && grep -c '^## Unreleased' CHANGELOG.md || true
cd /workspace && awk '/^## v/{exit} /metrics_agent_turns/{f=1} END{exit !f}' CHANGELOG.md
cd /workspace && awk '/^## v/{exit} /metrics_interaction_count/{f=1} END{exit !f}' CHANGELOG.md
```

Expect exit `0` for the first (it exits `1` today), a printed count of `1` for the second, and exit `0` for both key-name checks.

Spec AC 12 — the full gate, run ONCE at the end, at repo root:

```
cd /workspace && make precommit
```

Expect exit `0` with the full Ginkgo suite green — the accumulation specs from prompt 1, the two-run invariant and the regression locks from prompt 2, and every pre-existing spec, all passing unmodified. If it fails, iterate on the specific failing target (`make test`, `make check`, `make lint`) rather than re-running the whole chain, then re-run the full `make precommit` once the individual targets pass.

</verification>
