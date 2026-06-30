---
status: completed
spec: ["002"]
summary: Added pure period-token decrementor helper in pkg/command/ recognizing 6 token shapes with correct boundary-case handling
execution_id: agent-task-controller-auto-supersede-exec-003-spec-002-period-token-decrementor
dark-factory-version: dev
created: "2026-06-30T10:00:00Z"
queued: "2026-06-30T09:46:00Z"
started: "2026-06-30T09:46:01Z"
completed: "2026-06-30T09:50:41Z"
---

<summary>

- Adds a pure date-arithmetic helper that, given a recurring-task title like `Aquascape PWC - 2026W27`, computes the title of the prior period (`Aquascape PWC - 2026W26`).
- Recognizes six recurrence kinds purely from the shape of the period-token suffix: Daily, Weekday-list, Weekly, Monthly, Quarterly, Yearly.
- Correctly wraps across year boundaries (e.g. quarter `2026Q1` becomes `2025Q4`, ISO week `2026W01` becomes `2025W52`).
- Returns a clear "unrecognized" signal when a title has no period-token suffix, so the caller can no-op instead of guessing.
- Has zero git, network, or Kafka dependencies — it is a self-contained function exercised by table-driven unit tests at multiple anchor dates including boundary cases.
- No production behavior changes yet; this helper is consumed by the supersede hook added in prompt 2.

</summary>

<objective>

Add a pure, dependency-free helper in `pkg/command/` that decrements the period-token suffix of a recurring-task instance title to produce the prior-period title, following the durable contract in `/workspace/docs/period-token-semantics.md`. The helper recognizes the recurrence kind from the token shape (six kinds) and handles year/quarter/week boundaries. It performs no I/O. Prompt 2 consumes it.

</objective>

<context>

Read `/workspace/CLAUDE.md` for project conventions.
Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-patterns.md` for error wrapping and naming conventions.
Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` for `errors.Errorf(ctx, ...)` usage (never `fmt.Errorf`, never `context.Background()` in `pkg/`).
Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` for the Ginkgo/Gomega + table-driven layout.

Read `/workspace/docs/period-token-semantics.md` in full — this is the durable contract this prompt implements. It defines the six token shapes, the decrement rules, the shape-recognition order, and the four named boundary cases. Implement EXACTLY this table; do not invent additional shapes.

Read `/workspace/pkg/command/task_increment_frontmatter_executor.go` (lines 119-151) for the package's helper-function style (plain unexported funcs, `errors.Wrapf(ctx, ...)`).

Read `/workspace/pkg/command/command_suite_test.go` — this is the Ginkgo suite bootstrap for `package command_test`. New `_test.go` files in this package use `package command_test` and the `. "github.com/onsi/ginkgo/v2"` / `. "github.com/onsi/gomega"` dot-imports (see `task_create_task_executor_test.go` lines 1-22 for the exact import block).

Contract recap from `/workspace/docs/period-token-semantics.md` (implement verbatim):

| Recurrence kind | Token shape (new) | Decrement rule |
|---|---|---|
| Daily | `YYYY-MM-DD` | one calendar day before |
| Weekday (list) | `YYYYWww-<3-letter-abbrev>` | one ISO week before; abbrev preserved (`2026W27-sat` → `2026W26-sat`) |
| Weekly | `YYYYWww` | one ISO week before (`2026W27` → `2026W26`) |
| Monthly | `YYYY-MM` | one calendar month before (`2026-06` → `2026-05`) |
| Quarterly | `YYYYQq` | one quarter before; wrap `Q1` → prior-year `Q4` |
| Yearly | `YYYY` | one calendar year before |

Shape-recognition order (try in this exact order): Daily, Weekday, Weekly, Monthly, Quarterly, Yearly.

Boundary cases that MUST pass: Quarterly `2026Q1 → 2025Q4`; Weekly `2026W01 → 2025W52`; Monthly `2026-01 → 2025-12`; Daily `2026-01-01 → 2025-12-31`; Weekday `2026W01-mon → 2025W52-mon`.

</context>

<requirements>

1. **Create `/workspace/pkg/command/period_token_decrementor.go`** in `package command` with the standard BSD license header (copy the 3-line header from `/workspace/pkg/command/task_create_task_executor.go` lines 1-3).

2. **Implement the title-splitting and decrement entry point.** Define an unexported function with this exact signature:
   ```go
   // decrementRecurringTaskTitle splits title into its slug prefix and period-token
   // suffix on the final " - " separator, decrements the token to the prior period
   // per docs/period-token-semantics.md, and returns "<slug> - <prior-token>".
   // It returns an error if the title has no " - <token>" suffix or the token suffix
   // matches none of the six recognized shapes (caller must no-op on error).
   func decrementRecurringTaskTitle(ctx context.Context, title string) (string, error)
   ```
   - Split on the LAST occurrence of the literal separator `" - "` (space-hyphen-space). Use `strings.LastIndex(title, " - ")`. If not found, return `"", errors.Errorf(ctx, "title %q has no period-token suffix", title)`.
   - The part before the separator is the slug; the part after is the raw token. Reconstruct as `slug + " - " + priorToken`.
   - Slug may itself contain `" - "` (e.g. a title `Foo - Bar - 2026W27` → slug `Foo - Bar`, token `2026W27`) — LastIndex handles this correctly.

3. **Implement the per-shape decrement.** Define an unexported function:
   ```go
   // decrementPeriodToken returns the prior-period token for a single period-token
   // string, recognizing the kind by shape (order per docs/period-token-semantics.md).
   func decrementPeriodToken(ctx context.Context, token string) (string, error)
   ```
   Recognition order and rules:
   1. **Daily** — `YYYY-MM-DD`. Match with `regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})$`)`. Parse with `time.Parse("2006-01-02", token)`, subtract one day via `t.AddDate(0, 0, -1)`, format back with `t.Format("2006-01-02")`.
   2. **Weekday (list)** — `YYYYWww-<3-letter-abbrev>`. Match `^(\d{4})W(\d{2})-([a-z]{3})$`. Decrement the `YYYYWww` ISO-week part by one ISO week (see step 3's algorithm) and re-append `-<abbrev>` unchanged. The abbrev is preserved verbatim.
   3. **Weekly** — `YYYYWww`. Match `^(\d{4})W(\d{2})$`. To decrement one ISO week:
      - Parse the ISO year and week ints from the two capture groups (`strconv.Atoi`).
      - Construct a `time.Time` for the Monday of that ISO week. Go's stdlib has no direct ISO-week constructor; compute it by: take `time.Date(isoYear, 1, 4, 0,0,0,0, time.UTC)` (Jan 4 is always in ISO week 1), find that week's Monday, then add `(week-1)*7` days. Subtract 7 days to move to the prior ISO week.
      - Read the prior week's canonical ISO year+week via `prior.ISOWeek()`, then format as `fmt.Sprintf("%04dW%02d", py, pw)`. This yields the correct `2026W01 → 2025W52` wrap because `ISOWeek()` returns the canonical ISO year.
   4. **Monthly** — `YYYY-MM`. Match `^(\d{4})-(\d{2})$`. Parse with `time.Parse("2006-01", token)`, subtract one month via `t.AddDate(0, -1, 0)`, format with `t.Format("2006-01")`. (`2026-01 → 2025-12` follows from `AddDate`.)
   5. **Quarterly** — `YYYYQq` where `q` is `1`-`4`. Match `^(\d{4})Q([1-4])$`. Decrement: if `q > 1` → `(year, q-1)`; if `q == 1` → `(year-1, 4)`. Format `fmt.Sprintf("%04dQ%d", year, q)`.
   6. **Yearly** — `YYYY`. Match `^(\d{4})$`. Parse year, subtract one, format `fmt.Sprintf("%04d", year-1)`.
   - If NONE match, return `"", errors.Errorf(ctx, "period token %q matches no recognized shape", token)`.
   - Compile each regexp once at package scope (`var dailyRe = regexp.MustCompile(...)` etc.) — do NOT compile inside the function (lint + perf).

4. **Ordering correctness for Daily vs Monthly vs Yearly.** Because `YYYY-MM-DD`, `YYYY-MM`, and `YYYY` are distinct anchored regexps (`^...$`), order matters only between overlapping shapes; the anchored patterns are mutually exclusive, so checking in the documented order is safe. Still, follow the documented order literally (Daily → Weekday → Weekly → Monthly → Quarterly → Yearly) so the code matches the contract doc one-to-one.

5. **Do NOT add path-separator rejection here.** Path-traversal defense on the computed prior token lives in the supersede hook (prompt 2), not in this pure helper. This helper only does date arithmetic on token strings.

6. **Create `/workspace/pkg/command/period_token_decrementor_test.go`** in `package command_test` with a table-driven Ginkgo spec. Because the test lives in an external package, the unexported `decrementRecurringTaskTitle` is not directly accessible — export a thin test seam OR test through the supersede hook. Use this approach: add an `export_test.go`-style seam. Create `/workspace/pkg/command/export_test.go` (if it does not already exist; check with `ls /workspace/pkg/command/export_test.go`) in `package command` exposing:
   ```go
   // Test-only re-exports for the external command_test package.
   var DecrementRecurringTaskTitle = decrementRecurringTaskTitle
   ```
   If `export_test.go` already exists, append the var to it instead of overwriting. Then the test calls `command.DecrementRecurringTaskTitle(ctx, title)`.

7. **Table-driven test rows.** Cover all six kinds at ≥2 anchor dates each, INCLUDING the boundary case for that kind, so a hardcoded lookup keyed on exact test strings cannot satisfy the test. Minimum rows (newTitle → expected priorTitle):
   - Daily mid-year: `Cleanup Inbox - 2026-06-15` → `Cleanup Inbox - 2026-06-14`
   - Daily year-boundary: `Cleanup Inbox - 2026-01-01` → `Cleanup Inbox - 2025-12-31`
   - Weekday mid-year: `Standup - 2026W27-sat` → `Standup - 2026W26-sat`
   - Weekday year-boundary: `Standup - 2026W01-mon` → `Standup - 2025W52-mon`
   - Weekly mid-year: `Aquascape PWC - 2026W27` → `Aquascape PWC - 2026W26`
   - Weekly ISO-week year-boundary: `Aquascape PWC - 2026W01` → `Aquascape PWC - 2025W52`
   - Monthly mid-year: `Pay Rent - 2026-06` → `Pay Rent - 2026-05`
   - Monthly year-boundary: `Pay Rent - 2026-01` → `Pay Rent - 2025-12`
   - Quarterly mid-year: `Quarterly Review - 2026Q2` → `Quarterly Review - 2026Q1`
   - Quarterly year-boundary: `Quarterly Review - 2026Q1` → `Quarterly Review - 2025Q4`
   - Yearly: `Annual Filing - 2026` → `Annual Filing - 2025`
   - Yearly boundary (decade): `Annual Filing - 2020` → `Annual Filing - 2019`
   - Slug containing the separator: `Foo - Bar - 2026W27` → `Foo - Bar - 2026W26`
   For each row assert `command.DecrementRecurringTaskTitle(ctx, newTitle)` returns the expected prior title and a nil error.

8. **Error-path test rows** (separate `It` blocks or table with `expectErr bool`):
   - No suffix separator: `RandomTask` → error (no `" - "`).
   - Unrecognized token shape: `Weird Task - notaperiod` → error (matches no shape).
   - Empty token after separator: `Bad - ` → error.
   For each, assert the returned error is non-nil.

9. **Use `ctx := context.Background()` in tests** (the standard test-suite context). Production callers pass the handler context; the helper only uses `ctx` to wrap errors.

</requirements>

<constraints>

- Implement EXACTLY the six shapes in `/workspace/docs/period-token-semantics.md` — no extra shapes, no opt-out flags, no config. Spec Non-goal (verbatim): *"Do NOT add a per-feature opt-out flag, config field, or tunable threshold on the controller side."*
- This helper has ZERO I/O — no git, no Kafka, no filesystem, no clock. It is pure date arithmetic on strings.
- Per `go-error-wrapping-guide.md`: errors wrap with `errors.Errorf(ctx, ...)` / `errors.Wrapf(ctx, err, ...)` from `github.com/bborbe/errors` — never `fmt.Errorf`, never `context.Background()` inside `pkg/`.
- Per `go-precommit.md`: file-level lint limits (funlen 80, nestif 4, golines 100). Keep `decrementPeriodToken` under 80 lines — if it approaches the limit, extract per-kind helpers (`decrementWeek`, `decrementQuarter`).
- Do NOT add a `CHANGELOG.md` entry. Spec Non-goal (verbatim): *"Do NOT add a `CHANGELOG.md` entry — this repo has no `CHANGELOG.md`."*
- Compile regexps at package scope, not inside functions.
- Existing tests in `/workspace/pkg/command/` must still pass.
- Do NOT commit — dark-factory handles git.

</constraints>

<verification>

Run iteratively while implementing:

```
cd /workspace && go build ./pkg/command/...
cd /workspace && go test ./pkg/command/... -v
```

Run ONCE at the end:

```
cd /workspace && make precommit
```

Expected: all period-token table rows pass, including the four named boundary cases (`2026Q1 → 2025Q4`, `2026W01 → 2025W52`, `2026-01 → 2025-12`, `2026-01-01 → 2025-12-31`); error rows return non-nil errors; `make precommit` exits 0.

</verification>
