---
status: draft
spec: [001-scanner-auto-inject-flag]
created: "2026-06-27T21:30:00Z"
branch: dark-factory/scanner-auto-inject-flag
---

<summary>

- The vault scanner now consults the `autoInject` boolean (wired in prompt 1) at its three injection trigger sites.
- When `autoInject=false`, a UUID-less / non-UUID / duplicate-UUID task file is skipped with a warn log line; `writeFile` is not called; `processFile` returns `(nil, "", false)` — matching the existing "invalid frontmatter" skip shape.
- The warn log line carries the frozen substring `AUTO_INJECT_TASK_IDENTIFIER=false; skipping task without valid task_identifier: <relPath>` so operators can grep `kubectl logs` and immediately identify the gate as the cause.
- When `autoInject=true` (or unset, since the field defaults to `true`), behavior is byte-identical to today — the existing 13+ scanner tests continue to pass with no fixture changes.
- The unrelated `writeCounterReset` path (empty→named assignee transition at line 275) is NOT gated; prompt's existing assignment-transition tests still pass.
- A new `ReasonAutoInjectDisabled` constant is added to `pkg/metrics` and pre-initialized in `init()`, so the existing parity invariant ("every skip-site log line has a matching counter increment") continues to hold.
</summary>

<objective>

Gate the three `injectAndStore` trigger sites in `vaultScanner.processFile` on the `autoInject` flag added by prompt 1. When disabled, the scanner observes a UUID-less / non-UUID / duplicate-UUID file, emits the frozen-substring warn log, increments a new `ReasonAutoInjectDisabled` counter, and returns the standard skip shape `(nil, "", false)` without calling `writeFile`. Cover the behavior with three new Ginkgo tests: flag=true (parity), flag=false (skip), and an `autoInject=false` regression test on the `writeCounterReset` path to pin the spec's AC7 invariant.

</objective>

<context>

Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` for Ginkgo/Gomega + counterfeiter mock patterns.
Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` for `errors.Errorf` usage.
Read `/workspace/pkg/scanner/vault_scanner.go` end-to-end. The three trigger sites are at lines 261 (empty `taskID`), 264 (non-UUID), 268 (duplicate UUID). They each look like `return v.injectAndStore(ctx, ..., relPath, currentFMAssignee)`. The `writeCounterReset` path is at line 275 — DO NOT touch it.
Read `/workspace/pkg/scanner/vault_scanner_test.go` end-to-end. Existing tests at lines 661 ("UUID injected when task_identifier absent"), 700 ("non-UUID task_identifier is replaced..."), 727 ("duplicate task_identifier across files...") already exercise the inject path with the default `autoInject=true` — they MUST continue to pass with no changes.
Read `/workspace/pkg/scanner/vault_scanner_internal_test.go` (the existing internal-test file) for the `&vaultScanner{...}` literal pattern used to construct a scanner without going through the public constructor. Prompt 1 added the `autoInject` field to the struct; tests that construct `&vaultScanner{...}` literally will get `autoInject: false` by Go's zero-value unless they explicitly set it to `true`. **Important**: every existing `&vaultScanner{...}` literal in `vault_scanner_test.go` (external test package, line 39) and `vault_scanner_internal_test.go` (internal, line 39) MUST be updated to add `autoInject: true` so they exercise the parity case. Search and update them all.
Read `/workspace/pkg/metrics/metrics.go` lines 147–210 to understand the `Reason*` constants and `init()` pre-initialization. A new constant `ReasonAutoInjectDisabled = "auto_inject_disabled"` follows the same naming.
Read the parity-check test at `/workspace/pkg/scanner/vault_scanner_test.go` lines 1035–1058. It asserts `skipCount == 6` and `counterCount == 6`. After this prompt adds three new "skipping" warn lines AND three matching `SkippedFilesTotal(...)` calls, the counts both become 9. The test's `skipCount` regex (`glog.Warningf("skipping`) / `glog.Errorf("skipping`) / `glog.Warningf("failed to read`) ) MUST be updated to expect 9.

</context>

<requirements>

1. **Add the new reason constant in `/workspace/pkg/metrics/metrics.go`.**
   - In the `const ( ... )` block at line 147, add `ReasonAutoInjectDisabled = "auto_inject_disabled"` as a new entry.
   - In the `init()` loop at lines 201–208 that pre-initializes counters, add `ReasonAutoInjectDisabled` to the slice so `prometheus.DefaultGatherer` exposes it from boot.
   - Update the package doc comment on `SkippedFilesTotal` (line 159) to mention "auto-inject disabled" as a labeled reason — minor edit, not load-bearing.

2. **Gate the three injection trigger sites in `/workspace/pkg/scanner/vault_scanner.go`.**
   - The three sites are at lines 261 (empty `taskID`), 264 (non-UUID), 268 (duplicate UUID). Currently they are:
     ```go
     if taskID == "" {
         return v.injectAndStore(ctx, content, relPath, currentFMAssignee)
     }
     if !isValidUUID(taskID) {
         glog.Warningf("replacing non-UUID task_identifier %q in %s", taskID, relPath)
         return v.injectAndStore(ctx, removeTaskIdentifier(content), relPath, currentFMAssignee)
     }
     if !v.isIdentifierUnique(taskID, relPath) {
         glog.Warningf("replacing duplicate task_identifier %q in %s", taskID, relPath)
         return v.injectAndStore(ctx, removeTaskIdentifier(content), relPath, currentFMAssignee)
     }
     ```
   - Wrap each `return v.injectAndStore(...)` so that when `v.autoInject == false`, the call is replaced with a gate:
     ```go
     if !v.autoInject {
         glog.Warningf(
             "AUTO_INJECT_TASK_IDENTIFIER=false; skipping task without valid task_identifier: %s",
             relPath,
         )
         v.metrics.SkippedFilesTotal(metrics.ReasonAutoInjectDisabled).Inc()
         return nil, "", false
     }
     return v.injectAndStore(...)
     ```
   - Extract this gate into a private helper method on `*vaultScanner` to avoid duplicating the warn log + counter increment at three sites:
     ```go
     func (v *vaultScanner) skipAutoInjectDisabled(relPath string) (*lib.Task, string, bool) {
         glog.Warningf(
             "AUTO_INJECT_TASK_IDENTIFIER=false; skipping task without valid task_identifier: %s",
             relPath,
         )
         v.metrics.SkippedFilesTotal(metrics.ReasonAutoInjectDisabled).Inc()
         return nil, "", false
     }
     ```
     Place it after `processFile` (around line 313). Then call it from the three trigger sites:
     ```go
     if taskID == "" {
         if !v.autoInject {
             return v.skipAutoInjectDisabled(relPath)
         }
         return v.injectAndStore(ctx, content, relPath, currentFMAssignee)
     }
     if !isValidUUID(taskID) {
         if !v.autoInject {
             return v.skipAutoInjectDisabled(relPath)
         }
         glog.Warningf("replacing non-UUID task_identifier %q in %s", taskID, relPath)
         return v.injectAndStore(ctx, removeTaskIdentifier(content), relPath, currentFMAssignee)
     }
     if !v.isIdentifierUnique(taskID, relPath) {
         if !v.autoInject {
             return v.skipAutoInjectDisabled(relPath)
         }
         glog.Warningf("replacing duplicate task_identifier %q in %s", taskID, relPath)
         return v.injectAndStore(ctx, removeTaskIdentifier(content), relPath, currentFMAssignee)
     }
     ```
   - The `glog.Warningf("replacing non-UUID...")` and `glog.Warningf("replacing duplicate...")` lines are at the top of their respective `if` blocks today; they were always emitted regardless of `autoInject` because they only log, not act. They SHOULD remain — they fire AFTER the gate passes (`autoInject==true`) and describe the rewrite. Move them inside the `else`/post-gate branch if necessary. The simplest pattern is to leave the existing log line where it is and add the gate check above the `injectAndStore` call — see the snippet above.
   - Preserve the order: the empty-`taskID` site at line 261 does not log before injecting (no "replacing..." line). The non-UUID and duplicate sites do log. Match the existing structure.

3. **Do NOT touch the `writeCounterReset` path at line 275.** Spec Constraint #6 pins this. AC7 is the regression test.

4. **Do NOT touch the existing skip sites at lines 230, 240, 246, 255, 300, 327.** Spec Constraint #51 ("existing public API and existing fixtures must continue to pass when flag is true").

5. **Update the parity-check test at `/workspace/pkg/scanner/vault_scanner_test.go` lines 1035–1058.**
   - Change `Expect(skipCount).To(Equal(6), ...)` to `Expect(skipCount).To(Equal(9), ...)`.
   - Change `Expect(counterCount).To(Equal(6), ...)` to `Expect(counterCount).To(Equal(9), ...)`.
   - Update the comment to reflect: "expected 9 skip-site log lines (3 existing + 1 write-failed + 1 read-failed + 1 empty-status + 3 auto-inject-disabled sites that share a single `skipAutoInjectDisabled` helper counted 3 times because the regex matches `glog.Warningf("skipping` per call site)" — or similar.
   - Verify by counting: the existing 6 sites are `glog.Warningf("skipping`, `glog.Errorf("skipping`, `glog.Warningf("failed to read` (the regex `glog.Warningf("skipping` / `glog.Errorf("skipping` / `glog.Warningf("failed to read` counts the SOURCE occurrences in the `processFile`/`injectAndStore` function bodies). Adding the gate adds 3 new `glog.Warningf("skipping` lines (one per trigger site via `skipAutoInjectDisabled`). The helper itself contains ONE source occurrence, but the test only counts in `processFile`/`injectAndStore` function bodies. So the three call sites add 3 to the count, bringing it to 9.
   - Update the expected message: `"expected 9 skip-site log lines"`.

6. **Update existing `&vaultScanner{...}` literals in test files.**
   - In `/workspace/pkg/scanner/vault_scanner_test.go` line 39 (inside the `It("increments inject_task_identifier_failed...")` block) and `/workspace/pkg/scanner/vault_scanner_internal_test.go` line 39 (the only `&vaultScanner{...}` literal in that file), add `autoInject: true` to the struct literal. Without this, the test scanner will have `autoInject == false` (Go zero value) and the existing inject test will fail because it expects the UUID to be injected (which won't happen when gated off).
   - Search `/workspace/pkg/scanner/` for `&vaultScanner{` and update every occurrence to include `autoInject: true`.

7. **Add a new Ginkgo test for the flag=true parity case (AC2).**
   - File: `/workspace/pkg/scanner/vault_scanner_internal_test.go` (internal test package has access to the unexported `vaultScanner` and the `autoInject` field; external test package does not).
   - Test name: `It("injects UUID at all three trigger sites when autoInject=true (parity)", func() {...})`.
   - Place it inside a new `Describe("autoInject flag", func() { ... })` block.
   - Construct `&vaultScanner{metrics: metrics.New(), ops: fileOps{...}, autoInject: true}` where `fileOps` has a `readFile` returning the fixture content and a `writeFile` mock that records calls and the bytes written.
   - Feed three fixtures via `processFile(ctx, relPath)`:
     - Empty `task_identifier`: `"---\nstatus: todo\nassignee: claude\n---\n# body"`, relPath `empty-id.md`.
     - Non-UUID: `"---\ntask_identifier: not-a-uuid\nstatus: todo\nassignee: claude\n---\n# body"`, relPath `non-uuid.md`.
     - Duplicate UUID (pre-populate `v.hashes` with another entry holding the same UUID): prepopulate `v.hashes["other.md"] = fileEntry{taskIdentifier: "11111111-1111-4111-8111-111111111111"}`, feed `"---\ntask_identifier: 11111111-1111-4111-8111-111111111111\nstatus: todo\nassignee: claude\n---\n# body"` at relPath `dup.md`.
   - Assertions for each fixture:
     - `processFile` returns `(non-nil task or nil, non-empty writtenRelPath == relPath, false)` — `writeFile` was called exactly once for this file.
     - The bytes captured by `writeFile` contain a fresh UUID `task_identifier:` line matching the regex `^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$` (same regex as the existing test at line 697).
   - Use a closure-captured `writeCount int` and `var writtenContent []byte` in the test to track calls. The existing pattern in `vault_scanner_test.go` uses real file I/O; the internal-test pattern can use mock closures.

8. **Add a new Ginkgo test for the flag=false skip case (AC3).**
   - File: same as step 7 (`vault_scanner_internal_test.go`).
   - Test name: `It("skips all three trigger sites when autoInject=false without writing", func() {...})`.
   - Construct `&vaultScanner{metrics: metrics.New(), ops: fileOps{readFile: returns fixture, writeFile: counts calls}, autoInject: false}`.
   - Feed the same three fixtures.
   - Assertions for each fixture:
     - `processFile` returns `(nil, "", false)` — the standard skip shape.
     - The captured `writeCount` for this fixture is `0`.
     - The captured glog buffer (see step 9) contains the substring `"AUTO_INJECT_TASK_IDENTIFIER=false; skipping task without valid task_identifier: "` followed by the fixture's `relPath`.
   - Also assert the `ReasonAutoInjectDisabled` counter increments by exactly 1 per fixture (3 increments total across the three fixtures).
   - Do this in three separate `It(...)` blocks — one per trigger site — so failure messages pinpoint which site regressed.

9. **Capture glog output to a buffer for the AC3 substring assertion.**
   - The existing test files do NOT capture glog. Add a `BeforeEach`/`AfterEach` in the new `Describe("autoInject flag", ...)` block that:
     - In `BeforeEach`: call `glog.SetOutput(buffer)` (where `buffer` is a `*bytes.Buffer` captured in a closure) — note: `glog.SetOutput` may not exist; the actual API is `glog.LogToStderr(false)` + redirecting stderr, OR `klog.SetLogger` in newer versions, OR a custom `glog.UsingLoggerFactory`.
   - **Verify the correct API first**: read `/home/node/go/pkg/mod/github.com/golang/glog@*/glog_file.go` (find the actual installed version via `grep -rn "func SetOutput\|func LogToStderr\|func SetLogger" /home/node/go/pkg/mod/github.com/golang/glog*`).
   - If `glog.SetOutput` exists: use it. If only `glog.LogToStderr(false)` exists (typical for `golang/glog`), redirect via `os.Pipe()` + goroutine reading stderr into a buffer. If neither, the test should set `flag.Set("logtostderr", "true")` and parse the test runner's stderr — fragile, prefer avoiding.
   - **Fallback if glog capture is unreliable**: instead of capturing glog, assert the `SkippedFilesTotal(ReasonAutoInjectDisabled)` counter increments (which is functionally equivalent to "the warn log fired" since the helper does both as one unit). Document in `## Improvements` that glog capture was non-trivial and may warrant a follow-up pattern doc.

10. **Add a new Ginkgo test for the `writeCounterReset` regression case (AC7).**
    - File: `/workspace/pkg/scanner/vault_scanner_internal_test.go`.
    - Test name: `It("writeCounterReset path is NOT gated by autoInject=false", func() {...})`.
    - Construct `&vaultScanner{metrics: metrics.New(), ops: fileOps{readFile: ..., writeFile: records calls}, autoInject: false, hashes: map[string]fileEntry{...}}`.
    - Pre-populate `v.hashes["parked.md"] = fileEntry{hash: hashOfPreviousContent, taskIdentifier: "valid-uuid-here", assignee: ""}` (simulating a previously-seen parked file).
    - Feed a new content with the same UUID but now non-empty assignee (`assignee: claude`) at relPath `parked.md`. The hash differs because content changed (assignee field changed).
    - Assertions:
      - `processFile` returns `(nil, relPath == "parked.md", false)` — the counter reset write happened, writeFile was invoked exactly once.
      - The captured `writeCount` is exactly `1`.
      - The `ReasonAutoInjectDisabled` counter did NOT increment (this is the AC7 regression bar — the gate must not affect `writeCounterReset`).

11. **Do NOT touch `mocks/vault_scanner.go`.** It was already regenerated by prompt 1 (which added `SetAutoInject`). The factory does not call any mock method beyond what's already there.

12. **Do NOT add a CHANGELOG entry.** Spec Non-goal #9.

</requirements>

<constraints>

- Spec Constraint #51 (existing fixtures must pass when flag is true) — verified by running the existing test suite unmodified after step 6's `autoInject: true` additions.
- Spec Constraint #53 (frozen log substring) — the warn log line in `skipAutoInjectDisabled` MUST contain exactly `"AUTO_INJECT_TASK_IDENTIFIER=false; skipping task without valid task_identifier: "` followed by the relPath. Do not reformat, do not add a newline, do not change "AUTO_INJECT_TASK_IDENTIFIER" to a different case.
- Spec Constraint #54 (return shape `(nil, "", false)` for a flag-disabled skip) — verified by `skipAutoInjectDisabled` returning those exact values.
- Spec Failure Mode table row "Pod has `AUTO_INJECT_TASK_IDENTIFIER=false` and operator drops a UUID-less task file" — covered by AC3 + the counter assertion.
- Per `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`: never `fmt.Errorf`. The `skipAutoInjectDisabled` helper does not return an error, but if it ever does, use `errors.Errorf(ctx, ...)`.
- Per `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`: external test packages use `package_test`, internal tests use the same package. Use internal tests here because the new helper `skipAutoInjectDisabled` is unexported and the test needs to construct `&vaultScanner{...}` literally with `autoInject: false`.
- Per `/home/node/.claude/plugins/marketplaces/coding/docs/go-precommit.md`: file-level linter limits (funlen 80, nestif 4, golines 100) apply to `vault_scanner.go`. The current file already has a `//nolint:funlen` on `processFile` at line 222; adding the gate will increase funlen further. Either extract the three gate checks into the helper (already done) and verify `processFile`'s funlen stays ≤80, or add a second `//nolint:funlen` annotation with a one-line reason.
- Do NOT commit — dark-factory handles git.

</constraints>

<verification>

Run iteratively while implementing:

```
cd /workspace && go test ./pkg/scanner/... -run TestSuite -v
cd /workspace && go test ./pkg/metrics/... -v
cd /workspace && go vet ./...
```

Run ONCE at the end:

```
cd /workspace && make precommit
```

Expected new test names in `go test -v` output:
- `autoInject flag > injects UUID at all three trigger sites when autoInject=true (parity)` (AC2)
- `autoInject flag > skips empty task_identifier site when autoInject=false without writing` (AC3, site 1)
- `autoInject flag > skips non-UUID task_identifier site when autoInject=false without writing` (AC3, site 2)
- `autoInject flag > skips duplicate UUID site when autoInject=false without writing` (AC3, site 3)
- `autoInject flag > writeCounterReset path is NOT gated by autoInject=false` (AC7)

Existing tests that MUST continue to pass unchanged in body (only the `&vaultScanner{...}` literal gains `autoInject: true`):
- `UUID injected when task_identifier absent` (line 661)
- `task published on second cycle after injection` (line 679)
- `non-UUID task_identifier is replaced with generated UUID` (line 700)
- `duplicate task_identifier across files is replaced with fresh UUID` (line 727)
- `valid unique UUID task_identifier is preserved unchanged` (line 750)
- All `SkippedFilesTotal counter > ...` tests (line 780 onward)

</verification>