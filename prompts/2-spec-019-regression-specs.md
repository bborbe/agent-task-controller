---
status: draft
spec: [019-bug-planning-retry-gate-reads-on-disk-phase]
created: "2026-11-01T10:26:17Z"
branch: dark-factory/bug-planning-retry-gate-reads-on-disk-phase
---

<summary>
- Adds the regression test that locks the fix in the direction the deliverer change created: a delivered result with no phase, on a task file parked in planning, must retry.
- Adds the opposite regression test: a delivered result claiming planning, on a task file that has moved to execution, must not retry and must not write.
- Adds a test for a task file whose phase is absent: the gate skips and writes nothing.
- Adds a test for a task file that cannot be found: the gate skips before ever consulting a phase.
- Proves the new no-phase test fails against the pre-fix gate logic, then passes against the fixed logic, and records both runs.
- No production behaviour changes in this prompt beyond the temporary, reverted proof step.
</summary>

<objective>
Lock the on-disk-phase behaviour of the planning-retry gate with four Ginkgo specs, and prove the central regression spec fails against the pre-fix gate logic and passes against the fixed logic. This prompt depends on `1-spec-019-on-disk-phase-gate.md` having landed: the gate must already read `existingFrontmatter.Phase()` and the test fixture helper `onDiskFileWithPhase` must already exist.
</objective>

<context>
Read `CLAUDE.md` for project conventions, if it exists. This repo has no root `CLAUDE.md` today, so do not treat its absence as an error.

Read these files fully before changing anything:

- `pkg/command/planning_retry.go` — the fixed gate. `Handle` runs `matchesRetryGate` (payload-driven task-type/assignee/failure-marker pre-filters only), then `result.FindTaskFilePath`, then the `relPath == ""` guard, then the on-disk phase predicate, then the `planning_retry_count` handling. `matchesRetryGate` no longer reads `req.Frontmatter.Phase()`.
- `pkg/command/planning_retry_test.go` — the shared suite. The fixture helpers are:
  - `buildPRReviewTask(taskID string, phaseVal string, content string) lib.Task` — builds a payload with `task_type: pr-review`, `phase: phaseVal`, `assignee: pr-reviewer-agent`.
  - `onDiskFile(taskID string, retryCount int, body string, withPRMetadata ...bool) []byte` — on-disk file with `phase: planning`; `retryCount < 0` omits `planning_retry_count`.
  - `onDiskFileWithPhase(taskID string, phase string, retryCount int, body string, withPRMetadata ...bool) []byte` — same, with an explicit on-disk phase (added by prompt 1).
  - The existing pattern for capturing the modify closure:
    ```go
    var capturedModify func([]byte) ([]byte, error)
    fakeGit.AtomicReadModifyWriteAndCommitPushStub = func(_ context.Context, _ string, modify func([]byte) ([]byte, error), _ string) error {
    	capturedModify = modify
    	if _, invokeErr := modify(diskContent); invokeErr != nil {
    		return invokeErr
    	}
    	return nil
    }
    ```
  - The existing read pattern: `fakeGit.ListFilesReturns([]string{"tasks/pr-123.md"}, nil)` and `fakeGit.ReadFileReturns(diskContent, nil)`.
  - Assertions use `testutil.ToFloat64(metrics.PlanningRetryTotal.WithLabelValues("retry"))` for metric deltas, `result.ExtractFrontmatter` + `yaml.Unmarshal` for written frontmatter, and `fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()` for write counts.
- `pkg/result/result_writer.go` — `FindTaskFilePath` returns `("", nil, nil)` when no file matches; that is the not-found path.

Verified API facts (do not re-derive, do not substitute):

- `lib.TaskFrontmatter` is `map[string]interface{}` from `github.com/bborbe/agent`.
- `func (f TaskFrontmatter) Phase() *domain.TaskPhase`; `domain.TaskPhase` has `String()` and the constant `domain.TaskPhasePlanning`.
- `result.ExtractFrontmatter(ctx, bytes)` and `result.ExtractBody(ctx, bytes)` are from `github.com/bborbe/agent-task-controller/pkg/result`.
- `errors.Wrapf` is from `github.com/bborbe/errors`; `glog` is `github.com/golang/glog`.

Reference guides (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-patterns.md`
</context>

<requirements>
1. In `pkg/command/planning_retry_test.go`, add a new `Describe("on-disk phase is the source of truth", func() { ... })` block inside the top-level `Describe("PlanningRetryGate", ...)`, after the existing `Describe("passthrough cases", ...)` block. It contains four `Context` blocks.

2. Context `"payload carries no phase, on-disk phase is planning"` — this is the AC3 regression spec. Build the payload inline so it has **no** `phase` key:
   ```go
   req := lib.Task{
   	TaskIdentifier: lib.TaskIdentifier("pr-123"),
   	Frontmatter: lib.TaskFrontmatter{
   		"task_type": "pr-review",
   		"assignee":  "pr-reviewer-agent",
   	},
   	Content: lib.TaskContent("## Result\nStatus: failed\nMessage: boom\n"),
   }
   diskContent := onDiskFile("pr-123", 0, "## Objective\n\nreview the PR\n")
   fakeGit.ListFilesReturns([]string{"tasks/pr-123.md"}, nil)
   fakeGit.ReadFileReturns(diskContent, nil)
   ```
   Capture the modify closure with the existing pattern (invoking it with `diskContent`), then assert:
   - `err` is nil.
   - `handled` is `true`.
   - `fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()` equals `1`.
   - Applying `capturedModify(diskContent)` yields frontmatter whose `planning_retry_count` (via `fm.Int("planning_retry_count")`) equals `1`.
   Do not add a `phase` key to the payload. Do not use `buildPRReviewTask` for this spec.

3. Context `"payload phase is planning, on-disk phase is execution"` — this is the AC4 regression spec. Use `buildPRReviewTask("pr-123", "planning", "## Result\nStatus: failed\nMessage: boom\n")` for the payload and `onDiskFileWithPhase("pr-123", "execution", 0, "## Objective\n\nreview the PR\n")` for the file. Assert:
   - `err` is nil.
   - `handled` is `false`.
   - `fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()` equals `0`.
   Do not weaken this spec by removing the payload phase — the payload must actively claim `planning` while the disk says `execution`.

4. Context `"on-disk phase is absent"` — build the on-disk bytes inline with no `phase` key:
   ```go
   diskContent := []byte("---\ntask_identifier: pr-123\ntask_type: pr-review\nassignee: pr-reviewer-agent\nstatus: in_progress\nplanning_retry_count: 0\n---\n## Objective\n\nreview the PR\n")
   ```
   Payload: `buildPRReviewTask("pr-123", "planning", "## Result\nStatus: failed\nMessage: boom\n")`. Stub `ListFilesReturns`/`ReadFileReturns` as above. Assert `err` is nil, `handled` is `false`, and `AtomicReadModifyWriteAndCommitPushCallCount()` equals `0`.

5. Context `"task file not found"` — payload `buildPRReviewTask("pr-123", "planning", "## Result\nStatus: failed\nMessage: boom\n")`, then `fakeGit.ListFilesReturns([]string{}, nil)`. Do not stub `ReadFileReturns`. Assert `err` is nil, `handled` is `false`, and `AtomicReadModifyWriteAndCommitPushCallCount()` equals `0`. This locks the frozen ordering: the not-found guard runs before the phase predicate.

6. Prove the AC3 spec fails against the pre-fix gate logic. The container's `.git` is masked, so a literal `git checkout ede6d9d` is not available; reproduce the pre-fix predicate by temporarily reinstating it.
   - Save the fixed file: `cp pkg/command/planning_retry.go /tmp/planning_retry.go.fixed`
   - Insert this block at the very top of the body of `matchesRetryGate` (before the task-type check):
     ```go
     	phase := req.Frontmatter.Phase()
     	if phase == nil || phase.String() != domain.TaskPhasePlanning.String() {
     		glog.V(3).
     			Infof("planning-retry: task %s phase is not planning; skipping gate", req.TaskIdentifier)
     		return false
     	}
     ```
   - Run `go test ./pkg/command/ -count=1 -args -ginkgo.focus="payload carries no phase"`. It must FAIL at the `handled` assertion (`Expected <bool>: false to be true`). Capture the failing output.
   - Restore the fixed file: `cp /tmp/planning_retry.go.fixed pkg/command/planning_retry.go`
   - Re-run the same focused command. It must PASS.
   - Confirm the restore is byte-identical with `cmp /tmp/planning_retry.go.fixed pkg/command/planning_retry.go` (must exit 0).
   - Record both runs (the failing output and the passing output) in the completion report's evidence/notes, and state explicitly that the pre-fix logic was reproduced from `ede6d9d`'s predicate because `.git` is masked.

7. Do not change any production file permanently in this prompt. The only durable edits are to `pkg/command/planning_retry_test.go`.

8. Self-check before finishing: re-run every `<verification>` command and confirm each passes; confirm `cmp /tmp/planning_retry.go.fixed pkg/command/planning_retry.go` exits 0; walk each acceptance criterion against the change.
</requirements>

<constraints>
- This prompt depends on prompt 1. `matchesRetryGate` must already be free of `req.Frontmatter.Phase()` and `onDiskFileWithPhase` must already exist. If either is missing, stop and report `status: failed` with `"prompt 1 (on-disk phase gate) not yet deployed"` — do not re-implement prompt 1 here.
- Do not modify `pkg/command/planning_retry.go` permanently. The temporary pre-fix predicate exists only for the evidence step and must be restored.
- Do not edit any existing spec. The only edits in this prompt are the four new specs.
- Do not re-add a payload phase read anywhere to make a spec pass.
- Do not change the retry cap, the escalation shape, or any metric name.
- Do not commit; dark-factory handles git.
- Existing tests must still pass.
</constraints>

<verification>
Run `go test ./pkg/command/ -count=1` — must pass.

Confirm the four new specs are present:
`grep -q 'payload carries no phase, on-disk phase is planning' pkg/command/planning_retry_test.go`
`grep -q 'payload phase is planning, on-disk phase is execution' pkg/command/planning_retry_test.go`
`grep -q 'on-disk phase is absent' pkg/command/planning_retry_test.go`
`grep -q 'task file not found' pkg/command/planning_retry_test.go`

Confirm the production file was restored after the evidence step:
`cmp /tmp/planning_retry.go.fixed pkg/command/planning_retry.go`

Confirm the payload-phase read is still absent:
`! grep -rq 'req\.Frontmatter\.Phase()' --include='*.go' pkg/`

Run `ROOTDIR=/workspace make precommit` — must exit 0 (includes `go test ./...`).
</verification>
