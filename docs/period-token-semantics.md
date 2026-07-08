# Period-Token Semantics

The recurrence kind is inferred from the period-token suffix shape in the task title. On each eligible materialize, the controller performs a **scan-and-collapse**: it lists same-slug instances, ranks them most-recent-first by a parsed period-token **ordinal**, and closes every still-open prior within a bounded **look-back** window of K instances (default 7).

Token shapes and firing rules are defined in the Obsidian KB page [[Per-Kind Firing Semantics for Recurring Task Schedulers]]. This document covers the scan-and-collapse contract that `bborbe/agent-task-controller` uses to implement auto-supersede.

## Token Shapes and Ordinal

The controller identifies the recurrence kind by matching the suffix against these shapes in recognition order:

1. Daily (`YYYY-MM-DD`)
2. Weekday (`YYYYWww-<3-letter-abbrev>`)
3. Weekly (`YYYYWww`)
4. Monthly (`YYYY-MM`)
5. Quarterly (`YYYYQq`)
6. Yearly (`YYYY`)

Each shape yields a comparable **ordinal**: a value such that a more-recent period produces a larger ordinal. This lets the controller sort same-slug instances most-recent-first regardless of recurrence kind. Because the ordinal is monotonic with calendar time, the ordering is consistent across ISO-week and year boundaries:

- `2025W52 < 2026W01` (ISO-week boundary)
- `2025Q4 < 2026Q1` (quarterly boundary)
- `2025-12 < 2026-01` (monthly boundary)
- `2025-12-31 < 2026-01-01` (daily boundary)

If no shape matches, the hook logs a warning and is a no-op (non-recurring or unrecognized title).

## Scan-and-Collapse Contract

When a recurring-task instance is materialized, the controller executes the following scan-and-collapse step:

### Slug

The **slug** is the task title up to and including the final ` - ` before the period-token suffix. For example, `IBKR Swing Trading - 2026W28-mon` has slug `IBKR Swing Trading`.

### Candidate Set

All same-slug files in the task directory are listed. The newly-materialized instance is excluded from its own candidate set. Only candidates strictly older than (or the same ISO-week sibling as) the new instance are inspected.

### Look-Back Bound

At most **K** (the look-back bound, `SUPERSEDE_LOOKBACK`, default 7) most-recent candidates are read and evaluated. K is the **total** number of prior candidates inspected — not the number closed. If fewer than K priors exist, all are inspected. Older priors beyond the K-window are left open.

### Collapse (Auto-Supersede)

Each still-open (`phase: in_progress`) prior candidate is **collapsed**: the controller writes the frozen transition frontmatter fields:

```
status: aborted
phase: done
completed_date: <ISO8601>
superseded_by: <relPath of the new instance>
created_by: recurring-task-creator
```

### Best-Effort Per File

List, read, parse, and write errors for any individual file are logged and swallowed. The scan continues to the next candidate. If the new instance's own write fails, the error is propagated (not swallowed).

### Idempotency on Redelivery

If a prior candidate is already `status: aborted` (redelivery of the new instance), it is skipped. Re-running collapse on the same candidate set is safe.

### Non-Goal

This contract does not change the `bborbe/recurring-task-creator` publisher, the Schedule CRD, or the supersede output schema (the frozen transition fields above). Only the controller's scan-and-collapse logic is in scope.
