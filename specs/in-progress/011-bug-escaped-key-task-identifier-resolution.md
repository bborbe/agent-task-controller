---
status: verifying
tags:
    - dark-factory
    - spec
approved: "2026-09-10T19:47:28Z"
generating: "2026-09-10T19:47:28Z"
prompted: "2026-09-10T19:59:09Z"
verifying: "2026-09-10T20:09:48Z"
branch: dark-factory/bug-escaped-key-task-identifier-resolution
---

## Summary

- The vault scanner's `task_identifier` repair removes a malformed key only when its literal text matches a fixed set of spellings. YAML accepts other spellings that resolve to the same parsed key — escaped characters inside a quoted key, e.g. the escaped-underscore spelling — so removal silently no-ops.
- The miss is not theoretical: the reproduction below, verified by execution, drives the file into the present-but-invalid repair branch, where removal no-ops, injection prepends a fresh UUID, and last-wins deduplication resolves `task_identifier` back to the integer — the original accumulator loop, at flat file size.
- The v0.8.0 convergence guard (spec 009, live on dev+prod) bounds the damage to one ERROR log + one counter increment per cycle. It does not close the door: the file is never repaired, and the operator must notice and fix it by hand.
- This spec fixes the resolution at source: keys are resolved by parsing frontmatter with yaml.v3, and lines whose *parsed key* is `task_identifier` are removed — not lines whose literal text matches.
- The fix is deliberately separate from spec 009's guard (per its Constraints): the guard bounds any non-converging repair; this spec makes the escaped-key repair converge again.

## Problem

The scanner's self-healing repair removes a malformed `task_identifier` key by literal text matching. YAML accepts key spellings the regex does not enumerate — escaped characters inside quoted keys, of which `"task_identifier"` (escaped underscore) is one — and yaml.v3 resolves those spellings to the same parsed key. The result is a repair that observes the malformed value (the parse path routes the file into the present-but-invalid branch) but cannot remove it (the removal path matches a different notion of "the key"). Every scan cycle re-triggers the same repair, and only the spec-009 convergence guard prevents an unbounded write loop. With the guard live, the remaining cost is a file that never self-heals plus a permanent ERROR log + counter increment per cycle against a shared vault repository.

## Reproduction

### Fixture (deterministic, unit-level)

Frontmatter region between the `---` delimiters:

```
"task_identifier": 501
status: in_progress
```

where `_` is the literal backslash-u-005f escape sequence inside a double-quoted YAML key (file bytes: `"task_identifier": 501`).

### Steps

1. `removeTaskIdentifier(content)` is called from the present-but-invalid branch (`processFile`, `!isString || !isValidUUID(taskID)`).
2. `taskIdentifierKeyLine` = `^\s*['"]?task_identifier['"]?\s*:` is tested against the line `"task_identifier": 501`. The leading `"` matches `['"]?`, but the regex then requires the literal characters `task_identifier`; the actual characters are `task_identifier` (the escape sequence is literal text to the regex). No match → removal no-ops.
3. `InjectTaskIdentifier` prepends `task_identifier: <fresh-uuid>` after the opening `---`. The file now has two keys: the UUID first, `501` last.
4. `DeduplicateFrontmatter` keeps the LAST value per key, so the surviving `task_identifier` is `501` (int) again.
5. Next cycle: `fmMap["task_identifier"]` is still a non-string int → same branch → same no-op removal → the loop condition is unchanged.

Observed evidence (verified against `v0.8.5` code, which is byte-identical to `v0.7.4` for `task_identifier.go`): removal produces zero line deletions for the fixture; the frontmatter survives the repair with the integer `501` intact.

**Spelling correction vs the original incident report:** the plain double-quoted spelling `"task_identifier": 501` (no escape) IS matched by the current regex (`['"]?` before and after the key). The surviving defect is specifically the escaped-underscore spelling, which spec 009's AC2 Fixture A already names as its non-converging input.

## Expected vs Actual

| | Expected | Actual |
|---|---|---|
| Escaped-underscore key `"task_identifier": 501` | Stripped by repair; file converges to one fresh UUID in one write | Removal no-ops; key survives; only the spec-009 guard stops an unbounded loop |
| Repair of a file with this spelling | Self-heals (one write, one valid `task_identifier`) | Never repairs; permanent ERROR log + counter increment per cycle until a human edits the file |
| Key resolution basis | Parsed key (yaml.v3) | Literal key text (regex) |

## Why this is a bug

Two layers of the same scanner disagree about what "the key `task_identifier`" means. The parse layer (`yaml.Unmarshal` into `fmMap`) resolves YAML spellings to their parsed key, so the escaped-underscore spelling is correctly recognized as a present-but-invalid `task_identifier` and routed into the repair branch. The removal layer (`removeTaskIdentifier`) re-implements key detection with a regex over literal text, which does not resolve YAML escapes. The disagreement is structural: any spelling yaml.v3 resolves but the regex does not enumerate re-opens the same door. The spec-009 guard (correctly, per its constraints) bounds rather than fixes this; closing the door at source is this spec's job.

## Goal

The scanner's repair removal resolves keys the same way the parse layer does: by parsing frontmatter with yaml.v3 and removing lines whose parsed key is `task_identifier`. Every spelling YAML accepts — bare, quoted, escaped-underscore — is removed in the repair, and the escaped-underscore file converges in exactly one write. Flow-style frontmatter (a single `{...}` line carrying multiple keys) is explicitly out of scope: removing that line would over-delete sibling keys, so it remains bound by the spec-009 guard.

## Constraints

- The fix is a source-side resolution change in `pkg/scanner/task_identifier.go` only. It does NOT modify the spec-009 convergence guard (`injectAndStore` / `repairConverges`), the metrics, or `DeduplicateFrontmatter`'s last-wins semantics.
- The convergence guard must stay silent on the escaped-underscore fixture after the fix: the repair converges in one write, so `repair_not_converging` must NOT increment and no halt log may appear.
- Flow-style frontmatter (spec-009 AC2 Fixture B, `{task_identifier: 501, status: in_progress}` on one line) remains non-converging and guard-bounded. Removing the whole line would delete `status: in_progress`; the fix must not change Fixture B's behavior.
- Lines outside the frontmatter region — including a body line beginning `task_identifier:` inside a fenced block — are preserved byte-for-byte (existing behavior).
- CRLF files keep their `\r` on every retained line (existing behavior).
- Unterminated frontmatter stays a no-op (existing behavior).
- `make precommit` green at repo root; coverage not below the current baseline (92%+ at v0.8.5).

## Acceptance Criteria

- [ ] **AC1 — Escaped-underscore key is removed.** `removeTaskIdentifier` on the fixture `"task_identifier": 501` (plus `status: in_progress`) returns frontmatter containing only `status: in_progress`. — evidence: the spec-008 `removeTaskIdentifier` Ginkgo block gains a row asserting the exact output bytes; `go test ./pkg/scanner/...` passes.
- [ ] **AC2 — The repair converges in one write under the scanner.** A `vaultScanner` with `autoInject=true` over the fixture, run through five `RunCycle` calls: `writeFile` invoked exactly 1 time, the resulting file has exactly one `task_identifier` key whose value parses as a valid UUID, and `status: in_progress` survives. — evidence: the spec-009 convergence harness (`convergenceHarness`, `writeCount`) asserts write count `1`, `grep -c '^task_identifier:'` = `1` on the final bytes, and `yaml.Unmarshal` of the final frontmatter yields a string UUID under `task_identifier`.
- [ ] **AC3 — The guard stays silent on the escaped-underscore shape.** Across those five cycles, `agent_controller_vault_scanner_skipped_files_total{reason="repair_not_converging"}` delta is exactly `0` and the captured log contains zero `task_identifier repair did not converge` lines. — evidence: `skipCounterValue(metrics.ReasonRepairNotConverging)` before/after delta `0`; `countLinesMatching(captured, haltLogAnywhereRe)` = `0`.
- [ ] **AC4 — Flow-style frontmatter behavior unchanged.** Spec-009 AC2 Fixture B (`{task_identifier: 501, status: in_progress}`) still refuses the repair: zero writes, one halt log, one counter increment across five cycles. — evidence: the existing AC2 row for Fixture B passes unmodified.
- [ ] **AC5 — Existing removal cases keep their byte-exact behavior.** All current spec-008 `removeTaskIdentifier` cases (double-quoted key, spaced key, block sequence/mapping/scalar spans, multiple key lines, fenced-body survival, CRLF, unterminated) pass unchanged. — evidence: `go test ./pkg/scanner/...` exits 0 with no skipped or pending specs.
- [ ] **AC6 — Production removal is parse-based, not regex-based.** The production removal code contains no regex-based key matcher: `grep -n 'regexp.MustCompile' pkg/scanner/task_identifier.go` returns nothing (the `regexp` import and the literal-key matcher are both gone), and `git diff origin/master -- pkg/scanner/task_identifier.go` shows the regex match replaced by a yaml.v3 parse-based key-line resolution. — evidence: both greps; `go test ./pkg/scanner/...` exits 0.
- [ ] **AC7 — Spec-009 halt-path tests re-based onto Fixture B.** AC3 (halt self-clears on content change), AC4 (halted file never emits empty identifier on delete), and AC5-disabled (auto-inject off) of the convergence suite, which previously used the escaped-underscore fixture as their halted input, now use Fixture B so the guard's refusal path stays exercised. — evidence: the convergence suite passes with the escaped-underscore fixture moved out of the refused set; `go test ./pkg/scanner/...` exits 0.

## Verification

### Container-executable

```bash
make precommit   # exits 0
go test ./pkg/scanner/...   # exits 0, no skipped/pending
grep -n 'regexp.MustCompile' pkg/scanner/task_identifier.go   # returns nothing (regexp import + literal matcher gone)
git diff origin/master -- pkg/scanner/task_identifier.go   # regex match replaced by yaml.v3 parse-based resolution
```

### Operator-executable (host, after container commits land)

```bash
# in the feature worktree: confirm the diff is the parse-based change only
git log origin/master..HEAD --oneline
git diff origin/master -- pkg/scanner/task_identifier.go
```

## Desired Behavior

1. `removeTaskIdentifier` parses the frontmatter region with yaml.v3 into a `yaml.Node`.
2. Walking the top-level mapping, every key node whose parsed `Value` is `task_identifier` contributes its line number to the removal set; that line and its value span (existing `markValueSpan` logic) are removed.
3. A frontmatter that yaml.v3 cannot parse is a no-op (unchanged content returned) — matching today's safety behavior.
4. A top-level mapping in flow style (`{...}` on one line) is left untouched: the line carries sibling keys, so removing it would over-delete; the spec-009 guard continues to bound it.
5. Lines outside the frontmatter region are never candidates (unchanged).
6. The escaped-underscore spelling resolves to the parsed key `task_identifier` and is removed like any other spelling.

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---|---|---|
| Frontmatter unparseable by yaml.v3 | Removal no-ops; content returned unchanged (fail-closed) | Guard/operator path unchanged |
| Flow-style frontmatter carrying `task_identifier` | Line not removed (would over-delete sibling keys); guard refuses the repair | Operator fixes the file; guard bounds the loop |
| Escaped-underscore key | Removed by parsed-key resolution; repair converges in one write | None needed — self-heals |
| Body line `task_identifier:` inside a fenced block | Preserved byte-for-byte (outside frontmatter region) | None |

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Replace regex key-line matching with yaml.v3 parsed-key line resolution in `pkg/scanner/task_identifier.go`; add escaped-underscore row to spec-008 block; re-base convergence halt-path tests onto Fixture B; CHANGELOG bullet | 1-6 | 1-7 | — |
