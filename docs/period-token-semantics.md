# Period-Token Semantics

The recurrence kind is NOT carried in task frontmatter. Decrement is inferred from the period-token suffix shape in the task title. This table is the durable contract between `bborbe/recurring-task-creator` (the publisher that stamps tokens) and `bborbe/agent-task-controller` (the consumer that decrements them for auto-supersede).

Token shapes and firing rules are defined in the Obsidian KB page [[Per-Kind Firing Semantics for Recurring Task Schedulers]]. This document adds the **decrement direction** — the arithmetic that maps a new instance's token to the prior period's token — which the KB page does not cover.

## Decrement Table

| Recurrence kind | Period-token shape (new) | Prior-token shape | Decrement rule |
|---|---|---|---|
| Daily | `YYYY-MM-DD` | `YYYY-MM-DD` | one calendar day before the new instance's token date |
| Weekday (list) | `YYYYWww-<3-letter-abbrev>` | `YYYYWww-<same abbrev>` | one ISO week before; weekday suffix preserved (e.g. `2026W27-sat` → `2026W26-sat`) |
| Weekly | `YYYYWww` (e.g. `2026W27`) | `YYYYWww` | one ISO week before (e.g. `2026W27` → `2026W26`) |
| Monthly | `YYYY-MM` | `YYYY-MM` | one calendar month before (e.g. `2026-06` → `2026-05`) |
| Quarterly | `YYYYQq` (e.g. `2026Q2`) | `YYYYQq` | one quarter before (e.g. `2026Q2` → `2026Q1`); wrap to prior year at `Q1` → `Q4` |
| Yearly | `YYYY` | `YYYY` | one calendar year before (e.g. `2026` → `2025`) |

## Shape Recognition Order

The hook identifies the kind by matching the suffix against these shapes in order:

1. Daily (`YYYY-MM-DD`)
2. Weekday (`YYYYWww-abc`)
3. Weekly (`YYYYWww`)
4. Monthly (`YYYY-MM`)
5. Quarterly (`YYYYQq`)
6. Yearly (`YYYY`)

If no shape matches, the hook logs a warning and is a no-op (non-recurring or unrecognized title).

## Boundary Cases

Decrement must handle period boundaries correctly:

- **Quarterly year-boundary**: `2026Q1 → 2025Q4` (wraps to prior year)
- **Weekly ISO-week-boundary**: `2026W01 → 2025W52` (wraps to prior year)
- **Monthly year-boundary**: `2026-01 → 2025-12` (wraps to prior year)
- **Daily year-boundary**: `2026-01-01 → 2025-12-31` (wraps to prior year)

ISO-week and weekday arithmetic uses the Go `time` package's ISO-week helpers; the decrement must produce the same canonical token shape the publisher would have produced for the prior period.
