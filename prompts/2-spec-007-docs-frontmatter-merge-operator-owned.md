---
spec: ["007-bug-writeback-clobbers-operator-edits"]
status: draft
created: "2026-09-03T18:15:22Z"
---

# Document the operator-owned row and the body section merge in `docs/controller-design.md`

<summary>

- The `## Frontmatter Merge` section of the controller design doc gains an "Operator-owned" row in its ownership table, covering `assignee` and `previous_assignee`.
- The row states the on-disk value always wins, that an incoming value may introduce an absent key (unlike controller-owned counters), and that an incoming empty `assignee` is always honored as the deliverer's Failed/needs_input clear with no guard decision or log line.
- The section gains a body-merge paragraph describing how `WriteResult` now merges the body by heading instead of replacing it wholesale — operator-authored headings survive, same-named headings are replaced, and the preamble follows the stated rule.
- The doc change satisfies the spec's two machine-decidable grep counts inside the section (both currently 0).
- This is a docs-only change with no Go code, so it does not run `make precommit`.

</summary>

<objective>

Make `docs/controller-design.md` § "Frontmatter Merge" describe what the result-write chokepoint now actually does after the code change ships: an operator-owned row for `assignee`/`previous_assignee` and a body section-merge paragraph, so the documented contract matches the implemented ownership and merge behavior.

</objective>

<context>

There is no CLAUDE.md in this repo; the global YOLO container CLAUDE.md (already in your context) governs project conventions. Read the repo's own code and docs as the source of truth.

Read `docs/controller-design.md` § "Frontmatter Merge" — the section spans from the `## Frontmatter Merge` heading (line 52) to the `## Terminal Task Status (create-task dedup)` heading (line 89). It currently contains:
- an ownership table with three rows (`Controller-owned` for `trigger_count`/`retry_count`, `Controller-owned (terminal pin)` for `status`, `Agent-owned` for everything else),
- a merge example block,
- a "**Terminal short-circuit.**" paragraph,
- a "**Guard logging.**" paragraph,
- a `frontmatterValueEqual` paragraph,
- and a "**What the guard does and does not cover.**" paragraph.

The behavior this doc must now describe is implemented in `pkg/result/result_writer.go` (prompt 1, already shipped before this prompt runs):
- `operatorOwnedFields = []string{"assignee", "previous_assignee"}` — when the key exists on disk, the on-disk value wins; an incoming value may introduce a key absent on disk; an incoming empty `assignee` is always applied (the deliverer's deliberate Failed/needs_input clear) and produces no guard decision.
- `buildResultModifyFn` merges the body by heading: on-disk-only headings are preserved in place with their content, same-named headings are replaced in place by the incoming content, incoming-only headings are appended after the last on-disk section, and the on-disk preamble survives when the incoming body starts with a heading (an incoming preamble replaces it when present). A bare `---` line is never a heading; `\n` and `\r\n` line endings are both tolerated.

Read the coding-plugin docs that apply:
- `/home/node/.claude/plugins/marketplaces/coding/docs/documentation-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` (this is a docs change, not a changelog entry — do NOT edit `CHANGELOG.md` in this prompt)

Read the doc you are editing in full first (16KB), and read `pkg/result/result_writer.go` around `operatorOwnedFields` and `buildResultModifyFn` to confirm the shipped semantics before writing prose.

</context>

<requirements>

All changes are in `docs/controller-design.md`, inside the `## Frontmatter Merge` section (between the `## Frontmatter Merge` heading and the `## Terminal Task Status (create-task dedup)` heading). Do not touch any other section, do not edit `CHANGELOG.md`, and do not edit any Go file.

1. **Add an "Operator-owned" row to the ownership table.** The table currently ends with the `Agent-owned` row. Insert a new row between the `Controller-owned (terminal pin)` row and the `Agent-owned` row, using the same three-column `| Ownership | Fields | Rule |` shape:
   ```
   | Operator-owned | `assignee`, `previous_assignee` | The on-disk value always wins when the key exists on disk. An incoming value may introduce a key absent on disk (a spawn/claim names an assignee on a task that never carried one) — unlike controller-owned counters. Exception for `assignee` only: an incoming empty string is always applied, as the deliverer's deliberate Failed/needs_input clear (spec 039) rather than a stale snapshot, and produces no guard decision or log line. |
   ```
   The word `Operator-owned` MUST appear in this row (spec AC 16 greps for it inside the section), and the word `assignee` MUST appear in this row (spec AC 16 also greps for it).

2. **Add a body-merge paragraph.** After the sentence "The terminal `status` is pinned and the controller-owned counter keeps its on-disk value, while the agent-owned `phase` still lands." (the paragraph following the merge example block), add a new paragraph describing the body merge, in the voice of the existing section (plain prose, no code). It MUST state:
   - The body is merged by heading rather than replaced wholesale: an on-disk heading absent from the incoming body is preserved in place with its content (an operator `## Parked` section recording a park reason and resume options survives), a heading present in both bodies is replaced in place by the incoming content (the agent's fresh `## Result` lands), and a heading present only in the incoming body is appended after the last on-disk section.
   - The preamble rule: text before the first `## ` heading is preserved from the on-disk file only when the incoming body starts with a heading (no preamble); an incoming body that carries its own preamble replaces the on-disk preamble, and a body with no `## ` heading at all is preamble-only on both sides, so an incoming preamble-only body still replaces an on-disk preamble-only body.
   - A bare `---` line is never treated as a heading and is preserved unescaped; both `\n` and `\r\n` line endings are tolerated.
   - Escalation sections still append exactly once: an on-disk `## Trigger Cap Escalation` / `## Retry Escalation` section survives the merge, so the dedup check still sees it and does not append a duplicate.

3. **Self-check before finishing:** re-run the `<verification>` block and confirm both section-scoped grep counts return ≥1 and that the section boundaries are unchanged (`## Frontmatter Merge` still starts the section and `## Terminal Task Status (create-task dedup)` still ends it).

</requirements>

<constraints>

- Docs-only change: no Go source is modified, so per project convention `make precommit` is NOT run for this prompt — the `<verification>` block's section-scoped greps are the evidence.
- Do NOT commit — dark-factory handles git. The execution container's `.git` is masked, so do NOT attempt git commands.
- The change is confined to the `## Frontmatter Merge` section of `docs/controller-design.md`. Do NOT edit the `## Assignee-Clear on Escalation` section, the `## Terminal Task Status` section, any other doc, or any source file.
- The prose must describe the behavior that prompt 1 shipped (operator-owned `assignee`/`previous_assignee` with the empty-clear exception, and the body section merge) — do not document features that do not exist (e.g. no mention of a config flag or per-task opt-out, and no claim that the create-task reopen write site is covered — that site is explicitly out of scope).
- The doc must remain accurate for what an operator edit is guaranteed to survive; do NOT extend the guarantee to `stage`, `tags`, or any other operator-backfilled key, and do NOT claim the `[[Agent Task File Contract]]` vault page is updated (that is a follow-up task outside this spec).

</constraints>

<verification>

Spec AC 16 — both greps are section-scoped (from `## Frontmatter Merge` to `## Terminal Task Status`) and each `grep -c` is wrapped in `|| true` because it exits 1 on a zero count:

```
cd /workspace && sed -n '/^## Frontmatter Merge/,/^## Terminal Task Status/p' docs/controller-design.md | grep -c 'Operator-owned' || true
cd /workspace && sed -n '/^## Frontmatter Merge/,/^## Terminal Task Status/p' docs/controller-design.md | grep -c 'assignee' || true
cd /workspace && sed -n '/^## Frontmatter Merge/,/^## Terminal Task Status/p' docs/controller-design.md | grep -c 'body' || true
```

Expect: first count ≥1 (the new table row), second count ≥1 (the operator-owned row and the body-merge paragraph), third count ≥1 (the body-merge paragraph). All three return 0 today. No `make precommit` for this docs-only change.

</verification>
