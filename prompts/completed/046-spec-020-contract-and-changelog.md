---
status: completed
spec: [020-open-task-census]
summary: Documented the open task census contract in docs/controller-design.md and extended CHANGELOG.md's Unreleased section
execution_id: agent-task-controller-open-tasks-gauge-exec-046-spec-020-contract-and-changelog
dark-factory-version: v0.198.7
created: "2026-11-01T14:30:00Z"
queued: "2026-11-01T13:28:07Z"
started: "2026-11-01T13:44:31Z"
completed: "2026-11-01T13:45:13Z"
branch: dark-factory/open-task-census
---

# Write the census contract into the design doc and record the change

<summary>

- The controller design document gains an Open Task Census section that states the series name and its three labels.
- The section defines what "open" means and says explicitly that `human_review` and `done` are counted, with the resting-phase exclusion living in the alert.
- It states that a missing phase or task type is published as an empty label value and the record is still counted.
- It states that a pair which empties disappears from the exposition, and that the deletion is scoped to the controller's own vault.
- It states the 60-second cadence and that the census is a level recomputed by the same ticker as the age series.
- It names the companion metric the alert joins against, so the two-repository contract is readable from this repo.
- The changelog records the new series under a new `## Unreleased` section with one feature bullet.
- Documentation only: no Go file changes and no behaviour change.

</summary>

<objective>

Write the cross-repository metric contract into `docs/controller-design.md` as a new `## Open Task Census` section, and record the change in `CHANGELOG.md` under a new `## Unreleased` section with one `feat:` bullet naming `agent_controller_open_tasks` (spec 020, Desired Behavior 8, AC 21 and AC 22).

</objective>

<context>

This repo has no root `CLAUDE.md`; the global YOLO container `CLAUDE.md` (already in your context) governs project conventions.

Read the spec: `specs/in-progress/020-open-task-census.md` — Desired Behavior 8, Acceptance Criteria 21 and 22, Constraints ("The contract lives in the design doc"), Assumptions, and the "Suggested Decomposition" table (this prompt is row 4).

DEPENDENCY — prompts 1, 2 and 3 must have shipped before this prompt runs, because this prompt documents what they built rather than what was intended. Verify first:

```bash
grep -n 'Name: "agent_controller_open_tasks"' pkg/metrics/metrics.go
grep -n 'func (v \*vaultScanner) recomputeOpenTaskCensus' pkg/scanner/open_task_census.go
grep -c 'recomputeOpenTaskCensus' pkg/scanner/vault_scanner.go
```

Expect the declaration line, the function line, and `2` (the start-up call and the tick arm). If any check fails, STOP and report `status: failed` with message `"open-task census not yet deployed (prompts 1-3)"` — do not document behaviour that did not ship.

Read these files before writing anything:

- `docs/controller-design.md` — the `## Task Age Metrics` section in full (it runs from that heading to `## What the Controller Does NOT Do`). It is the model for the new section: a one-paragraph statement that the series are **levels** rather than counters, a `| Series | Labels | Meaning |` table, a paragraph defining the population and the label-value source, a paragraph on the empty and broken cases, a paragraph on recompute and the single-writer rule, and a closing sentence saying the dashboard and alert live in `bborbe/nuke` and that a change to the contract needs its own spec.
- `pkg/metrics/metrics.go` — the `OpenTasks` variable and its doc comment (prompt 1), so the prose matches the code exactly.
- `pkg/scanner/open_task_census.go` — `recomputeOpenTaskCensus`, `publishOpenTaskCensus` and `openTaskPairFromFile` (prompt 2), so the prose describes the shipped open definition, the empty-label rule and the clearing rule and no more.
- `pkg/scanner/vault_scanner.go` — `Run`'s metrics arm (prompt 3), so the cadence and placement sentences are accurate.
- `CHANGELOG.md` — the preamble (five lines: title, blank, the Keep a Changelog sentence, the semver sentence, blank) followed immediately by `## v0.23.0`. There is currently **no** `## Unreleased` section; you create it.
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — `## Unreleased` placement, the required conventional prefix, bullet style, and the anti-patterns (never use the prompt filename as an entry, never describe what you verified, never write a bare `- fix: fix bug`).
- `/home/node/.claude/plugins/marketplaces/coding/docs/documentation-guide.md` — prose style for the doc edit.

Environment facts that shape this prompt:

- The execution container's `.git` is masked (the daemon runs with `hideGit=true`; `git status` fails with `fatal: not a git repository`). Do NOT run any `git` command anywhere in this prompt — a failed `git` command would read as a pass.
- Do NOT run `kubectl*`, `docker`, `make build`, `make buca`, `gh`, or any deploy command. The `bborbe/nuke` image bump, the `{vault="openclaw"}` selector edit and the dev/prod apply belong to a separate task.
- Do NOT commit. dark-factory owns git.

</context>

<requirements>

This prompt changes exactly two files: `docs/controller-design.md` and `CHANGELOG.md`. No Go file is created, edited or deleted.

1. **Add `## Open Task Census` to `docs/controller-design.md`.** Place it immediately after the `## Task Age Metrics` section and immediately before `## What the Controller Does NOT Do`, so the two metrics sections sit together. The heading must be exactly `## Open Task Census` at the start of the line (a checker greps `^## Open Task Census`).

   The section body — everything from that heading to the next `## ` heading — must contain each of these fixed strings, spelled exactly (a checker runs `grep -cF` for each and expects ≥ 1). Write them as **plain text**: do not put markdown backticks or bold markers inside a fixed string. The `## Task Age Metrics` section writes the open definition as `` `status` neither `completed` nor `aborted` ``; your section must instead read `status` neither completed nor aborted, because the checker matches the literal characters and the backticked form returns 0.

   - `agent_controller_open_tasks`
   - `vault`, `phase`, `task_type` (each as the label name; a table row or a sentence satisfies this)
   - `neither completed nor aborted`
   - `60 seconds`
   - `level`
   - `disappears from the exposition`
   - `empty label value`
   - `human_review`
   - `executor_config_accepted`

   Write the section in the shape of `## Task Age Metrics`. It must state, in prose and not only in a table:

   - The series is `agent_controller_open_tasks` with exactly three labels — `vault`, `phase` and `task_type` — and it reports the number of open records in each pair for this controller's own vault. Include a `| Series | Labels | Meaning |` table row for it.
   - It is a **level**, not a counter: the metrics ticker recomputes it from scratch on every pass and sets each pair's count, so two passes over an unchanged board publish the same value.
   - **Open** is `status` **neither completed nor aborted** — a missing status, an unknown status, `human_review` and `done` all count. The resting-phase exclusion (`human_review` and `done` are excluded) belongs to the alert expression, not to the instrument, and say so explicitly.
   - `vault` is the controller's own configured `VAULT_NAME`, passed through verbatim; the vault-api display name is not applied.
   - A missing or non-string `phase` or `task_type` is published as an **empty label value** and the record is still counted — no normalisation, no sentinel and no filtering.
   - A pair that empties **disappears from the exposition** entirely rather than lingering at `0`; the deletion is scoped to this controller's own `vault` value, so a second controller's series are never touched, and a blanket reset is explicitly rejected.
   - A file that cannot be read, carries no frontmatter, or will not deduplicate or unmarshal is skipped silently and counted in no pair. A failed listing writes nothing: the previous values stay, and on a pod's first failed pass no series exists yet.
   - **Recompute.** The census rides the same dedicated ticker as the age series, independent of `POLL_INTERVAL`: one recompute at start-up and then every **60 seconds**, so it publishes on a trigger-only controller (`POLL_INTERVAL=0s`) where the scan cycle is dead. It runs as a **sibling** of the task-age pass, not folded into it, and it never runs from the scan cycle or the start-up index build. It is read-only: it does not pull, scan, write, commit or send on the results channel.
   - The alert joins this series against the executor-side `executor_config_accepted` metric: an open pair with no accepted config is the defect. Note that the alert rule, the resting-phase exclusion and the join expression live in `bborbe/nuke`, and that a change to the name, a label, the open definition or the cadence is a cross-repo change needing its own spec that updates this section in the same change.

   Do not restate the executor-side metric's contract, do not claim the alert fires correctly today, and do not describe the deploy. This repo produces one half of the join.

2. **Add `## Unreleased` to `CHANGELOG.md`.** Insert it immediately after the frozen preamble (the Keep a Changelog / SemVer lines and their trailing blank line) and immediately before `## v0.23.0`. Do not modify the preamble and do not renumber or edit any existing version section.

   Under it, write exactly one bullet, in the repository's established style — a conventional prefix, a bold summary sentence, then the specifics:

   ```
   - feat: **open task census: the controller now publishes `agent_controller_open_tasks{vault,phase,task_type}`.** ...
   ```

   The bullet must name `agent_controller_open_tasks` (a checker greps the `## Unreleased` … `## v` range for it), and should state in one or two sentences: the series counts open records per `(phase, task_type)` pair, "open" means status neither `completed` nor `aborted` (so `human_review` and `done` are counted and the resting-phase exclusion stays in the alert), the value is a level recomputed on the existing 60-second metrics ticker so it publishes under `POLL_INTERVAL=0s` where the scan cycle is dead, and a pair that empties is deleted from the exposition scoped to the controller's own vault rather than left at `0` or cleared wholesale.

   Do not add a second bullet for the docs change, do not describe what you verified, and do not use the prompt filename as the entry.

3. **Change nothing else.** No Go file, no Makefile, no `.dark-factory.yaml`, no spec file. If a `.go` file turns out to need a change to make this prompt's claims true, STOP and report `status: failed` — the contract documents shipped behaviour, it does not fix it.

</requirements>

<constraints>

- **The contract lives in the design doc.** `docs/controller-design.md` § Open Task Census is the cross-repo contract `bborbe/nuke` consumes. A change to the name, a label, the open definition or the cadence is a cross-repo change and needs its own spec that updates that section in the same change.
- **Frozen metric name:** `agent_controller_open_tasks` — the section and the changelog must spell it exactly.
- **Frozen label names:** `vault`, `phase`, `task_type`.
- **Frozen open definition:** `status` neither `completed` nor `aborted`; missing and unknown statuses are open.
- **Do not document the executor-side metric as this repo's** — `executor_config_accepted` is owned by another component; this repo only names it as the join's other half.
- **No release tag.** The `## Unreleased` bullet is in scope because it is this repo's convention; the repo's own releaser turns it into a version on master. Do not add or edit a version heading.
- Do NOT commit — dark-factory handles git.

</constraints>

<verification>

Run from the repository root:

```bash
# the section exists at the right level
grep -n '^## Open Task Census' docs/controller-design.md

# every fixed string is present inside the section body
sed -n '/^## Open Task Census/,/^## /p' docs/controller-design.md | grep -cF 'agent_controller_open_tasks'
sed -n '/^## Open Task Census/,/^## /p' docs/controller-design.md | grep -cF 'neither completed nor aborted'
sed -n '/^## Open Task Census/,/^## /p' docs/controller-design.md | grep -cF '60 seconds'
sed -n '/^## Open Task Census/,/^## /p' docs/controller-design.md | grep -cF 'level'
sed -n '/^## Open Task Census/,/^## /p' docs/controller-design.md | grep -cF 'disappears from the exposition'
sed -n '/^## Open Task Census/,/^## /p' docs/controller-design.md | grep -cF 'empty label value'
sed -n '/^## Open Task Census/,/^## /p' docs/controller-design.md | grep -cF 'human_review'
sed -n '/^## Open Task Census/,/^## /p' docs/controller-design.md | grep -cF 'executor_config_accepted'
sed -n '/^## Open Task Census/,/^## /p' docs/controller-design.md | grep -cF 'vault'
sed -n '/^## Open Task Census/,/^## /p' docs/controller-design.md | grep -cF 'phase'
sed -n '/^## Open Task Census/,/^## /p' docs/controller-design.md | grep -cF 'task_type'

# the changelog section, its bullet and its release prefix
grep -n '^## Unreleased' CHANGELOG.md
sed -n '/^## Unreleased/,/^## v/p' CHANGELOG.md | grep -c 'agent_controller_open_tasks'
sed -n '/^## Unreleased/,/^## v/p' CHANGELOG.md | grep -c '^- feat: '

# the section sits between the age section and the "does NOT do" section
grep -n '^## Task Age Metrics\|^## Open Task Census\|^## What the Controller Does NOT Do' docs/controller-design.md

# Unreleased is the first version heading in the changelog
grep -n '^## ' CHANGELOG.md | head -1

# docs only: confirm no Go file changed
find . -name '*.go' -newer docs/controller-design.md -not -path './vendor/*' | head
```

Expected: the heading grep prints one line; every `grep -cF` prints ≥ 1; the `## Unreleased` grep prints one line and the changelog range grep prints ≥ 1; the three heading line numbers are in ascending order (Task Age Metrics < Open Task Census < What the Controller Does NOT Do); the `find` prints nothing newer than the doc you just wrote (if it lists a file, you edited Go code and must revert it).

This prompt changes no Go file, so do **not** run `make precommit` or `make test` — the spec's whole-repo gate (AC 1 and AC 23) is satisfied by prompt 3's run. If you did touch a Go file, revert it rather than running the gate.

</verification>
