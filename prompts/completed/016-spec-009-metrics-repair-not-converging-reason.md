---
status: completed
spec: [009-scanner-repair-convergence-guard]
summary: Added the repair_not_converging skip reason to the closed SkipReason set and AvailableSkipReasons so the series pre-initialises at 0, corrected the SkippedFilesTotal doc comment and Help string to document the edge exception with its increase(...[6h]) detection query, extended the registry-scraping specs, and fixed the stale design-doc paragraph
execution_id: agent-task-controller-identifier-convergence-exec-016-spec-009-metrics-repair-not-converging-reason
dark-factory-version: dev
created: "2026-09-05T18:05:00Z"
queued: "2026-09-05T18:30:53Z"
started: "2026-09-05T18:31:37Z"
completed: "2026-09-05T18:36:10Z"
---

# Add the repair_not_converging skip reason and correct the skipped-files metric documentation

<summary>

- Operators get a new, pre-declared reason label on the existing skipped-files counter, so the series exists at zero on a fresh pod instead of appearing out of nowhere the first time it fires.
- No new metric is introduced — the new reason joins the counter operators already watch, so there is still exactly one series to dashboard.
- The counter's documented contract is corrected: it currently promises that every label stays rate-positive while a file is broken, which will be false for the new label.
- The new label is documented as a deliberate exception: it is an edge that fires once per distinct file content, not a level that stays hot.
- The correct operator detection query for the new label is written down next to the metric, along with why the two obvious alternatives (a short-window rate, and a bare level check) are wrong for it.
- The project design doc's stale claim about how many reason labels exist, and its blanket alerting advice, are brought back in line with reality.
- This prompt changes no scanner control flow and nothing can increment the new label yet — it only makes the label exist and be honestly described.

</summary>

<objective>

Add a `repair_not_converging` value to the closed `SkipReason` set in `pkg/metrics/metrics.go` and to `AvailableSkipReasons` so the Prometheus series pre-initialises at `0`, and rewrite the `SkippedFilesTotal` documentation so its stated contract is true for every label including the new one. This prompt satisfies spec 009 AC1 and AC7 and is a prerequisite for the convergence guard that will increment the label.

</objective>

<context>

Read these before changing anything:

- `specs/in-progress/009-scanner-repair-convergence-guard.md` — the spec. Read AC1, AC7, the Design Decisions bullet titled "The counter is an edge, and the metric documentation moves to match", and the Non-goals.
- `pkg/metrics/metrics.go` — the `SkipReason` type, the reason constants, `AvailableSkipReasons`, the `SkippedFilesTotal` `CounterVec` and its doc comment, and the `init()` pre-initialisation loop at the bottom of the file.
- `pkg/metrics/metrics_test.go` — the existing registry-scraping Ginkgo specs and the `gatherLabels` helper at the bottom.
- `docs/controller-design.md` — the paragraph on line 28 that describes the skipped-files counter and its alerting query.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-prometheus-metrics-guide.md` — Prometheus conventions for this codebase (closed label sets, zero pre-initialisation).
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-precommit.md` — what `make precommit` runs.

This repo has no root `CLAUDE.md`; `README.md` and `docs/controller-design.md` carry the project conventions.

</context>

<requirements>

1. In `pkg/metrics/metrics.go`, add one constant to the existing `SkipReason` const block, as the last entry, keeping the block's gofmt alignment:

```go
const (
	ReasonInvalidFrontmatter          SkipReason = "invalid_frontmatter"
	ReasonDuplicateFrontmatterInvalid SkipReason = "duplicate_frontmatter_invalid"
	ReasonEmptyStatus                 SkipReason = "empty_status"
	ReasonInjectTaskIdentifierFailed  SkipReason = "inject_task_identifier_failed"
	ReasonReadFailed                  SkipReason = "read_failed"
	ReasonAutoInjectDisabled          SkipReason = "auto_inject_disabled"
	ReasonRepairNotConverging         SkipReason = "repair_not_converging"
)
```

   The label value string MUST be exactly `repair_not_converging`. Do not introduce a new `CounterVec`, a new metric name, or a second series — the spec's Non-goals forbid it.

2. Add `ReasonRepairNotConverging` as the last entry of `AvailableSkipReasons`:

```go
var AvailableSkipReasons = []SkipReason{
	ReasonInvalidFrontmatter,
	ReasonDuplicateFrontmatterInvalid,
	ReasonEmptyStatus,
	ReasonInjectTaskIdentifierFailed,
	ReasonReadFailed,
	ReasonAutoInjectDisabled,
	ReasonRepairNotConverging,
}
```

   Do NOT touch the `init()` loop at the bottom of the file — it already iterates `AvailableSkipReasons` and calls `SkippedFilesTotal.WithLabelValues(reason.String()).Add(0)`, which is what makes the new series exist at `0` before the first increment.

3. Replace the `SkippedFilesTotal` doc comment — the block of `//` lines immediately above `var SkippedFilesTotal = promauto.NewCounterVec(`. This block makes the SAME false promise as the `Help` string in different words ("a stuck broken file will keep the relevant label rate-positive until repaired"), so fixing only the `Help` string in requirement 4 leaves the lie in place one comment higher. Note the hyphenated `rate-positive` here does not match AC7's `grep -c 'keep the rate positive'`, which is why this requirement exists separately.

   Old (delete all six lines exactly as they appear):

```go
// SkippedFilesTotal counts vault task files the scanner skipped during a scan cycle,
// labelled by the structured reason for the skip. A non-zero value on any label
// indicates operator-actionable vault health issues (broken frontmatter, empty status,
// unreadable files, injection failures); a stuck broken file will keep the relevant
// label rate-positive until repaired. The closed set of reason values is declared
// as constants above and pre-initialised in init().
```

   New:

```go
// SkippedFilesTotal counts vault task files the scanner skipped during a scan cycle,
// labelled by the structured reason for the skip. A non-zero value on any label
// indicates operator-actionable vault health issues (broken frontmatter, empty status,
// unreadable files, injection failures). For every label EXCEPT repair_not_converging,
// a stuck broken file is re-read every cycle and keeps its label rate-positive until
// repaired, so rate(agent_controller_vault_scanner_skipped_files_total[5m]) > 0 is the
// detection query.
//
// repair_not_converging is the documented exception: it is an edge, not a level. The
// scanner short-circuits on the file's content hash, so a file whose repair was halted is
// not re-processed while its bytes are unchanged, and the counter increments exactly once
// per distinct file content. Detect it with:
//
//	increase(agent_controller_vault_scanner_skipped_files_total{reason="repair_not_converging"}[6h]) > 0
//
// A short-window rate is wrong for this label (it goes flat while the file stays broken),
// and a bare level check is wrong too (it survives forever after the file is repaired and
// resets on pod restart).
//
// The closed set of reason values is declared as constants above and pre-initialised in init().
```

4. Replace the `Help` string of `SkippedFilesTotal` so it no longer contains the substring `keep the rate positive`, and so it names `repair_not_converging`. Old → new, exact find-and-replace of the single `Help:` line:

   Old:

```go
		Help: "Total number of vault task files the scanner skipped during a scan cycle, by reason. Increments exactly once per skipped file per cycle — re-scans of an unrepaired broken file keep the rate positive.",
```

   New:

```go
		Help: "Total number of vault task files the scanner skipped during a scan cycle, by reason. Increments once per skipped file per cycle for every reason except repair_not_converging, which is an edge that fires once per distinct file content and then goes flat while the file stays broken; see the doc comment for its increase(...[6h]) detection query.",
```

   Do not embed double quotes inside the `Help` literal — the full PromQL selector with `{reason="repair_not_converging"}` belongs in the doc comment from requirement 3, not in the Go string. After this edit `grep -c 'keep the rate positive' pkg/metrics/metrics.go` must return `0`.

5. In `pkg/metrics/metrics_test.go`, extend the existing spec `It("pre-initializes all vault_scanner_skipped_files_total label combinations", ...)` so its `ContainElements` list covers the full closed set as it now stands — the list is currently missing `auto_inject_disabled` too:

```go
		Expect(labels).To(ContainElements(
			"invalid_frontmatter",
			"duplicate_frontmatter_invalid",
			"empty_status",
			"inject_task_identifier_failed",
			"read_failed",
			"auto_inject_disabled",
			"repair_not_converging",
		))
```

6. In `pkg/metrics/metrics_test.go`, add a value helper next to the existing `gatherLabels` function at the bottom of the file, in the same package-level style:

```go
// gatherCounterValue returns the counter value of the sample of metricName whose
// label labelName equals labelValue, and whether such a sample exists at all.
func gatherCounterValue(
	mfs []*dto.MetricFamily,
	metricName string,
	labelName string,
	labelValue string,
) (float64, bool) {
	for _, mf := range mfs {
		if mf.GetName() != metricName {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, lp := range m.GetLabel() {
				if lp.GetName() == labelName && lp.GetValue() == labelValue {
					return m.GetCounter().GetValue(), true
				}
			}
		}
	}
	return 0, false
}
```

7. In `pkg/metrics/metrics_test.go`, add a new Ginkgo spec inside the existing `Describe("Metrics", ...)` block that proves AC1's zero pre-initialisation. Nothing in the `pkg/metrics` test suite increments the scanner skip counter, so the value scraped in this suite is the value `init()` left behind:

```go
	It("pre-initializes repair_not_converging at zero before any skip (spec 009 AC1)", func() {
		mfs, err := prometheus.DefaultGatherer.Gather()
		Expect(err).NotTo(HaveOccurred())

		value, found := gatherCounterValue(
			mfs,
			"agent_controller_vault_scanner_skipped_files_total",
			"reason",
			"repair_not_converging",
		)
		Expect(found).To(BeTrue(), "repair_not_converging series must exist after init()")
		Expect(value).To(BeNumerically("==", 0))
	})
```

8. In `docs/controller-design.md`, update the single paragraph on line 28 so it no longer claims the closed set has five members and no longer gives blanket `rate(...) > 0` advice that is wrong for the new label. Keep it to that one paragraph — do not restructure the document. Replace it with:

```
> The scanner increments `agent_controller_vault_scanner_skipped_files_total{reason=<closed enum>}` at every skip site (broken frontmatter, unreadable file, empty status, injection failure, unresolvable duplicate frontmatter, auto-inject disabled, repair did not converge). The counter is pre-initialised at zero for every reason label so dashboards see the whole closed set before the first skip. Operators alert on `rate(agent_controller_vault_scanner_skipped_files_total{reason!="repair_not_converging"}[5m]) > 0`; a positive rate means a broken file is currently in the vault and is not being scanned. The `repair_not_converging` label is an edge rather than a level — the scanner's content-hash short-circuit means it fires once per distinct file content — so it is alerted on separately with `increase(agent_controller_vault_scanner_skipped_files_total{reason="repair_not_converging"}[6h]) > 0`.
```

9. Do not add any Ginkgo spec that asserts on the exact `Help` string text. The `Help` wording is documentation, not a contract, and pinning it makes future honest edits fail the build. AC7 is verified by `grep`, which the `<verification>` section below runs.

</requirements>

<constraints>

- Do NOT commit — dark-factory handles git.
- Do NOT introduce a new Prometheus counter, metric name, or `CounterVec`. The new reason joins the existing closed `SkipReason` set so operators keep watching one series. (Spec Non-goals.)
- Do NOT make the guard or the label configurable, opt-outable, or threshold-tunable. No new env var, no new config field, no flag. (Spec Non-goals — an escape hatch on a termination guard is the regression itself.)
- Do NOT change any scanner control flow in this prompt. `pkg/scanner/` is out of scope here; the guard that increments this label ships in prompt 2.
- Do NOT change `AUTO_INJECT_TASK_IDENTIFIER` semantics or the `ReasonAutoInjectDisabled` label.
- Do NOT touch `pkg/scanner/vault_scanner_test.go` or `pkg/scanner/vault_scanner_internal_test.go` in this prompt.
- Do NOT reorder or rename the existing `SkipReason` constants or existing entries of `AvailableSkipReasons` — dashboards depend on the label values.
- Do NOT add commit-rate alerting, a write-rate circuit breaker, or reinstate the removed `AgentControllerWritebackFailing` alert. (Spec Non-goals.)
- The landing mechanism is a manually opened PR to `master` (`.dark-factory.yaml` is `workflow: direct`, `pr: false`, `autoMerge: false`) — do not attempt any git, `gh`, or PR steps in this container.
- Do NOT run bare `git` commands — dark-factory owns git for this repo, and a git command in `<verification>` whose exit code is not checked would produce a false-positive verification pass.
- Do NOT run `kubectl*`, `docker`, `make buca`, `make build`, or any operator/deploy command.
- Existing tests must still pass. `make precommit` runs `make format` (goimports-reviser plus `golines --max-len=100 -w`) over every non-vendor `.go` file on every run, green or not — files being reformatted in place is expected, not a failure.
- The CHANGELOG entry for spec 009 is owned by prompt 2. Do NOT edit `CHANGELOG.md` here.

</constraints>

<verification>

Fast loop while implementing:

```
go build ./pkg/metrics/...
go test -mod=mod ./pkg/metrics/...
```

AC1 evidence — the constant and the label value exist:

```
grep -n 'repair_not_converging' pkg/metrics/metrics.go
```

Expect at least three lines: the const declaration, the `AvailableSkipReasons` entry, and the documentation naming it. (The doc comment and `Help` string add more.)

AC7 evidence — the false contract sentence is gone:

```
grep -c 'keep the rate positive' pkg/metrics/metrics.go || true
```

Expect `0`. The `|| true` is required because `grep -c` exits `1` when the count is zero.

AC7 evidence — the operator query is stated next to the metric:

```
grep -n 'increase(agent_controller_vault_scanner_skipped_files_total' pkg/metrics/metrics.go
```

Expect at least one line inside the `SkippedFilesTotal` doc comment.

AC7 evidence — the doc comment's unscoped promise is gone too, not just the `Help` string:

```
grep -c 'a stuck broken file will keep the relevant' pkg/metrics/metrics.go || true
grep -n 'For every label EXCEPT repair_not_converging' pkg/metrics/metrics.go
```

Expect `0` for the first and exactly one line for the second. Without this, requirement 3 could be skipped entirely and every other verification command would still pass.

AC1 evidence — the zero pre-initialisation spec runs and passes:

```
go test -mod=mod -count=1 -v ./pkg/metrics/... -ginkgo.v 2>&1 | grep -c 'spec 009 AC1'
```

Expect a non-zero count with the surrounding Ginkgo run reporting `SUCCESS`. Both `-count=1` and `-ginkgo.v` are required: Ginkgo v2 does not print spec text under plain `go test -v`, and a cached result suppresses output entirely — without them this grep returns `0` on a perfectly healthy suite.

Run ONCE at the end:

```
make precommit
```

Expect exit 0 with the full Ginkgo suite green. If it fails, iterate on the specific failing target (`make test`, `make check`) rather than re-running the whole chain, then re-run `make precommit` once the individual targets pass.

NEVER weaken or delete the `Equal(9)` parity assertions in `pkg/scanner/vault_scanner_test.go` to make a target green. This prompt does not add a counter call inside `processFile` or `injectAndStore`, so those assertions must still hold unchanged at `9`; if they fail, something in `pkg/scanner/` was edited that should not have been.

</verification>
