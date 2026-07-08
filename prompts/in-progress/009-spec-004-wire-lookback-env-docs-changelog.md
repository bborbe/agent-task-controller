---
status: approved
spec: [004-recurring-task-supersede-scan-collapse]
created: "2026-07-08T09:12:00Z"
queued: "2026-07-08T11:18:41Z"
branch: dark-factory/recurring-task-supersede-scan-collapse
---

<summary>

- Makes the supersede look-back bound (how many prior instances the collapse scan inspects per materialize) operator-configurable via a controller env var, defaulting to 7.
- Threads that config value from the application arguments through the factory into the create-task executor, replacing the temporary hard-coded literal.
- Rewrites the period-token semantics doc from the old single-prior "decrement table" to the new scan-and-collapse contract (slug scoping, ranking, look-back bound, best-effort per-file).
- Records the behavior change in the changelog as the single feature entry.
- Updates the supersede scenario coverage under `scenarios/` to describe the collapse-to-one seam (missed-day gap and multi-stream weekday case).

</summary>

<objective>

Expose the supersede look-back bound K as a controller env var (`SUPERSEDE_LOOKBACK`, default 7), wired from the application config through the factory into the create-task executor (replacing the literal `7` placeholder from prompt 2). Rewrite `docs/period-token-semantics.md` to the scan-and-collapse contract, add the CHANGELOG entry, and update the supersede scenario coverage.

</objective>

<context>

Read `/workspace/CLAUDE.md` for project conventions.
Read `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` for CHANGELOG entry format (prefix required, specific, one bullet per logical change).
Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` for the Ginkgo argument-parse test style.

Read `/workspace/main.go` IN FULL. The `application` struct (lines 52-71) declares config fields using struct tags `required:"..." arg:"..." env:"..." usage:"..." default:"..."`. Note the existing `PollInterval time.Duration ... default:"60s"` (line 59) and `GitRestURL string ... default:"..."` (line 63) as the tag pattern to follow. The `factory.CreateCommandConsumer(...)` call is at line 163; prompt 2 inserted a literal `7` argument (with a `// TODO(spec-004 prompt 3): ...` comment) immediately AFTER `currentDateTime` and BEFORE `prCommenter`. You will replace that literal with the config field.

Read `/workspace/main_argument_parse_test.go` IN FULL for the exact `package main_test` argument-parse test harness (uses `libargument "github.com/bborbe/argument/v2"` and `libargument.Parse(ctx, &app)`, with `os.LookupEnv` / `os.Setenv` / `os.Unsetenv` save-restore). Your new default-value test mirrors this style.

Read `/workspace/main_internal_test.go` and `/workspace/main_test.go` for any existing full-struct parse test that would break when you add a field (a new field with a `default:` and `required:"false"` must not break existing parse tests).

Read `/workspace/pkg/factory/factory.go` IN FULL. After prompt 2, `CreateCommandConsumer` has a `k int` parameter (inserted after `currentDateTime` and before `prCommenter`) and passes it to `command.NewCreateTaskExecutor(..., k)`. This prompt does NOT change the factory signature — it only changes what `main.go` passes.

Read `/workspace/docs/period-token-semantics.md` IN FULL — this is the doc rewritten by this prompt. It currently documents a "Decrement Table" and "Shape Recognition Order" for the single-prior decrement model. The new model is scan-and-collapse: the recognition order stays (still used for the ordinal), but the "decrement direction" narrative is replaced by the scan contract.

Read `/workspace/scenarios/use-git-rest-for-vault-writes.md` for the scenario file format (frontmatter `status: draft`, `# Scenario:` heading, `## Setup` / `## Action` / `## Expected` / `## Cleanup` checklists). Read `/workspace/scenarios/001-result-writeback-happy-path.md` for a shorter scenario shape. Search for any EXISTING supersede scenario: `grep -rln "supersede\|collapse\|auto_abort_prior" /workspace/scenarios/`. If one exists, UPDATE it (add the collapse seam); if none exists, CREATE a new one per Requirement 4.

DEPENDENCY GATE: if `grep -n "k int" /workspace/pkg/factory/factory.go` returns no match, the factory has not been updated by prompt 2 — STOP and report `status: failed` with message `"look-back k not yet wired through factory (spec-004 prompt 2)"`. Do NOT add the factory parameter here.

</context>

<requirements>

1. **Add the `SupersedeLookback` config field to the `application` struct** in `/workspace/main.go`, after the existing fields (append after `GitHubToken` at line 70). Because `libargument`'s `default:` tag is parsed into the field type, use an `int` field with a string default:
   ```go
   SupersedeLookback int `required:"false" arg:"supersede-lookback" env:"SUPERSEDE_LOOKBACK" usage:"max number of most-recent prior same-schedule instances the auto-supersede scan inspects per materialize (look-back bound); older priors are left open by design" default:"7"`
   ```
   Align the struct tags with the surrounding fields' gofmt column style (gofmt/golines will reflow; do not hand-align beyond what gofmt produces).

2. **Guard against a non-positive value.** In `(a *application) Run(...)` in `/workspace/main.go`, near the other early validations (after the `AutoInjectTaskIdentifier` parse at lines 78-85), add:
   ```go
   if a.SupersedeLookback < 1 {
       return errors.Errorf(ctx, "SUPERSEDE_LOOKBACK must be >= 1, got %d", a.SupersedeLookback)
   }
   ```
   (A zero or negative look-back would silently disable all collapse — fail loud at startup instead.)

3. **Replace the literal `7` at the `CreateCommandConsumer` call site.** In `/workspace/main.go` (the `factory.CreateCommandConsumer(...)` call, ~line 163), replace the placeholder literal `7` argument (inserted by prompt 2, with the `// TODO(spec-004 prompt 3): ...` comment) with `a.SupersedeLookback`, and delete the TODO comment line. The argument position (after `currentDateTime`, before `prCommenter`) must match the factory's `k int` parameter position from prompt 2.

4. **Add a default-value argument-parse test** in a new file `/workspace/main_supersede_lookback_test.go` in `package main_test`, mirroring `main_argument_parse_test.go` style. Because the real `application` struct requires many fields, test against a minimal local shape carrying only the field under test:
   ```go
   type lookbackShape struct {
       SupersedeLookback int `required:"false" arg:"supersede-lookback" env:"SUPERSEDE_LOOKBACK" usage:"x" default:"7"`
   }
   ```
   - **Default applied when env unset:** save+unset `SUPERSEDE_LOOKBACK`, `libargument.Parse(ctx, &app)`, assert no error and `app.SupersedeLookback == 7`. Restore the env var in `AfterEach`.
   - **Env override honored:** set `SUPERSEDE_LOOKBACK=3`, parse, assert `app.SupersedeLookback == 3`. (This proves a non-default K flows through parsing — pairs with prompt 2's small-K bound test which proves a non-default K bounds the reads.)
   - Use the same `os.LookupEnv`/`os.Setenv`/`os.Unsetenv` save-restore discipline as `main_argument_parse_test.go`.

5. **Rewrite `/workspace/docs/period-token-semantics.md`** to the scan-and-collapse contract. Keep the file focused; the new content MUST include (grep-checkable per the ACs):
   - A short intro stating the recurrence kind is inferred from the period-token suffix shape (unchanged), and that on each eligible materialize the controller performs a **scan-and-collapse**: it lists same-slug instances, ranks them most-recent-first by a parsed period-token **ordinal**, and closes every still-open prior within a bounded **look-back** window of K instances (default 7).
   - A "Token Shapes and Ordinal" section keeping the six shapes and the recognition order (Daily, Weekday, Weekly, Monthly, Quarterly, Yearly), now described as feeding a comparable ordinal (a more-recent period → larger ordinal) rather than a decrement. Keep the boundary examples reframed as ordering facts: `2025W52` < `2026W01`, `2025Q4` < `2026Q1`, `2025-12` < `2026-01`, `2025-12-31` < `2026-01-01`.
   - A "Scan-and-Collapse Contract" section describing: slug = title before the final ` - `; new instance excluded from its own candidate set; only candidates strictly-older-or-same-week-sibling than the new instance are inspected; at most K (look-back bound, `SUPERSEDE_LOOKBACK`, default 7) most-recent candidates are read and closed; each closed prior gets the frozen transition (`status: aborted`, `phase: done`, `completed_date`, `superseded_by`, `created_by: recurring-task-creator`); best-effort per file (list/read/parse/write errors logged and swallowed); idempotent on redelivery (already-aborted priors skipped).
   - Remove the heading "Decrement Table" and the decrement-direction narrative entirely (the AC greps for its ABSENCE).
   - The words `scan`, `collapse`, and `look-back` MUST each appear at least once (AC grep target).
   - Do NOT reference `bborbe/recurring-task-creator` changes — the publisher and CRD are unchanged (spec Non-goal).

6. **Add the CHANGELOG entry** to `/workspace/CHANGELOG.md`. If a `## Unreleased` section does not exist, add one immediately below the top header block (above `## v0.1.1`). Add a single `feat:` bullet describing the change; the word `scan` MUST appear (AC grep target). Example:
   ```
   ## Unreleased

   - feat: Auto-supersede now scans same-slug recurring-task instances and collapses a schedule to a single open instance (closing every still-open prior within a K-instance look-back window, default 7, configurable via `SUPERSEDE_LOOKBACK`), replacing the single-prior period-token decrement; missed-day gaps and multi-stream weekday schedules self-heal.
   ```
   Follow `changelog-guide.md`: one bullet, specific (names the env var and behavior), prefix `feat:`.

7. **Update / create the supersede scenario under `/workspace/scenarios/`.** First run `grep -rln "supersede\|collapse\|auto_abort_prior" /workspace/scenarios/`.
   - If a supersede scenario file already exists, UPDATE it: add an Action/Expected pair proving the collapse-to-one seam (multiple open same-slug priors → one materialize → all priors within K close, new stays open), and ensure the words `collapse` and `supersede` both appear.
   - If none exists, CREATE `/workspace/scenarios/004-recurring-task-supersede-collapse.md` with frontmatter `status: draft` and the standard `# Scenario:` / `## Setup` / `## Action` / `## Expected` / `## Cleanup` structure. The scenario must encode the spec's live post-deploy check (this is the E2E evidence unit tests cannot reach — real git-rest + Kafka replay):
     - Setup: quant dev controller running with the recurring-task publisher; a schedule that fans out weekday streams (the `IBKR Swing Trading` case); `SUPERSEDE_LOOKBACK` visible in the pod env.
     - Action: replay `/trigger?date=2026-07-07` then `/trigger?date=2026-07-08` on the dev controller (mirroring the spec Verification's live steps), producing `IBKR Swing Trading - 2026W28-mon`, `-tue`, then `-wed`.
     - Expected: after the replay, `IBKR Swing Trading - 2026W28-mon` and `-tue` are `aborted` / `phase: done` with `superseded_by` set to the `-wed` instance's relPath, while `-wed` stays `in_progress`; the controller log shows two `auto-supersede: ... -> ...` lines. Include a Cleanup step. Ensure the words `collapse` and `supersede` both appear.
   - This satisfies the spec's scenario-coverage AC (`grep -rln "collapse\|supersede" scenarios/` returns ≥1).

8. **Sibling entry-point check.** Run `grep -rn "factory.CreateCommandConsumer" /workspace/ --include="*.go"` to confirm the single `main.go` caller is the only one and now passes `a.SupersedeLookback`. There is no `cmd/` variant. If grep reveals another caller, update it too or document in `## Improvements`.

</requirements>

<constraints>

- The look-back bound K is the ONLY new controller config knob this feature adds. Do NOT add a per-schedule opt-out, a refresh interval, or any other tunable. Spec Non-goal (verbatim): *"Do NOT add a per-schedule opt-out of the collapse behavior — collapse-to-one is the invariant this spec ships."*
- `SUPERSEDE_LOOKBACK` default is `7` (spec: "K default 7, configurable via a controller env var"). `required:"false"`, so existing deployments without the env var get the default.
- A non-positive `SUPERSEDE_LOOKBACK` fails startup with a clear error (Requirement 2) — do NOT silently clamp or disable collapse.
- Do NOT change the factory signature or the executor — prompt 2 already wired `k int` through them. This prompt only replaces what `main.go` passes and documents the behavior.
- Do NOT change `bborbe/recurring-task-creator`, the Schedule CRD, or the supersede output contract / eligibility gate — controller-only, doc-and-config-only for this prompt.
- Per `changelog-guide.md`: the CHANGELOG entry has a `feat:` prefix, is specific, and is a single logical bullet.
- Per `go-error-wrapping-guide.md`: the startup guard uses `errors.Errorf(ctx, ...)` (already the idiom in `main.go`'s `Run`).
- Do NOT commit — dark-factory handles git.

</constraints>

<verification>

Run iteratively while implementing:

```
cd /workspace && go build ./...
cd /workspace && go test . -v -run Lookback
cd /workspace && go test . ./pkg/... -v
```

Env-var wired with default 7 (spec AC):

```
cd /workspace && grep -n 'SUPERSEDE_LOOKBACK' main.go
cd /workspace && grep -n 'default:"7"' main.go
cd /workspace && grep -n 'a.SupersedeLookback' main.go
```
Expect: the struct field with `arg:"supersede-lookback"` + `env:"SUPERSEDE_LOOKBACK"` + `default:"7"`, and the `factory.CreateCommandConsumer` call passing `a.SupersedeLookback`.

Doc rewritten (spec AC):

```
cd /workspace && grep -n 'scan\|collapse\|look-back' docs/period-token-semantics.md
cd /workspace && grep -n 'Decrement Table' docs/period-token-semantics.md
```
Expect: first grep returns ≥1 line; second grep returns NOTHING (exit 1).

CHANGELOG (spec AC):

```
cd /workspace && grep -n 'scan' CHANGELOG.md
```
Expect ≥1 line under the top unreleased section.

Scenario (spec AC):

```
cd /workspace && grep -rln 'collapse\|supersede' scenarios/
```
Expect ≥1 file.

Run ONCE at the end:

```
cd /workspace && make precommit
```

Expected: exit 0; the lookback default + override parse tests pass; the doc/CHANGELOG/scenario greps above satisfy the ACs; all existing tests still pass.

</verification>
