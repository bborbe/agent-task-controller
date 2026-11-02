---
status: completed
spec: [019-bug-planning-retry-gate-reads-on-disk-phase]
summary: Moved the planning-retry phase predicate to read the on-disk frontmatter after the not-found guard, updated the test fixture, design doc and changelog.
execution_id: agent-task-controller-phase-gate-exec-039-spec-019-on-disk-phase-gate
dark-factory-version: v0.198.7
created: "2026-11-01T10:26:17Z"
queued: "2026-11-01T10:33:01Z"
started: "2026-11-01T10:33:03Z"
completed: "2026-11-01T10:42:04Z"
---

<summary>
- The planning-retry gate decides "is this task still in planning?" from the task file on disk, not from the delivered payload.
- A failed `pr-review` task parked in `planning` retries again even when the delivered result carries no `phase` field at all.
- A task that has already moved past `planning` never retries, whatever phase the stale payload claims.
- A task file with no readable phase is skipped, and the phase the gate actually saw is written to the log.
- The task-type, assignee and failure-marker pre-filters are untouched and stay payload-driven.
- The retry cap of three and the escalation to `human_review` are untouched.
- Exactly one existing test fixture changes: the "non-planning phase" case now puts `execution` on disk, which is the input that decides.
- The controller design document records that the gate reads the on-disk phase and never consults the payload phase.
- The changelog records the fix.
</summary>

<objective>
Make the controller-side planning-retry gate read the task's **on-disk** phase instead of the delivered payload's `phase`. `bborbe/agent` v0.136.0 stopped publishing `phase` on a failed result that advanced nothing, so the current predicate (`req.Frontmatter.Phase()` inside `matchesRetryGate`) sees `nil` and silently disables the retry path for exactly the tasks it exists to serve: a `pr-review` task that fails while parked in `planning` stalls with `planning_retry_count` at 0 and no PR comment. The on-disk phase is the authoritative value; the payload phase must never re-drive a task that has moved on.
</objective>

<context>
Read `CLAUDE.md` for project conventions, if it exists. This repo has no root `CLAUDE.md` today, so do not treat its absence as an error.

Read these files fully before changing anything:

- `pkg/command/planning_retry.go`
  - `func (g *planningRetryGate) Handle(ctx context.Context, req lib.Task) (handled bool, err error)` — the pre-filters run first via `matchesRetryGate`, then `result.FindTaskFilePath` returns `(relPath, existingFrontmatter, err)`, then the `relPath == ""` not-found guard, then `count, _ := existingFrontmatter.Int("planning_retry_count")`.
  - `func (g *planningRetryGate) matchesRetryGate(req lib.Task) bool` — currently contains the phase block to be removed.
  - `const maxControllerPlanningRetries = 3` — do not touch.
- `pkg/result/result_writer.go`, function `FindTaskFilePath` (around line 427). Its verified signature is:
  ```go
  func FindTaskFilePath(
  	ctx context.Context,
  	gitClient gitclient.GitClient,
  	taskDir string,
  	id lib.TaskIdentifier,
  	resolver TaskPathResolver,
  ) (string, lib.TaskFrontmatter, error)
  ```
  It returns `("", nil, nil)` when no file matches. It already reads and parses the file's frontmatter, so no second on-disk read is needed.
- `pkg/command/planning_retry_test.go` — the `onDiskFile` closure (17 call sites) and the `Context("non-planning phase")` block inside `Describe("passthrough cases")`.
- `pkg/command/planning_retry_integration_test.go` — its own `seedFile` closure already takes a `phase` parameter and both call sites pass `"planning"`, so it passes unchanged under this fix. Do not edit it.
- `docs/controller-design.md` — section `### 2. Command Processing (Kafka → git)`; the paragraph beginning `**Heal-on-write.**` is where the new retry-gate paragraph is inserted.
- `CHANGELOG.md` — the top section is `## v0.22.10`; there is no `## Unreleased` section. Copy the bullet style (`- fix: **bold lead.** ...`).

Verified API facts (do not re-derive, do not substitute):

- `lib.TaskFrontmatter` is `map[string]interface{}` from `github.com/bborbe/agent`.
- `func (f TaskFrontmatter) Phase() *domain.TaskPhase` where `domain` is `github.com/bborbe/vault-cli/pkg/domain`.
- `domain.TaskPhase` is a string type with `func (t TaskPhase) String() string` and constant `domain.TaskPhasePlanning`.
- `Phase()` returns a pointer; `String()` has a value receiver, so calling `phase.String()` on a nil pointer panics. Guard for nil before calling it.
- `errors.Wrapf(ctx, err, "message", args...)` comes from `github.com/bborbe/errors`; `glog` is `github.com/golang/glog`.

Reference guides (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-patterns.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md`
</context>

<requirements>
1. In `pkg/command/planning_retry.go`, delete this entire block from `matchesRetryGate` (it is the payload-phase read):
   ```go
   	phase := req.Frontmatter.Phase()
   	if phase == nil || phase.String() != domain.TaskPhasePlanning.String() {
   		glog.V(3).
   			Infof("planning-retry: task %s phase is not planning; skipping gate", req.TaskIdentifier)
   		return false
   	}
   ```
   Leave the task-type/assignee block and the `hasFailureMarker` block exactly as they are. `matchesRetryGate` keeps its `req lib.Task` parameter.

2. In `Handle`, insert the on-disk phase predicate **after** the `relPath == ""` guard and **before** `count, _ := existingFrontmatter.Int("planning_retry_count")`. Insert exactly this code:
   ```go
   	phase := existingFrontmatter.Phase()
   	phaseValue := ""
   	if phase != nil {
   		phaseValue = phase.String()
   	}
   	if phase == nil || phaseValue != domain.TaskPhasePlanning.String() {
   		glog.V(3).
   			Infof("planning-retry: task %s on-disk phase=%s; skipping gate", req.TaskIdentifier, phaseValue)
   		return false, nil
   	}
   ```
   Requirements this code encodes, all of which are load-bearing:
   - The format string is exactly `planning-retry: task %s on-disk phase=%s; skipping gate` and the second argument is the string value (`phaseValue`, derived from `phase.String()`), never the `*domain.TaskPhase` pointer (a nil pointer renders as `<nil>` rather than the phase, and the log contract names the value).
   - A nil/absent on-disk phase takes the same branch and logs an empty phase value.
   - The predicate sits **after** the `relPath == ""` guard, so a missing task file skips before the phase is consulted.
   - It consumes the frontmatter `FindTaskFilePath` already returned. Do not add a second read, a second lookup, or a new helper function.
   - It returns `(false, nil)`, not an error, and performs no write.

3. Keep the `domain` import. It is still used by the new predicate. Remove any import only if the compiler says it is unused; do not otherwise reorder imports by hand — `make format` handles that.

4. In `pkg/command/planning_retry_test.go`, refactor the fixture closure without changing the 17 existing `onDiskFile(...)` call sites:
   - Add a new closure **above** `onDiskFile`:
     ```go
     	// onDiskFileWithPhase creates a task file content with an explicit on-disk phase.
     	onDiskFileWithPhase := func(taskID string, phase string, retryCount int, body string, withPRMetadata ...bool) []byte {
     		fm := map[string]interface{}{
     			"task_identifier": taskID,
     			"task_type":       "pr-review",
     			"assignee":        "pr-reviewer-agent",
     			"status":          "in_progress",
     			"phase":           phase,
     		}
     		if retryCount >= 0 {
     			fm["planning_retry_count"] = retryCount
     		}
     		if len(withPRMetadata) > 0 && withPRMetadata[0] {
     			fm["repository"] = "bborbe/maintainer"
     			fm["pull_request_number"] = 62
     		}
     		fmBytes, _ := yaml.Marshal(fm)
     		return []byte("---\n" + string(fmBytes) + "\n---\n" + body)
     	}
     ```
   - Replace the body of `onDiskFile` with a delegation that preserves its existing signature and its existing doc comment:
     ```go
     	onDiskFile := func(taskID string, retryCount int, body string, withPRMetadata ...bool) []byte {
     		return onDiskFileWithPhase(taskID, "planning", retryCount, body, withPRMetadata...)
     	}
     ```
     Every existing call site keeps compiling unchanged.

5. In the same test file, change **only** the `Context("non-planning phase")` block inside `Describe("passthrough cases")`: replace
   ```go
   				fakeGit.ReadFileReturns(
   					onDiskFile("pr-123", 0, "## Objective\nreview the PR\n"),
   					nil,
   				)
   ```
   with
   ```go
   				fakeGit.ReadFileReturns(
   					onDiskFileWithPhase("pr-123", "execution", 0, "## Objective\nreview the PR\n"),
   					nil,
   				)
   ```
   Do not change its assertions (`handled == false`, zero `AtomicReadModifyWriteAndCommitPush` calls, zero retry-metric delta). Under the fix the on-disk fixture is the deciding input, so its on-disk phase must be `execution`. No other spec may be edited in this prompt.

6. In `docs/controller-design.md`, insert one new paragraph immediately after the `**Heal-on-write.**` paragraph (before `**Build-fix supersede (spec 014).**`):
   ```markdown
   **Planning-retry gate (spec 019).** `PlanningRetryGate.Handle` (`pkg/command/planning_retry.go`) retries a failed `pr-review` task that is parked in `planning`, at most `maxControllerPlanningRetries` (3) times, then escalates to `human_review`. The cheap pre-filters — task type, assignee, and the `Status: failed` marker — are payload-driven and stay in `matchesRetryGate`. The phase predicate is **not** payload-driven: it reads the on-disk frontmatter returned by `result.FindTaskFilePath`, runs after the `relPath == ""` not-found guard, and never consults the payload's `phase`. A `pr-review` task whose on-disk phase is `planning` therefore retries whether or not the delivered payload carries `phase`, and a task whose on-disk phase is past `planning` never retries, whatever the payload says. Both directions are required: `bborbe/agent` v0.136.0 stopped publishing `phase` on a failed result that advanced nothing, so a payload-phase predicate would silently disable the retry path; and a stale payload phase must never re-drive a task that has moved on. A non-`planning` or absent on-disk phase skips the gate with one `glog.V(3)` line, `planning-retry: task %s on-disk phase=%s; skipping gate`, and writes nothing.
   ```

7. In `CHANGELOG.md`, insert a `## Unreleased` section directly above `## v0.22.10`, with one blank line before and after the heading, containing exactly this bullet:
   ```markdown
   - fix: **the planning-retry gate reads the on-disk phase.** `bborbe/agent` v0.136.0 stopped publishing `phase` on a failed result that advanced nothing, so `matchesRetryGate` read a nil payload phase and silently disabled the controller-side planning retry — a `pr-review` task failing in `planning` stalled with `planning_retry_count` at 0 and no PR comment. The phase predicate now runs in `Handle` against the frontmatter `FindTaskFilePath` already returns, after the not-found guard; the payload phase is never consulted. The task-type, assignee and failure-marker pre-filters stay payload-driven, and the 3-retry cap and escalation shape are unchanged.
   ```

8. Self-check before finishing: re-run every `<verification>` command and confirm each passes, then walk each acceptance criterion against the change — the payload-phase read is gone, the on-disk read sits after the not-found guard, the log format string is exact, only the one fixture changed, and the design doc names the on-disk phase as the source of truth.
</requirements>

<constraints>
- The phase predicate moves; the pre-filters do not. Task type, assignee and `hasFailureMarker` stay in `matchesRetryGate` and stay payload-driven.
- The phase check must follow the `relPath == ""` guard. A missing task file must skip the gate before the phase is consulted.
- No second on-disk read. `FindTaskFilePath` already returns the frontmatter the check consumes.
- Do not change `maxControllerPlanningRetries`, the escalation shape, the retry write, or the metrics.
- Do not change `MergeFrontmatter` or any other `result_writer.go` phase handling.
- The only sanctioned test edit in this prompt is the `onDiskFile` refactor plus the `Context("non-planning phase")` fixture. Do not "fix" any other spec by re-adding a payload phase read.
- Do not change any wire format, config field, env var, or CLI flag.
- Do not commit; dark-factory handles git.
- Existing tests must still pass.
</constraints>

<verification>
Run `go test ./pkg/command/ -count=1` — must pass.

Run the AC1 negative half (must print nothing and exit 0):
`! grep -rq 'req\.Frontmatter\.Phase()' --include='*.go' pkg/`

Run the AC1 positive half:
`grep -q 'existingFrontmatter\.Phase()' pkg/command/planning_retry.go`

Run the AC2 format-string check:
`grep -q 'planning-retry: task %s on-disk phase=%s; skipping gate' pkg/command/planning_retry.go`

Run the AC2 argument check (the log is called with the string value, never the pointer):
`grep -qE 'on-disk phase=%s.*phaseValue' pkg/command/planning_retry.go`

Run the AC6 doc check:
`grep -q 'the on-disk frontmatter returned by' docs/controller-design.md`

Run the changelog section check (must print `## Unreleased`):
`awk '/^## /{sec=$0} /planning-retry gate reads the on-disk phase/{print sec}' CHANGELOG.md`

Run `ROOTDIR=/workspace make precommit` — must exit 0 (includes `go test ./...`).
</verification>
