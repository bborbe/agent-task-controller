---
status: completed
approved: "2026-11-03T20:35:44Z"
generating: "2026-11-04T01:26:38Z"
prompted: "2026-11-04T01:42:19Z"
verifying: "2026-11-04T02:12:13Z"
completed: "2026-11-04T02:12:19Z"
branch: dark-factory/bug-task-identifier-remint-orphans-result
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

`dark-factory --version` is not applicable — this is a service defect in the controller binary, not a dark-factory pipeline defect.

## Expected vs Actual

| | Expected | Actual |
|---|---|---|
| A result whose identifier the scanner replaced | Resolves to the file it belongs to; the result is written | `not_found`, retried three times, then skipped with `return nil` |
| The task's own record | Shows the result, or shows that a result was dropped | Unchanged — no `## Result`, no `## Failure` |
| Operator-visible signal | One alert per dropped result, naming the task | One generic `AgentControllerResultNotFound`, naming only the identifier |

## Why this is a bug

`docs/controller-design.md` and the `FindTaskFilePath` contract both treat the identifier as the task's stable address ("There is no path: the identifier is the address", `pkg/vaultapi/client.go:109-111`). The scanner breaks that invariant for one class of file and neither side is told. `WriteResult` then treats the miss as a non-event (`pkg/result/result_writer.go:578-581`, `return nil`), which is correct for a transient git-rest lag and wrong for a permanent identity change — the retry loop exists precisely because the two are indistinguishable at the call site.

## Goal

An agent result always lands on the task that produced it, even when the scanner has since replaced that task's identifier. The replacement itself is unchanged: still one write, still exactly one `task_identifier` key, still a valid UUID (spec 008's contract holds for every shape it enumerates).

## Non-goals

- Rewriting the 47 active task files that already carry a non-UUID identifier. The fix makes them resolvable; it does not re-mint them.
- Preserving more than one predecessor. A second repair overwrites the alias, so only the most recent replaced value resolves.
- Changing `WriteResult`'s retry/back-off, its `unowned` routing, or the `not_found` threshold.
- A live prod replay of the full in-flight-result path. The Rung-3 AC covers the alias write; the resolution half is covered by AC2, and a live end-to-end replay is the follow-up named in Progress.

## Acceptance Criteria

- [ ] **AC1 — the scanner records the replaced value.** After one cycle over a file carrying a non-blank, single-line, non-UUID `task_identifier`, the written file contains exactly one `task_identifier_previous` key that round-trips to the original string. — evidence: Ginkgo `DescribeTable` in `pkg/scanner/` over two rows — `task_identifier: "501"` (spec 008 AC2 row 13, numeric-looking and therefore a quoting case) and `task_identifier: my-slug` (the live incident shape). Per row, on the written bytes: `grep -c '^task_identifier_previous:'` returns `1`; `yaml.Unmarshal` of the frontmatter yields `task_identifier_previous` equal to the original value; `repairConverges` returns true (the spec-009 guard still passes).
- [ ] **AC2 — the walk resolves the replaced identifier.** `FindTaskFilePath` returns the file for the *old* identifier when that identifier appears only as `task_identifier_previous`. — evidence: a unit test over a task dir holding one file with `task_identifier: <uuid>`, `task_identifier_previous: my-slug`, `target_vault: personal`; `FindTaskFilePath(ctx, …, "my-slug")` returns that path; a `WriteResult` for a task carrying `my-slug` increments `ResultsWrittenTotal{outcome="success"}` and leaves both `not_found` and `unowned` unchanged.
- [ ] **AC3 — no alias for values that were never in flight (negative).** — evidence: over spec 008's AC2 rows 1-12 and 14-16 (every row except the parity row 13) plus the key-absent fixture, `grep -c '^task_identifier_previous:'` on the written bytes returns `0` for every row; and `grep -c 'replacing invalid task_identifier of type'` over the captured log for rows 1-4, 8-11 and 14-16 — the **eleven** non-string shapes; row 12 is a block scalar, a Go `string`, and logs `replacing non-UUID task_identifier` instead — equals `11`.
- [ ] **AC4 — the duplicate branch writes no alias.** A file whose `task_identifier` is a valid UUID that duplicates another file's is repaired without an alias. — evidence: the duplicate fixture's written bytes have `grep -c '^task_identifier_previous:'` returning `0`; the existing duplicate-repair log substring is unchanged.
- [ ] **AC5 — spec 008's assertions pass unmodified.** — evidence: every existing spec-008 test passes with no edit, including the five-cycle write-count assertions (`== 1`), the byte-exact key-absent assertion (AC3), the byte-exact frontmatter-preservation assertion (AC5b), and the source-structure parity test. **AC5b is unaffected by construction**: its fixture (`pkg/scanner/vault_scanner_internal_test.go`, the exact-byte assertion) uses the unquoted integer `501`, which AC3 row 1 excludes from aliasing, so its written bytes gain no `task_identifier_previous` line. No spec-008 assertion is edited by this change.
- [ ] **AC6 — a repeated repair leaves exactly one alias.** Two successive repairs of one file leave one `task_identifier_previous` key, holding the most recent predecessor. — evidence: a fixture written with `task_identifier: slug-a`, cycled once; then rewrite **only the `task_identifier` line** to `slug-b`, leaving the `task_identifier_previous: slug-a` line in place, and cycle again. The final bytes have `grep -c '^task_identifier_previous:'` returning `1` with the value `slug-b`. Rewriting the whole file instead would pass this AC without ever exercising the replace-not-append rule.
- [ ] **AC7 — a second file claiming the identifier is an error, not a preference.** An identifier present as `task_identifier` on one file and as `task_identifier_previous` on another makes the lookup fail. — evidence: `FindTaskFilePath` returns an error naming both paths; `WriteResult` returns that error and the write double records zero calls (no metric increment is asserted — the code does not increment one on this path, and this change adds none).
- [ ] **AC8 — an alias match is never recorded in the index.** — evidence: after `FindTaskFilePath(ctx, …, "my-slug")` resolves via `task_identifier_previous`, the resolver double's `Record` call count for `my-slug` is `0`.
- [ ] **AC9 — `make precommit` exits 0** at repo root.
- [ ] **AC10 — CHANGELOG entry under `## Unreleased`.** — evidence: `## Unreleased` is created at the top (the file's first heading today is `## v0.26.1`); `awk '/^## v/{exit} /task_identifier_previous/{print}' CHANGELOG.md` prints the bullet, so it is positioned inside `## Unreleased`; `grep -c '^## Unreleased$' CHANGELOG.md` returns `1`.
- [ ] **AC11 — the design doc records the amended address rule.** — evidence: `grep -c 'task_identifier_previous' docs/controller-design.md` returns `≥1` **and** `grep -c 'or its recorded predecessor' docs/controller-design.md` returns `≥1` — the doc must carry that exact phrase, so the assertion is a literal, not a paraphrase.
- [ ] **Post-Deploy (Rung-3):** the alias is written on nuke **prod**, the only stage with the repair path enabled.
  - `deploy_check:` `kubectlnukeprod -n prod get pod agent-task-controller-personal-0 -o jsonpath='{.spec.containers[0].image}'`
  - `deploy_target:` `v0.26.3` — a **literal**, edited by the verifying operator to the tag this change actually releases. Never a command substitution. (This repo bumps patch for fixes. `v0.26.2` was cut on master for the unrelated metrics-recompute fix while this spec sat approved, so this one lands as `v0.26.3`.)
  - Probe: in the vault worktree create `25 Tasks/_test-probe-remint-<ts>.md` with `page_type: task`, `status: in_progress`, `assignee: ""`, `target_vault: personal`, and the slug `task_identifier: probe-<ts>`; commit and push. Wait 180 s (prod is event-driven, `pollInterval: "0s"` — there is no poll cycle to count). Then `git pull` **before** any grep, and capture: the frontmatter parses and carries a valid UUID under `task_identifier` **and** `task_identifier_previous` equal to `probe-<ts>` (assert via `yaml.Unmarshal`, not a line grep — the value may be quoted). Only then remove the probe file, commit and push.
  - Scope note: this AC proves the **write** half on the deployed binary. The resolution half is proven by AC2 at unit level; a live end-to-end replay needs a job spawned under a slug, which this AC deliberately does not buy.
  - Abort condition: if the alias key is absent or `task_identifier` is not a UUID, the fix did not land — delete the probe file and mark this AC failed. Do not leave the probe running.

**Scenario coverage: NO new scenario.** AC1-AC8 reach every branch of the change through the existing in-package harness. The deployed path is covered by the Rung-3 AC.

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

```
make precommit
go test -v ./pkg/scanner/... ./pkg/result/...
grep -rn 'task_identifier_previous' pkg/
grep -n 'task_identifier_previous' CHANGELOG.md docs/controller-design.md
```

### Operator-executable (runs on the host after PR merge)

```
# This repo is STANDALONE (split out of bborbe/agent) and uses the mirrored-semver
# deploy model. Never run `make buca` here.

# 1. Cut the release tag by hand (autoRelease is false), then publish the image —
#    ci.yml has no docker step, so the tag alone ships nothing.
git checkout <tag> && make build && make upload

# 2. Bump the image tag in SEVEN places in nuke/agent/: the MIRROR_IMAGES entry in
#    Makefile, plus SIX controllers[].image.tag entries — openclaw, personal AND
#    trading, in values-dev.yaml AND values-prod.yaml. Missing the trading controller
#    leaves one replica on the old tag; it runs autoInjectTaskIdentifier: "true" on
#    prod, which is exactly the rollout skew Failure Modes row 4 describes.
#    Confirm with a scoped check — NOT `grep -rn 'v<old>' Makefile`: MIRROR_IMAGES is
#    append-only history and still lists every past tag, so that grep never returns empty.
grep -c 'tag: v<old>' values-dev.yaml values-prod.yaml   # must be 0 for each file
grep -c 'agent-task-controller:v<new>' Makefile           # must be 1

# 3. Apply. `make apply` alone redeploys the OLD tag — step 2 is what makes it new.
cd ~/Documents/workspaces/nuke/agent && BRANCH=prod make apply
kubectlnukeprod -n prod rollout status sts/agent-task-controller-personal
kubectlnukeprod -n prod get pod agent-task-controller-personal-0 -o jsonpath='{.spec.containers[0].image}'
```

⚠️ **A dev deploy cannot exercise this fix.** `AUTO_INJECT_TASK_IDENTIFIER=false` on nuke dev (verified live 2026-11-03 against the StatefulSet env), so the repair path never runs there and the alias is never written. The Rung-3 AC targets prod for that reason. The prod apply is operator-only (`hooks/deny-prod-deploy.sh` refuses it to an agent).

## Desired Behavior

1. When the scanner replaces a `task_identifier` that is a string, non-blank after trimming, single-line, and not a valid UUID, it writes the replaced value into the same frontmatter as `task_identifier_previous`, in the same single write as the new UUID.
2. The alias value is written as a YAML scalar that round-trips to the exact original string — quoted when the value is not a safe plain scalar, including numeric-looking strings such as `501`, so it does not round-trip as an integer — so a value containing `#`, `: `, or leading/trailing spaces survives unchanged and `repairConverges` still accepts the write.
3. A file carries at most one `task_identifier_previous` key. A second repair replaces its value; it never appends a second key.
4. The replacement itself is unchanged from spec 008: exactly one `task_identifier` key, a valid UUID, one write, no repeat on the next cycle.
5. No alias is written when the replaced value was not a non-blank single-line string, when the key was absent, or when the repair was the duplicate branch's (a valid UUID) — a duplicated UUID is already indexed, so an alias would let the index resolve it to the wrong file before the walk's duplicate check could run.
6. `FindTaskFilePath` accepts a file as a match when the requested identifier equals either `task_identifier` or a non-empty `task_identifier_previous`.
7. An alias match is never recorded in the identifier→path index: the index stays keyed on the current `task_identifier` only, and `resolveFromIndex`'s existing "no longer carries" guard keeps rejecting an alias hit, so recording one would only add a dead entry and a full walk per lookup.
8. On the walk path, two files resolving the same identifier — one by `task_identifier`, one by `task_identifier_previous` — is an error naming both paths, and nothing is written. (The index-hit path is unchanged: an identifier the index holds under `task_identifier` resolves directly to that file and never reaches the walk, which is the pre-existing behaviour and out of scope here.)

## Constraints

- **Spec 008's repair is frozen.** `pkg/scanner/vault_scanner.go:612-638` keeps stripping and minting; the alias is additive. No spec-008 assertion is edited.
- **No new skip reason, no new metric.** The alias rides the existing repair path and its existing log line. `pkg/metrics/metrics.go:229-249` is untouched, so the parity test's `skipCount`/`counterCount` do not move.
- **`AUTO_INJECT_TASK_IDENTIFIER` semantics are unchanged.** When `false`, every shape still skips without writing, and no alias is written either.
- **The alias is never written on the key-absent path** — there is no replaced value to record, and spec 008 AC3 asserts that path's bytes exactly.
- **The duplicate branch writes no alias** — a valid UUID is indexed, so an alias there would resolve through `resolveFromIndex` to the other file and bypass the duplicate guard (`pkg/result/result_writer.go:400`).
- **No bulk repair of existing files.** 47 active tasks carry a non-UUID identifier today; the fix makes them resolvable, not rewritten.
- **`typedTaskFields` allowlist (vault-api) is not touched.** The scanner writes files through git-rest and the controller reads frontmatter directly, so a new key crosses no command API. Verified: no `DisallowUnknownFields` in this repo's parsing, and `vault-cli`'s lint inspects `task_identifier` only, so the new key trips no existing rule.
- **The alias helper's location is not frozen** — it may live in `pkg/scanner/task_identifier.go` beside `InjectTaskIdentifier` rather than in `vault_scanner.go`.

## Design Decisions

- **A durable frontmatter alias, not an in-memory map.** The identifier→path index is in-process, rebuilt from file content every cycle and never persisted (`docs/controller-design.md:31`), so an alias recorded there is wiped on the next cycle. The alias has to live where it survives a restart, and the task file is the only durable place the scanner already writes.
- **Keep the repair, do not pause it.** Pausing repair while a job is in flight requires the scanner to know about jobs — it has no cluster client, and the executor's job lifecycle is not visible to it. Rejected as a new coupling.
- **Refuse to repair a file with a live job.** Same reason, plus it would reintroduce the unbounded-rewrite risk for that file. Rejected.
- **Frontmatter key, not a body section.** A body section would need the walk to parse markdown rather than frontmatter, and the walk already parses frontmatter. The key name was chosen so spec 008's own key-count regex `^[[:space:]]*['\"]?task_identifier['\"]?[[:space:]]*:` does not match it.

## Failure Modes

| Trigger | Expected behavior | Recovery | Detection | Reversibility |
|---|---|---|---|---|
| A result arrives for an identifier replaced twice (slug → uuid A → uuid B) | The alias holds only `uuid A` (the most recent predecessor), so the older slug does not resolve | Operator edits the file's `task_identifier_previous` back to the missing slug, confirms `grep -c '^task_identifier_previous:'` returns `1`, and re-publishes the result | `task file not found … skipping` naming the slug | Reversible — the alias is one frontmatter line |
| A file legitimately carries `task_identifier_previous` from an earlier repair, and a new task is filed with that same value | Both files resolve the identifier → duplicate error, nothing written | Operator deletes the `task_identifier_previous` line, confirms `grep -c '^task_identifier_previous:'` returns `0`, then re-runs the result | `duplicate task_identifier … in <a> and <b>` | Reversible |
| The alias value contains characters that make it an unsafe plain YAML scalar | The value is quoted so it round-trips; `repairConverges` still accepts the write | None needed | AC1's `yaml.Unmarshal` equality assertion | Reversible |
| Two controllers (`openclaw`, `personal`) run different versions during a rollout, and the older one repairs a file the newer one would have aliased | The older controller writes no alias; that file's in-flight result still misses until the rollout completes | Complete the rollout; no data is lost that the old version did not already lose | The written bytes carry no `task_identifier_previous` | Reversible |
| A malformed file is repaired while a result is in flight and the alias write fails | `injectAndStore` returns the write error; nothing is committed; the next cycle retries | Unchanged from today — the next cycle retries | `failed to write %s` warn log | Reversible — nothing was committed |
| YAML drift introduces a value shape not in the tables | Routed by the same validity predicate; the alias is written only for non-blank single-line strings, so an unknown shape takes the no-alias path | None needed | Warn log names the Go type | Reversible |

## Security / Abuse

Frontmatter is untrusted input: any agent or operator can author a task file. The alias is a second identifier a file may claim, so it widens the set of identifiers that resolve to a given file. Three properties keep that bounded: the duplicate case is an error rather than a preference (AC7), so an alias cannot steal another task's result; the alias is only ever written by the scanner from a value already present in the file, so it adds no write path an attacker did not already control; and the alias is excluded from the duplicate branch, so it cannot be used to shadow an indexed UUID.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Write the alias on the non-UUID repair path and match it in the walk, with tests, doc and CHANGELOG, and `make precommit` | 1-8 | AC1-AC11 | — |

Rationale: a single prompt. The alias is inert without the walk match and the walk match is unreachable without the alias, so splitting them would leave a half-landed state whose ACs cannot pass until both exist. One package boundary each side, both small, and the regression lock (AC5) is only meaningful against the combined diff. The Rung-3 AC is operator-executed after the prod deploy and is not a prompt.

## Do-Nothing Option

Not acceptable, but the cost is bounded and worth naming. The loss is silent and per-task: each occurrence parks one task in `human_review` with no result and produces an alert that names only an identifier. The operator's recovery is to read the pod log, discover the identifier mismatch by hand, and re-file the task — roughly the hours the 2026-11-03 incident cost. It does not corrupt the vault and it does not repeat unboundedly for one task, which is why this is a fix and not an incident.
