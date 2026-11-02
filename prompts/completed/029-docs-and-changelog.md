---
status: completed
spec: [016-bug-task-identifier-path-index]
summary: Recorded the shipped identifier→path index in docs/controller-design.md § 1 (scanner publishes the index) and § 2 (lookup consults it first, with the superseded 'walk task directory' step line removed); CHANGELOG.md was left unmodified because its existing `## Unreleased` section already carries spec 016's `feat:` and `fix:` bullets from prompts 1 and 2, and adding prompt 3's bullet would duplicate that logical change and fail the prompt's own exactly-one-`- fix:` check.
execution_id: agent-task-controller-taskpath-index-exec-029-docs-and-changelog
dark-factory-version: v0.196.0
created: "2026-09-25T18:47:00Z"
queued: "2026-09-25T17:04:41Z"
started: "2026-09-25T17:20:21Z"
completed: "2026-09-25T17:27:29Z"
branch: dark-factory/bug-task-identifier-path-index
---

# Record the identifier index in the design doc and changelog

<summary>

- The controller's design document now describes the identifier-to-file mapping the scanner publishes on every scan cycle, so the document matches the code.
- The command-processing section no longer describes the lookup as a walk of the whole task directory — it now says the mapping is consulted first and the walk is the fallback.
- The routing guard's documented position relative to the lookup is left exactly as it is: the guard still runs first, so a command for another vault still resolves nothing.
- The changelog gains an unreleased entry naming the mapping, so the change is visible to anyone reading the release notes.
- No released changelog section and no other design-document paragraph is touched.
- No code changes in this prompt: it records the work the two preceding prompts shipped.

</summary>

<objective>

Make the written record match the shipped behaviour: `docs/controller-design.md` § 1 states that the scanner publishes the identifier→path index and § 2 states that the lookup consults it before walking the directory (with the superseded "walk the task directory" step line removed), and `CHANGELOG.md` gains an `## Unreleased` bullet naming the index. Satisfies spec 016 Desired Behavior 8 and Acceptance Criteria 11-12 (plus AC 1).

</objective>

<context>

This repo has no root `CLAUDE.md`; the global YOLO container `CLAUDE.md` already in your context governs project conventions. The spec for this work is `specs/in-progress/016-bug-task-identifier-path-index.md`.

**This prompt depends on prompt 2 having shipped** (`prompts/2-resolver-first-lookup-and-wiring.md`), which is what makes the lookup consult the index. Documenting the behaviour before it exists would put a false statement in the design doc, which the project treats as a contract. Before starting, confirm the shipped state:

- `grep -c 'index hit for task' pkg/result/result_writer.go` prints at least 1.
- `grep -c 'TaskPathResolver' pkg/factory/factory.go` prints at least 1.

If either prints 0, STOP and report `status: failed` with the message `"prompt 2 (resolver-first lookup and wiring) has not shipped"` — do not document behaviour that is not in the tree.

Read the spec first, in full. The parts that bind this prompt are Desired Behavior 8, Constraints, and Acceptance Criteria 11 and 12. AC 1 (`make precommit` exits 0) applies here too.

Read these files IN FULL before editing:

- `docs/controller-design.md` (265 lines) — the whole file, so the paragraph style is visible. The two sections you extend are `### 1. Change Detection (git → Kafka)` (starts ~line 15) and `### 2. Command Processing (Kafka → git)` (starts ~line 30); the next heading after § 2 is `## Frontmatter Merge` (~line 58). Note the house style: each section's logic is a fenced code block with `├──` / `└──` step lines, and the prose is **single unwrapped lines** with a bold lead-in (`**Heal-on-write.**`, `**Build-fix supersede (spec 014).**`). Also read the routing-guard paragraph at ~line 52 — it records that the guard runs **before** the task-file lookup, which this change must not contradict.
- `CHANGELOG.md` — the frozen preamble is the `# Changelog` title, the "All notable changes…" line, the Keep-a-Changelog line and the Semantic-Versioning line. The newest section today is `## v0.11.2` at line 8; there is NO `## Unreleased` section, so it must be **created**, not appended to.

Read the coding-plugin docs (in-container paths):

- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — entry format, the required prefix, one bullet per logical change, `## Unreleased` placement.
- `/home/node/.claude/plugins/marketplaces/coding/docs/documentation-guide.md` — prose style for the doc paragraphs.

Environment facts that shape this prompt:

- The execution container's `.git` is masked (the daemon launches with `hideGit=true`; this repo's `.dark-factory.yaml` sets `workflow: direct` and no `hideGit`, so the masking comes from the launch flags). Do NOT run any `git` command anywhere in this prompt — it dies with `fatal: not a git repository`, and the daemon does not check `<verification>` exit codes, so a failed git command reads as a pass. All the checks below are non-git equivalents.
- Do NOT run `kubectl*`, `docker`, `make build`, `make buca`, `gh`, or any operator/deploy command.
- `make precommit` and `make test` run from the **repo root** (single Go module). This prompt changes no `.go` file, so `make precommit`'s `test` and `check` targets must return exactly what they returned after prompt 2; a failure there is a pre-existing failure, not something this prompt introduced, and must be reported as such rather than worked around by editing code.

</context>

<requirements>

## 1. `docs/controller-design.md` § 1 — the scanner publishes the index

### 1a. Add the step to § 1's code block

The § 1 fenced block currently ends with:

```
  ├── changed file → parse frontmatter + body → publish agent-task-v1-event
  └── deleted file → publish agent-task-v1-event (deleted)
```

Replace those two lines with these three, keeping the `├──` / `└──` box-drawing style and the two-space indentation exactly:

```
  ├── changed file → parse frontmatter + body → publish agent-task-v1-event
  ├── deleted file → publish agent-task-v1-event (deleted)
  └── publish identifier→path index (task_identifier → task file path) for the command consumer
```

Do not touch any other line of the block.

### 1b. Add the § 1 paragraph

Insert one new paragraph **immediately after** the existing § 1 paragraph that begins "> The scanner increments `agent_controller_vault_scanner_skipped_files_total{reason=<closed enum>}`…" and **immediately before** the `### 2. Command Processing (Kafka → git)` heading, so it is inside § 1.

Write it as **one single unwrapped line**, in the doc's bold-lead-in style, with exactly this text — starting at column 1 with **no leading `>`**: § 1's only existing prose line is a blockquote (`docs/controller-design.md:28` begins `> The scanner increments …`), but this new paragraph must begin at column 1, because the paragraph-start grep in `<verification>` is anchored at `^\*\*Identifier→path index (spec 016)\.\*\*` and a `>` prefix would make it return 0 on an otherwise correct edit:

```
**Identifier→path index (spec 016).** Once a cycle's file walk and deleted-file collection have completed, the scanner publishes an in-process `task_identifier` → task-file-path index derived from the same per-file bookkeeping that decides whether a file changed. The map is built whole and swapped under a write lock, so a reader observes either the previous cycle's mapping or the new one — never a partially built map and never one being mutated — and the scanner's own bookkeeping stays single-goroutine and unguarded. The index is replaced wholesale once per cycle, so a deleted or renamed file stops being reported on the next cycle; an entry whose `task_identifier` is empty — the halted-repair shape — is never indexed, because an empty identifier is not a task. Two files sharing one identifier keep both paths, so the ambiguity can be reported rather than silently collapsed. The index holds only files under this controller's own `taskDir`, is in-process only, is never serialized or persisted, and adds no config field, env var, CLI flag or metric.
```

The word `index` MUST appear in § 1 (the spec's evidence greps § 1 for it), and it must be **stated**, not merely referenced.

## 2. `docs/controller-design.md` § 2 — the lookup consults the index first

### 2a. Replace the superseded step line

§ 2's code block currently contains this step (line ~38):

```
  ├── walk task directory, find file matching task_identifier in frontmatter
```

Replace that single line with:

```
  ├── resolve task_identifier → task file path (index hit: one read, no listing; miss: full directory walk as before)
```

The superseded line's exact text — `walk task directory, find file matching task_identifier in frontmatter` — must not survive anywhere in the file: the spec's evidence asserts its count is 0, because a section that mentions an index elsewhere while leaving this line intact would still describe the mechanism this change replaces. Keep the replacement short enough that it does not reintroduce that phrase.

### 2b. Add the § 2 paragraph

Insert one new paragraph **immediately after** the § 2 paragraph that begins "The frontmatter commands (`update-frontmatter`, `increment-frontmatter`, `complete-task`) carry the same `targetVault`…" (the routing-guard paragraph, ~line 52) and **before** the `**Heal-on-write.**` paragraph, so it stays inside § 2 and before the `## Frontmatter Merge` heading.

Write it as **one single unwrapped line**, bold lead-in, with exactly this text:

```
**Identifier→path index lookup (spec 016).** The task-file lookup consults the scanner's published identifier→path index before walking the task directory: an identifier the index holds resolves to its file with one read and no directory listing, and an identifier the index does not hold — a file the scanner skipped, or one created since the last cycle — falls back to the existing full walk with its 3-attempt retry loop unchanged. A resolver error is returned as an error and never treated as a miss, so an ambiguous identifier cannot be resolved by re-deriving the ambiguity through the walk; a hit whose single read or parse fails falls back to the walk, because the snapshot is rebuilt every cycle and a stale entry lives at most one cycle. The lookup's position relative to the vault-routing guard is unchanged — the guard still runs first, so a cross-vault command still performs no lookup at all. A hit emits one `glog.V(2)` line containing `index hit for task`, so a deployed controller's behaviour is readable in the pod log. The index adds no config surface: there is no switch that restores the per-command walk.
```

The word `index` MUST appear in § 2 as well (the spec's evidence greps § 2 for it).

### 2c. Do not touch anything else in the document

- Do not modify, reorder or rewrap any other paragraph, and do not change the § 2 routing-guard paragraph's claim that the guard runs before the lookup.
- Do not document a config knob, an opt-out flag, a tunable, a TTL or a new metric — the spec's Non-goals forbid all of them, and documenting one would describe a surface that must not exist.
- Do not add a new section heading; the two paragraphs belong inside the two existing sections.

## 3. `CHANGELOG.md` — the `## Unreleased` bullet

Insert a new `## Unreleased` section immediately after the frozen preamble (after the "and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html)." line) and immediately above the `## v0.11.2` heading, containing exactly one bullet:

```markdown
## Unreleased

- fix: resolve a task_identifier from the scanner's published identifier→path index instead of walking the whole vault on every command, so the command consumer no longer serializes behind a full-vault read per result write. Every result write resolved its task file by listing the task directory and reading every file in it — 8,712 git-rest reads per lookup against the 8,972-file openclaw vault, ~11s per command — so a burst of agent completions drained at ~0.6 msg/min, pushed tail commands past the 60-minute `commandExpireDuration` window, and dropped their frontmatter writes silently (one measured command was consumed 36 minutes late). The scanner now publishes the `task_identifier` → task-file-path index it already computed, rebuilt wholesale once per completed scan cycle and swapped under a read-write lock; the lookup consults it first, so a hit costs one file read and no directory listing, and an identifier the index does not hold still resolves through the unchanged walk with its 3-attempt retry loop. The fail-loud duplicate behaviour is preserved — two files sharing one identifier keep both paths and the lookup returns the same error naming both — the vault-routing guard still runs before the lookup, a hit emits one `glog.V(2)` line containing `index hit for task`, and the change adds no config field, env var, CLI flag, metric or second scanner (spec 016)
```

- The `fix:` prefix is required and is the only prefix used here: the spec frames this as a defect, so it ships a patch bump, not a new capability.
- The word `index` MUST appear above the first `## vX.Y.Z` heading. The check is position-aware: it stops at the first released heading, so an `index` mention inside an existing released bullet cannot satisfy it.
- One bullet, one logical change. Do not create a second `## Unreleased` section, do not touch any existing version section or bullet, and leave the `# Changelog` title and the SemVer preamble byte-for-byte unchanged.

## 4. Self-check before finishing

Re-run every command in `<verification>` and confirm each one passes. Then walk the spec's Acceptance Criteria against the change:

- **AC 11** — § 1 mentions the index, § 2 mentions the index, and the superseded `walk task directory, find file matching task_identifier in frontmatter` line is gone.
- **AC 12** — the `## Unreleased` bullet names the index and sits above the first released heading, and the section was created rather than appended to an existing one.
- **AC 1** — `make precommit` exits 0 at the repo root.

Also confirm: the `.go`-newer-than-baseline check in `<verification>` printed nothing (no code was touched), no new heading was added to the design doc, and no config knob, opt-out flag, tunable, TTL or metric is mentioned anywhere in the new text.

</requirements>

<constraints>

- **The project convention is that a behavioural rule lands in `docs/controller-design.md`.** A completed spec is immutable, so this prompt only adds to the design doc; it does not touch `specs/`.
- **The routing guard's documented position is frozen.** `docs/controller-design.md:52` records that the guard runs before the task-file lookup; the new § 2 paragraph must state that this ordering is unchanged, and the guard must not be described as moving.
- **The superseded step line must be gone.** `walk task directory, find file matching task_identifier in frontmatter` must have a count of 0 in `docs/controller-design.md`.
- **The full walk is not removed.** The documentation must describe it as the fallback for every identifier the index does not hold, with its 3-attempt retry loop unchanged.
- **No knob is documented.** No config field, env var, CLI flag, allowlist, TTL or bypass for the index — invariant. A switch that restores the per-command walk re-opens the bug at the moment the vault is largest.
- **No new metric is documented.** The hit is observable through the existing V(2) log line and the existing `agent_controller_git_rest_calls_total{op="list"}` counter. Do not invent a counter or a gauge in prose.
- **`commandExpireDuration` is frozen.** The documentation must not describe the 60-minute window as widened, narrowed or configurable.
- **Changelog format:** `## Unreleased` above the newest released section, `- <prefix>: <what> [context]`, prefix required, one bullet per logical change, naming types and packages. Do not create a second `## Unreleased`.
- **Documentation style:** single unwrapped lines, bold lead-in, matching the surrounding paragraphs. Do not rewrap existing paragraphs.
- **No `.go` file changes.** This prompt is documentation and changelog only; if `make precommit`'s `test` or `check` target fails, that failure is pre-existing and must be reported as such rather than fixed by editing code.
- **`make precommit` and `make test` run from the repo root** (single Go module).
- Do NOT commit — dark-factory handles git.
- Do NOT run any `git` command — the container's `.git` is masked and a failed git command reads as a pass.
- Do NOT run `kubectl*`, `docker`, `make build`, `make buca`, `gh`, or any operator/deploy command.

</constraints>

<verification>

Run from the repo root. Every check below is a non-git equivalent of the spec's container-executable block, scoped to this prompt.

Spec AC 11 — § 1 mentions the index, § 2 mentions the index, and the superseded line is gone. The two section checks are **positive-count** assertions, so they use `test "$(…)" -ge 1` rather than a bare `grep -c … || true`: the `|| true` form exits 0 unconditionally, so a zero count would read as a pass. The third is the absence assertion and is written as `! grep -q`:

```
test "$(sed -n '/^### 1\. Change Detection/,/^### 2\. Command Processing/p' docs/controller-design.md | grep -c 'index')" -ge 1
test "$(sed -n '/^### 2\. Command Processing/,/^## Frontmatter Merge/p' docs/controller-design.md | grep -c 'index')" -ge 1
! grep -q 'walk task directory, find file matching task_identifier in frontmatter' docs/controller-design.md
```

Expect the first two to exit 0 (a count of at least 1 each), and the third to exit 0 with no output.

Spec AC 12 — the changelog bullet names the index above the first released heading, the section was created rather than duplicated, and the bullet carries the required `fix:` prefix exactly once:

```
awk '/^## v/{exit} tolower($0) ~ /index/{f=1} END{exit !f}' CHANGELOG.md
test "$(grep -c '^## Unreleased' CHANGELOG.md)" -eq 1
awk '/^## Unreleased/{u=NR} /^## v0\.11\.2/{v=NR} END{exit !(u && v && u < v)}' CHANGELOG.md
awk '/^## Unreleased/{f=1;next} /^## /{f=0} f && /^- fix:/{n++} END{exit !(n==1)}' CHANGELOG.md
```

Expect the first `awk` to exit 0 (the match precedes the first `## v` heading), the `test` to exit 0 (exactly one `## Unreleased` section, not two), the second `awk` to exit 0 (the new section sits above `## v0.11.2`, which is still present), and the third `awk` to exit 0 (the section holds exactly one `- fix:` bullet). The third `awk` is what enforces requirement 3's prefix and one-bullet rules — a plain grep for `index` asserts neither.

Confirm the two new paragraphs landed inside their sections rather than in a new one, and that no heading was added or removed:

```
test "$(grep -c '^\*\*Identifier→path index (spec 016)\.\*\*' docs/controller-design.md)" -eq 1
test "$(grep -c '^\*\*Identifier→path index lookup (spec 016)\.\*\*' docs/controller-design.md)" -eq 1
test "$(grep -c '^## Frontmatter Merge' docs/controller-design.md)" -eq 1
test "$(grep -c '^## ' docs/controller-design.md)" -eq 11
test "$(grep -c '^### ' docs/controller-design.md)" -eq 5
```

Expect all five to exit 0 — exactly one of each paragraph, the `## Frontmatter Merge` heading untouched (so nothing was renamed or reordered around the insertions), and the heading sets unchanged at 11 `## ` and 5 `### ` headings. The two heading counts are what enforce requirement 2c's "do not add a new section heading": both `sed` ranges above still resolve if a wrong implementation slips a `### 3. Identifier Index` between § 1 and § 2, so a surviving-heading check alone would not catch it. Both counts were measured against the pre-change file.

Confirm no Go file was modified by this prompt (the design doc and changelog are markdown). Record a
baseline timestamp **before you edit anything**, then compare after the edits:

```
date +%s > /tmp/spec016-prompt3-start.txt
# ... make the edits ...
find . -name '*.go' -not -path './vendor/*' -newer /tmp/spec016-prompt3-start.txt
```

Expect no output. Any `.go` file listed was touched after the baseline, which means this prompt modified code it should not have. Run this pair with **no `make` invocation in between**: `make precommit`'s `generate` target rewrites the counterfeiter fakes under `mocks/`, which would make generated files appear newer than the baseline and produce a false positive.

The full gate, run ONCE at the end (AC 1):

```
make precommit
```

Expect exit 0. This prompt changes no `.go` file, so `test` and `check` must return exactly what they returned after prompt 2 — a green run here is the confirmation that the documentation change broke nothing. If `make precommit` fails in a target that inspects Go code, report `status: partial` with the failing target named; do not edit code to make it pass.

NOT run here, and deliberately so — the spec's Post-Deploy rungs (AC 13/14) read a running controller's pod log on dev and prod, the image build, pin bump and deploy steps are operator-side, and no `git` command runs here because the container's `.git` is masked.

</verification>

<!--
NOTES FOR THE HUMAN REVIEWER — decisions taken and open questions, non-blocking:

1. The spec's Suggested Decomposition is followed exactly: this is its row 3, depending on prompt 2. AC 1
   (`make precommit` exits 0) applies to every prompt, so it appears here as well as in prompts 1 and 2,
   even though this prompt changes no Go file.

2. The `make precommit` step is included despite the global rule that a non-Go change may skip it, because
   spec AC 1 states it for every prompt and the spec's container-executable block lists it unconditionally.
   It is cheap insurance here: a green run proves the markdown change broke nothing.

3. § 1's index step line is added to the fenced logic block AND a paragraph is added to § 1's prose. The
   grep in AC 11 would be satisfied by the step line alone, but the doc's convention is that a behavioural
   rule is stated in prose, so both are written.

4. AC 11's § 2 grep is satisfied by the § 2 paragraph; the step-line replacement in 2a is separately
   required by AC 11's negative clause (the superseded line's count must be 0). Both are needed — the
   spec calls this out explicitly, because a change that mentions an index elsewhere while leaving that
   line intact would still describe the replaced mechanism.

5. AMBIGUITY RESOLVED — the spec's § 2 evidence uses `sed -n '/^### 2\. Command Processing/,/^## Frontmatter Merge/p'`.
   That range is confirmed to exist in the current file (`### 2. Command Processing (Kafka → git)` at ~line 30,
   `## Frontmatter Merge` at ~line 58), so the new § 2 paragraph is placed between the routing-guard
   paragraph and the `**Heal-on-write.**` paragraph, which is inside that range.

6. AMBIGUITY RESOLVED — the spec's `awk '/^## v/{exit} tolower($0) ~ /index/{f=1} END{exit !f}' CHANGELOG.md`
   stops at the first `## v` heading, so only the new `## Unreleased` bullet can satisfy it. The heading does
   not exist today (`CHANGELOG.md:8` is `## v0.11.2`), so the section is created, not appended to.
-->

