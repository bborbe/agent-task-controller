---
status: approved
tags:
    - dark-factory
    - spec
approved: "2026-10-19T17:01:42Z"
generating: "2026-10-19T17:03:33Z"
branch: fix/rename-remints-identifier
---

## Summary

- Renaming a task file makes the controller replace its identifier with a freshly minted UUID, so every reference keyed on that identifier silently orphans.
- The scanner decides whether an identifier is duplicated by checking its own per-path bookkeeping. After a rename the old path is still in that bookkeeping while the new path is processed, so the renamed file matches its own identifier and is judged a duplicate.
- The code that prunes vanished paths already exists, but the scan runs it after the file loop — too late for the check that needed it.
- The duplicate branch strips the identifier and mints a new one, which is the observed corruption.
- The fix prunes the bookkeeping before the scan loop, so a rename is reconciled as a move and never reaches the duplicate branch.

## Problem

Two facts about the scanner combine badly.

First, the uniqueness check answers "is this identifier used by any other file?" against the scanner's own bookkeeping, which is keyed by path, and it returns false when any entry at a different path carries the same identifier. Entries for files that no longer exist survive until something deletes them.

Second, the scan builds the current cycle's path set and prunes vanished paths only after the per-file loop has finished. For the whole duration of the loop, the bookkeeping still holds every path from the previous cycle, including one a rename has just removed from disk.

A rename therefore produces this sequence: the old path is gone from the listing but still in the bookkeeping; the new path is read and its identifier parsed; the uniqueness check finds the old path carrying the same identifier and reports a duplicate; the scanner strips the identifier and writes a brand-new UUID. The rename has re-minted the identity of a task that never changed.

The consequence is not a parse error or a visible failure. The file stays valid and keeps its title, status and body — only the identity changes. Every consumer keyed on the identifier loses its row without an error: the manager's per-task escalation card uses the identifier as its deduplication key, so a re-mint makes a second card post for a row that already has one, and vault-cli's escalation payload carries the same value. Nothing reports the loss.

## Reproduction

Environment: prod `agent-task-controller-personal` (v0.11.6, `AUTO_INJECT_TASK_IDENTIFIER=true`, `TASK_DIR=25 Tasks`, `GIT_REST_URL=http://vault-obsidian-personal:9090`), observed 2026-10-19 against `bborbe/obsidian-personal`. The `dark-factory --version` field is not applicable — the defect is in this service, not in the pipeline.

**Observed on prod (the symptom).**

1. A task file was renamed with `notesmd-cli move` (an `os.Rename` plus a byte-level link rewrite; it cannot alter frontmatter).
2. `git diff` of the file before and after the rename showed `task_identifier` unchanged.
3. About one minute later — inside the 60s `POLL_INTERVAL` — commit `c4ed9cd4ea` (`git-rest: update 25 Tasks/<file>.md`) changed the file by exactly two lines: `+task_identifier: 15ed9d6a-…` prepended at frontmatter line 1, and `-task_identifier: 5d83b642-…` removed from its sorted position. Every other key was preserved.
4. The same shape appears in the older commit `e3dfe7e974` (`d16c1479…` → `8d81ac42…`), which is how the defect was first noticed.
5. Population check: exactly **1** genuine re-mint in the last 200 `git-rest:` commits touching `25 Tasks/` — the one above. The defect fires on rename, not on every write.

**Locally runnable reproduction (the same mechanism, deterministic).**

1. Build a scanner whose bookkeeping holds one entry, `tasks/old.md` → identifier `X`, and whose file operations list only `tasks/new.md` (the post-rename directory state).
2. Make `tasks/new.md` carry `task_identifier: X` in its frontmatter.
3. Drive one scan cycle.
4. **Pre-fix observation:** `tasks/new.md` is written, its `task_identifier` is a value **≠ X**, and the injector recorded one call.
5. **Post-fix observation:** `tasks/new.md` is not written at all (its hash is unchanged), and its `task_identifier` is still `X`.

## Expected vs Actual

| | Behavior |
|---|---|
| **Expected** | A rename moves a task file without changing its identity. The uniqueness check answers the question its name asks — "does another *existing* file carry this identifier?" — so the renamed file, whose old path no longer exists, is not a duplicate. |
| **Actual** | The check answers "did any path I have ever seen carry this identifier?", so the renamed file collides with its own former path, and the scanner resolves the phantom collision by re-minting the identifier. |

## Why this is a bug

The scanner's own contract is to repair *genuinely* broken identifiers: a missing one, a non-UUID one, or one duplicated across two live files. A rename is none of those — the identifier is present, valid, and unique among existing files. The duplicate branch exists to prevent a result landing on the wrong task file; applied to a rename it does the opposite of its purpose, detaching a task from the references that name it.

The pruning code already encodes the correct intent — it deletes entries for paths absent from the current listing, and the published-index doc comment states that a path the cycle deleted is already gone from the bookkeeping. The defect is purely ordering: the deletion happens after the code that needed it to have happened.

## Workaround

None available to the operator. The re-mint is automatic and silent, and it is not detectable afterwards without comparing git history — which is how it was found. Setting `AUTO_INJECT_TASK_IDENTIFIER=false` stops the re-mint but also stops the legitimate repair of missing and invalid identifiers, and makes the scanner skip those files entirely, so it is not a usable mitigation.

## Goal

After this work:

- Renaming a task file leaves its `task_identifier` byte-identical.
- A genuine duplicate — two **existing** files carrying the same identifier — is still repaired by re-minting, exactly as today.
- The change composes with the identifier→path index from spec 016: after a rename the index holds the new path and not the old one.

## Non-goals

- Repairing references already orphaned by past re-mints.
- Changing how identifiers are generated for new tasks.
- The identifier stripper's prefix match, which also removes a **body** line beginning `task_identifier:` — a separate defect, noted here only so it is not mistaken for part of this fix.
- Any change to the identifier→path index introduced by spec 016 beyond keeping it correct across a rename.

## Acceptance Criteria

- [ ] A rename does not re-mint. Evidence: `go test ./pkg/scanner/... -run TestSuite -ginkgo.focus='RenameKeepsIdentifier'` exits **non-zero before the fix** and 0 after it; the test seeds `tasks/old.md → X`, lists only `tasks/new.md`, and asserts the file still carries `X` with 0 writes recorded.
- [ ] A genuine duplicate is still repaired. Evidence: `go test ./pkg/scanner/... -run TestSuite -ginkgo.focus='GenuineDuplicateStillReMints'` exits 0; both paths are listed and carry `X`, and the test asserts exactly one still carries `X`, the other a new UUID, with 1 write.
- [ ] A missing identifier is still injected. Evidence: the existing `empty-id.md` case passes unchanged in the same `go test ./pkg/scanner/...` run.
- [ ] A non-UUID identifier is still replaced. Evidence: the existing `non-uuid.md` case passes unchanged in the same run.
- [ ] The package suite is green. Evidence: `go test -count=1 ./pkg/scanner/...` exits 0.
- [ ] **Post-Deploy (Rung-3):** renaming a task in a watched vault leaves its identifier unchanged.
  - deploy_target: `v0.11.8`
  - deploy_check: `kubectlnukeprod -n prod get sts/agent-task-controller-personal -o jsonpath='{.spec.template.spec.containers[0].image}' | awk -F: '{print $NF}'`
  - Evidence: rename a throwaway task in a vault watched by `agent-task-controller-personal`, wait one `POLL_INTERVAL` (60s) plus a scan cycle, then `git log --follow -p -1 -- "25 Tasks/<renamed file>.md"` shows no `task_identifier` delta across the rename, and `grep '^task_identifier:'` before and after print the same value. `deploy_target` is the expected next patch release; confirm it against the tag the merge actually produces before running `/dark-factory:verify-spec`.

## Verification

Rationale: AC1 and AC2 are deliberately opposite — AC1 would pass trivially if the duplicate branch were deleted, and AC2 would pass on today's broken build. Together they pin the behaviour to "duplicate means two existing files", which is the actual fix. AC3 and AC4 are over-correction guards and must stay green.

```bash
# package tests, including the two new cases and the existing regression guards
go test -count=1 ./pkg/scanner/...

# full gate
make precommit

# live check (AC6), operator-run, after the fix is deployed
grep '^task_identifier:' "25 Tasks/<renamed file>.md"
git log -p -1 -- "25 Tasks/<renamed file>.md"
```

## Desired Behavior

1. Renaming a task file leaves its `task_identifier` byte-identical after a full scan cycle.
2. Two existing files carrying the same identifier still result in exactly one of them being re-minted.
3. A file with no identifier still receives one when `AUTO_INJECT_TASK_IDENTIFIER=true`.
4. A file with a non-UUID identifier still has it replaced.
5. The set of identifiers reported deleted per cycle is unchanged from today's behaviour.
6. The published identifier→path index holds the renamed file's new path and not its old one.

## Constraints

- The uniqueness check's signature must not change: it has one production caller, and the scanner's internal tests call the per-file processor directly at seven sites, so widening the call chain is a larger diff than the defect warrants. Pruning at the scan level keeps every signature intact.
- The deletion list reported downstream must be unchanged: the same identifiers must be reported deleted for the same cycles, only computed earlier.
- The index publication must still be called exactly once per cycle, after the walk.

## Assumptions

- The file listing returns the complete set of task paths for the cycle, so a path absent from it is genuinely gone. This is the same assumption the deletion collector already makes when it prunes.
- A rename is observed as "old path absent, new path present" within a single cycle, or across two — either way the old path is absent from the listing that triggers the check.

## Failure Modes

| Trigger | Expected | Recovery |
|---|---|---|
| The file listing returns a partial set (transient git-rest error) | Live paths are pruned, then re-added as new on the next cycle | Pre-existing risk in the deletion collector; this change does not widen it, because it uses the same set. Named in the PR body. |
| A genuine duplicate where one path is briefly absent | The surviving file keeps `X`; the other is re-minted on the next cycle when both are present | None needed: the end state matches today's behaviour once both are visible. |
| The spec-016 index lags a rename by one cycle | A reader resolving the identifier may miss for one cycle | Bounded by `pollInterval`; spec 016's fallback full walk still resolves it. |

## Security / Abuse Cases

No new input surface: the change reorders two existing operations over the same data. The scanner still writes only within the task directory, and the duplicate branch's write is unchanged in shape.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Prune vanished paths before the scan loop, then add the rename-preserved and genuine-duplicate scanner tests | DB1-6 | AC1-6 | — |

## Do-Nothing Option

Not acceptable. The re-mint is silent and recurring: every rename of a task file is a permanent identity loss for that task, with no error, no metric and no way to notice without reading git history. It has already hit at least two tasks, one of them twice, and it corrupts precisely the identity field the rest of the fleet keys on.
