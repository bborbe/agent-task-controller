---
status: approved
spec: [019-task-age-metrics]
created: "2026-10-31T16:35:00Z"
queued: "2026-10-31T15:31:37Z"
branch: dark-factory/spec-019
---

# Write the task-age metric contract and the release record

<summary>
- The controller design document carries the written contract the dashboard and the alert in another repository consume.
- The contract states both series and their label sets, the four bucket boundaries, the age precedence with the future clamp, the sixty-second cadence, the single-writer rule and the reset-to-zero behaviour.
- The design document already contains this section, so the work is to verify every required statement is present and add only what is missing.
- The changelog gains an unreleased entry naming both series, so the repository's releaser turns it into a version on merge.
- No Go code changes in this prompt.
</summary>

<objective>
Verify the existing metric contract in `docs/controller-design.md` § Task Age Metrics and record the change in `CHANGELOG.md` under `## Unreleased`. The design section is the cross-repo contract the alert rule and the dashboard panel consume, and it already exists on HEAD (it landed in commit `e16020e`) — so this prompt's only write is the `## Unreleased` bullet, and the doc half is verification, not authoring. The changelog entry is this repository's established convention and is what the releaser turns into a version.
</objective>

<context>
Read these files fully before changing anything:
- `docs/controller-design.md` — the `## Task Age Metrics` section already exists (around line 302, immediately before `## What the Controller Does NOT Do`). **Do not rewrite or reflow it.** Verify it against requirement 1 and add only the statements that are genuinely missing. The rest of the document shows the house style (tables for series and boundaries, bold lead sentences).
- `CHANGELOG.md` — the top section today is `## v0.22.10`; there is no `## Unreleased` section. Copy the bullet style used by existing entries: `- <prefix>: **bold lead.** ...` with a specific, named explanation.
- `specs/in-progress/019-task-age-metrics.md` — the source of every frozen name, boundary and wording this section must carry.

Relevant coding guides (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/documentation-guide.md`
</context>

<requirements>
1. **The doc section is pre-existing: this prompt writes only the CHANGELOG bullet.** Verify the `## Task Age Metrics` section in `docs/controller-design.md` contains every one of the following, and add only what is missing (keep the existing prose where it already says it):
   - The heading exactly `## Task Age Metrics` (level two, no trailing text).
   - Both metric names: `agent_controller_task_oldest_age_seconds` and `agent_controller_task_age_bucket_tasks`.
   - Both label sets: `vault` for the age series, `vault` and `bucket` for the bucket series.
   - All four bucket values: `lt_1d`, `1d_7d`, `gt_7d`, `undated`.
   - All four timestamp field names in precedence order: `created`, `created_date`, `queued_at`, `job_started_at`.
   - The fixed cadence written as the string `60 seconds`.
   - The words `level` and `single writer`.
   - The three boundary statements as fixed strings: `age < 24h`, `24h ≤ age ≤ 7 days`, `age > 7 days`.
   - The alert comparison as the fixed string `> 604800`.
   - The truncation statement as the fixed string `truncated to whole seconds`.
   - The qualification as the fixed string `after a successful pass`.
   - The deduplication skip case, containing the words `duplicate keys`.
   - The reset-to-zero behaviour: the age falls back to 0 when the last dated record turns terminal, while the bucket series stay present.
   - The vault label source: the controller's own configured vault name passed through verbatim, with no display-name or slug mapping.
   - The failed-listing behaviour: a failed pass writes nothing, so the previous successful pass's values stay.
   - The statement that the dashboard panel and the alert rule live in `bborbe/nuke`, and that a change to any name, label, boundary, precedence or cadence is a cross-repo change needing its own spec.

   If every statement is already present, make no edit to this file. Do not restructure the document.

2. In `CHANGELOG.md`, add a `## Unreleased` section above the current top section (`## v0.22.10`) with exactly one bullet, using the `feat:` prefix (this is a new feature, so the releaser bumps the minor version). The bullet must name both series:
   ```markdown
   ## Unreleased

   - feat: **task age metrics: the oldest non-terminal record's age and its bucket distribution are now published per vault.** `agent_controller_task_oldest_age_seconds{vault}` carries the age in whole seconds of the oldest dated non-terminal record and `agent_controller_task_age_bucket_tasks{vault,bucket}` counts non-terminal records in `lt_1d` / `1d_7d` / `gt_7d` / `undated`. Both are levels recomputed by a dedicated 60-second ticker that runs even under `POLL_INTERVAL=0s`, where the scan-cycle gauge is dead. Age comes from `created`, then `created_date`, then `queued_at`, then `job_started_at`; a record with none is counted in `undated`, and a future timestamp clamps to 0. The contract is written in `docs/controller-design.md` § Task Age Metrics.
   ```
   Do not add a version heading, do not cut a release tag, and do not touch any existing entry.
</requirements>

<constraints>
- Frozen metric names: `agent_controller_task_oldest_age_seconds` and `agent_controller_task_age_bucket_tasks`.
- Frozen label names: `vault` and `bucket`; frozen bucket values: exactly `lt_1d`, `1d_7d`, `gt_7d`, `undated`.
- Frozen threshold arithmetic: 7 days is `604800` seconds, and `1d_7d` is inclusive at 7 days, so the alert expression `> 604800` selects the same records as `gt_7d`. A `>=` would double-report a record exactly 7 days old.
- Frozen recompute interval: 60 seconds, a package constant in `main.go`, not configuration.
- The design section is a cross-repo contract: do not rename or reword a frozen name, label, boundary or cadence. A change to any of them needs its own spec that updates this section in the same change.
- No Go code changes in this prompt.
- Do not commit; dark-factory handles git.
- Do not cut a release tag or bump any version.
</constraints>

<verification>
Run from the repo root. The doc greps below are a regression check, not this prompt's evidence — every one of them already passes on HEAD, because the section landed in commit `e16020e`. Each must still print a number greater than zero (or the line it looks for):

```bash
grep -n '^## Task Age Metrics' docs/controller-design.md
grep -cF 'age < 24h' docs/controller-design.md
grep -cF '24h ≤ age ≤ 7 days' docs/controller-design.md
grep -cF 'age > 7 days' docs/controller-design.md
grep -cF '> 604800' docs/controller-design.md
grep -cF 'truncated to whole seconds' docs/controller-design.md
grep -cF 'after a successful pass' docs/controller-design.md
grep -c 'duplicate keys' docs/controller-design.md
grep -c '60 seconds' docs/controller-design.md
grep -c 'single writer' docs/controller-design.md
grep -c 'agent_controller_task_oldest_age_seconds' docs/controller-design.md
grep -c 'agent_controller_task_age_bucket_tasks' docs/controller-design.md
```

```bash
grep -n '^## Unreleased' CHANGELOG.md
sed -n '/^## Unreleased/,/^## v/p' CHANGELOG.md | grep -c 'agent_controller_task_oldest_age_seconds'
sed -n '/^## Unreleased/,/^## v/p' CHANGELOG.md | grep -c 'agent_controller_task_age_bucket_tasks'
```

```bash
ROOTDIR=/workspace make test
```

```bash
# The final command is the gate: its exit code must be 0. The doc greps above already
# pass on HEAD, so only the two CHANGELOG assertions distinguish this prompt's work.
grep -q '^## Unreleased' CHANGELOG.md \
  && sed -n '/^## Unreleased/,/^## v/p' CHANGELOG.md | grep -q 'agent_controller_task_oldest_age_seconds' \
  && sed -n '/^## Unreleased/,/^## v/p' CHANGELOG.md | grep -q 'agent_controller_task_age_bucket_tasks' \
  && grep -q '^## Task Age Metrics' docs/controller-design.md
```

Before finishing, re-run the commands above and walk each numbered requirement against the files. `make test` must exit 0, and the final `grep -q` chain must exit 0 — a non-zero exit there means the `## Unreleased` bullet is missing, or it does not name both series.
</verification>
