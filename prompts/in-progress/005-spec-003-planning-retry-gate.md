---
status: failed
spec: [003-pr-reviewer-plan-recover]
execution_id: agent-task-controller-plan-recover-exec-005-spec-003-planning-retry-gate
dark-factory-version: dev
created: "2026-07-01T12:11:43Z"
queued: "2026-07-01T12:26:40Z"
started: "2026-07-01T12:26:42Z"
completed: "2026-07-01T13:01:33Z"
branch: dark-factory/pr-reviewer-plan-recover
lastFailReason: 'execute prompt: docker run failed: wait command: exit status 137'
---

<summary>

- When an automated PR-review planning attempt fails, the controller now retries the planning job up to three times on its own instead of leaving the PR stuck with no review and no signal.
- Each retry writes a visible `retry N/3: <reason> at <timestamp>` line into the task file's `## Progress` section so an operator can see the recovery in progress by reading the task or the git history.
- A retry bumps a `planning_retry_count` counter in the task file, resets the task back to the planning phase, and hands it a fresh identity so the pipeline re-attempts the review from scratch.
- The retry only fires for PR-review tasks that failed in the planning phase; every other task type, phase, and result (success, skipped, non-planning failure) is untouched and flows through the existing path.
- The behavior is safe under duplicate delivery: replaying the same failed result does not double-count, does not add a duplicate progress line, and does not spawn a second attempt.
- A new Prometheus counter records each retry so operators can watch the retry rate.
- The escalation on final exhaustion (posting a PR comment, moving the task to human review) is deliberately NOT in this prompt — it ships in prompt 2, which depends on this one.

</summary>

<objective>

Add controller-side Layer 2 recovery for failed PR-review planning results: on a `failed` result for a `pr-review` task in `phase: planning`, increment a `planning_retry_count` frontmatter counter, append a `retry N/3` line to the task file's `## Progress` section, reset `phase` to `planning`, and requeue a fresh planning attempt by rewriting the task file's `task_identifier` to a new UUID (which bypasses the executor's `(repo, sha, task_identifier)` dedup and causes the scanner to republish → executor spawns a fresh Job). Cap at three controller retries. Register a `agent_controller_planning_retry_total{result="retry"}` counter. This is idempotent under Kafka redelivery. Exhaustion escalation (GitHub comment + `human_review`) is prompt 2 and is intentionally stubbed here.

</objective>

<context>

Read `/workspace/CLAUDE.md` for project conventions.
Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-patterns.md`, `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`, `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`, `/home/node/.claude/plugins/marketplaces/coding/docs/go-time-injection.md`, `/home/node/.claude/plugins/marketplaces/coding/docs/go-factory-pattern.md`, `/home/node/.claude/plugins/marketplaces/coding/docs/go-prometheus-metrics-guide.md`, and `/home/node/.claude/plugins/marketplaces/coding/docs/go-cqrs.md`.

Read the spec `/workspace/specs/in-progress/003-pr-reviewer-plan-recover.md` IN FULL — this prompt implements Desired Behaviors 1, 2, 3, 6 (the `result="retry"` label), 7, and 8, plus the reason sanitization/truncation constraints from the Security section and the `maxControllerPlanningRetries` constant. Desired Behaviors 4, 5, 9 (exhaustion → GitHub COMMENT + `human_review`) are OUT OF SCOPE for this prompt (prompt 2).

Read these source files IN FULL before writing code:

- `/workspace/pkg/command/task_result_executor.go` — the current `NewTaskResultExecutor(writer result.ResultWriter) cdb.CommandObjectExecutorTx`. This is the interception point. The handler deserializes `commandObject.Command.Data` into `var req lib.Task`, validates, then calls `writer.WriteResult(ctx, req)`. You will insert the retry gate BEFORE the `writer.WriteResult` call.
- `/workspace/pkg/result/result_writer.go` — read `WriteResult`, `FindTaskFilePath`, `ExtractFrontmatter`, `ExtractBody`, `ClearAssigneeIfHumanReview`. In particular `FindTaskFilePath(ctx, gitClient, taskDir, id) (string, lib.TaskFrontmatter, error)` returns the matched repo-root-relative path plus the on-disk merged frontmatter; it returns `("", nil, nil)` when no file matches (not an error). You will reuse `FindTaskFilePath`, `ExtractFrontmatter`, and `ExtractBody` (all exported from `package result`).
- `/workspace/pkg/gitrestclient/git_rest_client.go` lines 298-342 — the `GitClient` interface. The methods you use: `ReadFile(ctx, relPath) ([]byte, error)`, `Path() string`, `AtomicReadModifyWriteAndCommitPush(ctx, absPath, modify func(current []byte) ([]byte, error), message string) error`.
- `/workspace/pkg/scanner/task_identifier.go` — `InjectTaskIdentifier(ctx context.Context, content []byte, id string) ([]byte, error)` is EXPORTED from `package scanner`. It replaces/prepends the `task_identifier` frontmatter line. Use `scanner.InjectTaskIdentifier` together with `removeTaskIdentifier` semantics — NOTE `removeTaskIdentifier` is UNEXPORTED, so you cannot call it; instead do the read-modify-write inside the `AtomicReadModifyWriteAndCommitPush` modify closure by (a) parsing frontmatter, (b) mutating fields, (c) re-marshaling with the fresh `task_identifier`. See requirement 4 for the exact approach.
- `/workspace/pkg/factory/factory.go` — `CreateCommandConsumer(...)` wires `command.NewTaskResultExecutor(resultWriter)` at line 34. You will change this call and add parameters.
- `/workspace/main.go` lines 88-170 — the single `application.Run`. `gitClient` is created at line 91, `currentDateTime := libtime.NewCurrentDateTime()` at line 112, `a.TaskDir` is the task dir, and `factory.CreateCommandConsumer(...)` is called at lines 152-162. There is a single `main.go` (no `cmd/` variant).
- `/workspace/pkg/metrics/metrics.go` — the metrics package. Metrics are declared as package-level `promauto.NewCounterVec` vars and pre-initialized with `.WithLabelValues(...).Add(0)` in `init()`. You will add a new counter following `ResultsWrittenTotal`'s exact shape.
- `/workspace/pkg/command/task_result_executor_test.go` IN FULL — the Ginkgo harness (`package command_test`, `mocks.ResultWriter`, `buildCommandObject`, `base.ParseEvent`, `libtimetest "github.com/bborbe/time/test"`). Your new tests extend this file. NOTE: the sole `command.NewTaskResultExecutor(fakeWriter)` construction is in the `BeforeEach` (line 33) — it must gain the new arguments.
- `/workspace/pkg/command/task_create_task_executor_test.go` — for the `mocks.GitClient` usage patterns: `PathReturns`, `ReadFileReturns`, `ReadFileReturnsOnCall`, `ListFilesReturns`, `AtomicReadModifyWriteAndCommitPushStub`, `AtomicReadModifyWriteAndCommitPushArgsForCall`, `AtomicReadModifyWriteAndCommitPushCallCount`.

Verified library facts (grepped at authoring time — DO NOT re-derive from training data):

- `lib "github.com/bborbe/agent"` (v0.70.0). `lib.Task` has fields `TaskIdentifier lib.TaskIdentifier`, `Frontmatter lib.TaskFrontmatter`, `Content lib.TaskContent`.
- `lib.TaskFrontmatter` is `map[string]interface{}` with typed accessors:
  - `func (f TaskFrontmatter) TaskType() TaskType` — reads `task_type`; returns `TaskType("")` when absent/non-string.
  - `func (f TaskFrontmatter) Assignee() TaskAssignee` — reads `assignee`.
  - `func (f TaskFrontmatter) Phase() *domain.TaskPhase` — reads `phase`, normalizes aliases, returns nil when absent/empty.
  - `func (f TaskFrontmatter) String(key string) (string, bool)` — generic string accessor.
  - `func (f TaskFrontmatter) Int(key string) (int, bool)` — accepts both `int` (JSON) and `float64` (YAML) underlying types; ok=false when absent/non-numeric.
- `lib.TaskType` constant: `lib.TaskTypePRReview TaskType = "pr-review"` (in `agent_task-type.go`). `TaskType` has `.String()`.
- `lib.TaskAssignee` is a `string` type. The canonical pr-reviewer assignee value observed in this repo's tests is the string `"pr-reviewer-agent"` (see `pkg/result/result_writer_test.go` line 799 and `pkg/command/task_update_frontmatter_executor_test.go` line 288).
- `domain "github.com/bborbe/vault-cli/pkg/domain"` (v0.68.0): `domain.TaskPhasePlanning TaskPhase = "planning"`, `domain.TaskPhaseHumanReview TaskPhase = "human_review"`. `TaskPhase` has `.String()`. `domain.NormalizeTaskPhase(raw string) (TaskPhase, bool)`.
- `libtime "github.com/bborbe/time"` (v1.27.1): `libtime.CurrentDateTimeGetter` interface with `Now() DateTime`; `libtime.NewCurrentDateTime() CurrentDateTime` (settable via `SetNow(DateTime)`). Timestamp idiom in this repo: `currentDateTime.Now().UTC().Format(time.RFC3339)` (see `pkg/result/result_writer.go` line 304). Test helper: `libtimetest "github.com/bborbe/time/test"` `libtimetest.ParseDateTime("2026-01-15T10:00:00Z")` returns a `libtime.DateTime` (see `pkg/command/task_result_executor_test.go` line 68 and `pkg/publisher/task_publisher_test.go` lines 42-43 which call `currentDateTime.SetNow(fixedTime)`).
- `"github.com/google/uuid"` (v1.6.0) is already a direct dependency. `uuid.NewString()` returns a fresh random UUID string.
- The failure marker: the failed pr-reviewer result arrives as a `lib.Task` whose `Content` (body) contains a `## Result` section with a line `Status: failed` (verified in `pkg/result/result_writer_test.go` — failed results carry body `"## Result\nStatus: failed\n"` while frontmatter `status` stays `in_progress`). The gate detects failure by scanning `string(req.Content)` for a line whose trimmed form starts with `Status: failed` (case-sensitive, matching the agent's emitted marker). This is the "body marker" path the spec's Assumptions describe.

Scanner republish mechanism (the "fresh Job spawn" observable requirement, spec DB 2e): the controller does NOT spawn K8s Jobs. The `bborbe/agent` executor spawns Jobs from scanner-published task events, and dedups on `(repo, sha, task_identifier)` plus phase-allowlist gates (see `/home/node/.claude/plugins/marketplaces/coding/docs`-adjacent flow doc `task-flow-and-failure-semantics.md` in the agent module). Therefore the controller triggers a fresh attempt by writing the task file back with (a) `phase: planning` (re-enters the executor allowlist) AND (b) a brand-new `task_identifier` UUID (defeats the `(repo, sha, task_identifier)` dedup). The vault scanner then observes the changed file and republishes it; the executor spawns a fresh Job. This is the chosen mechanism for this spec — implement exactly this; do not attempt to publish a Kafka CreateCommand or call the executor directly.

DEPENDENCY NOTE: this prompt is prompt 1 of 2. It ships the retry loop (`result="retry"`) only. Retry EXHAUSTION (`planning_retry_count` at cap → GitHub COMMENT + `phase: human_review` + assignee clear + `result="exhausted"`) ships in prompt 2. In this prompt, when the gate detects the counter is already at cap, it MUST fall through to the existing `WriteResult` path (the pre-spec behavior) so nothing regresses — do NOT invent a partial escalation here. See requirement 6.

</context>

<requirements>

1. **Add the retry-total Prometheus counter in `/workspace/pkg/metrics/metrics.go`.** Follow the exact shape of `ResultsWrittenTotal` (a `promauto.NewCounterVec` package var with a single `result` label):
   ```go
   // PlanningRetryTotal counts controller-side pr-review planning-retry gate outcomes
   // by result ("retry" | "exhausted"). "passthrough" is intentionally NOT a label —
   // the metric fires only when the retry gate matches (spec DB 7).
   var PlanningRetryTotal = promauto.NewCounterVec(
       prometheus.CounterOpts{
           Name: "agent_controller_planning_retry_total",
           Help: "Total number of controller-side pr-review planning-retry gate outcomes, by result.",
       },
       []string{"result"},
   )
   ```
   In `init()`, pre-initialize both labels to zero (mirror the `ResultsWrittenTotal.WithLabelValues(...).Add(0)` block):
   ```go
   PlanningRetryTotal.WithLabelValues("retry").Add(0)
   PlanningRetryTotal.WithLabelValues("exhausted").Add(0)
   ```
   Register the `"exhausted"` label now even though prompt 1 never increments it — prompt 2 consumes it, and pre-registering avoids a scrape-time gap. Do NOT add this counter to the `Metrics` interface or `defaultMetrics` struct — the retry gate references the package var directly (matching how `result_writer.go` references `metrics.ResultsWrittenTotal` directly). Do NOT add any other label.

2. **Add the frozen constant.** In a new file `/workspace/pkg/command/planning_retry.go` (`package command`, BSD 3-line license header copied from `/workspace/pkg/command/task_result_executor.go` lines 1-3), declare:
   ```go
   // maxControllerPlanningRetries is the frozen cap on controller-side planning
   // retries. Invariant per spec 003 — never an env var, CLI flag, config field,
   // or CRD field.
   const maxControllerPlanningRetries = 3
   ```

3. **Add a `PlanningRetryGate` type in `/workspace/pkg/command/planning_retry.go`** that holds the collaborators it needs and exposes a single method the result executor calls. Use the public-interface + private-struct + `New*` constructor pattern (per `go-patterns.md`), returning an interface:
   ```go
   //counterfeiter:generate -o ../../mocks/planning_retry_gate.go --fake-name PlanningRetryGate . PlanningRetryGate

   // PlanningRetryGate decides whether a failed pr-review planning result should be
   // retried controller-side. Handle inspects the incoming result and the on-disk
   // task state and, when the retry gate matches, performs the retry write (counter
   // bump + ## Progress append + phase reset + fresh task_identifier) and returns
   // handled=true. When the gate does not match (non-pr-review, non-planning,
   // success/skipped, no failure marker, or counter already at cap in this prompt),
   // it returns handled=false and the caller falls through to the default
   // WriteResult path.
   type PlanningRetryGate interface {
       Handle(ctx context.Context, req lib.Task) (handled bool, err error)
   }

   // NewPlanningRetryGate constructs the gate.
   func NewPlanningRetryGate(
       gitClient gitclient.GitClient,
       taskDir string,
       currentDateTime libtime.CurrentDateTimeGetter,
   ) PlanningRetryGate {
       return &planningRetryGate{
           gitClient:       gitClient,
           taskDir:         taskDir,
           currentDateTime: currentDateTime,
       }
   }

   type planningRetryGate struct {
       gitClient       gitclient.GitClient
       taskDir         string
       currentDateTime libtime.CurrentDateTimeGetter
   }
   ```
   Import aliases: `lib "github.com/bborbe/agent"`, `libtime "github.com/bborbe/time"`, `gitclient "github.com/bborbe/agent-task-controller/pkg/gitrestclient"`.
   After adding the `//counterfeiter:generate` directive, regenerate mocks with `cd /workspace && go generate ./...` (or `make generate` if the Makefile exposes it — check `/workspace/Makefile*`). The mock lands at `/workspace/mocks/planning_retry_gate.go`.

4. **Implement `(*planningRetryGate) Handle`** per spec Desired Behaviors 1, 2, 3, 8. Keep the method body under the funlen limit (80 lines — check `/workspace/.golangci.yml`); extract helpers as needed (e.g. `matchesRetryGate`, `sanitizeReason`, `buildRetryModifyFn`). Logic in order:

   a. **Gate match (spec DB 1, 7).** Return `(false, nil)` immediately (default passthrough — do NOT touch `PlanningRetryTotal`) unless ALL of:
      - `req.Frontmatter.TaskType() == lib.TaskTypePRReview` OR (`task_type` absent AND `req.Frontmatter.Assignee() == lib.TaskAssignee("pr-reviewer-agent")`). Prefer `task_type` when present; fall back to the assignee identity. Rationale: the spec Assumptions permit either routing key; `task_type` is the durable field (`TaskFrontmatter.TaskType()` exists), assignee is the fallback.
      - The incoming result's phase is planning: `p := req.Frontmatter.Phase(); p != nil && p.String() == domain.TaskPhasePlanning.String()`.
      - The body carries the failure marker: scan `string(req.Content)` line-by-line; a line whose `strings.TrimSpace(line)` has prefix `"Status: failed"` means failure. If NO failure marker is present, log `glog.V(2).Infof("planning-retry: no failure signal; skipping retry gate for task %s", req.TaskIdentifier)` (frozen substring `planning-retry: no failure signal;` per spec Failure Modes) and return `(false, nil)`.
      If any of the three conditions is not met, return `(false, nil)` with no metric increment and no log beyond an optional `glog.V(3)`.

   b. **Locate the on-disk task file (spec DB 2, DB 8).** Call `result.FindTaskFilePath(ctx, g.gitClient, g.taskDir, req.TaskIdentifier)`. If it errors, return `(false, errors.Wrapf(ctx, err, "planning-retry: find task file"))` — a genuine git-rest error should surface (the CQRS handler will retry the whole command). If the returned relPath is `""` (no matching file), log `glog.Warningf("planning-retry: task file not found for %s; skipping", req.TaskIdentifier)` and return `(false, nil)` (fall through). The returned `existingFrontmatter` (2nd value) is the authoritative on-disk frontmatter — read `planning_retry_count` from it, NOT from `req.Frontmatter` (the incoming result may not carry the counter).

   c. **Read the counter defensively (spec Security).** `count, _ := existingFrontmatter.Int("planning_retry_count")`. Clamp: if `count < 0`, set `count = 0`. Do NOT clamp the high end here — instead:
      - If `count >= maxControllerPlanningRetries` (i.e. `>= 3`, covering the defensive `>3` case): this is EXHAUSTION. In THIS prompt, log `glog.V(2).Infof("planning-retry: counter at cap (%d) for %s; exhaustion escalation ships in prompt 2, falling through", count, req.TaskIdentifier)` and return `(false, nil)` so the default `WriteResult` path runs. Do NOT increment `PlanningRetryTotal`. (Prompt 2 replaces this branch with the escalation.)
      - Otherwise `count < 3` → proceed to retry (step d).

   d. **Compute the sanitized reason (spec DB 3, Security).** Extract the last-error string from the failed result body: use the first non-empty line after the `Status: failed` marker, or fall back to the whole trimmed body if no message line exists. Sanitize via a helper `sanitizeReason(raw string) string`:
      - Replace every `\r` and `\n` with a single space (strip newlines).
      - Strip other ASCII control characters (`< 0x20` other than the space you just substituted) — drop them.
      - Collapse the result and `strings.TrimSpace`.
      - Truncate to at most 200 runes (use rune-aware truncation, not byte slicing, to avoid splitting a multibyte rune). If truncated, that is fine — no ellipsis required by the spec, but you MAY append `"…"` (the AC allows "≤ 200 + suffix"). Keep it simple: truncate to 200 runes, no suffix.

   e. **Idempotency mechanism (spec DB 8) — enforced inside the modify closure.** The counter bump and the `## Progress` append happen in the SAME `AtomicReadModifyWriteAndCommitPush` write (step f), so the idempotency guard also lives inside the modify closure where the on-disk state is re-read under the git lock. Use `count` (the pre-bump value read from `existingFrontmatter` in step c) as the EXPECTED pre-bump value. Inside the closure, re-read `onDiskCount` from the current bytes. The retry proceeds ONLY when `onDiskCount == count` AND the current body does NOT already contain a line whose `strings.TrimSpace` starts with `retry <count+1>/3:`. If EITHER guard fails (a concurrent or redelivered write already advanced the counter, or the attempt line is already present), the closure returns the `current` bytes UNCHANGED and sets a captured `*bool` (`bump`) to `false` — no counter change, no duplicate `## Progress` line, no fresh `task_identifier`. When both guards pass, the closure performs the mutation and sets `*bump = true`. The metric increment (step g) is conditioned on `*bump == true`. `Handle` always returns `(true, nil)` once it reaches this write (whether or not the write mutated), so redelivery does NOT fall through to the default `WriteResult`. Set `<ts> = g.currentDateTime.Now().UTC().Format(time.RFC3339)` in `Handle` and capture it in the closure. Do NOT perform any pre-closure body scan for idempotency — the git-lock re-read inside the closure is the single authoritative check.

   f. **Perform the retry write (spec DB 2a-2e).** Build `absPath := filepath.Join(g.gitClient.Path(), relPath)`. Call `g.gitClient.AtomicReadModifyWriteAndCommitPush(ctx, absPath, modify, msg)` where:
      - `msg := "[agent-task-controller] planning retry " + strconv.Itoa(count+1) + "/3 for task " + string(req.TaskIdentifier)`.
      - `modify := func(current []byte) ([]byte, error)` does, under the git lock:
        1. `fmStr, err := result.ExtractFrontmatter(ctx, current)` (wrap error).
        2. `body, err := result.ExtractBody(ctx, current)` (wrap error).
        3. Unmarshal `fmStr` into `lib.TaskFrontmatter` via `yaml.Unmarshal([]byte(fmStr), &fm)` (import `"gopkg.in/yaml.v3"` — matching `result_writer.go`'s inlined yaml usage). Wrap unmarshal errors.
        4. Re-read the on-disk counter: `onDiskCount, _ := fm.Int("planning_retry_count")`. **Idempotency guard:** if `onDiskCount != count`, return `current` unchanged and set the captured `*bump = false` — a concurrent/redelivered write already advanced the counter. Otherwise continue.
        5. Also guard the `## Progress` duplicate line: if `body` already contains a line (split on `\n`, `TrimSpace`) with prefix `retry <count+1>/3:`, return `current` unchanged, set `*bump = false`.
        6. Set `fm["planning_retry_count"] = count + 1`.
        7. Set `fm["phase"] = "planning"` (frozen phase reset — a plain string, since `Phase()` reads the `phase` key as a string).
        8. Set `fm["task_identifier"] = uuid.NewString()` — fresh identity to bypass the executor `(repo, sha, task_identifier)` dedup and force a fresh Job spawn (spec DB 2e). Import `"github.com/google/uuid"`. Set BOTH the frontmatter `task_identifier` key here. (The scanner's `InjectTaskIdentifier` prepends the line for files that lack it; here the file already has the key, so overwriting the map value + re-marshaling is the correct and simpler path — do NOT call `scanner.InjectTaskIdentifier` in the modify closure.)
        9. Append the progress line: build the new body by ensuring a `## Progress` section exists. Implement a helper `appendProgressLine(body, line string) string`:
           - `line := "retry " + strconv.Itoa(count+1) + "/3: " + reason + " at " + ts`.
           - If `body` already contains a line equal (after TrimSpace) to the exact header `## Progress`, append `"\n- " + line` after the LAST existing `## Progress` bullet region — simplest robust approach: append `"\n- " + line + "\n"` to the end of the body (bullets accumulate; operators grep for `retry N/3`). Ensure the body ends with a newline before appending.
           - If no `## Progress` header exists, append `"\n## Progress\n\n- " + line + "\n"` to the end of the body (create the section, spec Failure Mode "Task file missing `## Progress` section on first retry").
           NOTE: the AC regex is `^retry N/3: .* at [0-9T:+-]+$` matched against a "line" — the leading `- ` bullet marker means the AC checks the content AFTER any list marker. The spec's frozen `## Progress` line format is exactly `retry N/3: <reason> at <RFC3339 timestamp>`. Write the bullet as `- retry N/3: ... at ...` so the frozen substring `retry N/3:` is present and greppable; the test asserts the line CONTAINS a match for `retry <n>/3: .* at <rfc3339>`. Confirm your test regex accounts for the `- ` prefix (use `ContainSubstring` or a regex without `^` anchoring on the bullet).
        10. Re-marshal: `marshaled, err := yaml.Marshal(map[string]any(fm))` (wrap error), then return `[]byte("---\n" + string(marshaled) + "---\n" + newBody), nil`. This mirrors `result_writer.go`'s `buildResultModifyFn` return shape exactly.
      - If `AtomicReadModifyWriteAndCommitPush` returns an error, return `(false, errors.Wrapf(ctx, err, "planning-retry: write retry for task %s", req.TaskIdentifier))` — a git-rest write failure should surface so the CQRS layer can redeliver.

   g. **Increment the metric and finish (spec DB 6, retry label).** After a successful write, if the captured `*bump` is true, call `metrics.PlanningRetryTotal.WithLabelValues("retry").Inc()` and log `glog.Infof("planning-retry: attempt %d/3 for task %s (reason=%q)", count+1, req.TaskIdentifier, reason)` (frozen substring `planning-retry:`). If `*bump` is false (idempotent no-op inside the closure), do NOT increment. Return `(true, nil)` — the gate handled this result; the caller MUST NOT run the default `WriteResult`.

5. **Wire the gate into `NewTaskResultExecutor` in `/workspace/pkg/command/task_result_executor.go`.**
   - Change the signature from `func NewTaskResultExecutor(writer result.ResultWriter) cdb.CommandObjectExecutorTx` to:
     ```go
     func NewTaskResultExecutor(
         writer result.ResultWriter,
         retryGate PlanningRetryGate,
     ) cdb.CommandObjectExecutorTx {
     ```
     (Inject the gate as a collaborator per `go-composition.md` — do NOT construct it inside the executor.)
   - Inside the handler, AFTER the `req.Validate(ctx)` success and BEFORE the `writer.WriteResult(ctx, req)` call, insert:
     ```go
     handled, err := retryGate.Handle(ctx, req)
     if err != nil {
         return nil, nil, errors.Wrapf(ctx, err, "planning retry gate for task %s", req.TaskIdentifier)
     }
     if handled {
         // The retry gate performed the write (or idempotent no-op); skip the default
         // WriteResult so we do not double-write. Still emit the result event so the
         // CQRS framework's outer return shape is unchanged (spec Constraint: the
         // retry decision is transparent to the CQRS layer).
         event, err := base.ParseEvent(ctx, req)
         if err != nil {
             return nil, nil, errors.Wrapf(ctx, err, "parse result event for task %s", req.TaskIdentifier)
         }
         eventID := base.EventID(req.TaskIdentifier)
         return eventID.Ptr(), event, nil
     }
     ```
     Then the existing `writer.WriteResult(...)` path runs unchanged for the non-handled case.
   - Do NOT change the outer return shape or introduce new sentinel errors surfaced to the framework (spec Constraint). The `errors.Wrapf(ctx, err, ...)` on a real git error is a normal error return, matching the existing `WriteResult` error path.

6. **Wire the gate through the factory in `/workspace/pkg/factory/factory.go`.**
   - The `CreateCommandConsumer` signature already carries `gitClient gitclient.GitClient`, `taskDir string`, and `currentDateTime libtime.CurrentDateTimeGetter` (verified — they are used by `NewCreateTaskExecutor`). Construct the gate and pass it to the result executor:
     ```go
     retryGate := command.NewPlanningRetryGate(gitClient, taskDir, currentDateTime)
     executors := cdb.CommandObjectExecutorTxs{
         command.NewTaskResultExecutor(resultWriter, retryGate),
         command.NewIncrementFrontmatterExecutor(gitClient, taskDir),
         command.NewUpdateFrontmatterExecutor(gitClient, taskDir),
         command.NewCreateTaskExecutor(gitClient, taskDir, vaultName, currentDateTime),
     }
     ```
   - NOTE per `go-factory-pattern.md`: `CreateCommandConsumer` is a factory — a single `New*` call to build the gate is acceptable wiring (no loops/conditionals/business logic). Do NOT add logic here.
   - No new parameter is needed on `CreateCommandConsumer` (all collaborators already flow in). `main.go` requires NO change for this prompt — verify with the sibling entry-point grep in requirement 8.

7. **Update the `NewTaskResultExecutor` construction in `/workspace/pkg/command/task_result_executor_test.go`.** The `BeforeEach` at line 33 constructs `command.NewTaskResultExecutor(fakeWriter)`. Add a `mocks.PlanningRetryGate` (the counterfeiter mock generated in requirement 3) and pass it:
   ```go
   fakeGate = &mocks.PlanningRetryGate{}
   // Default: gate does not handle → falls through to WriteResult (preserves existing tests).
   fakeGate.HandleReturns(false, nil)
   executor = command.NewTaskResultExecutor(fakeWriter, fakeGate)
   ```
   The existing four tests (valid command, malformed JSON, empty task ID, WriteResult error) must still pass — with `HandleReturns(false, nil)` the gate is a transparent passthrough. Add a `fakeGate` field to the `var (...)` block. Add these NEW `Context` tests to the same `Describe`:
   - **gate handled → WriteResult skipped:** set `fakeGate.HandleReturns(true, nil)`; send any valid task; assert `fakeWriter.WriteResultCallCount() == 0`, `handleErr` is nil, `eventID` is non-nil and equals the task identifier, `resultEvent` is non-nil.
   - **gate errors → wrapped and returned:** set `fakeGate.HandleReturns(false, errors.New("git-rest 503"))`; assert `handleErr` is non-nil, contains substring `planning retry gate for task`, `fakeWriter.WriteResultCallCount() == 0`.
   - **gate not handled → WriteResult runs (regression guard):** default `HandleReturns(false, nil)`; assert `fakeWriter.WriteResultCallCount() == 1` and the passed task matches.

8. **Add the gate unit tests in a NEW file `/workspace/pkg/command/planning_retry_test.go`** (`package command_test`; suite is bootstrapped by `command_suite_test.go`). Use `mocks.GitClient`, `libtime.NewCurrentDateTime()` with `SetNow(libtimetest.ParseDateTime("2026-07-01T12:00:00Z"))` for a deterministic RFC3339 timestamp, and drive `g := command.NewPlanningRetryGate(fakeGit, "tasks", clock)`. For the on-disk task-file lookup, `FindTaskFilePath` calls `ListFiles(ctx, "tasks/*.md")` then `ReadFile` per path — stub `fakeGit.ListFilesReturns([]string{"tasks/pr-123.md"}, nil)` and `fakeGit.ReadFileReturns(<content bytes>, nil)` so the file matches `req.TaskIdentifier`. The matched file's frontmatter `task_identifier` must equal `string(req.TaskIdentifier)` for `FindTaskFilePath` to match. Stub `fakeGit.PathReturns("/repo")`. Capture the retry write via `AtomicReadModifyWriteAndCommitPushArgsForCall(0)` (the 3rd return value is the `modify func`; run it against the on-disk content to inspect the produced bytes). Cover ALL of these ACs (spec Acceptance Criteria — the retry-related subset):

   Build the incoming `req lib.Task` with helper values. A canonical planning-failure result: `TaskIdentifier = "pr-123"`, `Frontmatter = lib.TaskFrontmatter{"task_type": "pr-review", "phase": "planning", "assignee": "pr-reviewer-agent"}`, `Content = lib.TaskContent("## Result\nStatus: failed\nMessage: minimax B-case empty plan\n")`. The on-disk file content for retry-count N:
   ```
   ---
   task_identifier: pr-123
   task_type: pr-review
   assignee: pr-reviewer-agent
   status: in_progress
   phase: planning
   planning_retry_count: N
   ---
   ## Objective

   review the PR
   ```
   (Omit `planning_retry_count` entirely for the N==0 case to exercise the "absent → 0" default.)

   - **non-planning passthrough:** `phase: execution`, everything else pr-review+failed → `Handle` returns `(false, nil)`; `AtomicReadModifyWriteAndCommitPushCallCount() == 0`; `PlanningRetryTotal` shows zero increments (capture the counter value before/after via `testutil.ToFloat64(metrics.PlanningRetryTotal.WithLabelValues("retry"))` — import `"github.com/prometheus/client_golang/prometheus/testutil"`).
   - **non-pr-review passthrough:** `task_type: llm` (or absent task_type AND `assignee: claude`), `phase: planning`, failed → `(false, nil)`, no write, zero metric.
   - **success passthrough:** pr-review + planning but body is `## Result\nStatus: done\n` (no `Status: failed`) → `(false, nil)`, no write, zero metric, and a `glog.V(2)` "no failure signal" path (you may assert the return only; log capture is optional here).
   - **retry attempt 1 (count absent → 0):** on-disk file omits `planning_retry_count`; `Handle` returns `(true, nil)`; `AtomicReadModifyWriteAndCommitPushCallCount() == 1`; run the captured modify closure against the on-disk bytes and assert the produced frontmatter has `planning_retry_count: 1`, `phase: planning`, a `task_identifier` that is a valid UUID and DIFFERENT from `pr-123` (parse with `uuid.Parse`), and the produced body contains a line matching regex `retry 1/3: .* at 2026-07-01T12:00:00Z`; assert `PlanningRetryTotal{result="retry"}` incremented by exactly 1.
   - **retry attempt 2 (count 1 → 2):** on-disk `planning_retry_count: 1` AND on-disk body already has a `- retry 1/3: ...` line; `Handle` returns `(true, nil)`; produced frontmatter `planning_retry_count: 2`; produced body contains BOTH `retry 1/3:` (preserved) AND `retry 2/3:` (new).
   - **retry attempt 3 (count 2 → 3):** on-disk `planning_retry_count: 2`; produced `planning_retry_count: 3`, body gains `retry 3/3:`; still `(true, nil)`; metric +1. (This attempt is a RETRY, not exhaustion — exhaustion is `count >= 3` on the NEXT failure.)
   - **counter at cap → passthrough (prompt-1 stub for exhaustion):** on-disk `planning_retry_count: 3` → `Handle` returns `(false, nil)`; `AtomicReadModifyWriteAndCommitPushCallCount() == 0`; zero `PlanningRetryTotal` increment. (Prompt 2 will replace this with escalation; here it MUST fall through so nothing regresses.)
   - **defensive counter > 3 → passthrough:** on-disk `planning_retry_count: 5` → same as at-cap: `(false, nil)`, no write, no metric. (Prompt 2 upgrades this to escalation.)
   - **defensive negative counter → clamped to 0, retry attempt 1:** on-disk `planning_retry_count: -2` → clamps to 0, behaves as attempt 1 (produced `planning_retry_count: 1`).
   - **redelivery idempotency:** on-disk `planning_retry_count: 1` AND on-disk body already contains `- retry 1/3: <same reason> ...`. Simulate redelivery of the ATTEMPT-1 failed result (incoming `req` still the original). Because `FindTaskFilePath` returns the post-bump on-disk state (count == 1) and the incoming failed result would compute "next attempt" from count == 1 → attempt 2, this is actually the NORMAL attempt-2 path, NOT a redelivery. To test TRUE redelivery (the failed result for attempt 1 arriving twice before the executor spawned attempt 2): set the modify closure's re-read counter to differ from the `count` captured at `FindTaskFilePath` time. Concretely: stub the on-disk `ReadFile` (used by `FindTaskFilePath`) to return `planning_retry_count: 0`, but have the modify closure's `current` argument (the git-lock re-read) return `planning_retry_count: 1` — i.e. someone bumped it between the find and the write. Assert the modify closure returns the `current` bytes UNCHANGED (no new `retry` line, counter stays 1) and `PlanningRetryTotal{result="retry"}` is NOT incremented (the `*bump` flag stayed false). To exercise this, pass a distinct `current` byte slice to the captured modify func in the test (you control what you feed it). Document in a test comment that this simulates the race-free idempotency guard.
   - **reason newline/CR stripped:** incoming body `## Result\nStatus: failed\nMessage: line one\rline two\nmore` → produced `retry 1/3:` line's reason substring contains NO literal `\n` or `\r` (assert the produced body line, split on `\n`, that contains `retry 1/3:` has no embedded `\r`).
   - **reason truncated to ≤200 runes:** incoming failure message of 300 `x` chars → the produced `retry 1/3:` line's reason portion is ≤ 200 runes.

   For assertions that parse the produced frontmatter, unmarshal the produced bytes with `result.ExtractFrontmatter` + `yaml.Unmarshal` into a `lib.TaskFrontmatter` and read via `.Int("planning_retry_count")`, `.String("task_identifier")`, `.String("phase")`.

9. **Reset the metric between tests** to keep counter assertions independent. Prometheus counters are process-global; use `testutil.ToFloat64(...)` to capture a baseline in each test and assert the DELTA (e.g. `Expect(after - before).To(Equal(1.0))`), rather than absolute values. Do NOT attempt to unregister/re-register the promauto var.

10. **CHANGELOG.** There is NO `CHANGELOG.md` in this repo root (verified: `/workspace` has no `CHANGELOG.md`). Do NOT create one. If a `CHANGELOG.md` appears (added by another prompt), append a single `## Unreleased` bullet `- feat: Add controller-side Layer 2 planning-retry loop for failed pr-review planning results (counter, ## Progress append, phase reset, fresh task_identifier requeue)`. Otherwise skip.

</requirements>

<constraints>

- Frozen constant: `maxControllerPlanningRetries = 3` in `pkg/command/planning_retry.go`. No env var, CLI flag, config field, or CRD field. Spec Non-goal (verbatim): *"Do NOT expose the retry cap as an env var, CLI flag, config field, or CRD field."*
- Frozen `## Progress` line format: the frozen substring `retry N/3:` MUST appear on the line; the full format is `retry N/3: <reason> at <RFC3339 timestamp>` (written as a `- ` bullet). Load-bearing for the idempotency check and operator greps.
- Frozen frontmatter counter name: `planning_retry_count`. Frozen phase reset value: `planning`.
- Frozen metric: a single `agent_controller_planning_retry_total{result=...}` counter. Only `result="retry"` fires in this prompt. Spec Non-goal (verbatim): *"Do NOT add a metric-per-retry cardinality explosion."* Do NOT add any label beyond `result`.
- The gate keys strictly on pr-review task type (or pr-reviewer assignee fallback) AND `phase == planning` AND a body failure marker. All other task types, phases, and result statuses (`done`/skipped/non-planning-failure) pass through to the existing `WriteResult` path unchanged, with NO metric increment (spec DB 7). Spec Non-goals (verbatim): *"Do NOT add retry recovery for `execution`, `ai_review`, or `verdict` phases"*; *"Do NOT extend the retry loop to task types other than `pr-review`"*; *"Do NOT retry on `AgentStatusSkipped` ... or `AgentStatusSuccess`."*
- No backoff, jitter, or sleep between retries. Immediate requeue via the frontmatter write (spec Non-goal, verbatim: *"Do NOT add exponential backoff, jitter, or a sleep between retries."*).
- The retry gate MUST NOT change the CQRS command-handler's outer return shape or introduce new sentinel errors surfaced to the framework (spec Constraint). A real git-rest error is wrapped and returned as an ordinary error, matching the existing `WriteResult` error path.
- git-rest client is the only vault-write path — reuse the injected `gitClient`. No new git client, no new PVC, no new STS, no new env var (spec Constraint).
- The retry must be idempotent under Kafka redelivery: the counter bump + `## Progress` append happen in a single `AtomicReadModifyWriteAndCommitPush`; the modify closure re-reads the on-disk counter under the git lock and no-ops when it has already advanced past the expected pre-bump value (spec DB 8).
- `<reason>` sanitization: strip `\n`/`\r` and other control chars, truncate to ≤200 runes, before interpolation (spec Security).
- `planning_retry_count` read defensively: clamp negative → 0; treat `>= 3` as at-cap (spec Security).
- EXHAUSTION escalation (GitHub COMMENT, `phase: human_review`, assignee clear, `result="exhausted"`) is OUT OF SCOPE — it ships in prompt 2. In this prompt, at-cap falls through to `WriteResult`. Do NOT implement a partial escalation.
- Do NOT add a new E2E scenario — spec states behavior is fully reachable via Ginkgo unit tests with mock `GitClient` (spec: "NO new E2E scenario").
- Existing tests in `/workspace/pkg/command/`, `/workspace/pkg/result/`, `/workspace/pkg/publisher/`, and `/workspace/pkg/factory/` MUST still pass (spec Constraint).
- Per `go-error-wrapping-guide.md`: `errors.Wrapf(ctx, ...)` / `errors.Errorf(ctx, ...)` from `github.com/bborbe/errors` — never `fmt.Errorf`, never `context.Background()` in `pkg/`.
- Per `go-precommit.md` / `.golangci.yml`: funlen 80, nestif 4, golines 100. Keep `Handle` under 80 lines by extracting `matchesRetryGate`, `sanitizeReason`, `appendProgressLine`, and `buildRetryModifyFn` helpers.
- Per `go-patterns.md`: public interface + private struct + `New*` constructor + counterfeiter annotation on `PlanningRetryGate`.
- Coverage ≥80% for the new `pkg/command/planning_retry.go` (per `definition-of-done.md`); test all error and edge paths.
- Do NOT commit — dark-factory handles git.

</constraints>

<verification>

Run iteratively while implementing:

```
cd /workspace && go generate ./...
cd /workspace && go build ./...
cd /workspace && go test ./pkg/command/... -v
cd /workspace && go test ./pkg/factory/... ./pkg/result/... ./pkg/publisher/... ./pkg/metrics/...
```

Frozen-constant / frozen-field grep checks (spec Acceptance Criteria):

```
cd /workspace && grep -rn 'maxControllerPlanningRetries' pkg/    # ≥1 line, value literal 3
cd /workspace && grep -rn 'planning_retry_count' pkg/            # ≥1 line in pkg/command
cd /workspace && grep -rn 'agent_controller_planning_retry_total' pkg/metrics/
```

Coverage check for the new gate:

```
cd /workspace && go test -coverprofile=/tmp/cover.out -mod=mod ./pkg/command/... && go tool cover -func=/tmp/cover.out | grep planning_retry
```
Expect ≥80% for `planning_retry.go` functions.

Run ONCE at the end:

```
cd /workspace && make precommit
```

Expected: exit 0; all new gate specs pass (passthrough cases with zero metric, retry attempts 1/2/3 with counter bump + fresh UUID + `## Progress` line + metric +1, at-cap/>3 passthrough, negative-clamp, redelivery idempotent no-op, reason newline-strip, reason truncate); the three new result-executor tests pass; all existing command/result/publisher/factory specs still pass.

</verification>
