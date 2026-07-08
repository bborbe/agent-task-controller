---
status: verifying
tags:
    - dark-factory
    - spec
approved: "2026-07-08T08:40:06Z"
generating: "2026-07-08T09:00:18Z"
prompted: "2026-07-08T10:20:00Z"
verifying: "2026-07-08T11:52:43Z"
branch: dark-factory/recurring-task-supersede-scan-collapse
---

## Summary

- Recurring-task auto-supersede must collapse a schedule to **one open instance**, even when firings were missed — not just close the single arithmetic predecessor.
- Replace the per-kind "decrement one token" approach with a **scan** of same-slug instances that closes every open prior within a bounded look-back window.
- Applies uniformly to all recurrence kinds (Daily / Weekday / Weekly / Monthly / Quarterly / Yearly); steady-state behavior is unchanged, gap behavior is fixed.
- Reads are capped to K instances per materialize (K default 7, configurable via a controller env var) so history depth never drives cost.
- Deletes the now-obsolete decrement module and rewrites the period-token semantics doc to the scan-and-collapse contract.

## Problem

The auto-supersede hook computes exactly one prior instance by decrementing the new instance's period token (same-weekday-last-week for Weekday), reads that one file, and aborts it if open. When a schedule fans out across several open streams — e.g. `IBKR Swing Trading` firing Weekday Mon–Fri produces five simultaneous `in_progress` weekly streams because each weekday only ever supersedes the same weekday of the prior week — the vault accumulates multiple open tasks for one logical schedule. Any missed firing (a skipped day, a controller outage) also leaves a stale `in_progress` task open forever, because the single computed predecessor may not be the one that is actually still open. The user wants ONE open instance per schedule, with any skipped-day leftover auto-closing when the next instance fires.

## Goal

After an eligible recurring-task instance is materialized, the vault contains exactly one open (`in_progress`) instance for that schedule — the just-created one — and every earlier open instance of the same schedule within the bounded look-back window has been transitioned to `aborted` / `phase: done` with `completed_date` and `superseded_by` pointing at the new instance. This holds for all recurrence kinds and is robust to missed firings and gaps, without the controller knowing the schedule's weekday set or recurrence kind.

## Non-goals

- Do NOT change `bborbe/recurring-task-creator` or the Schedule CRD — controller-only.
- Do NOT change the eligibility gate — supersede stays opt-in (`created_by == recurring-task-creator` AND `auto_abort_prior == true`); audit-style schedules without the flag remain untouched.
- Do NOT retro-abort stale backlog deeper than K instances — that is a separate one-off backfill sweep.
- Do NOT add any UI / dashboard surface for supersede.
- Do NOT add a per-schedule opt-out of the collapse behavior — collapse-to-one is the invariant this spec ships; if a future consumer needs partial collapse, that is a separate spec.

## Desired Behavior

1. On eligible materialize, the hook derives `slug` = the title substring before the final `" - "` separator (the existing split logic), and lists same-schedule candidate instances from the task directory scoped to that slug.
2. The just-written new instance is excluded from its own candidate set (it never supersedes itself).
3. Candidates are ranked by their parsed period-token in descending (most-recent-first) order; ranking is correct across ISO-week and year boundaries (e.g. `2026W01` ranks after `2025W52`).
4. Only the K most-recent candidates whose token is strictly less than the new instance's token are inspected (K = look-back bound). Every inspected candidate whose `status` is `in_progress` is transitioned exactly as the current single-prior transition: `status: aborted`, `phase: done`, `completed_date: <now>`, `superseded_by: <new instance relPath>`, `created_by` preserved as `recurring-task-creator`.
5. The scan is best-effort per file: read / parse / write errors on any single candidate are logged and swallowed; the new instance is never rolled back, and remaining candidates are still processed.
6. The look-back bound K is process-configurable (default 7) so an operator can widen or narrow the window without a code change.
7. The listing is scoped to the schedule's slug so it never returns unrelated tasks; if the slug cannot be embedded in a glob safely, the hook falls back to listing all task files and filtering in memory on the `<slug> - ` prefix.

## Constraints

- Frozen supersede output contract (must remain byte-identical to today's single-prior transition): `status: aborted`, `phase: done`, `completed_date` in RFC3339, `superseded_by` = new instance relPath, `created_by` = `recurring-task-creator`.
- Frozen eligibility gate: `created_by == recurring-task-creator` AND `auto_abort_prior == true` (bool `true` or string `"true"`).
- All timestamps via the injected `libtime.CurrentDateTimeGetter` — no `stdlib time.Now()`.
- Listing uses the existing `GitClient.ListFiles(ctx, glob)` (single-level glob, repo-root-relative); no new git-rest endpoint.
- Idempotent on Kafka redelivery: candidates already `aborted` are skipped (no-op, no rewrite).
- Reads bounded to at most K candidate files per materialize regardless of history depth.
- Daily and Weekly (and Monthly / Quarterly / Yearly) MUST still collapse to a single open instance — regression must be proven, not assumed.
- Token-shape recognition reuses the existing six regexes (`dailyRe`, `weekdayRe`, `weeklyRe`, `monthlyRe`, `quarterlyRe`, `yearlyRe`).
- Referenced design docs (do not contradict): `docs/controller-design.md` (single-writer-per-vault, git-rest, `24 Tasks/` taskDir), `docs/period-token-semantics.md` (to be rewritten by this spec).

## Failure Modes

| Trigger | Expected behavior | Recovery | Detection | Reversibility | Concurrency |
|---|---|---|---|---|---|
| git-rest `ListFiles` unavailable / errors | Log warning, swallow; new instance stays written; no priors closed | Next materialize retries the scan | WARN log line `auto-supersede: list` | Reversible (priors reprocessed next fire) | New instance already committed; scan is a follow-on |
| Transient read error on one candidate | Log warning, skip that candidate, continue remaining K | Next materialize reprocesses the skipped open prior | WARN log naming the candidate relPath | Reversible | Per-file best-effort, others unaffected |
| Candidate token matches no known shape | Skip that candidate silently (V(3) log), continue | n/a — non-schedule file filtered out | V(3) log | No-op | n/a |
| Slug contains glob-special chars | Fall back to list-all + in-memory prefix filter; still scoped to `<slug> - ` | Automatic within same materialize | V(2) log noting fallback | No-op | n/a |
| Kafka redelivery of same create | Priors already `aborted` → no rewrite; new instance already exists → `ErrTaskAlreadyExists` benign | Idempotent no-op | No new commit for unchanged priors | No-op | Per-task Kafka partition serializes |
| Crash mid-scan after closing some priors | Closed priors stay closed; unclosed open priors reprocessed on next materialize | Next fire re-scans and closes remainder | Open prior still `in_progress` in vault | Partial (forward-safe) | Each transition is its own atomic commit |
| More than K open priors (deep backlog) | Only K most-recent closed; older left open by design | Manual backfill sweep (out of scope) | K-deep window in log | Partial by design | n/a |
| Clock skew on `completed_date` | Uses injected time getter; value = getter's `Now().UTC()` | n/a | `completed_date` matches injected clock | Reversible | n/a |

## Security / Abuse Cases

- Attacker-controlled input is the task `Title` (hence `slug`) arriving via Kafka. The slug flows into a glob and into file paths.
- Path traversal: computed candidate paths must stay within `taskDir`; titles containing path separators are already rejected upstream and MUST remain rejected for candidate paths.
- Glob injection: slug characters that are glob metacharacters (`*`, `?`, `[`, `]`, `\`) must not widen the listing beyond `<slug> - *`; on any doubt the hook falls back to list-all + exact in-memory prefix filter.
- Unbounded work: the scan reads at most K files and closes at most K files per materialize regardless of how many candidates the listing returns — a huge same-slug backlog cannot cause unbounded reads/writes.

## Acceptance Criteria

- [ ] With N prior `in_progress` same-slug instances all inside the K window, a single eligible materialize transitions ALL N to `aborted` / `phase: done` / `completed_date` set / `superseded_by` = new relPath, and the new instance stays `in_progress` — evidence: Ginkgo assertions on the fake GitClient's written contents.
- [ ] Missed-day gap: Mon (`2026W28-mon`) and Tue (`2026W28-tue`) open, Wed (`2026W28-wed`) materializes → both Mon and Tue transition to aborted; Wed stays in_progress — evidence: Ginkgo, GitClient fake write assertions.
- [ ] Weekday-set-agnostic: a sparse set (`-mon`, `-wed`, `-fri`) collapses using slug + open-status only, with no weekday-set knowledge encoded — evidence: Ginkgo, both priors aborted on next fire.
- [ ] Look-back honored: with > K prior `in_progress` instances, only the K most-recent are read and closed; older remain `in_progress`; the fake GitClient records at most K `ReadFile` calls on candidates — evidence: Ginkgo call-count assertion with a small K.
- [ ] Ranking is correct across ISO-week + year boundary (`2026W01` ordered after `2025W52`) for Weekday and Weekly tokens — evidence: Ginkgo table test on the pure ranking function.
- [ ] Daily and Weekly still collapse to a single open instance (regression) — evidence: Ginkgo, prior instance aborted, new stays in_progress.
- [ ] `pkg/command/period_token_decrementor.go` and `pkg/command/period_token_decrementor_test.go` are deleted and no code references them — evidence: `grep -rn "decrementRecurringTaskTitle\|decrementPeriodToken\|period_token_decrementor" pkg/` returns zero matches (exit 1).
- [ ] K is configurable via a controller env var with default 7, wired from `main.go` application config through `factory` to the create-task executor constructor — evidence: `grep -n` shows the new `arg:`/`env:` struct field with `default:"7"`, plus a Ginkgo/unit assertion that a non-default K bounds the reads.
- [ ] `docs/period-token-semantics.md` is rewritten to describe the scan-and-collapse contract (the decrement table removed/replaced) — evidence: `grep -n "scan\|collapse\|look-back" docs/period-token-semantics.md` returns line ≥1 and `grep -n "Decrement Table" docs/period-token-semantics.md` returns exit 1.
- [ ] `CHANGELOG.md` has an `## Unreleased` (or next-version) entry describing the scan-and-collapse change — evidence: `grep -n "scan" CHANGELOG.md` returns a line under the top unreleased section.
- [ ] The supersede scenario is added/updated to cover the collapse seam — evidence: a file under `scenarios/` referencing the collapse behavior (`grep -rln "collapse\|supersede" scenarios/` returns ≥1).
- [ ] `make precommit` exits 0 — evidence: exit code.
- [ ] **Post-Deploy (Rung-2):** after deploy (mirrored-semver model: `cd quant/agent && BRANCH=<stage> make apply`, NOT `make buca`), triggering the **next not-yet-materialized IBKR date** (`/trigger?date=2026-07-09`, thu) materializes a fresh `IBKR Swing Trading - 2026W28-thu` whose scan collapses the open same-week priors: `-mon`, `-tue`, AND `-wed` all transition to `aborted` / `phase: done` / `superseded_by` set, leaving `-thu` the single `in_progress` instance — evidence: the `auto-supersede: … -> …` controller log lines + the priors' vault frontmatter. **Do NOT re-trigger an already-materialized date (07-07/07-08): the controller returns `ErrTaskAlreadyExists` and the supersede hook runs only after a successful *new*-instance write, so an existing-date replay collapses nothing.**

**Scenario coverage:** update the existing supersede seam coverage under `scenarios/`. No brand-new E2E scenario beyond the existing seam unless the live-verification step below cannot be expressed as a Ginkgo test (it covers real git-rest + Kafka replay, which unit tests cannot reach) — the live replay in Verification is the E2E evidence.

## Verification

```
cd ~/Documents/workspaces/agent-task-controller
make precommit
```

Expected: exit 0; all Ginkgo specs above pass; `grep -rn "period_token_decrementor" pkg/` returns nothing.

Live (post deploy — mirrored-semver model: `cd quant/agent && BRANCH=<stage> make apply`, NOT `make buca`): trigger the **next not-yet-materialized IBKR date**, `/trigger?date=2026-07-09` (thu), on the recurring-task-creator. A fresh `IBKR Swing Trading - 2026W28-thu` materializes and its scan collapses the open same-week priors: `-mon`, `-tue`, and `-wed` all become `aborted` / `phase: done` with `superseded_by` set, leaving `-thu` the single `in_progress` instance; controller log shows the `auto-supersede: ... -> ...` lines. Re-triggering an *existing* date (07-07/07-08) collapses nothing — the controller returns `ErrTaskAlreadyExists` and the supersede hook only fires after a successful new-instance write.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Pure ranking + slug/token parsing: extract token-shape parse into a comparable ordinal, add the descending ranking function, reuse the six regexes; delete the decrement module | 1, 3 | ranking, decrementor-deleted | — |
| 2 | Scan-and-collapse in the supersede hook: list slug-scoped candidates, exclude new, cap to K, transition each open prior best-effort; glob-safety fallback | 1, 2, 4, 5, 7 | N-collapse, missed-day, sparse-set, look-back, Daily/Weekly regression, idempotency | prompt 1 |
| 3 | Wire K env var (main → factory → executor constructor, default 7); rewrite `docs/period-token-semantics.md`; CHANGELOG; scenario | 6 | K-config, doc-rewrite, CHANGELOG, scenario | prompt 2 |

Rationale: prompt 1 produces the pure, unit-testable ranking core with no I/O; prompt 2 builds the hook logic on it against the fake GitClient; prompt 3 wires configuration and docs last so the observable contract is settled before it is documented. No cycles: each prompt depends only on the prior.

## Do-Nothing Option

If unchanged, Weekday and any gap-prone schedule keep accumulating multiple simultaneous open instances (the `IBKR Swing Trading` five-stream case) and any missed firing leaves a stale `in_progress` task open indefinitely. The single-decrement approach cannot self-heal gaps; the vault drifts and the user must manually abort leftovers. Not acceptable — the motivating case is already live.
