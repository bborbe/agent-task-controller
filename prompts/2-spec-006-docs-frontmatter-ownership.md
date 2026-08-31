---
status: draft
spec: [006-bug-frontmatter-field-ownership]
created: "2026-08-31T11:56:38Z"
branch: dark-factory/bug-frontmatter-field-ownership
---

# Document frontmatter field-ownership contract in controller-design.md

<summary>

- The `## Frontmatter Merge` section of `docs/controller-design.md` now documents the field-ownership contract as a table: controller-owned counters (`trigger_count` / `retry_count`) always keep the on-disk value, a terminal on-disk `status` is pinned, and everything else keeps agent-wins semantics.
- The section's illustrative merge example no longer implies blanket agent precedence — it shows a controller-owned counter and a terminal status surviving an incoming payload that tries to overwrite them.
- The section states that a terminal on-disk status short-circuits the escalation machinery: no Trigger Cap / Retry Escalation section, no assignee clear, no `previous_assignee`, no phase restore, and an inherited `spawn_notification: true` survives.
- The section records the one legitimate counter-lowering path — the scanner's Empty-to-Named Reset — so operators know the reset is the only mechanism that can lower a counter post-fix.
- The change is documentation-only: no Go code is touched, and the existing `## Terminal Task Status (create-task dedup)` section and every other section are left intact.

</summary>

<objective>

Update `docs/controller-design.md` § "Frontmatter Merge" (lines 52-60, the section between the `## Frontmatter Merge` and `## Terminal Task Status (create-task dedup)` headers) so it documents the ownership rules the field-ownership guard enforces, including the terminal short-circuit, and so its illustrative merge example no longer implies that agent keys blanket-override existing keys. This is the spec AC 17/18 doc half of DB 7.

</objective>

<context>

There is no CLAUDE.md in this repo; the global YOLO container CLAUDE.md (already in your context) governs project conventions. This is a docs-only change — no changelog change in this prompt (prompt 1 owns `CHANGELOG.md`); the doc-style reference is the repo's own `docs/controller-design.md`.

DEPENDENCY — verify prompt 1 shipped before changing anything:

```
cd /workspace && grep -n 'controllerOwnedFields' pkg/result/result_writer.go
```

The field-ownership guard (`MergeFrontmatter`, `controllerOwnedFields`, `terminalStatuses`, the `ownership guard kept on-disk` log) MUST be in `pkg/result/result_writer.go`. If the grep returns no match, STOP and report `status: failed` with message `"field-ownership guard not yet deployed (prompt 1)"` — do NOT write the docs from assumptions; the doc text must describe what actually shipped.

Read `docs/controller-design.md` IN FULL, focusing on the `## Frontmatter Merge` section (around lines 52-60). Current text:

```
## Frontmatter Merge

When writing a result back, the ResultWriter merges frontmatter from the existing task file with frontmatter provided by the agent. Existing keys are preserved, agent keys override on conflict. This ensures fields like `assignee`, `tags`, and `task_identifier` survive result writeback even though agents don't receive frontmatter.

```
Existing file:  {assignee: backtest-agent, tags: [agent-task], task_identifier: xyz}
Agent provides: {status: completed, phase: done}
Merged result:  {assignee: backtest-agent, tags: [agent-task], task_identifier: xyz, status: completed, phase: done}
```
```

The next section header is `## Terminal Task Status (create-task dedup)` (line 62) — its `completed`/`aborted` terminal-status definition must be referenced by (not contradicted by) the new text. The section `## Empty-to-Named Reset (spec 021)` (line 101) documents the reset that writes `trigger_count: 0` / `retry_count: 0` to disk on an empty→named assignee transition — the ownership section must name it as the ONLY mechanism that may lower a counter.

The change MUST stay inside the section bounded by the `## Frontmatter Merge` header and the `## Terminal Task Status (create-task dedup)` header — the spec AC greps are scoped to exactly that range:
- `sed -n '/^## Frontmatter Merge/,/^## Terminal Task Status/p' docs/controller-design.md | grep -c 'trigger_count'` must return ≥1 (today 0)
- the same range with `grep -c 'Controller-owned'` must return ≥1 (today 0)
- the same range with `grep -c 'terminal'` must return ≥1 (today 0)

Note the regex `/^## Terminal Task Status/` matches the header `## Terminal Task Status (create-task dedup)` as a prefix, so the range covers lines 52 through the end of the Frontmatter Merge section. Do NOT move, rename, or delete the `## Terminal Task Status (create-task dedup)` header.

</context>

<requirements>

All changes are in `docs/controller-design.md`, inside the `## Frontmatter Merge` section only.

1. **Rewrite the prose of the `## Frontmatter Merge` section.** Keep the existing sentence about what this ensures for agent-owned fields (`assignee`, `tags`, `task_identifier` surviving result writeback), but make explicit that agent keys override on conflict ONLY for agent-owned keys, and that two field classes are controller-owned. The section MUST contain the word `Controller-owned` (AC 17) and the word `terminal` (AC 18), and MUST mention `trigger_count` (AC 17). Shape the prose around this ownership table:

   | Ownership | Fields | Rule |
   |---|---|---|
   | Controller-owned | `trigger_count`, `retry_count` | On-disk value always wins; an incoming value can never introduce a key that is absent on disk. |
   | Controller-owned (terminal pin) | `status` | A terminal on-disk status (`completed` or `aborted`, decided by the normalizing `Status()` accessor) is pinned and the incoming status is discarded; the write is a pin, not a freeze — `phase`, the agent's result fields, and the body still land. |
   | Agent-owned | everything else | Incoming value wins on conflict (unchanged). |

2. **Replace the illustrative merge example so it no longer implies blanket agent precedence.** The current example shows `status: completed` from the agent winning outright — replace it with a fixture that demonstrates the ownership rules, e.g.:
   ```
   Existing file:  {status: aborted, trigger_count: 5, phase: ai_review}
   Agent provides: {status: in_progress, trigger_count: 1, phase: execution}
   Merged result:  {status: aborted, trigger_count: 5, phase: execution}
   ```
   The example must show a controller-owned counter and a terminal status surviving an incoming payload, while the agent-owned `phase` still lands — one example that carries all three rules. A short caption line under the example stating the rules it demonstrates is fine.

3. **Add a paragraph documenting the terminal short-circuit (AC 18).** The section must state, in prose, that a terminal on-disk `status` takes the task out of the escalation machinery uniformly for both terminal statuses: no `## Trigger Cap Escalation` / `## Retry Escalation` section is appended, `assignee` is not cleared, `previous_assignee` is not written, `phase` is not restored by `restoreExistingPhase`, and an inherited `spawn_notification: true` key survives the write. State the rationale in one sentence: escalation exists to park a live runaway task, and a task an operator has already ended is not that.

4. **Add a paragraph recording the guard log and the reset path.** The section must state that when the guard discards an incoming value that differs from the kept on-disk value, the writer emits one unconditional INFO log line containing `ownership guard kept on-disk` naming the task, the field, and both values, and that equal values produce no log line (steady-state publishes are silent). It must also name the scanner's Empty-to-Named Reset (`## Empty-to-Named Reset (spec 021)`, which writes `trigger_count: 0` / `retry_count: 0` to disk) as the only mechanism that may lower a controller-owned counter.

5. **Self-check before finishing.** Run the `<verification>` block; confirm the section still reads coherently top-to-bottom, references (does not contradict) the `## Terminal Task Status (create-task dedup)` definition that follows it, and leaves every other section of the document untouched.

</requirements>

<constraints>

- Documentation-only change. Do NOT touch any Go source, test, `CHANGELOG.md`, or config file — prompt 1 owns those.
- Do NOT move, rename, or delete the `## Frontmatter Merge` header or the `## Terminal Task Status (create-task dedup)` header; the new text must live entirely between them.
- Do NOT document a config flag, env var, or per-task opt-out for the ownership rules — the guard is unconditional.
- The terminal statuses named are exactly `aborted` and `completed`; do NOT imply any other status is terminal, and do NOT treat `phase: done` as a status.
- Do NOT claim the guard freezes the file: state explicitly that the write is a pin, not a freeze — `phase`, agent result fields, and the body still land on a terminal task.
- Do NOT commit — dark-factory handles git.

</constraints>

<verification>

Section-scoped grep checks (spec ACs 17-18; wrap in `|| true` — `grep -c` exits 1 on a zero count):

```
cd /workspace && sed -n '/^## Frontmatter Merge/,/^## Terminal Task Status/p' docs/controller-design.md | grep -c 'trigger_count' || true
cd /workspace && sed -n '/^## Frontmatter Merge/,/^## Terminal Task Status/p' docs/controller-design.md | grep -c 'Controller-owned' || true
cd /workspace && sed -n '/^## Frontmatter Merge/,/^## Terminal Task Status/p' docs/controller-design.md | grep -c 'terminal' || true
```
Expect ≥1 from each (all three are 0 today).

Scope check (docs-only): verify by inspection that ONLY `docs/controller-design.md` was modified — no Go file, test file, `CHANGELOG.md`, or config file changed. Do NOT use git to check (the execution container's `.git` is masked).

Docs-only change: `make precommit` is not required (no Go code changed, per the container CLAUDE.md — run it only if you modified Go). `cd /workspace && make test` is a sufficient fast sanity check that nothing else broke.

</verification>
