---
status: draft
---

# Scenario: Recurring Task Supersede Collapse (Multi-Stream Weekday Schedule)

## Setup

1. A quant dev controller is running with the recurring-task publisher wired.
2. `SUPERSEDE_LOOKBACK` is set in the pod env (default 7; verify it is present via `env | grep SUPERSEDE_LOOKBACK`).
3. A weekday schedule `IBKR Swing Trading` fans out to weekday streams: `IBKR Swing Trading - 2026W28-mon`, `-tue`, `-wed`, etc.
4. At least one prior instance is already `in_progress` with `phase: in_progress` (created by a previous materialize or replay).

## Action

1. Replay `/trigger?date=2026-07-07` on the dev controller — this materializes `IBKR Swing Trading - 2026W28-mon`.
2. Replay `/trigger?date=2026-07-08` on the dev controller — this materializes `IBKR Swing Trading - 2026W28-tue`.

## Expected

1. After the first replay, `IBKR Swing Trading - 2026W28-mon` is `in_progress`.
2. After the second replay, `IBKR Swing Trading - 2026W28-mon` is `status: aborted`, `phase: done`, with `completed_date` set and `superseded_by` pointing to `IBKR Swing Trading - 2026W28-tue`.
3. `IBKR Swing Trading - 2026W28-tue` stays `in_progress`.
4. The controller log shows an `auto-supersede: ... -> ...` line for each collapse (one for the mon→tue supersede).
5. When `IBKR Swing Trading - 2026W28-wed` is materialized (either by replay or future cron), both `mon` and `tue` are closed, and the log shows two `auto-supersede` lines.
6. At most K (default 7, configured via `SUPERSEDE_LOOKBACK`) most-recent priors are inspected; older open priors beyond the look-back window remain open.

## Cleanup

1. Delete the materialized task files (`IBKR Swing Trading - 2026W28-mon.md`, `-tue.md`, `-wed.md`) from the vault git repo.
2. Reset any triggered state in the controller.
