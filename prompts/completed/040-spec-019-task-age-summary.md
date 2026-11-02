---
status: completed
spec: [019-task-age-metrics]
summary: Added pure task-age summary computation to pkg/scanner with publishTaskAgeMetrics, timestamp parsing helpers, and full Ginkgo spec coverage
execution_id: agent-task-controller-task-age-exec-040-spec-019-task-age-summary
dark-factory-version: v0.198.7
created: "2026-10-31T16:25:00Z"
queued: "2026-10-31T15:31:37Z"
started: "2026-10-31T15:36:29Z"
completed: "2026-10-31T15:42:33Z"
branch: dark-factory/spec-019
---

# Compute and publish the task-age summary

<summary>
- A read-only pass over a list of task file paths computes how old the oldest non-terminal record is and how many non-terminal records fall into each age bucket.
- Non-terminal means the status is neither completed nor aborted; an empty or unknown status counts as non-terminal.
- A record's age comes from the first timestamp field that actually parses, in a fixed order, so a record dated only by a queued timestamp is not mistaken for an undated one.
- Quoted and unquoted timestamps are both accepted, because an unquoted value reaches the parser as a native timestamp rather than a string; the board's records are mostly unquoted.
- A record with no parseable timestamp is counted in the undated bucket instead of being guessed at, and a future timestamp is treated as zero age rather than a negative one.
- The four bucket boundaries are exact at one day and at seven days, and the number published is the same truncated whole-second value the bucket was chosen from.
- Every successful pass republishes all four buckets and the oldest age, including the zero values, so a scrape never sees a series disappear.
- One unreadable, unparseable or malformed file is skipped and cannot blank the instrument; a file whose duplicate frontmatter keys deduplicate cleanly is counted once.
- The oldest age is the maximum over dated records, never a sum or an average, and it reads zero when no dated non-terminal record remains.
</summary>

<objective>
Add the pure age-summary computation to the scanner package: given a path list, one clock value and a vault name, it reads and parses each file and publishes the oldest-age and per-bucket gauges through the metrics accessors from the previous prompt. This is the logic the periodic ticker will call, and it is fully falsifiable with unit specs because it needs no ticker, no git client and no configured scanner.
</objective>

<context>
Read these files fully before writing code:
- `pkg/scanner/stuck_planning.go` — the closest existing exemplar: `(*vaultScanner).countStuckPlanningTasks(ctx, paths, now)` (line 42) iterates a path list, checks `ctx.Done()` per file, reads through `v.ops.readFile`, extracts frontmatter with `extractFrontmatter`, deduplicates with `DeduplicateFrontmatter`, unmarshals with `yaml.Unmarshal`, and skips any file it cannot parse. It also defines `stringField(fmMap, key)` (line 114), which you reuse for `status` but **not** for the timestamps — see requirement 2.
- `pkg/scanner/frontmatter.go` — `extractFrontmatter(ctx, content) (string, error)` and `DeduplicateFrontmatter(ctx, fmYAML) (string, bool, error)`.
- `pkg/scanner/vault_scanner.go` — the `vaultScanner` struct and its `ops fileOps` field (`listFiles`, `readFile`, `writeFile`), plus `RunCycle` and `scanFiles` for the logging style (`glog.V(3).Infof`, `glog.Warningf`).
- `pkg/metrics/metrics.go` — `Metrics.TaskOldestAgeSeconds(vault string) prometheus.Gauge`, `Metrics.TaskAgeBucketTasks(vault string, bucket TaskAgeBucket) prometheus.Gauge`, `AvailableTaskAgeBuckets`, and the `TaskAgeBucket` constants added by the previous prompt. `metrics.New()` returns the default implementation backed by the package-level gauge vectors.
- `pkg/scanner/stuck_planning_internal_test.go` and `pkg/scanner/vault_scanner_internal_test.go` (~line 128 onward) — how internal specs build a `&vaultScanner{...}` with `ops: fileOps{...}` closures and `metrics: metrics.New()`. The scanner's Ginkgo suite is `pkg/scanner/scanner_suite_test.go` (`package scanner_test`); internal spec files are `package scanner` and register specs into the same binary.

Relevant coding guides (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-context-cancellation-in-loops.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-precommit.md` (funlen 80, nestif 4, golines 100 — split helpers rather than growing one function)
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-prometheus-metrics-guide.md`
</context>

<requirements>
1. Create `pkg/scanner/task_age.go` (package `scanner`, license header required) with the following frozen contract:
   ```go
   const (
   	// taskAgeOneDay is the exclusive upper bound of the lt_1d bucket.
   	taskAgeOneDay = 24 * time.Hour
   	// taskAgeSevenDays is the inclusive upper bound of the 1d_7d bucket
   	// (604800 seconds), matching the alert expression `> 604800`.
   	taskAgeSevenDays = 7 * 24 * time.Hour
   )

   // taskAgeTimestampFields is the age precedence, first parseable value wins.
   var taskAgeTimestampFields = []string{"created", "created_date", "queued_at", "job_started_at"}

   // publishTaskAgeMetrics reads the given task files and publishes the task-age
   // gauges. It is read-only and never writes, pulls or scans.
   func (v *vaultScanner) publishTaskAgeMetrics(ctx context.Context, paths []string, now time.Time, vaultName string)
   ```
   The input order is frozen by the spec: path list, then one clock value, then the vault name. `now` is read once by the caller and used for every record in the pass.

2. Add two unexported helpers in the same file.

   The **field reader** is what the pass calls. It must accept an unquoted timestamp:
   `gopkg.in/yaml.v3` decodes an unquoted timestamp scalar into a `time.Time` inside a
   `map[string]interface{}` — verified for this repo: `created: 2026-10-29T12:00:00Z`
   and `created: 2026-10-29` both arrive as `time.Time` (the date-only value at
   midnight UTC), while a quoted value arrives as a `string` — and the existing
   `stringField` returns `""` for any non-string. Reading a timestamp through
   `stringField` would therefore classify every unquoted timestamp as undated, and
   unquoted is the majority shape on the real board (712 `created:` lines in the
   personal vault, 290+ of them unquoted at the 2026-10-31 read), so the oldest-age
   series would read 0 on the very board it exists to measure.
   ```go
   // taskAgeTimestampOf reads one frontmatter timestamp field. A quoted value arrives as
   // a string and is parsed as RFC3339 or YYYY-MM-DD; an unquoted value arrives from
   // yaml.v3 as a time.Time and is used directly (a date-only value already carries
   // midnight UTC). A missing or non-timestamp value is unparseable.
   func taskAgeTimestampOf(fmMap map[string]interface{}, key string) (time.Time, bool) {
   	switch value := fmMap[key].(type) {
   	case string:
   		return parseTaskAgeTimestamp(value)
   	case time.Time:
   		return value, true
   	default:
   		return time.Time{}, false
   	}
   }
   ```
   The **string parser** it delegates to:
   ```go
   // parseTaskAgeTimestamp parses one frontmatter timestamp value. RFC3339 (with or
   // without fractional seconds) and date-only YYYY-MM-DD (read as midnight UTC) are
   // accepted; anything else, including the empty string, is unparseable.
   func parseTaskAgeTimestamp(value string) (time.Time, bool)
   ```
   Implement with `time.Parse(time.RFC3339, value)` first (Go's RFC3339 parser already accepts fractional seconds) and `time.Parse("2006-01-02", value)` second.

3. Implement the pass in `publishTaskAgeMetrics`:
   - Start every bucket count at 0 and `oldest := time.Duration(0)`.
   - Iterate `paths`; for each path, do a non-blocking context check first (`select { case <-ctx.Done(): return; default: }`). On cancellation return immediately **without publishing anything** — a partial pass must not overwrite a complete one.
   - `content, err := v.ops.readFile(ctx, relPath)`; on error `continue` (skip the file, log nothing per file — this pass stays silent per file, matching the spec).
   - `fmYAML, err := extractFrontmatter(ctx, content)`; on error `continue`.
   - `deduped, _, err := DeduplicateFrontmatter(ctx, fmYAML)`; on error `continue`. On success use `deduped` for the unmarshal, so a file whose duplicate keys deduplicate cleanly is counted once with the last value winning.
   - `var fmMap map[string]interface{}; if err := yaml.Unmarshal([]byte(deduped), &fmMap); err != nil { continue }`.
   - Read the status with the existing `stringField(fmMap, "status")`. Skip the record entirely when it is `"completed"` or `"aborted"` — those are the terminal statuses; every other value, including `""`, is non-terminal.
   - Determine the record's age: iterate `taskAgeTimestampFields` in order, call `taskAgeTimestampOf(fmMap, field)`, and stop at the first field that returns `true`. If no field parses, increment the `undated` count and continue to the next file. Never route a timestamp through `stringField`: it returns `""` for the `time.Time` an unquoted value decodes to.
   - For a dated record compute `age := now.Sub(parsed).Truncate(time.Second)`; if `age < 0` set `age = 0` (a future timestamp clamps to zero, never a negative value). Truncating before classifying is load-bearing: a record 7 days plus a fraction of a second old publishes `604800` and must land in `1d_7d`, not `gt_7d`.
   - Classify the truncated age into exactly one bucket:
     ```go
     switch {
     case age < taskAgeOneDay:
     	// lt_1d
     case age <= taskAgeSevenDays:
     	// 1d_7d — inclusive at exactly 7 days
     default:
     	// gt_7d
     }
     ```
   - Track `oldest` as the maximum truncated age over dated records (not a sum, not the first, not an average).
   - After the loop, publish unconditionally through the accessors: `v.metrics.TaskOldestAgeSeconds(vaultName).Set(float64(oldest / time.Second))` (0 when no dated record exists) and, iterating `metrics.AvailableTaskAgeBuckets`, `v.metrics.TaskAgeBucketTasks(vaultName, bucket).Set(float64(counts[bucket]))`. Publishing all four buckets every pass — zero included — is required so the series exist after the first successful pass.
   - Use `vaultName` verbatim as the label value. Never map, slugify or translate it.

4. Do not reference the metric names as strings anywhere in `pkg/scanner/`; go through `v.metrics` accessors only. Do not add the series to `scanFiles`, `RunCycle`, `Run` or `buildIndex` — this prompt adds no caller at all. The single-writer rule is enforced in the next prompt.

5. Create `pkg/scanner/task_age_internal_test.go` (package `scanner`, license header) with Ginkgo/Gomega specs. Build the scanner under test as `&vaultScanner{metrics: metrics.New(), ops: fileOps{readFile: ...}}`; no git client, no `listFiles` and no `writeFile` are needed because the pass takes the path list. Give **each spec its own vault label value** (for example `"test-boundaries"`, `"test-precedence"`, `"test-undated"`, …) so the process-global registry cannot leak values between specs. Read values with `testutil.ToFloat64` from `github.com/prometheus/client_golang/prometheus/testutil` where a specific value is asserted; read presence through `prometheus.DefaultGatherer.Gather()` where a series may not exist yet. Helper functions must be prefixed `taskAgeSummary` (not bare `taskAge`): the next prompt adds `task_age_ticker_internal_test.go` into the same `package scanner` test binary, and a bare `taskAge` prefix would let the two files declare the same symbol.

   Required specs (fixed clock, e.g. `time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)`):
   - **Boundaries:** records exactly 24h old → `1d_7d`; 24h minus one second → `lt_1d`; exactly 7 days → `1d_7d`; 7 days plus one second → `gt_7d`. Assert the four bucket values with `testutil.ToFloat64`.
   - **Precedence:** a record carrying only `created`, only `created_date`, only `queued_at`, or only `job_started_at` yields the same age for the same instant; and a record whose `created` is unparseable but whose `created_date` is valid is aged from `created_date`.
   - **Undated:** a record carrying none of the four fields is counted in `undated` and contributes nothing to the oldest age; a board of only undated non-terminal records reads oldest `0` and `undated` equal to the record count.
   - **Terminal exclusion:** a board of one `completed`, one `aborted` and one two-day-old `in_progress` record reads `1d_7d = 1` and `lt_1d = gt_7d = undated = 0`.
   - **Presence through Gather:** publish over an empty path list, then gather `prometheus.DefaultGatherer` and locate the bucket family **without naming it**: iterate the gathered families and select the one whose samples carry both a `vault` label and a `bucket` label — it is the only family with a `bucket` label. Assert it holds a sample for each of `lt_1d`, `1d_7d`, `gt_7d` and `undated`, each reading 0. Assert presence from the gathered family, never through `testutil.ToFloat64`, because the child series exists only once the accessor's own `WithLabelValues` call has run. Never write either frozen metric name as a string literal anywhere in `pkg/scanner/` — test files included — because the verification grep requires zero occurrences in that package.
   - **Reset:** seed one non-terminal record dated 3 days before the clock, publish, read oldest `259200`; then make the file content `status: completed`, publish again, read oldest `0` with all four bucket samples still present through `Gather`.
   - **Maximum:** records at 1h, 3 days and 10 days read oldest `864000` exactly.
   - **Idempotency:** one record dated exactly 2 hours before the clock, publish twice; the oldest age reads `7200` after each pass and all five values are equal between the two passes.
   - **Skipped files:** a board with one non-terminal record dated 10 days before the clock plus one unreadable file (the `readFile` closure returns an error for that path), one file with no frontmatter block, and one file with malformed YAML — oldest reads `864000` and the four buckets sum to `1`.
   - **Duplicate keys:** one non-terminal record whose frontmatter repeats `status` (`completed` first, `in_progress` last) and repeats `created` (an old value first, a 2-day-old value last) is counted once; oldest reads `172800` and the four buckets sum to `1`.
   - **Future clamp:** one non-terminal record whose `created` is one hour after the clock reads oldest `0` and `lt_1d = 1`, with `1d_7d`, `gt_7d` and `undated` at 0.
   - **Unquoted timestamps:** two boards. On the first, one non-terminal record whose `created` is an **unquoted** RFC3339 value (`created: 2026-10-29T12:00:00Z`, no quotes) ages exactly as its quoted twin — same oldest value, same bucket. On the second, one non-terminal record whose `created` is an **unquoted** date-only value (`created: 2026-10-29`, no quotes) ages from midnight UTC of that date. Neither record lands in `undated`. This spec is required because a quoted fixture passes while the real board fails: `stringField` returns `""` for the `time.Time` an unquoted value decodes to, so without the `time.Time` branch every unquoted record would be counted in `undated` and the oldest age would read 0.

6. Keep `publishTaskAgeMetrics` within the linter's `funlen` (80) and `nestif` (4) limits by extracting the per-file classification into a small helper whose outcome is a **state enum — the three outcomes terminal / undated / dated, plus a skipped state — never a boolean:**
   ```go
   // taskAgeState is the outcome of classifying one file for the age summary.
   type taskAgeState int

   const (
   	// taskAgeTerminal is a completed or aborted record: skipped, counted in no bucket.
   	taskAgeTerminal taskAgeState = iota
   	// taskAgeUndated is a non-terminal record with none of the four fields parseable:
   	// counted in the undated bucket.
   	taskAgeUndated
   	// taskAgeDated is a non-terminal record with a parseable timestamp: age is its
   	// truncated, clamped age and is classified into a bucket.
   	taskAgeDated
   	// taskAgeSkipped is a file that cannot be read, carries no frontmatter, or carries
   	// frontmatter that will not deduplicate or unmarshal: skipped, counted in no bucket.
   	taskAgeSkipped
   )

   // taskAgeFromFile classifies one task file. For taskAgeDated, age is truncated to
   // whole seconds and clamped at 0; it is 0 for every other state.
   func (v *vaultScanner) taskAgeFromFile(
   	ctx context.Context,
   	relPath string,
   	now time.Time,
   ) (age time.Duration, state taskAgeState)
   ```
   A `(age time.Duration, dated bool)` return cannot express this: a terminal record and an undated record both have no age, so the boolean would force the caller to count every terminal record in `undated` and fail the terminal-exclusion AC. `taskAgeSkipped` is a fourth state beyond the three named outcomes because the skipped-file AC requires an unreadable, no-frontmatter or malformed-YAML file to land in **no** bucket — the same effect as `taskAgeTerminal`, but not the same meaning, and collapsing them would make the code claim a broken file is terminal. The pass switches on `state` and increments exactly one bucket, or none.
</requirements>

<constraints>
- Frozen bucket values: exactly `lt_1d`, `1d_7d`, `gt_7d`, `undated`. Frozen label names: `vault` and `bucket`.
- 7 days is `604800` seconds and the `1d_7d` upper bound is inclusive, so a record exactly 7 days old is in `1d_7d`, not `gt_7d`.
- One clock read per pass: the caller supplies `now`; do not call `time.Now()` inside the pass or per record.
- Timestamps may be quoted or unquoted: an unquoted scalar arrives from yaml.v3 as a `time.Time`, so the field reader must accept both shapes (requirement 2). Never read a timestamp with `stringField`.
- The pass is read-only: it must never pull, scan, write, commit, or send anything on a results channel.
- A file that cannot be read, has no frontmatter, or whose frontmatter will not deduplicate or unmarshal is skipped entirely — it lands in no bucket and never affects the oldest age.
- No per-record series: no record identifier, path or filename may become a label value.
- No filter by assignee, phase or task type.
- Do not add any configuration knob, flag or environment variable.
- Do not change `agent_controller_update_go_stuck_planning_tasks`, `scanFiles`, `RunCycle`, `Run` or the constructors in this prompt.
- Do not commit; dark-factory handles git.
- Existing tests must still pass.
</constraints>

<verification>
Run from the repo root:

```bash
GOFLAGS=-buildvcs=false go test -count=1 -mod=mod ./pkg/scanner/...
```

```bash
# The scanner must not reach the series by name (it goes through the accessors).
! grep -qE 'agent_controller_task_oldest_age_seconds|agent_controller_task_age_bucket_tasks' pkg/scanner/*.go && echo "no metric-name literals in pkg/scanner"
```

```bash
ROOTDIR=/workspace make precommit
```

Before finishing, re-run the commands above, confirm each spec in requirement 5 exists and passes, and walk every numbered requirement against the change. `make precommit` must exit 0.
</verification>
