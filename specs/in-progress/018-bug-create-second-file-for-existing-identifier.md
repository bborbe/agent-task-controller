---
status: verifying
tags:
    - dark-factory
    - spec
approved: "2026-10-27T21:45:09Z"
generating: "2026-10-27T21:48:53Z"
prompted: "2026-10-27T21:55:52Z"
verifying: "2026-10-27T22:10:49Z"
branch: dark-factory/bug-create-second-file-for-existing-identifier
---

## Summary

- The task record store is keyed on the **title**; the re-drive streak store is keyed on the **task_identifier**. The create path never consults the identifier, so the two keys can disagree.
- A deterministic emitter key emitted twice under two different titles lands on two free title paths and leaves **two files sharing one identifier**. Those two then share one streak counter, so one record's three failures cap the key and refuse a sibling that never failed.
- Measured 2026-10-27 in the prod task store: **16 identifiers across 35 files**. All five stuck `github-update-go` records were members of such a group.
- The fix: before creating, resolve the identifier against the existing identifier→path index and, on a hit, send the create under **the title of the file that already carries it** — so the server sees the occupied path and applies its own re-publish/reopen semantics instead of writing a second file.

## Problem

Two facts combine badly.

First, the identifier is **deterministic by design**. `github-update-go-watcher` mints `uuid5(ns, "update-go-<owner>-<repo>-<goVersion>-<headSHA>")` (`pkg/taskid.go:16,26-29`) and `github-pr-watcher` mints `uuid5(ns, "<owner>/<repo>#<number>@<sha>")` (`pkg/taskid.go:15,21-22`). Both are *meant* to reproduce on a re-emit — that is their documented dedup contract.

Second, the record is **not keyed on that identifier**. `NewCreateTaskExecutor` validates the command, computes a filename stem from the command's title (`createTaskTitle`, `pkg/command/task_create_task_executor.go:129-145`), posts it, and consults the resolver only *afterwards*, to record what it wrote (`:116`, `recordCreatedTaskPath`, `:270-283`). Nothing reads the identifier before creating.

So a re-emit of the same key under a different title finds a **free** title path and creates a second file. Two triggers, both present in the live store:

- **Retitled PR.** The `pr-review` key excludes the PR title, but the filename is the PR title slug, so a retitle re-emits the same identifier under a new filename — e.g. `… - chore-pin-vault-api-v0-12-1.md` and `… - chore-pin-vault-api-v0-13-0.md`, both `09f1f349-bf09-5610-9cf2-bb79cf0383be`.
- **Diverged title SHA.** An `Update Go` record's title carries the SHA seen at emit time while its `ref` frontmatter is rewritten afterwards by the updater — visible in `Update Go bborbe-sentry-proxy 99f450e - 6f923904.md`, whose title says `99f450e` and whose `ref` says `8657c89…`. A later emit therefore produces the same identifier with a different title SHA.

The consequence is invisible in the vault: both files stay valid, both keep their titles, and only the identity is shared. Every consumer keyed on the identifier silently conflates them — the re-drive streak store above all, which is why one record's failures refuse its siblings.

## Reproduction

**Observed on prod (the symptom).** A frontmatter scan of the prod task store on 2026-10-27 found **16 duplicated identifiers across 35 files**; 11 of the 16 groups share a single `ref` and 5 span two refs. The five stuck `github-update-go` records (`2c0c458a`, `3fb0dc91`, `6f923904`, `9371a3d2`, `d2a25e5e`) were each a member of such a group. The duplicated set has since been migrated to unique identifiers, so the live store no longer exhibits the symptom — the *mechanism* is unchanged and reproduces locally.

**Locally runnable reproduction (deterministic).**

1. Build the create executor with a fake `TaskPathResolver` whose `Resolve` returns `("tasks/Existing Title.md", true, nil)`.
2. Send a `CreateCommand` with `TaskIdentifier: X` and `Title: "A Different Title"`.
3. **Pre-fix observation:** the fake vault records a create for `"A Different Title"`; the identifier `X` now has two files.
4. **Post-fix observation:** the fake vault records a create for `"Existing Title"` — the resolved file's stem — so the server's path-based conflict logic fires and the executor returns the benign already-exists outcome. Exactly one file carries `X`.

## Expected vs Actual

| | Behavior |
|---|---|
| **Expected** | A `task_identifier` maps to at most one record file. A re-emit of the same key — whatever title it arrives under — updates or is refused by the existing record, never creating a second one. |
| **Actual** | The identifier is used only to record a path after the write, never to find one before it. A re-emit under a new title creates a second file, and every identifier-keyed consumer then conflates two records. |

## Why this is a bug

The identifier is the identity of a task: the streak store, the escalation dedup key and the manager's per-task card all key on it. A store that permits two files to carry one identity makes every one of those consumers wrong without reporting an error. The failure is silent by construction — the files are individually valid and no write fails.

## Workaround

None needed for the live store: the existing collisions were migrated to unique identifiers on 2026-10-27 (16 → 0). The migration does not stop recurrence, which is what this spec fixes.

## Goal

A `task_identifier` resolves to at most one record file, so no two records can share a streak counter — enforced on the create path rather than by cleaning up afterwards.

## Non-goals

- **The re-drive streak store's release path.** An already-capped key stays capped; releasing it is a store delete owned by a separate item.
- **The emitters.** Both derivations are correct and deterministic; this spec does not change a namespace, a seed or a title format.
- **The one-off migration.** Already done; this spec prevents recurrence, it does not re-run it.
- **vault-api's path-based conflict logic.** The server keeps owning the filename and the 409; the controller only decides which title to ask for.
- **Re-introducing the pre-check removed for vault-api#28.** See Constraints.

## Acceptance Criteria

- [ ] **A re-emit under a different title does not create a second file.** Evidence: a new case `ExistingIdentifierTitleIsReused` passes — resolver returns `("tasks/Existing Title.md", true, nil)`, command carries `Title: "A Different Title"` and identifier `X`; the test asserts the create was posted with stem `Existing Title`, that no create was posted with `A Different Title`, and that the executor returns the already-exists outcome.
- [ ] **A genuine miss still creates.** Evidence: the existing clean-miss case passes unchanged in the same `go test` run (resolver returns `("", false, nil)` → create posted with the command's own title).
- [ ] **A resolver error never redirects.** Evidence: a case with `Resolve` returning `("", false, errors.New("duplicate"))` asserts the create is posted with the command's own title — a known-duplicate index entry must not be used to choose a path.
- [ ] **A nil resolver is unchanged.** Evidence: the existing nil-resolver cases pass unchanged.
- [ ] **Two identifiers on one title still disambiguate.** Evidence: the existing different-identifier-collision case passes unchanged — the redirect must not suppress the ` - <id[:8]>` fallback.
- [ ] **The package suite is green.** Evidence: `go test -count=1 ./pkg/command/...` exits 0.
- [ ] **The full gate is green.** Evidence: `make precommit` exits 0.
- [ ] **Post-Deploy (Rung-3):** two emits of one identifier under two titles leave one file.
  - deploy_target: `v0.22.1`
  - deploy_check: `kubectlnukeprod -n prod get sts/agent-task-controller-personal -o jsonpath='{.spec.template.spec.containers[0].image}'`
  - Evidence: re-emit an existing `pr-review` task under an edited title, wait one `POLL_INTERVAL` plus a scan cycle, then confirm the vault holds one file for that `task_identifier` (`grep -rl 'task_identifier: <id>' "25 Tasks"` prints one path) and `git log --oneline -5` shows no second create commit. `deploy_target` is the expected patch release from `v0.22.0`; confirm it against the tag the merge actually produces before running `/dark-factory:verify-spec`.

## Verification

Rationale: AC1 and AC2 are deliberately opposite — AC1 would pass trivially if the redirect always fired, and AC2 would pass on today's build. Together they pin the behaviour to "redirect only on a clean hit". AC3 and AC4 are over-correction guards for the two ways the resolver can fail to be authoritative, and AC5 guards the fallback the redirect could suppress.

**Rung 1 — container-executable (the pipeline runs these):**

```bash
# package tests, including the new case and the existing guards
go test -count=1 ./pkg/command/...

# full gate
make precommit
```

**Rung 3 — operator-executable (after the fix is deployed, AC8):**

```bash
grep -rl 'task_identifier: <id>' "25 Tasks"
git log --oneline -5 -- "25 Tasks"
```

## Desired Behavior

1. Before posting a create, the executor resolves `cmd.TaskIdentifier` against the identifier→path index.
2. On a clean hit, the create is posted under the title stem of the resolved file, not the command's title.
3. On a miss, the create is posted under the command's title, exactly as today.
4. On a resolver error (more than one path for the identifier), the create is posted under the command's title and the ambiguity is logged; the create is never redirected by an ambiguous entry.
5. A nil resolver leaves behaviour byte-identical to today.
6. The redirect returns a **title stem**, never a path, so the write continues to land in `TASK_DIR`.

## Constraints

- **Must not reintroduce the pre-check removed for vault-api#28.** `task_create_task_executor.go:150-153` records that an identifier pre-check was removed because it keyed on the index's shared identifier/name/slug space, letting a page elsewhere shadow a live task and destroy it. This change is narrower by construction and must stay so: it reads the **identifier→path index only** (which holds task files alone, never arbitrary pages), and it returns a **title stem** rather than a path, so the write can only ever address the file that carries this very identifier.
- The resolver is already a constructor parameter and may be nil (tests, and any wiring that has no index); the change must not make it mandatory.
- The server remains the authority on the filename: it still answers with the path title it actually used, and the resolver is still recorded from that answer.
- No signature change to `createThroughAPI` or `createTaskTitle`.
- The redirect must not suppress the existing ` - <id[:8]>` disambiguation for two *different* identifiers colliding on one title.

## Assumptions

- The identifier→path index is populated from a completed scan cycle or the boot build, and `Record` keeps it current for files this controller creates. **It is not self-freshening: a stale entry costs a wrong redirect, not a missed one** (see the required guard under Security / Abuse Cases).
- The identifier→path index contains task files only. If it were ever widened to arbitrary pages, the vault-api#28 class returns and this spec must be revisited.
- The emitters keep minting deterministic identifiers; a random identifier would make the redirect a no-op and the defect moot, which is not the direction chosen.

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---|---|---|
| The index points at a path that no longer exists | The create posts under that stem and succeeds as a fresh file — the same outcome as today, no worse | None needed; the next scan cycle drops the vanished path. If it persists, restart the controller so the boot build rebuilds the index, then `grep -rl 'task_identifier: <id>' "$TASK_DIR"` must print one path |
| The index points at a file in another vault, or at a path whose file now carries a **different** identifier (stale entry) | **Without the required guard this overwrites that file's terminal record** — the redirect posts under its title and the server reopens any terminal occupant regardless of identifier | Add the guard (read the resolved file, redirect only when its `task_identifier` still equals the command's); if a record was replaced, restore it from git history |
| The redirect fires on a legitimately new task | Only reachable if an emitter assigned one identifier to two distinct tasks — the defect this spec removes | File the emitter defect separately; the record store now holds one file for the identifier, so no data is lost |
| The resolver is unavailable at boot | Miss path; behaviour identical to today | None; the index fills on the first completed scan cycle or the boot build |
| A resolver error (two paths for one identifier) | The create posts under the command's own title and the ambiguity is logged | Run the one-off migration to re-mint the duplicates, then confirm `grep -rl 'task_identifier: <id>' "$TASK_DIR"` prints one path |
| Two emits of one identifier arrive simultaneously under different titles | Both miss the index, because neither file exists yet to be recorded, and both create — this spec does **not** close the simultaneous-first-emit race | The second create is refused only when the two titles match; if two files appear, run the one-off migration and file the timing as a separate defect |

## Security / Abuse Cases

- A poisoned index entry could attempt to steer a create onto a chosen title. **The first version of this spec claimed that was bounded to a filename and could never overwrite another task. That claim was false and is retracted (bot review, 2026-10-27).** `postCreate` sends `ReopenTerminal: !isRecurringTaskCommand(cmd)`, and the vault API replaces **any** terminal occupant regardless of the identifier it carries — so a stale entry (`idX → T.md` where `T.md` has since been rewritten to carry `idA`) posts under `T`'s title and **replaces idA's terminal record**. A live occupant with a different identifier is not an already-exists outcome either: it falls through to ` - <id[:8]>` and creates a new file titled from the other task. The index is not self-freshening — `Record` only adds, and `buildIndex` replaces the map only on the next poll, never in a `POLL_INTERVAL=0s` deployment.
- **Required guard, not yet implemented:** on a resolver hit, read the resolved file and redirect **only if its `task_identifier` still equals `cmd.TaskIdentifier`**; otherwise keep the command's own title and log the mismatch. That closes both the stale-index overwrite and the wrong-title disambiguation with one read, and it must land with a test that redirects onto a terminal occupant of a *different* identifier and asserts no write.
- No new path is constructed from the index — the redirect returns a `filepath.Base` stem with `.md` trimmed, so a `..` or separator cannot enter the path from the resolver side. `createTaskTitle`'s existing separator rejection is unchanged.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Add `reuseExistingIdentifierTitle` (resolver → title stem, with miss / resolver-error / nil passthrough) and call it on the result of `createTaskTitle` before `createThroughAPI`; add `ExistingIdentifierTitleIsReused` plus the resolver-error and nil-resolver guards | DB1–DB6 | AC1–AC7 | — |

Single layer, single file pair (`pkg/command/task_create_task_executor.go`, `pkg/command/task_create_task_executor_test.go`), no wiring change, no new dependency. AC8 is the post-deploy gate and is not covered by a prompt.

## Do-Nothing Option

Doing nothing leaves the create path title-keyed. New duplicates recur on every retitled PR and every diverged title SHA, and each recurrence silently re-conflates two records in the streak store. The one-off migration bought a clean store, not immunity: the next retitle recreates the defect, and the next operator-visible symptom will again be a record refused by a sibling's failures rather than by its own.
