---
status: completed
spec: [011-bug-escaped-key-task-identifier-resolution]
summary: 'Replaced the scanner''s regex-based task_identifier key-line removal with yaml.v3 parsed-key resolution (new taskIdentifierKeyLines helper), added the spec-011 escaped-underscore row and fail-closed defensive cases, re-based the spec-009 halt-path specs onto Fixture B with a new spec-011 convergence spec, and added the ## Unreleased CHANGELOG bullet'
execution_id: agent-task-controller-escaped-key-exec-019-spec-011-escaped-key-task-identifier-resolution
dark-factory-version: dev
created: "2026-09-10T22:00:00Z"
queued: "2026-09-10T20:01:51Z"
started: "2026-09-10T20:01:52Z"
completed: "2026-09-10T20:09:47Z"
branch: dark-factory/bug-escaped-key-task-identifier-resolution
---

# Resolve task_identifier repair keys by parsing frontmatter with yaml.v3

<summary>

- The scanner's repair now removes a bad `task_identifier` key by parsing the file's frontmatter with the same YAML parser the scanner already uses for reading, instead of matching the key's literal text with a regex.
- Every spelling YAML accepts — bare, double-quoted, single-quoted, whitespace-before-colon, and the escaped-underscore form — is now recognised as the same parsed key and stripped during repair.
- A file whose key is written with the escaped-underscore spelling (`"task_identifier": 501`) now heals itself in exactly one write: one valid UUID survives, `status: in_progress` survives, and no further cycle rewrites it.
- The spec-009 convergence guard stays completely silent on that shape after the fix — no `repair_not_converging` counter increment, no `task_identifier repair did not converge` log.
- Frontmatter yaml.v3 cannot parse, frontmatter whose top level is not a mapping, and flow-style one-line mappings (`{...}`) are left byte-for-byte untouched, so the flow-mapping fixture still refuses repair exactly as before.
- All previously-fixed removal cases (block sequence/mapping/scalar spans, multiple key lines, fenced body lines, CRLF, unterminated frontmatter) keep their exact byte-for-byte behavior.
- The convergence suite's halted-file tests (self-clearing halt, empty-identifier-on-delete, auto-inject-off) move onto the flow-mapping fixture so the guard's refusal path stays exercised.
- The change is recorded in the changelog under a new `## Unreleased` section.

</summary>

<objective>

Make the scanner's `task_identifier` repair removal resolve keys the same way its parse layer does — parse the frontmatter region with yaml.v3 into a node tree and remove every line whose parsed key is `task_identifier` — so the escaped-underscore spelling is stripped and the file converges in exactly one write, while every other existing removal case and the flow-style non-converging case behave byte-for-byte as before. Satisfies spec 011 AC1-AC7.

</objective>

<context>

This repo has no root `CLAUDE.md`; `README.md` and `docs/controller-design.md` carry the project conventions.

Read these files fully before changing anything:

- `specs/in-progress/011-bug-escaped-key-task-identifier-resolution.md` — the spec. Read the Reproduction, Expected vs Actual, Goal, Constraints, Acceptance Criteria, Desired Behavior 1-6, and Failure Modes.
- `pkg/scanner/task_identifier.go` — `taskIdentifierKeyLine` (the production regex this spec removes), `removeTaskIdentifier`, `markValueSpan`, `frontmatterClosingIndex`, `isBlankLine`, `leadingWhitespaceLen`, `isValidUUID`, `isIdentifierUnique`, `InjectTaskIdentifier`.
- `pkg/scanner/frontmatter.go` — `extractFrontmatter`, `DeduplicateFrontmatter` (last-wins semantics; must not change).
- `pkg/scanner/vault_scanner.go` — `processFile` (the two repair sites that call `removeTaskIdentifier(content)` — the present-but-invalid branch and the duplicate branch; the key-absent branch calls `injectAndStore` directly without removal), `injectAndStore`, `repairConverges` (the spec-009 guard; must not change). No change lands in this file.
- `pkg/scanner/vault_scanner_internal_test.go` — the `Describe("removeTaskIdentifier (spec 008)")` block (~line 346, its first `It("removes key lines and their value spans byte-exactly")` starts ~line 347), and the `Describe("task_identifier backfill repair (spec 008)")` block (~line 509) whose `ac2Fixtures` table (16 rows, ~line 533) must NOT be touched.
- `pkg/scanner/vault_scanner_convergence_internal_test.go` — `convergenceHarness`, `newConvergenceHarness`, `runCycles`, `skipCounterValue`, `convergenceKeyLineRe`, `haltLogAnywhereRe`, `haltLogFor`, `convergenceFixtureA`, `convergenceFixtureB`, `convergenceAC2Fixtures`, and the `Describe("task_identifier repair convergence guard (spec 009)")` block with its AC2/AC3/AC4/AC5/AC6 specs.
- `CHANGELOG.md` — there is currently NO `## Unreleased` section; the newest section is `## v0.8.6`.
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — `## Unreleased` placement and bullet format.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-patterns.md`, `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`, `/home/node/.claude/plugins/marketplaces/coding/docs/go-precommit.md`, `/home/node/.claude/plugins/marketplaces/coding/docs/definition-of-done.md` — conventions this repo follows.

Design facts (verified against `gopkg.in/yaml.v3@v3.0.1` source and by execution on this repo, 2026-09-10):

- `yaml.Node.Line` is 1-based within the parsed document (`n.Line = p.event.start_mark.line + 1` in `decode.go`).
- Decoding into a `yaml.Node` (not a Go map) skips the duplicate-key check entirely — `out.Type() == nodeType` short-circuits in `decode.go`'s `unmarshal` before `mapping()` runs, so a frontmatter with two `task_identifier` lines parses fine and BOTH key nodes remain in `root.Content`. This is why the existing "Multiple key lines are all removed" case keeps working.
- A flow-style mapping node carries `Style |= yaml.FlowStyle` (set in `decode.go`'s parser `mapping()`); a block-style mapping has `Style == 0`. `yaml.FlowStyle` is a defined `Style` constant.
- `yaml.Unmarshal([]byte("\"task\\u005fidentifier\": 501\nstatus: in_progress\n"), &doc)` resolves the double-quoted escape to a key node with `Value == "task_identifier"`, `Style == yaml.DoubleQuotedStyle`, `Line == 1`. Verified by execution.
- If the frontmatter body (content lines 1..closing-1) is joined with `"\n"` and parsed, then a key node's `Line` IS its content line index (body line 1 == content index 1 == yaml line 1). Verified by execution, including a CRLF body.

</context>

<requirements>

## A. Production change — `pkg/scanner/task_identifier.go`

1. Delete the production regex variable and its doc comment:

```go
// taskIdentifierKeyLine matches any frontmatter line whose key resolves to
// task_identifier under the spellings YAML accepts: bare, double-quoted,
// single-quoted, or with whitespace before the colon.
var taskIdentifierKeyLine = regexp.MustCompile(`^\s*['"]?task_identifier['"]?\s*:`)
```

   The name must not appear anywhere in non-test code after this change (spec AC6). The test files have their own separate regexes (`taskIdentifierKeyLineRe` in `vault_scanner_internal_test.go`, `convergenceKeyLineRe` in `vault_scanner_convergence_internal_test.go`) — leave those alone.

2. Replace the whole `removeTaskIdentifier` function with the following (full body, verified to reproduce every existing spec-008 case byte-for-byte and to remove the escaped-underscore spelling):

```go
// removeTaskIdentifier removes every task_identifier key line from the
// frontmatter region of content, together with the full indentation span of
// each key's value (block sequences, block mappings, and block scalars), so
// injectAndStore can safely prepend a fresh value. Lines outside the frontmatter
// region — including a body line beginning task_identifier: — are preserved
// byte-for-byte.
//
// Keys are resolved by parsing the frontmatter region with yaml.v3, the same
// way the read path resolves them, rather than by matching literal key text:
// every spelling YAML accepts — bare, double-quoted, single-quoted,
// whitespace-before-colon, and escaped characters inside a quoted key, e.g.
// "task_identifier" — resolves to the same parsed key and is removed.
func removeTaskIdentifier(content []byte) []byte {
	lines := strings.Split(string(content), "\n")
	closing := frontmatterClosingIndex(lines)
	if closing == -1 {
		return content
	}
	keyLines, ok := taskIdentifierKeyLines(lines, closing)
	if !ok {
		return content
	}
	remove := make([]bool, len(lines))
	for _, i := range keyLines {
		remove[i] = true
		markValueSpan(lines, i+1, closing, leadingWhitespaceLen(lines[i]), remove)
	}
	out := make([]string, 0, len(lines))
	for i, line := range lines {
		if remove[i] {
			continue
		}
		out = append(out, line)
	}
	return []byte(strings.Join(out, "\n"))
}
```

3. Add this new package-level helper in the same file, placed directly above `removeTaskIdentifier`:

```go
// taskIdentifierKeyLines parses the frontmatter region (content lines
// 1..closing-1) with yaml.v3 into a node tree and returns the content line index
// of every top-level key whose parsed Value is task_identifier.
//
// The boolean result is false — and the caller must leave content unchanged —
// when the region cannot be parsed, when the top-level node is not a mapping, or
// when the top-level mapping is in flow style: a flow mapping carries sibling
// keys on the same line, so removing that line would over-delete, and the
// spec-009 convergence guard bounds that shape instead.
//
// The region is parsed with trailing CR stripped per line so CRLF files resolve
// the same as LF files; stripping never removes a line, so a key node's 1-based
// yaml.v3 Line still equals its content line index (body line 1 == content index
// 1). Decoding into a yaml.Node keeps duplicate keys in the node's Content, so
// every spelling of a repeated key is reported.
func taskIdentifierKeyLines(lines []string, closing int) ([]int, bool) {
	body := make([]string, 0, closing-1)
	for i := 1; i < closing; i++ {
		body = append(body, strings.TrimRight(lines[i], "\r"))
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(strings.Join(body, "\n")), &doc); err != nil {
		return nil, false
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil, false
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode || root.Style&yaml.FlowStyle != 0 {
		return nil, false
	}
	var out []int
	for i := 0; i+1 < len(root.Content); i += 2 {
		key := root.Content[i]
		if key.Value != "task_identifier" {
			continue
		}
		out = append(out, key.Line)
	}
	return out, true
}
```

4. Fix the imports in `pkg/scanner/task_identifier.go`: remove `"regexp"` (nothing else uses it after the variable deletion) and add `"gopkg.in/yaml.v3"`. Keep `"context"`, `"strings"`, `"github.com/bborbe/errors"`, `"github.com/google/uuid"`.

5. Do NOT modify any other production file. In particular: `markValueSpan`, `frontmatterClosingIndex`, `isBlankLine`, `leadingWhitespaceLen`, `isValidUUID`, `isIdentifierUnique`, and `InjectTaskIdentifier` in this file stay untouched; `pkg/scanner/vault_scanner.go` (`processFile`, `injectAndStore`, `repairConverges`) and `pkg/scanner/frontmatter.go` (`DeduplicateFrontmatter`) stay untouched. `processFile` keeps calling `removeTaskIdentifier(content)` unchanged — its internal behavior is all that changes.

## B. AC1 + defensive coverage — `pkg/scanner/vault_scanner_internal_test.go`

6. In the `Describe("removeTaskIdentifier (spec 008)")` block, inside `It("removes key lines and their value spans byte-exactly")`, add a new case IMMEDIATELY AFTER the existing double-quoted key spelling case (the first `Expect(...)` in that `It`, ~line 349). Assert the exact output bytes:

```go
		// Escaped-underscore key spelling (spec 011): yaml.v3 resolves the
		// double-quoted escape to the parsed key task_identifier, so the line is
		// removed like any other spelling.
		Expect(string(removeTaskIdentifier([]byte(`---
"task\u005fidentifier": 501
status: in_progress
---
body
`)))).To(Equal(`---
status: in_progress
---
body
`))
```

   The escape sequence is LOAD-BEARING: inside the backtick raw string, `_` must remain six literal characters (`\`, `u`, `0`, `0`, `5`, `f`) on disk. Do NOT "simplify" it to an underscore — that would turn the fixture into the already-covered plain double-quoted case and the spec would no longer prove the fix. Do not write `_` inside the quotes in the Go source.

7. Add a NEW `It` to the same `Describe("removeTaskIdentifier (spec 008)")` block, directly after `It("removes key lines and their value spans byte-exactly")` (i.e. after ~line 432), asserting the fail-closed no-op cases return the input unchanged (spec Desired Behavior 3-4). Cover all four, each `Expect(string(removeTaskIdentifier([]byte(in)))).To(Equal(in))`:

   - a frontmatter region yaml.v3 cannot parse — e.g. `"---\nfoo: [1, 2\nstatus: in_progress\n---\nbody\n"` (unclosed flow sequence);
   - a top-level that is a sequence, not a mapping — e.g. `"---\n- a\n- b\n---\nbody\n"`;
   - the flow-style top-level mapping — the exact bytes `"---\n{task_identifier: 501, status: in_progress}\n---\nbody\n"` (spec-009 AC2 Fixture B must stay untouched);
   - an empty frontmatter region — `"---\n---\nbody\n"`.

   Add a one-line comment on each case naming which spec requirement it guards. Keep the `It` under 80 lines; if it grows past that, split into two `It`s.

## C. Convergence suite re-base — `pkg/scanner/vault_scanner_convergence_internal_test.go`

8. Update the comment above `convergenceFixtureA` (currently ~lines 188-196). The current text claims the escaped-underscore spelling "matches only the literal spellings, so removeTaskIdentifier is a no-op" and that widening the regex "is explicitly NOT this spec's job" — both are now false. Replace the comment with one stating that spec 011 resolves keys by parsing with yaml.v3, so the escaped-underscore spelling now resolves to the parsed key `task_identifier`, is removed, and the file converges in exactly one write. Keep the constant value exactly as it is (it remains the input fixture for the new spec-011 spec):

```go
const convergenceFixtureA = "---\n\"task\\u005fidentifier\": 501\nstatus: in_progress\n---\nbody\n"
```

   (The Go escaping here is also load-bearing: `\\u005f` produces the literal on-disk text `_`. Do not change it.)

9. Remove the `ac2-a-escaped-key.md` row from `convergenceAC2Fixtures` so the table holds ONLY the flow-mapping fixture B row (`ac2-b-flow-map.md` with `convergenceFixtureB`). The spec-009 AC2 spec iterating this table ("AC2: refuses a non-converging repair with zero writes, one ERROR log, one counter increment") then exercises only Fixture B, which still refuses — this is the spec-011 AC4 evidence ("the existing AC2 row for Fixture B passes unmodified"). The constant `convergenceFixtureA` is still used by the new spec in requirement 11, so it must remain declared.

10. Re-base the three spec-009 specs that used `convergenceFixtureA` as their halted input onto `convergenceFixtureB` (spec 011 AC7), changing ONLY the `os.WriteFile(..., []byte(convergenceFixtureA), 0600)` call to `[]byte(convergenceFixtureB)` and keeping every assertion identical:

    - `"AC3: the halt self-clears when the file content changes"` (~line 266) — the repairable overwrite shape it later writes (`"---\ntask_identifier: 501\nstatus: in_progress\n---\nbody\n"`) is unchanged and still differs from Fixture B, so the content-keyed re-arm still fires.
    - `"AC4: a halted file never emits an empty identifier on delete"` (~line 312).
    - `"AC5: stays silent when auto-inject is disabled, re-skipping every cycle"` (~line 356) — Fixture B still routes into the present-but-invalid branch (`task_identifier: 501` parses as int), so `auto_inject_disabled` still increments `5.0` across five cycles.

11. Add a NEW `Describe("task_identifier escaped-key repair (spec 011)", ...)` block at the end of `vault_scanner_convergence_internal_test.go` (after the spec-009 `Describe` block), reusing the package-level harness (`newConvergenceHarness`, `runCycles`, `captureGlogWarnings`, `skipCounterValue`, `convergenceKeyLineRe`, `haltLogAnywhereRe`, `extractFrontmatter`, `convergenceFixtureA`). One spec covering spec-011 AC2 + AC3:

    - Create a fresh `os.MkdirTemp` dir and a fresh `newConvergenceHarness(dir, true)`; write `convergenceFixtureA` as the file `011-escaped-key.md`; read `skipCounterValue(metrics.ReasonRepairNotConverging)` BEFORE the cycles; call `h.runCycles(ctx, 5)` exactly once (five cycles) inside `captureGlogWarnings`; put NO Gomega assertion inside that closure.
    - Assert `h.writeCount` equals `1` (AC2: exactly one write).
    - Read the file back: `countLinesMatching(finalBytes, convergenceKeyLineRe)` equals `1`; the bytes contain `"status: in_progress\n"` (AC2: sibling key survives); `extractFrontmatter` + `yaml.Unmarshal` yields `fmMap["task_identifier"]` as a `string` that `uuid.Parse` accepts — use the comma-ok form `idStr, isString := fmMap["task_identifier"].(string)` (the repo enables `forcetypeassert`; a single-value assertion fails `make check`).
    - Assert AC3 (guard silent across those same five cycles): `skipCounterValue(metrics.ReasonRepairNotConverging) - before` equals `0.0`, and `countLinesMatching([]byte(captured), haltLogAnywhereRe)` equals `0`.

    Match the existing spec-009 specs' style: three-line BSD copyright header is already at the top of the file, `ctx := context.Background()`, `Expect(os.RemoveAll(dir)).To(Succeed())` cleanup, and `// #nosec G304 -- test-only path` on the `os.ReadFile` of the test file. Do not use `PIt`, `XIt`, `FIt`, `Skip(`, or `Pending`.

## D. Changelog — `CHANGELOG.md`

12. Insert a new `## Unreleased` section directly above the existing `## v0.8.6` heading (there is no `## Unreleased` today). Do not create a second `## Unreleased` and do not modify any released section. One bullet, `fix:` prefix:

```
## Unreleased

- fix: the vault scanner's task_identifier repair now resolves keys by parsing the frontmatter region with yaml.v3 into a node tree and removing every top-level key whose parsed value is `task_identifier`, instead of matching literal key text with a regex — so every spelling YAML accepts, including the escaped-underscore form `"task_identifier"`, is stripped and the file converges in exactly one write; a frontmatter yaml.v3 cannot parse, a non-mapping top level, or a flow-style mapping is left byte-for-byte untouched (fail-closed), and the spec-009 convergence guard stays silent on the previously non-converging escaped-key shape (spec 011)
```

## Self-check before finishing

Re-run `<verification>` and confirm it passes. Then walk each of the spec's AC1-AC7 against the change: AC1 (the new row's exact bytes), AC2+AC3 (one write, one key, valid UUID, status survives, guard silent), AC4 (Fixture B still refuses via the spec-009 AC2 row), AC5 (all existing spec-008 cases byte-exact), AC6 (no `taskIdentifierKeyLine` in production), AC7 (halt-path tests re-based onto Fixture B).

</requirements>

<constraints>

- Do NOT commit — dark-factory handles git.
- Do NOT modify the spec-009 convergence guard (`injectAndStore` / `repairConverges`), the metrics, or `DeduplicateFrontmatter`'s last-wins semantics. (Spec Constraints.)
- The convergence guard must stay silent on the escaped-underscore fixture after the fix: the repair converges in one write, so `repair_not_converging` must NOT increment and no `task_identifier repair did not converge` log line may appear. (Spec Constraints, AC3.)
- Flow-style frontmatter (spec-009 AC2 Fixture B, `{task_identifier: 501, status: in_progress}` on one line) must remain non-converging and guard-bounded — zero writes, one halt log, one counter increment across five cycles. Removing its line would over-delete sibling keys; the fix must not change Fixture B's behavior. (Spec Constraints, AC4.)
- Lines outside the frontmatter region — including a body line beginning `task_identifier:` inside a fenced block — are preserved byte-for-byte (existing behavior).
- CRLF files keep their `\r` on every retained line (existing behavior).
- Unterminated frontmatter stays a no-op (existing behavior).
- Do NOT introduce any literal-key-text matcher for `task_identifier` in production code (spec AC6). The only key-resolution mechanism in `pkg/scanner/task_identifier.go` may be yaml.v3 parse-based.
- Do NOT modify the spec-008 `ac2Fixtures` table (16 rows) in `pkg/scanner/vault_scanner_internal_test.go`, its rows 10-12, or any of their `Expect(writeCount).To(Equal(1))` assertions. They must pass unmodified (spec AC5).
- Do NOT use `PIt`, `XIt`, `FIt`, `Skip(`, or `Pending` anywhere in the edited test files — spec AC5 requires the scanner suite to run with zero skipped and zero pending specs.
- Keep each `It` closure under 80 lines so `funlen` stays green; lift per-fixture bodies into a package-level helper if a spec grows past that, rather than adding a `//nolint`.
- Do NOT change the parity invariant in `pkg/scanner/vault_scanner_test.go`: this change adds no log site and no counter call, so the skip-site/counter parity must remain `10` on both sides, untouched.
- Do NOT run bare `git` commands — the container has no working `.git` (verified on 2026-09-10: `git status` fails with `fatal: not a git repository` even though `.dark-factory.yaml` says `workflow: direct`). The spec's `git diff origin/master -- pkg/scanner/task_identifier.go` check belongs to the spec's operator-executable verification rung; use the non-git greps in `<verification>` instead.
- Do NOT run `kubectl*`, `docker`, `make buca`, `make build`, or any operator/deploy command.
- Existing tests must still pass. `make precommit` at repo root must exit 0; coverage must not fall below the current baseline (92%+). `make precommit` runs `make format` (goimports-reviser plus `golines --max-len=100 -w`) over every non-vendor `.go` file on every run, green or not — files being reformatted in place is expected, not a failure.

</constraints>

<verification>

Fast loop while implementing:

```
go build ./...
go test -mod=mod -count=1 ./pkg/scanner/...
```

Expect the Ginkgo suite to report `SUCCESS` with `0 Skipped` and `0 Pending`, including the spec-008 `removeTaskIdentifier` specs, the spec-008 backfill AC2 (16 shapes, write count 1 each), and the spec-009 convergence suite (Fixture B still refuses, re-based AC3/AC4/AC5-disabled on Fixture B, new spec-011 AC2+AC3 spec green).

AC1 + AC5 evidence — the exact bytes and no new skips:

```
go test -mod=mod -count=1 -v ./pkg/scanner/... 2>&1 | grep -E 'SUCCESS!|FAIL|Skipped|Pending'
```

AC6 evidence — no literal-key-text matcher remains in production:

```
grep -n 'taskIdentifierKeyLine' pkg/scanner/task_identifier.go; echo "grep exit: $?"
```

Expect the grep to print NOTHING for the file (exit 1). The test files keep their own `taskIdentifierKeyLineRe` / `convergenceKeyLineRe` — that is expected. Confirm the production file resolves keys by parsing instead:

```
grep -n 'yaml.Unmarshal\|yaml.FlowStyle\|yaml.Node' pkg/scanner/task_identifier.go
```

Expect at least the `yaml.Unmarshal` line inside `taskIdentifierKeyLines`.

Parity invariant — this change adds no skip site, so both counts stay `10`:

```
awk '/^func \(v \*vaultScanner\) (processFile|injectAndStore)\(/,/^}/' pkg/scanner/vault_scanner.go | grep -c 'SkippedFilesTotal('
```

Expect `10`. Never make this green by deleting an assertion or lowering a count.

No skipped/pending specs were introduced in the edited test files:

```
grep -c 'PIt(\|XIt(\|FIt(\|Skip(\|Pending(' pkg/scanner/vault_scanner_internal_test.go pkg/scanner/vault_scanner_convergence_internal_test.go || true
```

Expect `0` (two lines of `0`).

Changelog evidence — one `## Unreleased` section above every released section, containing the fix:

```
awk '/^## v/{exit} /spec 011/{f=1} END{exit !f}' CHANGELOG.md
grep -c '^## Unreleased' CHANGELOG.md
```

Expect the `awk` to exit `0` and the `grep -c` to print `1`.

Run ONCE at the end, at repo root:

```
make precommit
```

Expect exit `0` with the full suite green. If it fails, fix and re-run ONLY the failing target (`make test`, `make check`), then re-run `make precommit` once the individual targets pass.

</verification>

<!--
NOTES FOR THE HUMAN REVIEWER — open questions resolved from the spec alone, non-blocking:

1. The spec's Suggested Decomposition is exactly one prompt; this file follows it. The change is one
   coherent unit: the production rewrite cannot be verified independently of the convergence-suite
   re-base (the escaped fixture flips from non-converging to converging, which would break the spec-009
   AC2 table if left alone), and the AC1/defensive rows are coupled to the new parse-based code. No
   scenario prompt is emitted: the spec-009 convergence harness already runs real scanner cycles over a
   real tmpdir file, so unit/integration tests reach the behavior (scenario condition (a) fails).

2. Spec AC6's verification lists `git diff origin/master -- pkg/scanner/task_identifier.go` under
   Container-executable. This environment masks `.git` (verified: `git status` fails) despite
   `workflow: direct`, so a bare git command would false-positive-pass. Resolved: the container rung
   uses the non-git `grep -n 'taskIdentifierKeyLine' pkg/scanner/task_identifier.go` + `go test`; the
   `git diff` check is left on the spec's operator-executable rung where the spec already lists it.

3. Behaviour narrowing to flag: the old regex matched a `task_identifier:` line at ANY indentation, so
   a nested key inside a block-mapping value would have been removed; the new code walks only the
   TOP-LEVEL mapping keys (spec Desired Behavior 2). No existing test or production path depends on
   nested-key removal — processFile reads only the top-level `fmMap["task_identifier"]` — so this is a
   deliberate, spec-mandated narrowing, but it is a real behaviour change worth noting at review.

4. The `convergenceAC2Fixtures` table loses its fixture A row because fixture A stops being
   non-converging after this fix; that is exactly the AC7 re-base ("the escaped-underscore fixture
   moved out of the refused set"), applied to the table that feeds the spec-009 AC2 spec.
-->
