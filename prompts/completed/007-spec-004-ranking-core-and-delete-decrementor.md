---
status: completed
spec: [004-recurring-task-supersede-scan-collapse]
summary: Replaced period-token decrementor with pure ordinal ranking core — parsePeriodTokenOrdinal returns Unix-ordinal for all six recurrence kinds (correct across ISO-week and year boundaries); rankSameSlugCandidatesDescending sorts same-slug candidates most-recent-first; obsolete decrementor module and tests deleted; transitional nolint stub keeps hook a no-op until prompt 2 wires the scan
execution_id: agent-task-controller-weekday-collapse-exec-007-spec-004-ranking-core-and-delete-decrementor
dark-factory-version: v0.191.0
created: "2026-07-08T09:10:00Z"
queued: "2026-07-08T11:18:41Z"
started: "2026-07-08T11:31:20Z"
completed: "2026-07-08T11:37:58Z"
branch: dark-factory/recurring-task-supersede-scan-collapse
---

<summary>

- Replaces the old "decrement one period token" arithmetic with a pure, comparable ORDINAL for any period token, so same-schedule instances can be ranked most-recent-first.
- The ordinal orders correctly across ISO-week and year boundaries (e.g. `2026W01` ranks after `2025W52`) and across all six recurrence kinds (Daily / Weekday / Weekly / Monthly / Quarterly / Yearly).
- Adds a pure ranking helper that sorts a set of same-slug candidate titles into most-recent-first order using that ordinal, ignoring titles whose token matches no known shape.
- Reuses the existing six token regexes; introduces no new recurrence kinds and no I/O.
- Deletes the now-obsolete single-prior "decrementor" module and its test, and the test-only export of it, so nothing in the tree computes a single arithmetic predecessor anymore.
- No production behavior changes yet — the scan-and-collapse hook that consumes this ranking core lands in prompt 2.

</summary>

<objective>

Add a pure, dependency-free ranking core in `pkg/command/` that (a) parses any recurring-task period token into a comparable ordinal that sorts correctly across ISO-week and year boundaries for all six recurrence kinds, and (b) ranks a set of same-slug candidate titles most-recent-first by that ordinal. Delete the obsolete single-prior decrementor module, its test, and its test-only export. The hook in prompt 2 consumes this ranking core.

</objective>

<context>

Read `/workspace/CLAUDE.md` for project conventions.
Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-patterns.md` for interface/constructor/error-wrapping/counterfeiter conventions.
Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` for `errors.Errorf(ctx, ...)` / `errors.Wrapf(ctx, err, ...)` from `github.com/bborbe/errors` (never `fmt.Errorf`, never `context.Background()` inside `pkg/`).
Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` for the Ginkgo/Gomega + table-driven layout and external-test-package (`package command_test`) convention.
Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-precommit.md` for lint limits (funlen 80, nestif 4, golines 100).

Read `/workspace/pkg/command/period_token_decrementor.go` IN FULL. This is the file being replaced. It already contains the six package-scope regexes you MUST reuse verbatim (`dailyRe`, `weekdayRe`, `weeklyRe`, `monthlyRe`, `quarterlyRe`, `yearlyRe`) and the correct ISO-week helper `decrementISOWeek(isoYear, week int) (int, int)` whose Monday-of-ISO-week-1 arithmetic you will mirror when building the ordinal. The functions `decrementRecurringTaskTitle`, `decrementPeriodToken`, and `decrementISOWeek` in this file are being DELETED (the ordinal replaces decrement).

Read `/workspace/pkg/command/period_token_decrementor_test.go` IN FULL — this test file is DELETED wholesale (it tests the deleted decrement functions).

Read `/workspace/pkg/command/export_test.go` IN FULL. It contains exactly one line: `var DecrementRecurringTaskTitle = decrementRecurringTaskTitle`. This export is DELETED (the symbol it re-exports is gone). Replace it with the new ranking-core test seams described below.

Read `/workspace/pkg/command/command_suite_test.go` and the import block of `/workspace/pkg/command/task_create_task_executor_test.go` (lines 1-26) for the exact `package command_test` dot-import style.

Read `/workspace/pkg/command/task_create_task_executor.go` lines 205-257 to see how `supersedePriorRecurringTask` currently calls `decrementRecurringTaskTitle(ctx, cmd.Title)`. NOTE: this call is the ONLY production caller of the decrementor. It is rewritten in prompt 2; for THIS prompt you must keep the package compiling. See Requirement 6 for the exact transitional step.

Contract recap (implement verbatim) — the ordinal must be a single monotonically-increasing integer such that a more-recent period yields a strictly larger ordinal than any earlier period, across ALL six kinds. Because a vault's same-slug candidates are always the SAME recurrence kind (one schedule = one kind), the ordinal only needs to order correctly WITHIN a kind; cross-kind comparability is not required. The boundary cases that MUST order correctly:
- Weekly / Weekday: `2025W52` < `2026W01` (ISO-week + year boundary).
- Quarterly: `2025Q4` < `2026Q1`.
- Monthly: `2025-12` < `2026-01`.
- Daily: `2025-12-31` < `2026-01-01`.
- Yearly: `2025` < `2026`.

</context>

<requirements>

1. **Create `/workspace/pkg/command/period_token_ranking.go`** in `package command` with the standard 3-line BSD license header (copy from `/workspace/pkg/command/task_create_task_executor.go` lines 1-3).

2. **Move the six regexes and the ISO-week helper into this new file.** Copy the `var ( dailyRe ... yearlyRe )` block (lines 18-26 of `period_token_decrementor.go`) and the `decrementISOWeek` function (lines 105-125) verbatim into `period_token_ranking.go`. These are the only symbols preserved from the old file. Rename `decrementISOWeek` to `isoWeekMonday(isoYear, week int) time.Time` and change it to return the Monday `time.Time` of the given ISO week (NOT the prior week) — i.e. drop the final `.AddDate(0,0,-7)` and the final `.ISOWeek()`; return `mondayOfTargetWeek`. You need the Monday date to build a comparable ordinal. Keep the Jan-4-is-in-ISO-week-1 arithmetic.

3. **Implement the ordinal parser.** Define an unexported function with this exact signature:
   ```go
   // parsePeriodTokenOrdinal parses a single period-token string into a
   // monotonically-increasing ordinal: a more-recent period yields a strictly
   // larger ordinal than any earlier period of the same recurrence kind.
   // Recognition order matches docs/period-token-semantics.md (Daily, Weekday,
   // Weekly, Monthly, Quarterly, Yearly). Returns an error if the token matches
   // no recognized shape (caller must skip such candidates).
   func parsePeriodTokenOrdinal(ctx context.Context, token string) (int64, error)
   ```
   Recognition order and ordinal rules (try in this exact order; each regexp is anchored `^...$` so shapes are mutually exclusive):
   1. **Daily** — `dailyRe`. Parse the date with `time.Parse("2006-01-02", token)`; on parse error return the wrapped error. Ordinal = `t.UTC().Unix()`.
   2. **Weekday (list)** — `weekdayRe`. Groups: `m[1]`=ISO year, `m[2]`=ISO week, `m[3]`=abbrev. Ordinal = `isoWeekMonday(isoYear, week).UTC().Unix()`. The abbrev is IGNORED for ordering (all weekdays of the same week rank together by their week's Monday; ties are broken deterministically by the caller — see Requirement 4's stable sort). This is intentional and matches the spec's weekday-set-agnostic collapse.
   3. **Weekly** — `weeklyRe`. Ordinal = `isoWeekMonday(isoYear, week).UTC().Unix()`.
   4. **Monthly** — `monthlyRe`. Groups `m[1]`=year, `m[2]`=month. Ordinal = `time.Date(year, time.Month(month), 1, 0,0,0,0, time.UTC).Unix()`.
   5. **Quarterly** — `quarterlyRe`. Groups `m[1]`=year, `m[2]`=quarter `1..4`. Ordinal = `time.Date(year, time.Month((q-1)*3+1), 1, 0,0,0,0, time.UTC).Unix()` (first month of the quarter).
   6. **Yearly** — `yearlyRe`. Ordinal = `time.Date(year, time.January, 1, 0,0,0,0, time.UTC).Unix()`.
   - If NONE match, return `0, errors.Errorf(ctx, "period token %q matches no recognized shape", token)`.
   - Use `strconv.Atoi` on the capture groups (mirroring the old file); the regexes guarantee digit-only groups so the error branch is unreachable, but wrap any returned parse error with `errors.Wrapf(ctx, err, ...)` for defense-in-depth (do not ignore it with `_`).

4. **Implement the ranked-candidate helper.** Define an unexported struct and function:
   ```go
   // rankedCandidate pairs a candidate title with its parsed period-token ordinal.
   type rankedCandidate struct {
       Title   string
       Ordinal int64
   }

   // splitTitleToken splits a recurring-task title into its slug prefix and
   // period-token suffix on the FINAL " - " separator. Returns ok=false when the
   // title has no " - <token>" suffix. The slug may itself contain " - ".
   func splitTitleToken(title string) (slug string, token string, ok bool)

   // rankSameSlugCandidatesDescending parses each candidate title's period-token
   // ordinal and returns the candidates whose token is a recognized shape, sorted
   // most-recent-first (largest ordinal first). Candidates whose token matches no
   // shape are dropped (logged at V(3)). The sort is stable; equal ordinals keep a
   // deterministic order by descending Title so redelivery is idempotent.
   func rankSameSlugCandidatesDescending(ctx context.Context, titles []string) []rankedCandidate
   ```
   - `splitTitleToken` uses `strings.LastIndex(title, " - ")`; if `< 0` return `"", "", false`. Otherwise `slug = title[:idx]`, `token = title[idx+len(" - "):]`, `ok = true`.
   - `rankSameSlugCandidatesDescending`: for each title, call `splitTitleToken`; if `!ok`, `glog.V(3).Infof("auto-supersede: candidate %q has no period-token suffix, skipping", title)` and skip. Otherwise call `parsePeriodTokenOrdinal(ctx, token)`; on error `glog.V(3).Infof("auto-supersede: candidate %q token unrecognized, skipping: %v", title, err)` and skip. Collect the successful ones into `[]rankedCandidate`.
   - Sort with `sort.SliceStable`: primary key `Ordinal` DESC; tie-breaker `Title` DESC (`a.Ordinal > b.Ordinal || (a.Ordinal == b.Ordinal && a.Title > b.Title)`). Import `"sort"`.
   - Return the sorted slice.

5. **Delete the obsolete files.**
   - Delete `/workspace/pkg/command/period_token_decrementor.go` (all its symbols are either moved to `period_token_ranking.go` — the regexes and ISO-week helper — or deleted — the three decrement functions).
   - Delete `/workspace/pkg/command/period_token_decrementor_test.go`.
   - Run `rm /workspace/pkg/command/period_token_decrementor.go /workspace/pkg/command/period_token_decrementor_test.go`.

6. **Keep the package compiling — transitional stub in the supersede hook.** The current production caller `supersedePriorRecurringTask` in `/workspace/pkg/command/task_create_task_executor.go` (line 222) calls `decrementRecurringTaskTitle`, which no longer exists. This prompt does NOT implement the scan (that is prompt 2), but the package MUST compile. Make the SMALLEST change that keeps it compiling and preserves today's single-prior behavior using the NEW primitives: replace the body of `supersedePriorRecurringTask` such that it derives the prior title via the ordinal core is NOT possible (the ordinal cannot decrement). Instead, gate the hook off cleanly: change the `decrementRecurringTaskTitle(ctx, cmd.Title)` call site so the function early-returns doing nothing, and delete the now-unreachable helper functions it called (`readPriorForSupersede`, `priorIsInProgress`, `transitionPrior`, `buildSupersedeModifyFn`) ONLY IF they become unused — but they are reused in prompt 2. To avoid churn, do the following minimal transitional edit and NOTHING more:
   - **CRITICAL — this repo's `make precommit` runs golangci-lint with `unused` + `staticcheck` + `unparam` ENABLED (`.golangci.yml`).** Leaving the four helper functions OR the hook's parameters unreferenced FAILS the final `make precommit` gate (`U1000 unused` on the helpers, `unparam` on the params) — the compiler passing is NOT sufficient (`go build`/`go test` go green; the failure surfaces only at `make precommit`). The four helpers MUST NOT be deleted (prompt 2 reuses them), so REFERENCE them in an interim block instead. In `supersedePriorRecurringTask`, keep the `if !isEligibleForSupersede(cmd) { return }` guard as the first statement, then replace everything after it (from `priorTitle, err := decrementRecurringTaskTitle(...)` through the closing of `transitionPrior(...)`, roughly lines 222-256) with this exact interim body:
     ```go
     // Scan-and-collapse lands in spec-004 prompt 2; until then this hook is a no-op.
     glog.V(3).Infof("auto-supersede: scan-and-collapse not yet wired for %s (spec-004 prompt 2)", cmd.TaskIdentifier)
     // Interim references so `unused`/`unparam` stay green while the scan (prompt 2)
     // is not yet wired. Prompt 2 rewrites this whole body and deletes this block.
     _, _, _, _, _ = ctx, gitClient, taskDir, currentDateTime, newRelPath
     _ = []any{readPriorForSupersede, priorIsInProgress, transitionPrior, buildSupersedeModifyFn}
     ```
     `_ = []any{...}` marks the four helpers as used (kills U1000); the blank multi-assign covers every otherwise-unused parameter (kills unparam). Everything lives inside the function, so prompt 2's full-body rewrite removes it with zero package-level residue.
   - Do NOT delete `readPriorForSupersede`, `priorIsInProgress`, `transitionPrior`, or `buildSupersedeModifyFn` — prompt 2 rewrites/reuses them. Their imports (`result`, `time`, `libtime`, etc.) stay referenced by these still-present helper bodies, so NO import removal is needed; run `go build ./pkg/command/...` and only touch an import the compiler actually reports as unused.
   - Delete the existing `Context("supersede prior recurring task", ...)` block in `/workspace/pkg/command/task_create_task_executor_test.go` (starts at line 432). Those tests assert the single-prior decrement behavior which this transitional stub disables; prompt 2 adds the scan-based tests. Remove the entire `Context` block (from line 432 `Context("supersede prior recurring task", func() {` through its matching closing `})`). Leave all other Contexts (malformed payload, collision, vault routing, etc.) untouched — they must still pass.

7. **Replace `/workspace/pkg/command/export_test.go`.** Overwrite its single line with test-only re-exports of the new ranking-core symbols (external `command_test` needs them):
   ```go
   // Copyright (c) 2026 Benjamin Borbe All rights reserved.
   // Use of this source code is governed by a BSD-style
   // license that can be found in the LICENSE file.

   package command

   // Test-only re-exports for the external command_test package.
   var (
       ParsePeriodTokenOrdinal            = parsePeriodTokenOrdinal
       RankSameSlugCandidatesDescending   = rankSameSlugCandidatesDescending
       SplitTitleToken                    = splitTitleToken
   )
   ```

8. **Create `/workspace/pkg/command/period_token_ranking_test.go`** in `package command_test` with table-driven Ginkgo specs. Import block mirrors `task_create_task_executor_test.go` lines 1-26 (dot-import ginkgo/gomega, `"context"`, `"github.com/bborbe/agent-task-controller/pkg/command"`). Cover:

   a. **Ordinal ordering within each kind (the boundary ACs).** A table asserting `command.ParsePeriodTokenOrdinal(ctx, older)` returns a value strictly less than `command.ParsePeriodTokenOrdinal(ctx, newer)`, both errors nil, for:
   - Weekly boundary: `2025W52` < `2026W01`.
   - Weekday boundary: `2025W52-mon` < `2026W01-mon`.
   - Weekly steady: `2026W26` < `2026W27`.
   - Quarterly boundary: `2025Q4` < `2026Q1`.
   - Monthly boundary: `2025-12` < `2026-01`.
   - Daily boundary: `2025-12-31` < `2026-01-01`.
   - Yearly: `2025` < `2026`.
   Assert with `Expect(olderOrd).To(BeNumerically("<", newerOrd))`.

   b. **Weekday-of-same-week tie.** `command.ParsePeriodTokenOrdinal(ctx, "2026W28-mon")` equals `command.ParsePeriodTokenOrdinal(ctx, "2026W28-fri")` (same week Monday) — assert `Equal`.

   c. **Unrecognized token → error.** `command.ParsePeriodTokenOrdinal(ctx, "notaperiod")` returns a non-nil error and ordinal `0`.

   d. **`splitTitleToken` cases.** `command.SplitTitleToken("Aquascape PWC - 2026W27")` → slug `Aquascape PWC`, token `2026W27`, ok `true`. `command.SplitTitleToken("Foo - Bar - 2026W27")` → slug `Foo - Bar`, token `2026W27`, ok `true` (LastIndex). `command.SplitTitleToken("NoSeparator")` → ok `false`.

   e. **`rankSameSlugCandidatesDescending` ordering + cross-year.** Given input titles (deliberately shuffled)
      `["IBKR Swing Trading - 2025W52", "IBKR Swing Trading - 2026W02", "IBKR Swing Trading - 2026W01", "IBKR Swing Trading - notatoken"]`,
      assert the returned slice has length 3 (the unrecognized one dropped) and the `.Title` order is `2026W02`, `2026W01`, `2025W52` (most-recent-first, correct across the year boundary).

   f. **Weekday tie determinism.** Given `["Sched - 2026W28-mon", "Sched - 2026W28-wed", "Sched - 2026W28-fri"]` (all same week), assert the result length is 3 and the order is deterministic and stable (tie-break by descending Title → `-wed`, `-mon`, `-fri`? compute the actual descending-string order and assert it; the point is a fixed, reproducible order — assert the exact slice the tie-break rule produces). Document the expected order in a comment derived from descending string comparison of the full titles.

   Use `ctx := context.Background()` in the test suite.

</requirements>

<constraints>

- Reuse the existing six regexes verbatim — do NOT add new recurrence kinds, new token shapes, opt-out flags, config, or tunable thresholds. Spec Non-goal (verbatim): *"Do NOT add a per-schedule opt-out of the collapse behavior."*
- The ranking core has ZERO I/O — no git, no Kafka, no filesystem, no clock. Pure functions over strings.
- Per `go-error-wrapping-guide.md`: `errors.Errorf(ctx, ...)` / `errors.Wrapf(ctx, err, ...)` only — never `fmt.Errorf`, never `context.Background()` inside `pkg/`.
- Per `go-precommit.md`: funlen 80 / nestif 4 / golines 100. If `parsePeriodTokenOrdinal` approaches 80 lines, extract per-kind helpers.
- Compile regexps at package scope (moved block), not inside functions.
- Do NOT implement the scan/list logic in this prompt — that is prompt 2. This prompt only produces the pure ranking core and removes the decrementor.
- The transitional stub in `supersedePriorRecurringTask` (Requirement 6) makes the hook a no-op; do NOT try to preserve single-prior behavior — prompt 2 restores real behavior via the scan.
- Do NOT delete `isEligibleForSupersede`, `readPriorForSupersede`, `priorIsInProgress`, `transitionPrior`, or `buildSupersedeModifyFn` — prompt 2 reuses/rewrites them.
- Existing non-supersede tests in `/workspace/pkg/command/` must still pass.
- Do NOT add a `CHANGELOG.md` entry in this prompt — the CHANGELOG entry lands in prompt 3 (single entry for the whole feature).
- Do NOT commit — dark-factory handles git.

</constraints>

<verification>

Run iteratively while implementing:

```
cd /workspace && go build ./pkg/command/...
cd /workspace && go test ./pkg/command/... -v
```

Decrementor fully removed (spec AC):

```
cd /workspace && grep -rn "decrementRecurringTaskTitle\|decrementPeriodToken\|period_token_decrementor" pkg/
```
Expect ZERO matches (exit 1).

Ranking core present:

```
cd /workspace && grep -rn "parsePeriodTokenOrdinal\|rankSameSlugCandidatesDescending" pkg/command/period_token_ranking.go
```
Expect ≥2 lines.

Run ONCE at the end:

```
cd /workspace && make precommit
```

Expected: exit 0; ordinal boundary table rows pass (`2025W52` < `2026W01`, `2025Q4` < `2026Q1`, `2025-12` < `2026-01`, `2025-12-31` < `2026-01-01`); `rankSameSlugCandidatesDescending` returns most-recent-first across the year boundary; unrecognized tokens are dropped; the decrementor grep returns nothing; all other create-task specs still pass.

</verification>
