---
status: draft
kind: bug
---

## Summary

- The scanner repairs a `task_identifier` that is present but not a valid UUID by minting a fresh UUID over it. That repair is correct and stays: it closed a 50-hour rewrite loop (spec 008).
- The repair silently changes the task's identity while an agent run may already be in flight under the old value, so the result is published under an identifier that no longer resolves to any file.
- The controller logs `task file not found … after 3 attempts, skipping`, increments `not_found`, and returns success. The task keeps no record that its result was dropped.
- The fix records the replaced value in the file as `task_identifier_previous` and matches that key in the result walk, so an in-flight result still lands.

## Problem

The executor spawns a Job keyed by the identifier it read from the task event. The controller later resolves that same identifier back to a task file. The scanner's repair path sits between the two and rewrites the identifier without telling either side, so the two ends disagree permanently and the result is written nowhere. An operator sees a task parked in `human_review` with no `## Result` and a periodic `AgentControllerResultNotFound` alert, and has to reconstruct the mechanism from pod logs — which is exactly what happened on 2026-11-03 and cost hours of diagnosis that looked like a prompt problem.

## Reproduction

Live, nuke dev, `agent-task-controller-personal-0`, namespace `agent`, 2026-11-03:

```
$ kubectlnukedev -n agent logs agent-task-controller-personal-0 --since=20h | grep -c "task file not found"
45
$ kubectlnukedev -n agent logs agent-task-controller-personal-0 --since=20h | grep "task file not found" | tail -1
W1103 18:50:24.123148 1 result_writer.go:577] task file not found for identifier service-analysis-needs-input-probe-missing-path after 3 attempts, skipping
```

The result carries the slug `service-analysis-needs-input-probe-missing-path`. The file it belongs to (`25 Tasks/Service Analysis Needs-Input Probe Missing Path.md`) carries `task_identifier: f86cc502-20e7-4302-902e-837732fdb1e6`. The replacement is the scanner's, at `pkg/scanner/vault_scanner.go:625` (`replacing non-UUID task_identifier %q in %s`).

Deterministic, unit-level: a `vaultScanner` with `autoInject=true` over a file whose frontmatter carries `task_identifier: my-slug` replaces it with a UUID on the first cycle. A `WriteResult` for `my-slug` afterwards finds no file.

## Expected vs Actual

| | Expected | Actual |
|---|---|---|
| A result whose identifier the scanner replaced | Resolves to the file it belongs to; the result is written | `not_found`, retried three times, then skipped with `return nil` |
| The task's own record | Shows the result, or shows that a result was dropped | Unchanged — no `## Result`, no `## Failure` |
| Operator-visible signal | One alert per dropped result, naming the task | One generic `AgentControllerResultNotFound`, naming only the identifier |

## Why this is a bug

`docs/controller-design.md` and the `FindTaskFilePath` contract both treat the identifier as the task's stable address ("There is no path: the identifier is the address", `pkg/vaultapi/client.go:109-111`). The scanner breaks that invariant for one class of file and neither side is told. `WriteResult` then treats the miss as a non-event (`pkg/result/result_writer.go:578-581`, `return nil`), which is correct for a transient git-rest lag and wrong for a permanent identity change — the retry loop exists precisely because the two are indistinguishable at the call site.

## Goal

An agent result always lands on the task that produced it, even when the scanner has since replaced that task's identifier. The replacement itself is unchanged: still one write, still exactly one `task_identifier` key, still a valid UUID (spec 008's contract holds byte-for-byte for every shape it enumerates).

## Acceptance Criteria

- [ ] **AC1 — the scanner records the replaced value.** After one cycle over a file carrying a string-shaped, non-empty, non-UUID `task_identifier`, the written file contains exactly one `task_identifier_previous` key whose value is the replaced identifier verbatim. — evidence: Ginkgo `DescribeTable` in `pkg/scanner/` over each string shape from spec 008's AC2 table (rows 5, 7, 13, 14, 15, 16); per row, `grep -c '^task_identifier_previous:'` on the written bytes returns `1` and the value equals the original.
- [ ] **AC2 — the walk resolves the replaced identifier.** `FindTaskFilePath` returns the file for the *old* identifier when that identifier appears only as `task_identifier_previous`. — evidence: a unit test over a task dir holding one file with `task_identifier: <uuid>` and `task_identifier_previous: my-slug`; `FindTaskFilePath(ctx, …, "my-slug")` returns that path, and `WriteResult` for a task carrying `my-slug` increments `ResultsWrittenTotal{outcome="success"}` and leaves `not_found` unchanged.
- [ ] **AC3 — no alias for shapes that were never in flight (negative).** — evidence: the key-absent path (spec 008 AC3) and every non-string shape (spec 008 AC2 rows 1-4, 8-12) write `grep -c '^task_identifier_previous:'` returning `0`; `grep -c 'replacing invalid task_identifier of type'` over the captured log is unchanged from master.
- [ ] **AC4 — spec 008's contract is unbroken (regression lock).** — evidence: every existing spec-008 test passes unmodified, including the write-count assertions (`== 1` across five cycles), the byte-exact key-absent and frontmatter-preservation assertions (AC3, AC5b), and the source-structure parity test's `skipCount`/`counterCount` values updated in the same commit if the site count moved.
- [ ] **AC5 — the two-key match cannot be satisfied by a stale entry.** An identifier present as `task_identifier_previous` on one file and as `task_identifier` on another is a duplicate, not a preference. — evidence: a unit test with two such files makes `FindTaskFilePath` return an error naming both paths, and `WriteResult` increments `ResultsWrittenTotal{outcome="error"}`, writing nothing.
- [ ] **AC6 — `make precommit` exits 0** at repo root.
- [ ] **AC7 — CHANGELOG entry under `## Unreleased`.** — evidence: `grep -n 'task_identifier_previous' CHANGELOG.md` returns a line between `## Unreleased` and the next `## ` heading; `grep -c '^## Unreleased$' CHANGELOG.md` returns `1`.

**Scenario coverage: NO new scenario.** AC1-AC5 reach every branch of the change through the existing in-package harness. The behaviour is internal to two packages and the deployed path cannot exercise it on dev (see Verification).

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

```
make precommit
go test -v ./pkg/scanner/... ./pkg/result/...
grep -n 'task_identifier_previous' pkg/scanner/vault_scanner.go pkg/result/result_writer.go
grep -n 'task_identifier_previous' CHANGELOG.md
```

### Operator-executable (runs on the host after PR merge)

```
# This repo is STANDALONE (split out of bborbe/agent) and uses the mirrored-semver
# deploy model. Never run `make buca` here.
git checkout <tag> && make build && make upload
cd ~/Documents/workspaces/nuke/agent && BRANCH=dev make apply
kubectlnukedev -n agent get pod agent-task-controller-personal-0 -o jsonpath='{.spec.containers[0].image}'
```

⚠️ **A dev deploy cannot exercise this fix.** `AUTO_INJECT_TASK_IDENTIFIER=false` on nuke dev (verified live 2026-11-03 against the StatefulSet env), so the repair path never runs there and the alias is never written. The fix is inert on dev by construction. Live confirmation requires a prod deploy, which is a separate task — recorded as an open item rather than claimed here.

## Desired Behavior

1. When the scanner replaces a string-shaped, non-empty `task_identifier`, it writes the replaced value into the same frontmatter as `task_identifier_previous`, in the same single write as the new UUID.
2. The replacement itself is byte-for-byte what spec 008 specifies: exactly one `task_identifier` key, a valid UUID, one write, no repeat on the next cycle.
3. When the replaced value was not a non-empty string — an unquoted integer, a sequence, a mapping, a block scalar, or the key absent entirely — no `task_identifier_previous` key is written.
4. `FindTaskFilePath` accepts a file as a match when the requested identifier equals either `task_identifier` or a non-empty `task_identifier_previous`.
5. Two files resolving the same identifier — one by `task_identifier`, one by `task_identifier_previous` — is an error naming both paths, and nothing is written.
6. The identifier→path index is unchanged: it continues to index files by their current `task_identifier` only, so a replaced identifier misses the index and is served by the walk.

## Constraints

- **Spec 008's repair is frozen.** `pkg/scanner/vault_scanner.go:612-638` keeps stripping and minting; the alias is additive. No AC in spec 008 may be edited to accommodate this change.
- **No new skip reason, no new metric.** The alias rides the existing repair path and its existing log line. `pkg/metrics/metrics.go:229-249` is untouched, so the parity test's `skipCount`/`counterCount` do not move.
- **`AUTO_INJECT_TASK_IDENTIFIER` semantics are unchanged.** When `false`, every shape still skips without writing, and no alias is written either.
- **The alias is never written on the key-absent path** — there is no replaced value to record, and spec 008 AC3 asserts that path's bytes exactly.
- **No bulk repair of existing files.** 47 active tasks carry a non-UUID identifier today; the fix makes them resolvable, not rewritten. Rewriting them is a separate decision.
- **`typedTaskFields` allowlist (vault-api) is not touched.** The scanner writes files through git-rest and the controller reads frontmatter directly, so a new key crosses no command API.

## Failure Modes

| Trigger | Expected behavior | Recovery | Detection | Reversibility |
|---|---|---|---|---|
| A result arrives for an identifier replaced twice (slug → uuid A → uuid B) | The alias is overwritten by the second replacement, so only the most recent predecessor resolves; an older in-flight result misses | Operator resolves from the log line naming the identifier | `task file not found … skipping` for that identifier | Reversible — the alias is one frontmatter line |
| A file legitimately carries `task_identifier_previous` from an earlier repair, and a new task is filed with that same value | Both files resolve the identifier → duplicate error, nothing written | Operator clears the stale alias | `duplicate task_identifier … in <a> and <b>` | Reversible |
| The alias line is written but the walk change is not yet deployed (partial rollout) | Result still misses — the alias is inert until the reader ships | Deploy both halves together; they are in one prompt by design | AC2 fails against the deployed pod | Reversible |
| A malformed file is repaired while a result is in flight and the alias write fails | `injectAndStore` returns the write error; nothing is committed; the next cycle retries | Unchanged from today — the next cycle retries | `failed to write %s` warn log | Reversible — nothing was committed |
| YAML drift introduces a shape not in the tables | Routed by the same validity predicate; the alias is written only for non-empty strings, so an unknown scalar shape takes the no-alias path | None needed | Warn log names the Go type | Reversible |

## Security / Abuse

Frontmatter is untrusted input: any agent or operator can author a task file. The alias is a second identifier a file may claim, so it widens the set of identifiers that resolve to a given file. Two consequences are pinned: the duplicate case is an error rather than a preference (AC5), so an alias cannot steal another task's result; and the alias is only ever written by the scanner from a value that was already present in the file, so it adds no write path an attacker did not already control.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Write the alias on the repair path and match it in the walk, with tests, CHANGELOG and `make precommit` | 1-6 | AC1-AC7 | — |

Rationale: a single prompt. The alias is inert without the walk match and the walk match is unreachable without the alias, so splitting them would leave a half-landed state whose ACs cannot pass until both exist. One package boundary each side, both small, and the regression lock (AC4) is only meaningful against the combined diff.

## Do-Nothing Option

Not acceptable, but the cost is bounded and it is worth naming. The loss is silent and per-task: each occurrence parks one task in `human_review` with no result and produces an alert that names only an identifier. The operator's recovery is to read the pod log, discover the identifier mismatch by hand, and re-file the task — roughly the hours this task's Impact records. It does not corrupt the vault and it does not repeat unboundedly for one task, which is why this is a fix and not an incident.
