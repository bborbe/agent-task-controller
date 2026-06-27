---
status: approved
spec: [001-scanner-auto-inject-flag]
created: "2026-06-28T00:00:00Z"
queued: "2026-06-27T22:36:59Z"
---

<summary>

- The vault scanner now consults the construction-time auto-inject boolean (wired by prompt 1) at its three injection trigger sites.
- When the boolean is `false`, a UUID-less / non-UUID / duplicate-UUID task file is skipped with a warn log line in the exact frozen format the post-deploy probes grep for, the dedicated skip counter ticks once, and the scanner returns the same skip shape used by the existing "invalid frontmatter" sites — no file write happens.
- When the boolean is `true`, behavior is byte-identical to today and every existing scanner test continues to pass without further modification.
- The unrelated counter-reset path (the empty-to-named assignee transition) is explicitly NOT gated; a dedicated regression test pins this so a future refactor cannot accidentally tie it to the flag.
- A new dedicated skip-reason constant is added to the metrics package and pre-initialized at boot so the counter is visible from t=0 even before the first skip happens.
- The existing source-level parity check (skip-site log lines count = counter-increment count) continues to hold after the gate is inlined per-site, with the asserted total raised from six to nine.

</summary>

<objective>

Gate the three `injectAndStore` trigger sites in `vaultScanner.processFile` on the construction-time `autoInject` field (added by prompt 1). When the field is `false`, each site emits the frozen-substring warn log, increments a new `ReasonAutoInjectDisabled` counter, and returns the standard skip shape `(nil, "", false)` without calling `writeFile`. Cover the behavior with three Ginkgo tests (one per trigger site for `autoInject=false`), one parity test for `autoInject=true`, and one regression test asserting the `writeCounterReset` path is untouched by the flag.

</objective>

<context>

Read `/workspace/CLAUDE.md` for project conventions.
Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` for Ginkgo/Gomega + internal-vs-external test package conventions.
Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-prometheus-metrics-guide.md` for the `Reason*` constant + `init()` pre-initialization pattern.
Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-glog-guide.md` for `glog.Warningf` usage conventions.
Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-precommit.md` for file-level lint limits (funlen 80, nestif 4, golines 100).

Read `/workspace/pkg/scanner/vault_scanner.go` end-to-end. The three injection trigger sites inside `processFile` are at lines 261 (empty `taskID`), 264 (non-UUID), 268 (duplicate UUID). Each looks like `return v.injectAndStore(ctx, ..., relPath, currentFMAssignee)`. The `writeCounterReset` path is at line 275 — DO NOT touch it.

Read `/workspace/pkg/scanner/vault_scanner.go` lines 222-312 (`processFile`) and lines 314-340 (`injectAndStore`). Inside these two function bodies today there are exactly six "skip" markers (3× `glog.Errorf("skipping`, 1× `glog.Warningf("skipping`, 1× `glog.Warningf("failed to read`, 1× more `glog.Errorf("skipping`) and exactly six `SkippedFilesTotal(` calls. The parity-check test at `/workspace/pkg/scanner/vault_scanner_test.go` lines 1035-1058 asserts both totals equal 6 using an `awk` range that scans the bodies of `processFile` and `injectAndStore` only.

Read `/workspace/pkg/scanner/vault_scanner_test.go` lines 661, 700, 727, 750 — these existing tests exercise the three trigger sites with the default-true flag (after prompt 1's call-site edits). They MUST continue to pass with no further changes.

Read `/workspace/pkg/scanner/vault_scanner_internal_test.go` end-to-end. It is the only test file in `package scanner` (internal) and the only place a `&vaultScanner{...}` struct literal exists in any test file. After prompt 1 the existing literal at line 39 already has `autoInject: true`. New tests in this prompt that construct `&vaultScanner{...}` literals belong in this file — `vault_scanner_test.go` is `package scanner_test` (external) and cannot access the unexported struct.

Read `/workspace/pkg/metrics/metrics.go` end-to-end. The `Reason*` constants are at lines 147-153; the `init()` slice that pre-registers each reason with value 0 is at lines 201-209.

</context>

<requirements>

1. **Add the new skip-reason constant in `/workspace/pkg/metrics/metrics.go`.**
   - In the `const ( ... )` block at lines 147-153, add `ReasonAutoInjectDisabled = "auto_inject_disabled"` as a new entry (preserve alignment style).
   - In the `init()` slice at lines 201-207 that pre-initializes `SkippedFilesTotal` for each reason, add `ReasonAutoInjectDisabled` so the counter is exposed from boot.
   - Do NOT change the `SkippedFilesTotal` `Help` string (lines 162-165) — its existing wording covers any labelled reason; adding the new label does not require re-documenting the metric.

2. **Add the per-site gate to the three injection trigger sites in `/workspace/pkg/scanner/vault_scanner.go`.**
   - Inline the gate at each site (do NOT extract a helper). Inlining is required so the parity-check test at `vault_scanner_test.go` lines 1035-1058 keeps counting accurately: that test's `awk` regex `/^func \(v \*vaultScanner\) (processFile|injectAndStore)\(/,/^}/` only sees the bodies of those two functions, so any helper placed outside that range would be invisible to the counter regexes and the totals would diverge.
   - Required edits (apply exactly):

     Site 1 (empty `taskID`, current line 261):
     ```go
     if taskID == "" {
         if !v.autoInject {
             glog.Warningf(
                 "AUTO_INJECT_TASK_IDENTIFIER=false; skipping task without valid task_identifier: %s",
                 relPath,
             )
             v.metrics.SkippedFilesTotal(metrics.ReasonAutoInjectDisabled).Inc()
             return nil, "", false
         }
         return v.injectAndStore(ctx, content, relPath, currentFMAssignee)
     }
     ```

     Site 2 (non-UUID, current line 264):
     ```go
     if !isValidUUID(taskID) {
         if !v.autoInject {
             glog.Warningf(
                 "AUTO_INJECT_TASK_IDENTIFIER=false; skipping task without valid task_identifier: %s",
                 relPath,
             )
             v.metrics.SkippedFilesTotal(metrics.ReasonAutoInjectDisabled).Inc()
             return nil, "", false
         }
         glog.Warningf("replacing non-UUID task_identifier %q in %s", taskID, relPath)
         return v.injectAndStore(ctx, removeTaskIdentifier(content), relPath, currentFMAssignee)
     }
     ```

     Site 3 (duplicate UUID, current line 268):
     ```go
     if !v.isIdentifierUnique(taskID, relPath) {
         if !v.autoInject {
             glog.Warningf(
                 "AUTO_INJECT_TASK_IDENTIFIER=false; skipping task without valid task_identifier: %s",
                 relPath,
             )
             v.metrics.SkippedFilesTotal(metrics.ReasonAutoInjectDisabled).Inc()
             return nil, "", false
         }
         glog.Warningf("replacing duplicate task_identifier %q in %s", taskID, relPath)
         return v.injectAndStore(ctx, removeTaskIdentifier(content), relPath, currentFMAssignee)
     }
     ```

   - The frozen warn-log substring `AUTO_INJECT_TASK_IDENTIFIER=false; skipping task without valid task_identifier: ` (with the trailing space before the `%s`) must appear verbatim at all three sites. Spec Constraints bullet (verbatim quote): *"The warn log substring `AUTO_INJECT_TASK_IDENTIFIER=false; skipping task without valid task_identifier:` is frozen — AC4 greps for it."*
   - The return shape `(nil, "", false)` is mandatory. Spec Constraints bullet (verbatim quote): *"The return shape for a flag-disabled skip must be exactly `(nil, "", false)` — matching the existing skip paths at lines 240–256."*

3. **Do NOT touch the `writeCounterReset` path at line 275 or its `if currentFMAssignee != "" && prevEntry.taskIdentifier != "" && prevEntry.assignee == ""` guard.** Spec Desired Behavior bullet (verbatim quote): *"The flag has no effect on the unrelated `writeCounterReset` path (empty→named assignee transition at line 275) — that path does not mint a `task_identifier` and is out of scope."* AC7 is the regression test for this.

4. **Do NOT touch the existing skip sites at lines 230, 240, 246, 255, 300, 327.** Their existing log + counter behavior is unchanged.

5. **Update the parity-check test at `/workspace/pkg/scanner/vault_scanner_test.go` lines 1035-1058 so it counts the new multi-line gate sites correctly.**
   - **Why the existing counter fails on the new gate:** the test at lines 1050-1053 uses `strings.Count(body, ...)` with three byte-literal substrings:
     ```go
     strings.Count(body, `glog.Warningf("skipping`) +
     strings.Count(body, `glog.Errorf("skipping`) +
     strings.Count(body, `glog.Warningf("failed to read`)
     ```
     None of these match the multi-line gate spec'd in Req 2 — the bytes between `glog.Warningf(` and `"AUTO_INJECT_TASK_IDENTIFIER...` are `\n\t\t\t\t` (newline + indent), not empty, AND the literal begins with `AUTO_INJECT_TASK_IDENTIFIER=false; ` BEFORE the word `skipping`. Without the fix below, `skipCount` stays at 6, `counterCount` legitimately rises to 9, the assertion mismatches, and `make precommit` fails on the first run.
   - **Required edits:**
     1. Add the import `"regexp"` to the import block at the top of `/workspace/pkg/scanner/vault_scanner_test.go` (alphabetical position between `"path/filepath"` and `"strings"`). Verify with `grep -n '"regexp"' pkg/scanner/vault_scanner_test.go` after editing.
     2. Replace the `skipCount := ...` computation at lines 1050-1053 with a counter that ALSO counts the new gate via a whitespace-tolerant regex (so the gate's eventual single-line vs multi-line layout — golines may auto-wrap depending on column width — does not break the test):
        ```go
        autoInjectGateRe := regexp.MustCompile(`glog\.Warningf\(\s*"AUTO_INJECT_TASK_IDENTIFIER=false; skipping`)
        skipCount := strings.Count(body, `glog.Warningf("skipping`) +
            strings.Count(body, `glog.Errorf("skipping`) +
            strings.Count(body, `glog.Warningf("failed to read`) +
            len(autoInjectGateRe.FindAllStringIndex(body, -1))
        ```
        The `\s*` allows zero or more whitespace bytes between `(` and `"`, matching BOTH the multi-line form Req 2 specifies and any single-line collapse a future formatter might produce. The pattern intentionally does NOT consume the full frozen substring — it stops at `skipping` so the regex stays short and visually parses against the source.
     3. Change `Expect(skipCount).To(Equal(6), "expected 6 skip-site log lines, got %d", skipCount)` to `Expect(skipCount).To(Equal(9), "expected 9 skip-site log lines (6 existing + 3 auto-inject gate sites), got %d", skipCount)`.
     4. Change `Expect(counterCount).To(Equal(6), "expected 6 counter increment calls, got %d", counterCount)` to `Expect(counterCount).To(Equal(9), "expected 9 counter increment calls (6 existing + 3 auto-inject gate sites), got %d", counterCount)`. The existing `strings.Count(body, `SkippedFilesTotal(`)` matcher already catches the three new `v.metrics.SkippedFilesTotal(metrics.ReasonAutoInjectDisabled).Inc()` calls without any regex; only the `skipCount` side needs the regex.
     5. Update the test name's comment from `AC#6 invariant` to `AC#6 invariant (raised to 9 after per-site auto-inject gate, spec 001)`.
   - **Arithmetic verification (before saving):**
     - Pre-existing source matches inside `processFile` + `injectAndStore` bodies: 1× `glog.Warningf("failed to read` + 4× `glog.Errorf("skipping` + 1× `glog.Warningf("skipping` = 6 (matches today's `Equal(6)`).
     - New source matches added by the three per-site gates (each gate is one `glog.Warningf(\n\t\t\t\t"AUTO_INJECT_TASK_IDENTIFIER=false; skipping...` invocation): caught by the new regex, 3 total.
     - New `SkippedFilesTotal(metrics.ReasonAutoInjectDisabled).Inc()` matches: 3.
     - Final: `skipCount = 6 + 3 = 9`, `counterCount = 6 + 3 = 9`. Parity holds.
   - Do NOT widen the `awk` source range — `processFile` and `injectAndStore` already cover all three gate sites (the gate is inlined per-site inside `processFile`, per Req 2).
   - Do NOT change the frozen log substring `AUTO_INJECT_TASK_IDENTIFIER=false; skipping task without valid task_identifier:` — AC4/AC5 grep against real `kubectl logs` depends on it byte-for-byte.

6. **Update existing `&vaultScanner{...}` literals in test files (verification only; no edits expected after prompt 1).**
   - Run `grep -rn '&vaultScanner{' pkg/scanner/` and confirm the only test-file match is `pkg/scanner/vault_scanner_internal_test.go:39`, which already has `autoInject: true` from prompt 1. The two production matches in `vault_scanner.go` (lines 105 and 126) are the constructor bodies, already updated by prompt 1.
   - If grep surfaces any new `&vaultScanner{...}` literal added since prompt 1, add `autoInject: true,` to it.

7. **Add three Ginkgo tests for the `autoInject=false` skip behavior (AC3) — one per trigger site.**
   - File: `/workspace/pkg/scanner/vault_scanner_internal_test.go` (internal test package; required for unexported struct + private field access).
   - Place all three inside a new top-level `var _ = Describe("auto-inject flag gate (spec 001)", func() { ... })` block.
   - Test 1 — empty `task_identifier`:
     ```go
     It("skips the empty-task_identifier site without writing when autoInject=false", func() {
         ctx := context.Background()
         var writeCount int
         v := &vaultScanner{
             metrics: metrics.New(),
             hashes:  make(map[string]fileEntry),
             ops: fileOps{
                 readFile: func(_ context.Context, _ string) ([]byte, error) {
                     return []byte("---\nstatus: in_progress\nassignee: claude\n---\n# body\n"), nil
                 },
                 writeFile: func(_ context.Context, _ string, _ []byte) error {
                     writeCount++
                     return nil
                 },
             },
             autoInject: false,
         }
         before := counterValue(metrics.ReasonAutoInjectDisabled)

         task, written, werr := v.processFile(ctx, "empty-id.md")

         Expect(task).To(BeNil())
         Expect(written).To(Equal(""))
         Expect(werr).To(BeFalse())
         Expect(writeCount).To(Equal(0))
         Expect(counterValue(metrics.ReasonAutoInjectDisabled)).To(Equal(before + 1))
     })
     ```
   - Test 2 — non-UUID `task_identifier`: same shape; the fixture content is `"---\ntask_identifier: not-a-uuid\nstatus: in_progress\nassignee: claude\n---\n# body\n"`, relPath `non-uuid.md`.
   - Test 3 — duplicate `task_identifier`: pre-populate `v.hashes["other.md"] = fileEntry{taskIdentifier: lib.TaskIdentifier("11111111-1111-4111-8111-111111111111")}` before calling `processFile`. Fixture content is `"---\ntask_identifier: 11111111-1111-4111-8111-111111111111\nstatus: in_progress\nassignee: claude\n---\n# body\n"`, relPath `dup.md`. Same assertions as Test 1.
   - Re-use the `counterValue` helper that is already defined at the top of `vault_scanner_internal_test.go` (lines 18-34). Do NOT redeclare it.
   - For the `lib.TaskIdentifier` cast in Test 3, add the import `lib "github.com/bborbe/agent"` if not already present in the file's import block. Check first with `grep -n '"github.com/bborbe/agent"' pkg/scanner/vault_scanner_internal_test.go`.
   - The frozen warn-log substring is asserted only by source review (the literal `"AUTO_INJECT_TASK_IDENTIFIER=false; skipping task without valid task_identifier: %s"` appears verbatim three times in `vault_scanner.go` per Requirement 2 and is grepped end-to-end by the post-deploy AC4/AC5 probes against real `kubectl logs`). The unit tests intentionally do NOT capture glog output — the `SkippedFilesTotal(ReasonAutoInjectDisabled)` counter increment is the unit-level signal that the gate fired, and it is deterministic.

8. **Add one Ginkgo test for the `autoInject=true` parity behavior (AC2).**
   - File: same as Requirement 7. Place it inside the same `Describe("auto-inject flag gate (spec 001)", ...)` block.
   - Goal: exercise all three trigger sites with `autoInject: true` and assert `writeFile` is called exactly three times total (once per fixture) and the counter `ReasonAutoInjectDisabled` does NOT tick.
   - Skeleton:
     ```go
     It("injects UUIDs at all three trigger sites and does not tick the disabled counter when autoInject=true", func() {
         ctx := context.Background()
         var writeCount int
         dup := "11111111-1111-4111-8111-111111111111"
         fixtures := map[string][]byte{
             "empty-id.md":  []byte("---\nstatus: in_progress\nassignee: claude\n---\n# body\n"),
             "non-uuid.md":  []byte("---\ntask_identifier: not-a-uuid\nstatus: in_progress\nassignee: claude\n---\n# body\n"),
             "dup.md":       []byte("---\ntask_identifier: " + dup + "\nstatus: in_progress\nassignee: claude\n---\n# body\n"),
         }
         v := &vaultScanner{
             metrics: metrics.New(),
             hashes: map[string]fileEntry{
                 "other.md": {taskIdentifier: lib.TaskIdentifier(dup)},
             },
             ops: fileOps{
                 readFile: func(_ context.Context, relPath string) ([]byte, error) {
                     return fixtures[relPath], nil
                 },
                 writeFile: func(_ context.Context, _ string, _ []byte) error {
                     writeCount++
                     return nil
                 },
             },
             autoInject: true,
         }
         before := counterValue(metrics.ReasonAutoInjectDisabled)

         for _, relPath := range []string{"empty-id.md", "non-uuid.md", "dup.md"} {
             _, written, werr := v.processFile(ctx, relPath)
             Expect(werr).To(BeFalse(), "site %s: write error", relPath)
             Expect(written).To(Equal(relPath), "site %s: should have written", relPath)
         }
         Expect(writeCount).To(Equal(3))
         Expect(counterValue(metrics.ReasonAutoInjectDisabled)).To(Equal(before))
     })
     ```
   - This is a coarser parity assertion than the existing per-fixture tests at `vault_scanner_test.go:661/700/727` — those still run and assert the actual injected UUID shape. This test specifically asserts the *gate negative*: when `autoInject=true`, the disabled counter stays flat.

9. **Add one Ginkgo regression test for AC7 — `writeCounterReset` path is NOT gated.**
   - File: same as Requirement 7. Place it inside the same `Describe("auto-inject flag gate (spec 001)", ...)` block.
   - Skeleton:
     ```go
     It("does NOT gate the writeCounterReset path when autoInject=false (AC7)", func() {
         ctx := context.Background()
         var writeCount int
         taskID := "22222222-2222-4222-8222-222222222222"
         v := &vaultScanner{
             metrics: metrics.New(),
             hashes: map[string]fileEntry{
                 "parked.md": {
                     hash:           [32]byte{}, // any non-matching hash so the file looks "changed"
                     taskIdentifier: lib.TaskIdentifier(taskID),
                     assignee:       lib.TaskAssignee(""),
                 },
             },
             ops: fileOps{
                 readFile: func(_ context.Context, _ string) ([]byte, error) {
                     return []byte("---\ntask_identifier: " + taskID + "\nstatus: in_progress\nassignee: claude\n---\n# body\n"), nil
                 },
                 writeFile: func(_ context.Context, _ string, _ []byte) error {
                     writeCount++
                     return nil
                 },
             },
             autoInject: false,
         }
         beforeDisabled := counterValue(metrics.ReasonAutoInjectDisabled)

         _, written, werr := v.processFile(ctx, "parked.md")

         Expect(werr).To(BeFalse())
         Expect(written).To(Equal("parked.md"), "writeCounterReset write must have happened")
         Expect(writeCount).To(Equal(1))
         Expect(counterValue(metrics.ReasonAutoInjectDisabled)).To(Equal(beforeDisabled),
             "ReasonAutoInjectDisabled must NOT tick on the counter-reset path (AC7)")
     })
     ```
   - The fixture pre-populates `v.hashes["parked.md"]` with a valid `taskIdentifier` and EMPTY `assignee`, so the file's new content (same taskID, non-empty `assignee`) triggers the empty→named-assignee branch at `vault_scanner.go:275` (the `writeCounterReset` call) regardless of `autoInject`. The test asserts the write happened AND the disabled-counter did not tick.

10. **Do NOT modify `/workspace/mocks/vault_scanner.go`.** Prompt 1 did NOT change the public `VaultScanner` interface (`Run`, `RunCycle` are unchanged); the existing counterfeiter mock is still valid.

11. **Do NOT add a `CHANGELOG.md` entry.** Spec Non-goals bullet (verbatim quote): *"Do NOT add a `CHANGELOG.md` entry — this repo has no `CHANGELOG.md` (verified)."*

</requirements>

<constraints>

- Spec Constraints bullet (verbatim quote): *"Existing scanner public API and existing `vault_scanner_test.go` / `vault_scanner_internal_test.go` fixtures for the three trigger sites must continue to pass when the flag is `true` (parity is the regression bar)."*
- Spec Constraints bullet (verbatim quote): *"The warn log substring `AUTO_INJECT_TASK_IDENTIFIER=false; skipping task without valid task_identifier:` is frozen — AC4 greps for it."*
- Spec Constraints bullet (verbatim quote): *"The return shape for a flag-disabled skip must be exactly `(nil, "", false)` — matching the existing skip paths at lines 240–256; downstream code already handles this shape and must not need changes."*
- Spec Failure Modes row "Pod has `AUTO_INJECT_TASK_IDENTIFIER=false` and operator drops a UUID-less task file" → covered by Requirement 7 Tests 1+2+3 + the counter assertion in each.
- Spec Failure Modes row "Pod has `AUTO_INJECT_TASK_IDENTIFIER=false` and observes a duplicate-UUID file" → covered by Requirement 7 Test 3.
- Spec Desired Behavior bullet (verbatim quote): *"The flag has no effect on the unrelated `writeCounterReset` path (empty→named assignee transition at line 275) — that path does not mint a `task_identifier` and is out of scope."* → covered by Requirement 9.
- Per `go-error-wrapping-guide.md`: never use `fmt.Errorf`. (The gate paths do not return errors, but if any test helper does, use `errors.Errorf(ctx, ...)`.)
- Per `go-testing-guide.md`: internal-test package is required here because the gate-test fixtures construct `&vaultScanner{...}` literally with the private `autoInject` field set.
- Per `go-precommit.md`: file-level lint limits apply. `processFile` already carries `//nolint:funlen` at line 222. Inlining the gate adds ~21 lines to `processFile` (3 sites × 7 lines each); if the linter complains, extend the existing nolint comment with the additional reason rather than removing it, e.g. `//nolint:funlen // +5 statements from spec-043 + 21 lines from spec-001 per-site auto-inject gate; inlined per spec-001 prompt 2 to keep the parity-check awk range honest`.
- Do NOT commit — dark-factory handles git.

</constraints>

<verification>

Run iteratively while implementing:

```
cd /workspace && go build ./...
cd /workspace && go test ./pkg/metrics/... -v
cd /workspace && go test ./pkg/scanner/... -v
```

Run ONCE at the end:

```
cd /workspace && make precommit
```

Expected new test names in `go test -v` output:
- `auto-inject flag gate (spec 001) > skips the empty-task_identifier site without writing when autoInject=false` (AC3 site 1)
- `auto-inject flag gate (spec 001) > skips the non-UUID-task_identifier site without writing when autoInject=false` (AC3 site 2)
- `auto-inject flag gate (spec 001) > skips the duplicate-task_identifier site without writing when autoInject=false` (AC3 site 3)
- `auto-inject flag gate (spec 001) > injects UUIDs at all three trigger sites and does not tick the disabled counter when autoInject=true` (AC2)
- `auto-inject flag gate (spec 001) > does NOT gate the writeCounterReset path when autoInject=false (AC7)` (AC7)

The pre-existing parity-check test name continues to exist with its updated `Equal(9)` expectation:
- `maintains counter-call parity with skip-site log lines (AC#6 invariant ...)`

Existing tests that MUST continue to pass unchanged in body (already updated by prompt 1's call-site edits):
- `UUID injected when task_identifier absent` (`vault_scanner_test.go:661`)
- `non-UUID task_identifier is replaced with generated UUID` (`vault_scanner_test.go:700`)
- `duplicate task_identifier across files is replaced with fresh UUID` (`vault_scanner_test.go:727`)
- `valid unique UUID task_identifier is preserved unchanged` (`vault_scanner_test.go:750`)
- All `SkippedFilesTotal counter > ...` tests
- The `injectAndStore` increments-counter test at `vault_scanner_internal_test.go:39`

</verification>
