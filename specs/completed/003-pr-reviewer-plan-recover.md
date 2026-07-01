---
status: completed
tags:
    - dark-factory
    - spec
approved: "2026-07-01T12:08:19Z"
generating: "2026-07-01T12:08:20Z"
prompted: "2026-07-01T12:22:35Z"
verifying: "2026-07-01T13:43:56Z"
completed: "2026-07-01T14:31:04Z"
branch: dark-factory/pr-reviewer-plan-recover
---

## Summary

- The `maintainer-agent-pr-reviewer` planning step already refuses malformed Plan JSON (Layer 0, v0.41.1) and retries the Claude call up to three times in-agent before returning `AgentStatusFailed` (Layer 1, v0.42.0). When Layer 1 exhausts, the controller currently records the failed result, marks the task done, and does not spawn a new Job on the same PR SHA — the PR sits at `REVIEW_REQUIRED` indefinitely with no comment, no signal to the operator, and no automated recovery. The only escape today is a manual SHA-bump (push a trivial commit).
- This spec adds controller-side recovery: on receiving a `Status: failed` result from a `pr-review` task in `phase: planning`, the controller increments a `planning_retry_count` frontmatter counter and requeues the planning Job on a fresh `task_identifier` (bypassing the executor's `(repo, sha)` dedup by moving into a new task-id namespace). Up to three controller-side retries; each retry appends a line to the task file's `## Progress` section for operator observability.
- When controller retries also exhaust (`planning_retry_count == 3` after another failure), the controller posts a `COMMENT` review on the PR naming the task link and the last error, sets the task phase to `human_review` (not `done`), and clears the assignee so the task surfaces in the operator inbox via the existing spec-042 chokepoint.
- Layer 2 (retry) and Layer 3 (visibility) ship together in the controller. They gate strictly on `task_type == pr-review` and `phase == planning`; all other task types, phases, and result statuses pass through unchanged. The retry cap is a hardcoded package constant, not a config field or env var.
- This is the companion to `bborbe/maintainer#62` and closes the [[Harden PR-Reviewer Planning Validation and Visibility]] goal begun with Layer 0 (v0.41.1) and Layer 1 (v0.42.0, shipped 2026-07-01).

## Problem

The pr-reviewer agent occasionally returns malformed Plan JSON — a MiniMax `B`-case (empty content, missing required fields, invalid enum values). Layer 0 in the agent refuses to persist the bad plan; Layer 1 retries the Claude call three times. Both ship today (`maintainer` v0.42.0). But when all three in-agent retries fail, the agent surfaces `AgentStatusFailed` and the controller writes the failed result to the vault task file with `phase: done` — end of pipeline. The K8s Job exits cleanly, no new Job spawns (the executor's `(repo, sha)` dedup blocks a re-spawn on the same PR head), the PR's `reviewDecision` stays `REVIEW_REQUIRED`, no comment is posted to the PR, and the operator has no visible signal that a review was attempted and failed. The manual escape is a SHA-bump (push a trivial commit → new SHA → dedup no longer applies → executor spawns a fresh Job). This is a silent-limbo failure mode that erodes trust in the pr-reviewer pipeline.

## Goal

After a `pr-review` planning failure reaches the controller, the controller autonomously retries the planning Job up to three times on fresh task identifiers before escalating. Each retry attempt is visible in the task file's `## Progress` section. On the third retry exhaustion, the controller posts a `COMMENT` review on the PR describing the failure and links back to the task, sets the task's `phase` to `human_review`, and clears the assignee so the task surfaces in the operator inbox. Success paths, non-planning failures, and non-`pr-review` task types are unaffected.

## Non-goals

- Do NOT change the in-agent retry logic (`bborbe/maintainer` Layer 1). Layer 1 shipped in v0.42.0 and is the seam this spec sits above.
- Do NOT change the executor's `(repo, sha)` dedup mechanism. Retry works around it by writing a fresh `task_identifier`, not by mutating dedup rules.
- Do NOT add retry recovery for `execution`, `ai_review`, or `verdict` phases. Planning-phase-only.
- Do NOT extend the retry loop to task types other than `pr-review`. Behavior is gated on `task_type == pr-review`.
- Do NOT retry on `AgentStatusSkipped` (not a failure) or `AgentStatusSuccess`.
- Do NOT add exponential backoff, jitter, or a sleep between retries. Immediate requeue on the next event cycle, same pattern as Layer 1.
- Do NOT expose the retry cap as an env var, CLI flag, config field, or CRD field. `maxControllerPlanningRetries = 3` is a hardcoded package constant (invariant; if a future consumer demands variation, that's a separate spec).
- Do NOT add a metric-per-retry cardinality explosion. A single `agent_controller_planning_retry_total{result=<retry|exhausted|passthrough>}` counter is enough.
- Do NOT add a new secret or ConfigMap for a GitHub token. Reuse whatever credential surface the controller already has for git-rest / GitHub; if none exists, prompt-time must surface as an open question (see Assumptions).
- Do NOT retro-recover already-failed PRs sitting at `REVIEW_REQUIRED` today. Forward-going only.

## Assumptions

- The controller receives failed pr-reviewer planning results via the existing `agent-task-v1-request` update flow. The failed result's frontmatter carries `assignee: pr-reviewer-agent` (or the canonical pr-reviewer agent name) and `phase: planning`, and the body contains a marker equivalent to `Status: failed` (verify against the pr-reviewer agent's Result publishing code before implementation; if the marker shape differs, use the frontmatter phase + a top-level status field instead of grepping the body).
- The `task_type` distinction is derivable either from a frontmatter field (`task_type: pr-review`) or from the assignee identity (`assignee: pr-reviewer-agent`). Whichever the codebase already uses to route pr-review tasks is the field this spec keys off. If neither is durable in frontmatter today, add `task_type: pr-review` at pr-review-task materialization time in the prompt for prompt 1.
- The PR URL / repo / PR number is discoverable from the task file — either a frontmatter field (`pr_url`, `repository`, `pull_request_number`) or embedded in the task body. The Layer 3 comment-posting logic must locate these; the prompt is responsible for grep-verifying the exact field names before writing code.
- The controller has (or can be given) a GitHub token with `pull-requests: write` scope in its Kubernetes Secret. If the existing controller Secret lacks this scope, prompt 2 must surface this as a deploy-time prerequisite before merge.
- The executor's `(repo, sha)` dedup is keyed on the tuple `(repo_url, head_sha, task_identifier)` or similar — a new `task_identifier` with the same `(repo, sha)` bypasses dedup. This is the mechanism the caller named; if the actual dedup is on `(repo, sha)` alone, prompt 1 must surface the discrepancy before writing code.
- Kafka partitioning by `task_identifier` serializes writes per task; the retry write and the escalation write cannot race with each other on the same partition.
- git-rest auto-commits on every POST (see [[Task Controller]] "git-rest integration"), so the `## Progress` append + frontmatter counter bump lands atomically per retry.

## Desired Behavior

1. When the `task_result_executor` (or its collaborators in `pkg/result/`) processes an update whose incoming Task has `assignee` matching the pr-reviewer agent AND merged frontmatter `phase == "planning"` AND the result carries the failure marker, the executor branches into planning-retry logic before the default result-persistence path.
2. Planning-retry logic reads `planning_retry_count` from the merged frontmatter (default `0` when absent). If `planning_retry_count < maxControllerPlanningRetries` (constant, value `3`), the executor: (a) increments the counter to `planning_retry_count + 1`, (b) writes the counter back to the vault task file, (c) appends `retry N/3: <reason> at <RFC3339 timestamp>` to the task file's `## Progress` section (creating the section if absent), (d) resets `phase` to `planning`, (e) triggers a fresh Job spawn on a new `task_identifier` (the exact mechanism — new CreateCommand, new event with rewritten identifier, or another executor-visible signal — is chosen at implementation time; the observable requirement is that the executor spawns a new pr-reviewer planning Job for the same PR).
3. `<reason>` in the `## Progress` line is the concise last-error string from the failed result (truncated to 200 characters). `<RFC3339 timestamp>` is the controller's clock (via the existing `libtime.CurrentDateTimeGetter` seam).
4. When `planning_retry_count` reaches `3` (i.e. the incoming failure arrives with the counter already at `3` from a prior retry write), the executor does NOT spawn a fresh Job. Instead it: (a) appends the final `retry 3/3: <reason> at <timestamp>` line to `## Progress`, (b) posts a `COMMENT` review on the PR via a GitHub client, (c) sets `phase: human_review` on the task file, (d) clears the assignee via the existing `result.ClearAssigneeIfHumanReview` chokepoint (spec 042).
5. The COMMENT review body follows the frozen template: `Automated pr-review planning failed after <controller_retries> controller retries and <in_agent_retries> in-agent retries. Last error: <reason>. Please investigate <task_link>.` The `<task_link>` is the Obsidian-vault-relative path or the operator's canonical task URL, whichever the existing task-link convention uses in the codebase. `<in_agent_retries>` is a fixed literal `3` (matching Layer 1's cap).
6. Every retry attempt (including the first) and every exhaustion increments `agent_controller_planning_retry_total{result=<retry|exhausted>}` — a new Prometheus counter registered alongside the existing `ResultsWrittenTotal` metric.
7. Non-planning failures (`phase != "planning"`), non-pr-review task types, `AgentStatusSuccess`, and `AgentStatusSkipped` bypass all retry logic and route through the existing `WriteResult` path unchanged. `agent_controller_planning_retry_total{result="passthrough"}` is NOT incremented for these — the metric only fires on the retry gate matching.
8. The retry logic is idempotent under Kafka redelivery: if the same failed-result command is redelivered after the counter was already bumped to `N+1` and the fresh Job was spawned, the second processing reads `planning_retry_count == N+1`, sees the phase already flipped back to `planning`, and detects the `## Progress` line already contains `retry N/3` — it MUST no-op (no double-increment, no duplicate `## Progress` line, no duplicate Job spawn). The idempotency key is the combination of `planning_retry_count` and the presence of the `retry N/3` line at that count.
9. A GitHub API failure when posting the COMMENT does NOT block the frontmatter escalation. The executor sets `phase: human_review` and clears assignee unconditionally on retry exhaustion; the COMMENT post is best-effort with a `glog.Warning` log line on failure. Operator visibility falls back to the operator-inbox surface driven by `assignee == ""`.

## Constraints

- Frozen constant: `maxControllerPlanningRetries = 3` in the `pkg/command/` (or `pkg/result/`) package where the retry gate lives. No env var, no CLI flag, no CRD field.
- Frozen `## Progress` line format: `retry N/3: <reason> at <RFC3339 timestamp>` — load-bearing for the idempotency check in DB 8 and for operator greps.
- Frozen COMMENT review body template (DB 5) — load-bearing for downstream operator search / dashboards.
- Frozen frontmatter counter name: `planning_retry_count`. Frozen phase target on exhaustion: `human_review`. Frozen review event type: `COMMENT` (never `REQUEST_CHANGES` or `APPROVE` — the controller never gates the PR).
- The escalation path MUST route through `result.ClearAssigneeIfHumanReview` (single chokepoint per spec 042); do not duplicate the assignee-clear logic.
- Existing tests in `pkg/command/task_result_executor_test.go`, `pkg/result/result_writer_test.go`, and `pkg/publisher/task_publisher_test.go` MUST still pass.
- The retry gate MUST NOT change the CQRS command-handler's outer return shape (no new sentinel errors surfaced to the framework); a retry decision is an internal branch, transparent to the CQRS layer.
- git-rest client and its existing wiring are the only vault-write path. No new git client, no new PVC, no new STS.
- If the GitHub token is not present in the controller Secret, the executor logs at WARNING and skips the COMMENT post — this is a deploy-time prerequisite that must not crash the controller.

## Failure Modes

| Trigger | Detection | Expected behavior | Reversibility | Recovery |
|---|---|---|---|---|
| Failed planning result, counter=0..2 | `agent_controller_planning_retry_total{result="retry"}` increments; task file diff shows `planning_retry_count` bump and new `## Progress` line | Increment counter, append `## Progress`, reset phase to `planning`, trigger fresh Job on new `task_identifier` | Reversible (next attempt may succeed) | No operator action — retry loop continues |
| Failed planning result, counter=3 | `agent_controller_planning_retry_total{result="exhausted"}` increments; PR receives `COMMENT` review; task file `phase: human_review` + `assignee: ""` | Append final `## Progress` line, POST GitHub COMMENT, set `phase: human_review`, clear assignee | Irreversible for auto — operator handoff | Operator debugs via task link in COMMENT; escape via SHA-bump is still available |
| GitHub API 5xx / network hang when posting COMMENT | `glog.Warning` log line with substring `planning-retry: github COMMENT post failed:`; `agent_controller_planning_retry_total{result="exhausted"}` still increments (frontmatter escalation is not blocked) | Log WARNING, swallow, complete frontmatter escalation anyway | Partial — comment absent but task in human_review | Operator sees `assignee: ""` in inbox and investigates; separate cleanup script may retro-post if needed |
| GitHub token missing from controller Secret | Same WARNING as above at first fire | Skip COMMENT, complete frontmatter escalation | Partial | Deploy fix (add token to Secret), redeploy — separate incident |
| Kafka redelivery of the same failed result after retry write | No new metric increment; existing `## Progress` line detected via idempotency check | No-op (idempotent per DB 8) | Reversible | No operator action |
| Task file missing PR URL / repo / PR number | `glog.Warning` with substring `planning-retry: cannot resolve PR from task:`; COMMENT skipped; frontmatter escalation still fires | Log WARNING, escalate frontmatter, skip COMMENT | Partial | Operator inspects task file; retro-add PR metadata if needed |
| Task file missing `## Progress` section on first retry | (No warning — creation is expected) | Section created above any existing trailing section OR at end of body, with the standard `## Progress` header on its own line, followed by the first `retry 1/3:` bullet | Reversible | No operator action |
| Result marker unrecognizable (neither `Status: failed` nor phase-based signal) | `glog.V(2)` info line with substring `planning-retry: no failure signal;` skipping retry gate | Pass through to default `WriteResult` (fail-open — do not block success paths) | Reversible | Task lands in default handling path |
| Concurrent redelivery + fresh-attempt result race | Kafka partition ordering by `task_identifier` serializes; the retry write commits before the fresh-attempt result is consumed | The fresh attempt sees the new `task_identifier` (post-retry), so no cross-attempt collision | Reversible | No operator action |
| Clock skew: `## Progress` timestamps off by seconds/minutes | Timestamps informational only; not used for ordering or dedup | Acceptable | Reversible | No recovery needed |

## Security / Abuse Cases

- What can an attacker control? The Kafka update-command payload (in-cluster, already authenticated). A crafted `## Result` body or frontmatter field could try to inject shell-meta or path-traversal into the `<reason>` string embedded in the `## Progress` line and the COMMENT body. **Mitigation:** truncate `<reason>` to 200 characters and strip newlines + control characters before interpolation into markdown / GitHub API JSON. The frontmatter counter `planning_retry_count` is read via the existing `TaskFrontmatter` accessor (must handle non-int / negative / >3 values defensively — treat any value >=3 as "at cap, escalate").
- What crosses trust boundaries? Kafka `agent-task-v1-request` (in-cluster) → git-rest HTTP (gateway-secret auth) + GitHub API HTTPS (token auth). One new trust boundary: the GitHub token in the controller Secret. Token scope MUST be limited to `pull-requests: write` (no code push, no admin).
- What can hang, retry forever, or race? A single retry pass emits one git-rest POST and (on exhaustion) one GitHub POST. Neither has its own retry loop — one call, swallow errors, log. The context deadline flows from the CQRS handler. No sleep, no exponential backoff loop that could stack across redeliveries.
- What data must be validated? `planning_retry_count` (int, clamp negative → 0, clamp >3 → 3); `pr_url` / `repository` / `pr_number` (regex-check the repo slug and number before invoking GitHub API); `<reason>` (strip newlines + non-printable chars before interpolation).

## Acceptance Criteria

- [ ] `make precommit` exits 0 in the repo root — evidence: exit code.
- [ ] `grep -rn 'maxControllerPlanningRetries' pkg/` returns ≥1 line and the value on that line is the literal `3` — evidence: grep line count + line content (frozen constant).
- [ ] `grep -rn 'planning_retry_count' pkg/` returns ≥1 line in the executor package (frozen frontmatter field) — evidence: grep line count.
- [ ] A Ginkgo unit test asserts: incoming failed result with `phase: execution` (not planning), `assignee: pr-reviewer-agent` → the retry gate is a no-op, the default `WriteResult` path is exercised, `agent_controller_planning_retry_total` shows zero increments — evidence: mock call graph + Prometheus counter value.
- [ ] A Ginkgo unit test asserts: incoming failed result with `phase: planning`, `assignee: pr-reviewer-agent`, on-disk `planning_retry_count == 0` → after processing, on-disk `planning_retry_count == 1`, `phase == "planning"`, `## Progress` section contains a line matching regex `^retry 1/3: .* at [0-9T:+-]+$`, no COMMENT posted, GitHub client mock has zero calls — evidence: captured mock frontmatter write + captured body regex match + GitHub-client mock call count == 0.
- [ ] A Ginkgo unit test asserts: same shape, on-disk `planning_retry_count == 1` → counter becomes `2`, `## Progress` gains a `retry 2/3` line, existing `retry 1/3` line preserved — evidence: captured mock body contains both `retry 1/3` and `retry 2/3` lines.
- [ ] A Ginkgo unit test asserts: same shape, on-disk `planning_retry_count == 2` → counter becomes `3`, `## Progress` gains `retry 3/3`, still no COMMENT posted (this attempt is a retry, not exhaustion) — evidence: counter value == 3 AND GitHub-client mock call count == 0.
- [ ] A Ginkgo unit test asserts: same shape, on-disk `planning_retry_count == 3` (retry exhausted) → no counter increment, no new `## Progress` retry line beyond a terminal marker, `phase == "human_review"`, `assignee == ""`, GitHub-client mock invoked exactly once with review event `COMMENT` and body matching the DB 5 template — evidence: mock frontmatter capture + mock GitHub call count == 1 + captured GitHub call body substring match.
- [ ] A Ginkgo unit test asserts: retry exhaustion with a GitHub client that returns an error → frontmatter escalation still lands (`phase: human_review`, `assignee: ""`), `glog` sink captures a WARNING containing substring `planning-retry: github COMMENT post failed:` — evidence: captured frontmatter values + captured log substring.
- [ ] A Ginkgo unit test asserts: `AgentStatusSuccess` result for pr-review planning phase → retry gate no-op, existing `WriteResult` path unchanged, `agent_controller_planning_retry_total` zero increments — evidence: mock call graph + counter value.
- [ ] A Ginkgo unit test asserts: failed result for a task with `assignee != pr-reviewer-agent` (or `task_type != pr-review`, whichever the routing key is) → retry gate no-op, existing `WriteResult` path unchanged — evidence: mock call graph + counter value.
- [ ] A Ginkgo unit test asserts: Kafka redelivery of an already-processed failed result (on-disk `planning_retry_count == 1` AND `## Progress` already contains `retry 1/3` line matching the same reason substring) → no counter increment, no duplicate `## Progress` line — evidence: captured mock body line count for `retry 1/3` == 1 AND counter value unchanged.
- [ ] A Ginkgo unit test asserts: `<reason>` string containing a newline or a `\r` is stripped before interpolation into the `## Progress` line and the COMMENT body — evidence: captured mock body does NOT contain a literal `\n` inside the reason substring; captured GitHub call body does NOT contain a literal `\n` inside the reason substring.
- [ ] A Ginkgo unit test asserts: `<reason>` longer than 200 characters is truncated in both the `## Progress` line and the COMMENT body — evidence: captured line length ≤ 200 + suffix.
- [ ] A Ginkgo unit test asserts: on-disk `planning_retry_count == 5` (defensive: value already >3) → treated as at-cap, escalation fires (frontmatter + COMMENT), no counter overwrite to normalize value — evidence: mock GitHub call count == 1 + frontmatter capture shows `phase: human_review`.
- [ ] A Ginkgo unit test asserts: task file missing PR URL / repo metadata → COMMENT skipped, frontmatter escalation still fires, `glog` captures WARNING with substring `planning-retry: cannot resolve PR from task:` — evidence: mock GitHub call count == 0 + frontmatter capture shows `phase: human_review` + log substring.

Scenario coverage: NO new E2E scenario. All behavior is reachable via Ginkgo unit tests with mock `GitClient` and mock GitHub client. The operator-run post-deploy drill (deliberately force a planning-exhaustion in dev, observe the retry cycle + COMMENT + human_review phase) is captured in the Verification section below, not as an automated scenario. The maintainer-side planning-retry pipeline is already exercised end-to-end in `bborbe/maintainer` scenarios; this spec's behavior sits above that seam and the mock-based unit tests fully cover the controller branch.

## Verification

Unit + integration:

```
make precommit
```

Expected: exit 0, all Ginkgo/Gomega specs pass, counterfeiter mocks regenerated cleanly.

Post-deploy operator drill (dev first, then prod):

1. Deploy: `cd ~/Documents/workspaces/agent-dev && git pull && git merge master && cd task/controller && BRANCH=dev make buca`.
2. Force a planning-exhaustion in dev by one of:
   - (a) Temporarily patch the pr-reviewer runner CR to point at a build that always returns malformed Plan JSON, wait for three in-agent retries + three controller retries to exhaust.
   - (b) Wait for a natural MiniMax `B`-case that exhausts Layer 1's three in-agent retries.
3. Observe: `kubectl logs -n <ns> deploy/task-controller | grep planning-retry` shows three `retry N/3` lines and one `exhausted` line.
4. Observe: `git log --oneline -10` on the vault shows three `[agent-task-controller] write result for task ...` commits, each mutating `planning_retry_count` and appending a `## Progress` line.
5. Observe: `gh pr view <pr-number> --comments` shows a `COMMENT` review whose body matches the DB 5 template.
6. Observe: the task file's frontmatter is `phase: human_review`, `assignee: ""`, and the operator-inbox surface shows the task.
7. Restore the pr-reviewer CR to the normal image; SHA-bump the PR to confirm the pipeline recovers on a fresh SHA (existing behavior, unchanged).
8. Repeat on prod: `cd ~/Documents/workspaces/agent-prod && git pull && git merge master && cd task/controller && BRANCH=prod make buca`.

CHANGELOG update: append two `## Unreleased` bullets — one for the Layer 2 retry loop, one for the Layer 3 COMMENT + human_review escalation.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Layer 2: retry gate + counter + `## Progress` append + phase reset + fresh-task-id requeue. New helper in `pkg/command/` (or `pkg/result/`) invoked from `task_result_executor.go`. Grep-verify the exact routing key (`task_type` vs `assignee`) and the fresh-Job spawn mechanism (new CreateCommand, event rewrite, or other executor-visible signal) before writing code. Ginkgo tests with mock `GitClient` for retry cycles 1..3, redelivery idempotency, non-planning passthrough, non-pr-review passthrough, `AgentStatusSuccess` passthrough. Register `agent_controller_planning_retry_total{result="retry"}` counter. | DB 1, 2, 3, 6 (retry label), 7, 8 | retry-cycle ACs, redelivery-idempotency AC, passthrough ACs, reason sanitization/truncation ACs, `maxControllerPlanningRetries` constant AC | — |
| 2 | Layer 3: retry-exhaustion → GitHub COMMENT + `phase: human_review` + assignee clear via `ClearAssigneeIfHumanReview`. New minimal GitHub client wrapper (choose lib at prompt time — prefer `github.com/google/go-github/vXX` or the existing bborbe github lib if present, otherwise raw `net/http` + PAT). Wire GitHub client into factory. Handle GitHub-error swallow, missing-PR-metadata swallow, missing-token swallow — all log WARNING and still escalate frontmatter. Ginkgo tests with mock GitHub client for exhaustion, GitHub-error path, missing-PR path, defensive-`counter > 3` path. Register `agent_controller_planning_retry_total{result="exhausted"}` counter. Append the two CHANGELOG bullets in the same PR as prompt 2 lands. | DB 4, 5, 6 (exhausted label), 9 | exhaustion + COMMENT AC, GitHub-error AC, missing-PR AC, defensive-`counter > 3` AC | prompt 1 |

Rationale: prompt 1 is pure controller-internal logic (git-rest write + Kafka event) with no new external dependency — testable with existing mocks. Prompt 2 adds a new external dependency (GitHub API) and can only be exercised end-to-end after prompt 1 has laid down the frontmatter counter + phase-reset infrastructure. Splitting also allows prompt 2 to be reverted independently if the GitHub-token deploy prerequisite blocks it, while prompt 1's retry benefit (three attempts before permanent failure) still ships.

## Do-Nothing Option

If we don't do this: pr-review planning failures continue to land the PR in silent `REVIEW_REQUIRED` limbo. Operators discover the failure hours later by noticing a stale PR review status or by scanning task files for `phase: done` with a failed body. The manual escape (SHA-bump) works but requires the operator to know the recovery pattern. Layer 0 + Layer 1 (already shipped, v0.42.0) reduce the frequency of MiniMax `B`-case failures reaching this seam, but the tail is not zero — real prod incidents on 2026-06-30 confirmed the silent-limbo mode. Do-nothing is acceptable in the sense that the pipeline is not on fire, but it leaks operator attention on every failure, and the [[Harden PR-Reviewer Planning Validation and Visibility]] goal is left half-shipped (Layer 0 + Layer 1 without Layer 2 + Layer 3).

## Verification Result

**Verified:** 2026-07-01T14:24:43Z (HEAD 0a79358)
**Binary:** /Users/bborbe/Documents/workspaces/go/bin/dark-factory (dev)
**Scenario:** No E2E scenario by design (spec §Acceptance Criteria: "NO new E2E scenario. All behavior is reachable via Ginkgo unit tests with mock `GitClient` and mock GitHub client"). Verified via 16 ACs against fresh test-run + grep evidence.
**Evidence:**
- `make precommit` fresh run → "ready to commit" (exit 0)
- `go test ./pkg/command/...` → Ran 95 of 95 Specs, SUCCESS! 95 Passed
- `go test ./pkg/prcomment/...` → Ran 7 of 7 Specs, SUCCESS! 7 Passed
- `pkg/command/planning_retry.go:32:const maxControllerPlanningRetries = 3` (frozen constant)
- `pkg/command/planning_retry.go` covers all DB 1-9 branches; 11 `planning_retry_count` references in pkg/command/
- Ginkgo `PlanningRetryGate` Describe (`planning_retry_test.go`, 727 lines) covers passthroughs (non-planning, non-pr-review, success), retry attempts 1/2/3, cap escalation with COMMENT-template match, defensive counter>3, GitHub-error swallow, missing-PR swallow, negative-counter clamp, redelivery idempotency, reason sanitization (\n/\r strip + 200-rune truncate)
- `pkg/prcomment/pr_commenter_test.go` (202 lines) verifies frozen error substrings `planning-retry: cannot resolve PR from task:` and `planning-retry: github COMMENT post failed:` on all resolution / transport / non-2xx paths
- Merged PR #4 (commit 0a79358), Anthropic bot APPROVED on HEAD 83ea86f with "Ginkgo test coverage is thorough across all acceptance criteria"; CI test SUCCESS
**Verdict:** PASS
