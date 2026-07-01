---
status: completed
spec: [003-pr-reviewer-plan-recover]
summary: 'Implemented Layer 3 escalation on planning-retry exhaustion: PRCommenter posts a GitHub COMMENT (best-effort), sets phase: human_review, clears assignee, and increments exhausted metric'
execution_id: agent-task-controller-plan-recover-exec-006-spec-003-planning-retry-escalation
dark-factory-version: dev
created: "2026-07-01T12:11:43Z"
queued: "2026-07-01T12:26:41Z"
started: "2026-07-01T13:33:09Z"
completed: "2026-07-01T13:43:55Z"
branch: dark-factory/pr-reviewer-plan-recover
---

<summary>

- When all three controller planning retries are exhausted, the controller now escalates instead of leaving the task in silent limbo.
- It posts a plain COMMENT review on the pull request describing the failure and linking back to the task, so a human sees the failure on the PR itself.
- It moves the task into human review and clears the assignee, so the task surfaces in the operator inbox exactly like other escalations.
- The escalation is resilient: if the GitHub call fails, the token is missing, or the task file lacks PR metadata, the task still moves to human review and clears assignee — only the PR comment is skipped, with a warning logged.
- A Prometheus counter records each exhaustion so operators can watch how often auto-recovery gives up.
- The GitHub COMMENT never approves or requests changes — the controller never gates the PR merge decision.
- DEPLOY PREREQUISITES surfaced by this prompt: (1) the controller Secret needs a GitHub token with `pull-requests: write` scope; if absent, the comment is skipped (not a crash). (2) The task frontmatter must carry PR metadata fields (`repository` + `pull_request_number`, or a `pr_url`); no such fields exist in the controller's frontmatter contract today, so the pr-reviewer materialization side must populate them or the comment is skipped with a warning. Both are called out as open questions below.

</summary>

<objective>

Add controller-side Layer 3 escalation on planning-retry exhaustion (building on prompt 1's retry gate): when a failed `pr-review` planning result arrives with `planning_retry_count` already at the cap (`>= 3`), the controller appends a final `retry 3/3` progress line, posts a `COMMENT` review on the PR (best-effort), sets `phase: human_review`, and clears the assignee via `result.ClearAssigneeIfHumanReview`. A GitHub error, a missing token, or missing PR metadata does NOT block the frontmatter escalation — those are logged at WARNING and the task still moves to `human_review`. Register the `agent_controller_planning_retry_total{result="exhausted"}` increment. Implements spec Desired Behaviors 4, 5, 6 (exhausted label), and 9.

</objective>

<context>

Read `/workspace/CLAUDE.md` for project conventions.
Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-patterns.md`, `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`, `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`, `/home/node/.claude/plugins/marketplaces/coding/docs/go-factory-pattern.md`, `/home/node/.claude/plugins/marketplaces/coding/docs/go-composition.md`, `/home/node/.claude/plugins/marketplaces/coding/docs/go-http-service-guide.md`, `/home/node/.claude/plugins/marketplaces/coding/docs/go-security-linting.md`, and `/home/node/.claude/plugins/marketplaces/coding/docs/go-prometheus-metrics-guide.md`.

Read the spec `/workspace/specs/in-progress/003-pr-reviewer-plan-recover.md` IN FULL — this prompt implements Desired Behaviors 4, 5, 9, the `result="exhausted"` label of DB 6, and the corresponding Failure Modes (GitHub 5xx swallow, missing token swallow, missing PR metadata swallow). The retry loop (DBs 1, 2, 3, 7, 8, `result="retry"`) is prompt 1's — do NOT re-implement it.

DEPENDENCY: this prompt REQUIRES prompt 1 to have shipped. Before implementing, run `grep -rn "func NewPlanningRetryGate" /workspace/pkg/command/`. If it returns no match, STOP and report `status: failed` with message `"planning-retry gate not yet deployed (prompt 1)"` — do NOT re-implement the gate.

Read these source files IN FULL before writing code:

- `/workspace/pkg/command/planning_retry.go` (created by prompt 1) — the `PlanningRetryGate` interface, the `planningRetryGate` struct, `NewPlanningRetryGate`, `maxControllerPlanningRetries`, the `sanitizeReason`/`appendProgressLine`/`matchesRetryGate` helpers, and the at-cap branch that CURRENTLY returns `(false, nil)` (prompt 1's stub). You will REPLACE that at-cap branch with the escalation.
- `/workspace/pkg/command/planning_retry_test.go` (created by prompt 1) — the two at-cap tests ("counter at cap → passthrough" and "defensive counter > 3 → passthrough") currently assert `(false, nil)` + no write + no metric. You will REWRITE those two tests to assert escalation instead (see requirement 7).
- `/workspace/pkg/result/result_writer.go` — `ClearAssigneeIfHumanReview(merged lib.TaskFrontmatter) string` (exported). When `merged["phase"] == "human_review"` it captures `previous_assignee` and sets `assignee = ""`, returning the prior name. Route the assignee-clear through this chokepoint (spec 042 doctrine — do NOT duplicate the clear logic). Also reuse `ExtractFrontmatter`, `ExtractBody`.
- `/workspace/pkg/factory/factory.go` — `CreateCommandConsumer(...)` constructs `retryGate := command.NewPlanningRetryGate(gitClient, taskDir, currentDateTime)` (added by prompt 1). You will add a `PRCommenter` (or nil) argument to `NewPlanningRetryGate` and construct the commenter in the factory.
- `/workspace/main.go` — the single `application` struct (lines 49-66) with `argument`-tagged fields and `application.Run` (line 69). You will add a `GitHubToken` field and construct the `PRCommenter`, then pass it into `factory.CreateCommandConsumer`. There is a single `main.go` (no `cmd/` variant — verified).
- `/workspace/pkg/metrics/metrics.go` — the `PlanningRetryTotal` counter (added by prompt 1) already pre-registers the `"exhausted"` label in `init()`. You only reference `metrics.PlanningRetryTotal.WithLabelValues("exhausted").Inc()`; do NOT re-declare the counter.
- `/workspace/pkg/gitrestclient/git_rest_client.go` — for the `libhttp`/`net/http` client construction idiom used in this repo (`gitrestclient.NewGitRestClient` shows the HTTP-client + header-auth pattern). The `PRCommenter` implementation follows the same "small struct wrapping an `*http.Client` + base URL + auth header" shape.

Verified library facts (grepped at authoring time):

- `lib "github.com/bborbe/agent"` v0.70.0: `lib.TaskFrontmatter` accessors `String(key) (string, bool)`, `Int(key) (int, bool)`, `TaskType() TaskType`, `Assignee() TaskAssignee`. `lib.Task` fields `TaskIdentifier`, `Frontmatter`, `Content`.
- `domain "github.com/bborbe/vault-cli/pkg/domain"` v0.68.0: `domain.TaskPhaseHumanReview TaskPhase = "human_review"`.
- `libtime "github.com/bborbe/time"` v1.27.1: timestamp idiom `currentDateTime.Now().UTC().Format(time.RFC3339)`.
- `libhttp "github.com/bborbe/http"` v1.26.13 is a direct dependency (see `main.go` import). It provides HTTP server helpers; for the OUTBOUND GitHub call, use stdlib `net/http` directly (an `*http.Client` with a context-bound `http.NewRequestWithContext`) — this is the smallest surface and avoids adding a new dependency. NO `go-github` dependency exists in `go.mod` and none should be added (spec Non-goal: "Do NOT add a new secret or ConfigMap"; keeping the surface minimal keeps the deploy footprint small).
- `"github.com/golang/glog"` for logging; `errors "github.com/bborbe/errors"` for wrapping.

OPEN QUESTIONS surfaced from the spec's Assumptions (resolved conservatively here; flag in `## Improvements` if the resolution needs operator confirmation):

1. **PR metadata fields.** The spec assumes the PR URL/repo/number is discoverable from the task frontmatter (`pr_url`, `repository`, `pull_request_number`). Grep at authoring time found NO such fields in `bborbe/agent`'s frontmatter contract or in this repo's frontmatter usage. RESOLUTION: `PRCommenter` reads, in priority order, (a) `pr_url` (a full `https://github.com/<owner>/<repo>/pull/<n>` URL) if present, else (b) `repository` (an `<owner>/<repo>` slug) + `pull_request_number` (int). If neither resolves to a valid owner/repo/number, log the frozen WARNING `planning-retry: cannot resolve PR from task:` and SKIP the comment (still escalate frontmatter). This exactly matches the spec's "Task file missing PR URL / repo / PR number" Failure Mode. Document in `## Improvements` that the pr-reviewer materialization side must populate these fields for the COMMENT to fire, and that the field names are the ones this prompt chose.
2. **GitHub token.** The controller has no GitHub token surface today (only `GatewaySecret` for git-rest). RESOLUTION: add an OPTIONAL `GitHubToken` argument (`required:"false"`, empty default). When empty, `PRCommenter` is still constructed but its `PostComment` logs the frozen WARNING and skips — matching the spec's "GitHub token missing" Failure Mode (must not crash). Document in `## Improvements` that a `pull-requests: write` token must be added to the controller Secret for the COMMENT to fire.

</context>

<requirements>

1. **Define a `PRCommenter` interface in a new file `/workspace/pkg/prcomment/pr_commenter.go`** (`package prcomment`, BSD 3-line license header). Use public-interface + private-struct + `New*` constructor + counterfeiter annotation:
   ```go
   //counterfeiter:generate -o ../../mocks/pr_commenter.go --fake-name PRCommenter . PRCommenter

   // PRCommenter posts a plain COMMENT review on a GitHub pull request. It never
   // approves or requests changes — the controller never gates the PR merge.
   // PostComment is best-effort: it returns an error only for genuine send
   // failures; callers swallow the error and log at WARNING (the frontmatter
   // escalation must not be blocked by a failed comment).
   type PRCommenter interface {
       // PostComment posts body as a COMMENT review on the PR identified by
       // frontmatter (pr_url, or repository + pull_request_number). Returns an
       // error when the PR cannot be resolved, the token is missing, or the
       // GitHub API call fails. The error carries a frozen substring so callers
       // log the right WARNING.
       PostComment(ctx context.Context, frontmatter lib.TaskFrontmatter, body string) error
   }

   // NewPRCommenter constructs a PRCommenter. token may be empty — in that case
   // PostComment returns an error with the missing-token substring and posts nothing.
   func NewPRCommenter(httpClient *http.Client, token string) PRCommenter {
       return &prCommenter{httpClient: httpClient, token: token}
   }
   ```
   Imports: `lib "github.com/bborbe/agent"`, `"net/http"`, `"context"`.
   Add the counterfeiter generate directive and regenerate mocks with `cd /workspace && go generate ./...`. The mock lands at `/workspace/mocks/pr_commenter.go`. If `pkg/prcomment/` needs a `//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6 -generate` marker for `go generate` to pick it up, add one at the top of the file (mirror `pkg/metrics/metrics.go` line 12).

2. **Implement `(*prCommenter) PostComment`** in `/workspace/pkg/prcomment/pr_commenter.go`. Keep functions under funlen 80 / nestif 4; extract `resolvePR(frontmatter) (owner, repo string, number int, err error)`:
   - **Missing token guard (spec Failure Mode "GitHub token missing").** If `c.token == ""`, return `errors.Errorf(ctx, "planning-retry: github token missing; skipping COMMENT post")` — the frozen WARNING substring the caller logs is `planning-retry: github COMMENT post failed:` (the caller wraps this). Do NOT crash.
   - **Resolve PR (spec Failure Mode "missing PR metadata").** `resolvePR` reads `frontmatter`:
     - If `pr_url, ok := frontmatter.String("pr_url"); ok && pr_url != ""` — parse `https://github.com/<owner>/<repo>/pull/<n>` with a `regexp.MustCompile(`^https://github\.com/([^/]+)/([^/]+)/pull/(\d+)$`)` (compiled at package scope). On match, extract owner/repo/number.
     - Else read `repository` (`<owner>/<repo>` slug) via `frontmatter.String("repository")` and `pull_request_number` via `frontmatter.Int("pull_request_number")`. Validate the slug with `regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)` (spec Security: "regex-check the repo slug and number before invoking GitHub API") and require `number > 0`.
     - If neither path resolves, return `errors.Errorf(ctx, "planning-retry: cannot resolve PR from task: no pr_url or repository/pull_request_number in frontmatter")` — frozen substring `planning-retry: cannot resolve PR from task:`.
   - **Post the COMMENT review (spec DB 4b, 5).** Build the GitHub REST request:
     - URL: `https://api.github.com/repos/<owner>/<repo>/pulls/<number>/reviews`.
     - Method POST, body JSON `{"event":"COMMENT","body":"<body>"}` (marshal a small struct via `encoding/json`; do NOT hand-concatenate — the body may contain characters needing escaping). The event is the frozen literal `COMMENT` (never `APPROVE`/`REQUEST_CHANGES` — spec Constraint).
     - Headers: `Authorization: Bearer <token>`, `Accept: application/vnd.github+json`, `X-GitHub-Api-Version: 2022-11-28`, `Content-Type: application/json`.
     - Use `http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))` so the CQRS handler's context deadline flows through (spec Security: "context deadline flows from the CQRS handler"; no own retry loop, one call).
     - Send via `c.httpClient.Do(req)`. `defer resp.Body.Close()` (check the close error with `#nosec`-free idiom or `_ = resp.Body.Close()` — see `go-security-linting.md`). On transport error return `errors.Wrapf(ctx, err, "planning-retry: github COMMENT post failed")`. On non-2xx status, read a bounded slice of the body (e.g. `io.LimitReader(resp.Body, 512)`) and return `errors.Errorf(ctx, "planning-retry: github COMMENT post failed: status %d", resp.StatusCode)`.
     - On 2xx, return nil.
   - The frozen error substring `planning-retry: github COMMENT post failed:` MUST appear in every non-resolve, non-token send-failure error so the caller's WARNING log carries it (spec Failure Mode). The resolve-failure and token-missing errors carry their own frozen substrings above.

3. **Replace the at-cap branch in `(*planningRetryGate) Handle`** in `/workspace/pkg/command/planning_retry.go`. Where prompt 1 returns `(false, nil)` for `count >= maxControllerPlanningRetries`, now perform escalation. Add the `PRCommenter` as a gate collaborator: change `NewPlanningRetryGate` and the struct:
   ```go
   func NewPlanningRetryGate(
       gitClient gitclient.GitClient,
       taskDir string,
       currentDateTime libtime.CurrentDateTimeGetter,
       prCommenter prcomment.PRCommenter,
   ) PlanningRetryGate {
       return &planningRetryGate{
           gitClient:       gitClient,
           taskDir:         taskDir,
           currentDateTime: currentDateTime,
           prCommenter:     prCommenter,
       }
   }
   ```
   (Add `prCommenter prcomment.PRCommenter` to the struct; import `"github.com/bborbe/agent-task-controller/pkg/prcomment"`.)
   Implement the escalation (extract into `(*planningRetryGate) escalate(ctx, req, relPath, existingFrontmatter, reason) (handled bool, err error)` to keep `Handle` under funlen 80). Escalation logic (spec DB 4, 5, 9), in order:

   a. **Compute the sanitized reason** using prompt 1's `sanitizeReason` helper (reuse it; do not duplicate). `ts := g.currentDateTime.Now().UTC().Format(time.RFC3339)`.

   b. **Frontmatter escalation write (unconditional — spec DB 9).** Perform ONE `AtomicReadModifyWriteAndCommitPush` at `filepath.Join(g.gitClient.Path(), relPath)` with `msg := "[agent-task-controller] planning retry exhausted for task " + string(req.TaskIdentifier)`. The modify closure (mirrors prompt 1's shape and `result_writer.go`):
      1. `ExtractFrontmatter` + `ExtractBody` (wrap errors).
      2. `yaml.Unmarshal` frontmatter into `lib.TaskFrontmatter fm`.
      3. Idempotency: if `body` already contains a line (split `\n`, TrimSpace) with prefix `retry 3/3:`, treat the final progress line as already written — do NOT append a duplicate; still ensure `fm["phase"] == "human_review"` and the assignee clear are applied (they are idempotent). Track whether this is a fresh escalation via a captured `*bool` `firstEscalation` (true only when the `retry 3/3:` line was NOT already present) — used to gate the metric increment (step d) and the COMMENT post (step c) so redelivery does not double-post.
      4. If `firstEscalation`, append the final progress bullet via `appendProgressLine(body, "retry 3/3: "+reason+" at "+ts)` (frozen `retry 3/3:` — note the numerator is the literal cap `3`, not `count+1`, since at-cap means the third retry already happened; this final line records the exhaustion). NOTE: the AC for exhaustion says "no new `## Progress` retry line beyond a terminal marker" — write exactly one `retry 3/3:` terminal line and no further `retry N/3` lines.
      5. Set `fm["phase"] = "human_review"` (frozen).
      6. Call `result.ClearAssigneeIfHumanReview(fm)` — this clears `assignee` to `""` and captures `previous_assignee` because `phase == human_review` (spec 042 chokepoint; do NOT duplicate the clear).
      7. Do NOT rewrite `task_identifier` here (no fresh Job — the task is done retrying).
      8. Re-marshal and return `[]byte("---\n" + string(marshaled) + "---\n" + newBody)`.
      If the write returns an error, return `(false, errors.Wrapf(ctx, err, "planning-retry: escalation write for task %s", req.TaskIdentifier))` — a git-rest write failure surfaces (CQRS redelivers). The frontmatter escalation is the load-bearing part; if it fails, the whole command fails and is retried.

   c. **Post the COMMENT (best-effort — spec DB 4b, 9).** ONLY when `*firstEscalation` is true (avoid double-posting on redelivery). Build the body from the frozen template (spec DB 5):
      ```
      Automated pr-review planning failed after 3 controller retries and 3 in-agent retries. Last error: <reason>. Please investigate <task_link>.
      ```
      where `<controller_retries>` is the literal `3` (the cap), `<in_agent_retries>` is the fixed literal `3` (matching Layer 1), and `<task_link>` is the task file's repo-root-relative path `relPath` (the codebase has no richer canonical task-URL convention — grep at authoring time found only `[[WikiLink]]` obsidian refs in bodies, not a task-URL scheme; use `relPath` and document the choice in `## Improvements`). Call `g.prCommenter.PostComment(ctx, <the on-disk frontmatter you read>, commentBody)`. On error, log `glog.Warningf("planning-retry: github COMMENT post failed: task=%s err=%v", req.TaskIdentifier, err)` (frozen substring `planning-retry: github COMMENT post failed:`) and CONTINUE — do NOT return the error; the frontmatter escalation already landed. NOTE: pass the frontmatter you parsed inside the closure OR re-read it before the post — simplest is to pass `existingFrontmatter` (the on-disk snapshot from `FindTaskFilePath`), which carries the PR metadata fields the commenter resolves.

   d. **Increment the metric (spec DB 6, exhausted label).** ONLY when `*firstEscalation` is true, call `metrics.PlanningRetryTotal.WithLabelValues("exhausted").Inc()`. Then log `glog.Infof("planning-retry: exhausted after 3 retries for task %s; escalated to human_review", req.TaskIdentifier)`.

   e. Return `(true, nil)` — the gate handled the exhaustion; the caller (`NewTaskResultExecutor`) MUST NOT run the default `WriteResult`.

4. **Wire the commenter through the factory in `/workspace/pkg/factory/factory.go`.**
   - Add a `prCommenter prcomment.PRCommenter` parameter to `CreateCommandConsumer` (append after `currentDateTime`). Import `"github.com/bborbe/agent-task-controller/pkg/prcomment"`.
   - Pass it into the gate: `retryGate := command.NewPlanningRetryGate(gitClient, taskDir, currentDateTime, prCommenter)`.

5. **Wire the token + commenter in `/workspace/main.go`.**
   - Add a field to the `application` struct (mirror the `argument`-tag style of the existing fields; `required:"false"`, empty default, `display:"length"` since it is a secret):
     ```go
     GitHubToken string `required:"false" arg:"github-token" env:"GITHUB_TOKEN" usage:"GitHub token with pull-requests:write scope for posting planning-retry COMMENT reviews; empty disables the comment (frontmatter escalation still fires)" display:"length" default:""`
     ```
   - In `application.Run`, construct the commenter after `currentDateTime` (line 112). Use a plain `*http.Client` with a sane timeout:
     ```go
     prCommenter := prcomment.NewPRCommenter(&http.Client{Timeout: 30 * time.Second}, a.GitHubToken)
     ```
     (`net/http` and `time` are already imported in `main.go`.) Import `"github.com/bborbe/agent-task-controller/pkg/prcomment"`.
   - Pass `prCommenter` as the new trailing argument to the `factory.CreateCommandConsumer(...)` call (lines 152-162).

6. **Sibling entry-point check.** Run `grep -rn "factory.CreateCommandConsumer\|command.NewPlanningRetryGate\|prcomment.NewPRCommenter" /workspace/ --include="*.go"`. As verified at authoring time the complete production set is: `main.go` (one `CreateCommandConsumer` caller + one `NewPRCommenter` construction), `pkg/factory/factory.go` (the `CreateCommandConsumer` definition + the `NewPlanningRetryGate` construction). There is a single `main.go` (no `cmd/run-once` or `cmd/cli` variant). Update the factory test (`/workspace/pkg/factory/factory_suite_test.go` or any factory test that constructs `CreateCommandConsumer`) if it exercises the signature — check `grep -rn "CreateCommandConsumer" /workspace/pkg/factory/`. If any NEW call site appears not in this list, update it or document why it is out of scope in `## Improvements`.

7. **Rewrite the two at-cap tests in `/workspace/pkg/command/planning_retry_test.go`** (created by prompt 1). The gate constructor now takes a `PRCommenter` — update the `BeforeEach` (or per-test construction) to pass a `mocks.PRCommenter` (generated in requirement 1): `fakeCommenter := &mocks.PRCommenter{}` and `g := command.NewPlanningRetryGate(fakeGit, "tasks", clock, fakeCommenter)`. Update ALL existing `NewPlanningRetryGate` constructions in this test file to add the commenter argument (retry-attempt tests can pass a default `fakeCommenter` that is never expected to be called; assert `fakeCommenter.PostCommentCallCount() == 0` on the retry-attempt tests to prove COMMENT does not fire on retries — spec AC "still no COMMENT posted (this attempt is a retry, not exhaustion)"). REWRITE these two:
   - **exhaustion (on-disk `planning_retry_count: 3`):** on-disk file includes PR metadata (`repository: bborbe/maintainer`, `pull_request_number: 62`). `fakeCommenter.PostCommentReturns(nil)`. `Handle` returns `(true, nil)`. Run the captured escalation modify closure against the on-disk bytes; assert produced frontmatter has `phase: human_review`, `assignee: ""`, and the body contains a `retry 3/3:` terminal line. Assert `fakeCommenter.PostCommentCallCount() == 1`; capture args via `PostCommentArgsForCall(0)` and assert the body substring `Automated pr-review planning failed after 3 controller retries and 3 in-agent retries. Last error:` AND `Please investigate tasks/pr-123.md.` (the frozen template with `relPath` as `<task_link>`). Assert `PlanningRetryTotal{result="exhausted"}` delta == 1.
   - **defensive counter > 3 (on-disk `planning_retry_count: 5`):** treated as at-cap → escalation fires exactly as above: `phase: human_review`, `assignee: ""`, `PostCommentCallCount() == 1`, `exhausted` metric +1. Assert the counter is NOT normalized/rewritten to 3 in the produced frontmatter (spec AC: "no counter overwrite to normalize value") — assert the produced `planning_retry_count` is still whatever the escalation write leaves it (the escalation modify closure MUST NOT touch `planning_retry_count`; verify it either preserves `5` or leaves it unread — assert it is not set to `3`).

   ADD these NEW tests:
   - **GitHub error swallowed (spec AC + Failure Mode):** on-disk `planning_retry_count: 3` with PR metadata; `fakeCommenter.PostCommentReturns(errors.New("github 503"))`. Assert `Handle` returns `(true, nil)` (NOT an error — the frontmatter escalation is not blocked), produced frontmatter still `phase: human_review` + `assignee: ""`, `exhausted` metric still +1. (Log-substring capture of `planning-retry: github COMMENT post failed:` is optional — if you use a glog capture helper, assert it; otherwise assert the swallow via the `(true, nil)` return + escalation landing.)
   - **missing PR metadata → COMMENT skipped, escalation still fires (spec AC + Failure Mode):** on-disk `planning_retry_count: 3` WITHOUT `repository`/`pull_request_number`/`pr_url`; `fakeCommenter.PostCommentReturns(errors.New("planning-retry: cannot resolve PR from task: ..."))` (the real commenter returns this; the mock simulates it). Assert `Handle` returns `(true, nil)`, produced frontmatter `phase: human_review` + `assignee: ""`, `exhausted` metric +1, and the escalation still completed. (The gate does not itself inspect PR metadata — it delegates to `PRCommenter.PostComment`, which returns the resolve error; the gate swallows it. So the mock returning an error is the correct simulation of the missing-metadata path.)

8. **Add `PRCommenter` unit tests in a NEW file `/workspace/pkg/prcomment/pr_commenter_test.go`** (`package prcomment_test`; add a `pkg/prcomment/prcomment_suite_test.go` Ginkgo bootstrap mirroring `/workspace/pkg/command/command_suite_test.go`). Use `httptest.NewServer` to stand up a fake GitHub API and inject its URL — SINCE the production URL is hardcoded to `https://api.github.com`, make the base URL injectable: change `NewPRCommenter` to also accept a `baseURL string` (default `https://api.github.com` passed from the factory/main), OR expose an unexported `baseURL` field defaulting to `https://api.github.com` with a test-only setter via an `export_test.go` seam in `package prcomment`. Prefer the injectable `baseURL` parameter (cleaner): `NewPRCommenter(httpClient *http.Client, baseURL, token string)`; `main.go`/factory pass `"https://api.github.com"`. Update requirement 1's signature accordingly and thread `baseURL` through the factory + main (add a hardcoded `const githubAPIBaseURL = "https://api.github.com"` in `main.go` or `pkg/prcomment` and pass it). Cover:
   - **happy path:** `httptest` server asserts the request path is `/repos/bborbe/maintainer/pulls/62/reviews`, method POST, `Authorization: Bearer test-token`, and the JSON body has `"event":"COMMENT"` and the expected `"body"`. Server returns 200. `PostComment` returns nil. Frontmatter uses `repository: bborbe/maintainer` + `pull_request_number: 62`.
   - **pr_url path:** frontmatter uses `pr_url: https://github.com/bborbe/maintainer/pull/62` (no repository/number) → resolves to the same endpoint. Returns nil on 200.
   - **missing token:** `NewPRCommenter(client, baseURL, "")` → `PostComment` returns an error containing `github token missing`; NO HTTP request is made (assert the httptest handler was never hit).
   - **unresolvable PR:** frontmatter with neither `pr_url` nor `repository`/`pull_request_number` → error containing `planning-retry: cannot resolve PR from task:`; no HTTP request.
   - **invalid repo slug:** `repository: not a slug!!` → resolve error; no HTTP request (spec Security: regex-check the slug).
   - **non-2xx response:** server returns 500 → error containing `planning-retry: github COMMENT post failed:` and `status 500`.
   - **transport error:** point the client at an unroutable URL (or close the server before calling) → error containing `planning-retry: github COMMENT post failed:`.
   Coverage ≥80% for `pr_commenter.go`.

9. **CHANGELOG.** There is NO `CHANGELOG.md` in `/workspace` root today. Do NOT create one for this repo. If one exists (added since), append two `## Unreleased` bullets (spec Verification: "append two `## Unreleased` bullets — one for the Layer 2 retry loop, one for the Layer 3 escalation"). Since prompt 1 owns the Layer 2 bullet, add ONLY the Layer 3 bullet here: `- feat: Escalate exhausted pr-review planning retries — post a GitHub COMMENT review, set phase: human_review, clear assignee (best-effort comment; frontmatter escalation always lands)`. If no `CHANGELOG.md`, skip and note in `## Improvements`.

</requirements>

<constraints>

- Frozen COMMENT review body template (spec DB 5, load-bearing for operator search/dashboards): `Automated pr-review planning failed after 3 controller retries and 3 in-agent retries. Last error: <reason>. Please investigate <task_link>.` — `<controller_retries>` and `<in_agent_retries>` are both the literal `3`; `<task_link>` is the task file's repo-root-relative path.
- Frozen review event type: `COMMENT`. NEVER `REQUEST_CHANGES` or `APPROVE` — the controller never gates the PR (spec Constraint).
- Frozen phase target on exhaustion: `human_review`. Frozen assignee-clear routes through `result.ClearAssigneeIfHumanReview` (single chokepoint, spec 042) — do NOT duplicate the clear logic (spec Constraint).
- Frozen final progress line: `retry 3/3: <reason> at <RFC3339 timestamp>` (one terminal line; no further `retry N/3` lines on exhaustion).
- Frozen metric: `agent_controller_planning_retry_total{result="exhausted"}`, incremented once per fresh exhaustion (not on redelivery). Do NOT add labels beyond `result` (spec Non-goal: "Do NOT add a metric-per-retry cardinality explosion").
- A GitHub API failure, missing token, or missing PR metadata does NOT block the frontmatter escalation (spec DB 9). The escalation write (`phase: human_review` + assignee clear) is unconditional; the COMMENT post is best-effort with a `glog.Warning` carrying the frozen substring `planning-retry: github COMMENT post failed:`. Operator visibility falls back to the `assignee == ""` inbox signal.
- `<reason>` sanitization (strip `\n`/`\r`/control chars, truncate ≤200 runes) reuses prompt 1's `sanitizeReason` — do NOT duplicate it. The COMMENT body carries the same sanitized reason (spec Security: strip before interpolation into GitHub API JSON).
- PR resolution validates the repo slug (`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`) and requires `number > 0` before invoking the GitHub API (spec Security: "regex-check the repo slug and number").
- One git-rest POST (the escalation write) and at most one GitHub POST (the COMMENT). Neither has its own retry loop; the context deadline flows from the CQRS handler (spec Security: "No sleep, no exponential backoff loop"). Use `http.NewRequestWithContext`.
- No new secret or ConfigMap machinery beyond an OPTIONAL `GITHUB_TOKEN` env var wired through argument parsing; empty token disables the comment but not the escalation (spec Non-goal: "Do NOT add a new secret or ConfigMap for a GitHub token. Reuse whatever credential surface the controller already has ... if none exists, prompt-time must surface as an open question" — surfaced in Context OPEN QUESTIONS + `## Improvements`).
- No `go-github` or other new heavyweight dependency — use stdlib `net/http` + `encoding/json`.
- Do NOT retro-recover PRs already failed today — forward-going only (spec Non-goal). No batch job, no scan of existing task files.
- Do NOT change the executor's `(repo, sha)` dedup, the in-agent Layer 1 retry, or add retry recovery for non-planning phases (spec Non-goals).
- Do NOT add an E2E scenario — spec: "NO new E2E scenario"; all behavior reachable via Ginkgo unit tests with mock `GitClient` and mock `PRCommenter`.
- Existing tests in `/workspace/pkg/command/`, `/workspace/pkg/result/`, `/workspace/pkg/publisher/`, `/workspace/pkg/factory/`, and `/workspace/main_*_test.go` MUST still pass. `main_argument_parse_test.go` may assert the argument set — if adding `GitHubToken` breaks it, update the expected argument list there.
- Per `go-error-wrapping-guide.md`: `errors.Wrapf(ctx, ...)` / `errors.Errorf(ctx, ...)` — never `fmt.Errorf`, never `context.Background()` in `pkg/`.
- Per `go-precommit.md` / `.golangci.yml`: funlen 80, nestif 4, golines 100. Extract `resolvePR`, `escalate`, and `buildEscalationModifyFn` helpers to stay under limits.
- Per `go-security-linting.md`: handle `resp.Body.Close()` (no unchecked error), no hardcoded secrets, bounded body reads (`io.LimitReader`).
- Coverage ≥80% for `pkg/prcomment/pr_commenter.go` and the new escalation branch in `pkg/command/planning_retry.go`.
- Do NOT commit — dark-factory handles git.

</constraints>

<verification>

Run iteratively while implementing:

```
cd /workspace && go generate ./...
cd /workspace && go build ./...
cd /workspace && go test ./pkg/prcomment/... -v
cd /workspace && go test ./pkg/command/... -v
cd /workspace && go test ./pkg/factory/... ./pkg/result/... ./pkg/publisher/... ./pkg/metrics/...
cd /workspace && go test . -v   # main_argument_parse_test.go etc.
```

Frozen-substring grep checks (spec):

```
cd /workspace && grep -rn 'Automated pr-review planning failed after' pkg/command/   # frozen COMMENT template
cd /workspace && grep -rn '"event":"COMMENT"\|"COMMENT"' pkg/prcomment/               # frozen review event
cd /workspace && grep -rn 'planning-retry: github COMMENT post failed:' pkg/          # frozen WARNING substring
cd /workspace && grep -rn 'ClearAssigneeIfHumanReview' pkg/command/                   # routes through spec-042 chokepoint
cd /workspace && grep -rn 'human_review' pkg/command/planning_retry.go
```

Coverage:

```
cd /workspace && go test -coverprofile=/tmp/cover.out -mod=vendor ./pkg/prcomment/... ./pkg/command/... && go tool cover -func=/tmp/cover.out | grep -E 'pr_commenter|planning_retry'
```
Expect ≥80% for `pr_commenter.go` and the escalation branch.

Run ONCE at the end:

```
cd /workspace && make precommit
```

Expected: exit 0; `PRCommenter` specs pass (happy path, pr_url path, missing token, unresolvable PR, invalid slug, non-2xx, transport error); rewritten exhaustion + defensive->3 tests assert `human_review` + `assignee: ""` + one COMMENT + `exhausted` metric +1; GitHub-error and missing-metadata tests assert escalation still lands with COMMENT swallowed; retry-attempt tests (from prompt 1) still pass and assert `PostCommentCallCount() == 0`; all existing command/result/publisher/factory/main specs still pass.

</verification>
