---
status: completed
spec: [019-bug-planning-retry-gate-reads-on-disk-phase]
summary: Added four Ginkgo specs locking the planning-retry gate to the on-disk phase and proved the AC3 regression spec fails against the pre-fix predicate and passes after restore.
execution_id: agent-task-controller-phase-gate-exec-043-spec-019-regression-specs
dark-factory-version: v0.198.7
created: "2026-11-01T10:26:17Z"
queued: "2026-11-01T12:57:17Z"
started: "2026-11-01T12:57:19Z"
completed: "2026-11-01T13:06:26Z"
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
Lock the on-disk-phase behaviour of the planning-retry gate with four Ginkgo specs, and prove the central regression spec fails against the pre-fix gate logic and passes against the fixed logic. This prompt depends on prompt 1 (`prompts/completed/039-spec-019-on-disk-phase-gate.md`) having landed: the gate must already read `existingFrontmatter.Phase()` and the test fixture helper `onDiskFileWithPhase` must already exist.
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
   - `Expect(handled).To(BeTrue())`.
   - `fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()` equals `1`.
   - Applying `capturedModify(diskContent)` yields frontmatter whose `planning_retry_count` (via `fm.Int("planning_retry_count")`) equals `1`.
   Do not add a `phase` key to the payload. Do not use `buildPRReviewTask` for this spec. Assert the precondition before acting, so the spec cannot silently degrade: `Expect(req.Frontmatter.Phase()).To(BeNil())`.

3. Context `"payload phase is planning, on-disk phase is execution"` — this is the AC4 regression spec. Use `buildPRReviewTask("pr-123", "planning", "## Result\nStatus: failed\nMessage: boom\n")` for the payload and `onDiskFileWithPhase("pr-123", "execution", 0, "## Objective\n\nreview the PR\n")` for the file. Assert:
   - `err` is nil.
   - `Expect(handled).To(BeFalse())`.
   - `fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()` equals `0`.
   Do not weaken this spec by removing the payload phase — the payload must actively claim `planning` while the disk says `execution`. Assert both preconditions before acting, so the spec cannot degrade into two empty values: `Expect(req.Frontmatter.Phase().String()).To(Equal("planning"))` and the parsed on-disk phase equals `"execution"`.

4. Context `"on-disk phase is absent"` — build the on-disk bytes inline with no `phase` key:
   ```go
   diskContent := []byte("---\ntask_identifier: pr-123\ntask_type: pr-review\nassignee: pr-reviewer-agent\nstatus: in_progress\nplanning_retry_count: 0\n---\n## Objective\n\nreview the PR\n")
   ```
   Payload: `buildPRReviewTask("pr-123", "planning", "## Result\nStatus: failed\nMessage: boom\n")`. Stub `ListFilesReturns`/`ReadFileReturns` as above. Assert `err` is nil, `handled` is `false`, and `AtomicReadModifyWriteAndCommitPushCallCount()` equals `0`.

5. Context `"task file not found"` — payload `buildPRReviewTask("pr-123", "planning", "## Result\nStatus: failed\nMessage: boom\n")`, then `fakeGit.ListFilesReturns([]string{}, nil)`. Do not stub `ReadFileReturns`. Assert `err` is nil, `handled` is `false`, and `AtomicReadModifyWriteAndCommitPushCallCount()` equals `0`. This locks the frozen ordering: the not-found guard runs before the phase predicate.

6. Prove the AC3 spec fails against the pre-fix gate logic. Do not touch git history; reproduce the pre-fix predicate by temporarily reinstating it by hand in the working tree, then restoring it from the copy below.
   - Save the fixed file: `cp pkg/command/planning_retry.go /tmp/planning_retry.go.fixed`
   - Insert this block inside `matchesRetryGate` immediately after the task-type/assignee check and before the `hasFailureMarker` check — exactly where `ede6d9d` had it:
     ```go
     	phase := req.Frontmatter.Phase()
     	if phase == nil || phase.String() != domain.TaskPhasePlanning.String() {
     		glog.V(3).
     			Infof("planning-retry: task %s phase is not planning; skipping gate", req.TaskIdentifier)
     		return false
     	}
     ```
   - Run, capturing both the output and the exit code:
     `go test ./pkg/command/ -count=1 -v -args -ginkgo.focus="payload carries no phase" > /tmp/prefix-run.log 2>&1; echo "exit=$?" >> /tmp/prefix-run.log`
     It must FAIL: the log must contain `to be true` (the `handled` assertion — Gomega prints it across lines, so grep the phrase, not the whole sentence) and must contain `exit=1`.
   - Restore the fixed file: `cp /tmp/planning_retry.go.fixed pkg/command/planning_retry.go`
   - Re-run the same focused command with capture:
     `go test ./pkg/command/ -count=1 -v -args -ginkgo.focus="payload carries no phase" > /tmp/postfix-run.log 2>&1; echo "exit=$?" >> /tmp/postfix-run.log`
     It must PASS: the log must contain `Ran 1 of` and `exit=0`, and must NOT contain `to be true`.
   - Confirm the restore is byte-identical with `cmp /tmp/planning_retry.go.fixed pkg/command/planning_retry.go` (must exit 0).
   - Record both logs' contents in the completion report's evidence/notes, and state explicitly that the pre-fix logic was reproduced by hand from `ede6d9d`'s `matchesRetryGate` predicate.

7. Do not change any production file permanently in this prompt. The only durable edits are to `pkg/command/planning_retry_test.go`.

8. Self-check before finishing: re-run every `<verification>` command and confirm each passes; confirm `cmp /tmp/planning_retry.go.fixed pkg/command/planning_retry.go` exits 0; walk each acceptance criterion against the change.
   Ordering matters: `make precommit` runs `format` (goimports-reviser/golines over every `.go` file) and `generate` (it deletes and rebuilds `mocks/`). Run the focused proof runs *before* `make precommit`, so the byte-identity `cmp` compares like with like. After `make precommit`, do **not** re-run `cmp` against `/tmp/planning_retry.go.fixed` — that baseline predates the formatter and a formatting-only rewrite of `planning_retry.go` would fail it spuriously. Verify restoration after precommit with `git status --porcelain pkg/command/planning_retry.go` instead: it must print nothing.
   The AC2 log line (`planning-retry: task %s on-disk phase=%s; skipping gate`) is deliberately **not** asserted behaviourally: capturing a `glog.V(3)` line needs `-v=3` plus `logtostderr`, which the repo has no precedent for. Prompt 1 covers the format string by grep of the source, and that is the recorded decision — not an omission to fix here.
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

Confirm all four actually execute (names alone prove nothing):
`go test ./pkg/command/ -count=1 -v -args -ginkgo.focus="on-disk phase is the source of truth" -ginkgo.v 2>&1 | grep -q 'Ran 4 of'`
⚠️ `-v` is load-bearing: `go test` captures the package's stdout and only prints it with `-v` (or on failure), so without it the log contains no `Ran N of` line at all and this grep fails on a correct implementation. `-ginkgo.v` alone is not enough.

Confirm the AC3 spec really asserts the retry fired, and the AC4 spec really asserts the skip — by **count**, because the plain greps already match pre-existing specs (12 `BeTrue`, 3 `BeFalse` before this prompt):
`test "$(grep -c 'Expect(handled).To(BeTrue())' pkg/command/planning_retry_test.go)" -ge 13`
`test "$(grep -c 'Expect(handled).To(BeFalse())' pkg/command/planning_retry_test.go)" -ge 4`
`grep -q 'Expect(req.Frontmatter.Phase()).To(BeNil())' pkg/command/planning_retry_test.go`
`grep -q 'Expect(req.Frontmatter.Phase().String()).To(Equal("planning"))' pkg/command/planning_retry_test.go`

Confirm the two extra contexts are not empty shells (both strings match 0 times today):
`test "$(grep -c 'ListFilesReturns(\[\]string{}, nil)' pkg/command/planning_retry_test.go)" -ge 1`
`grep -q 'planning_retry_count: 0' pkg/command/planning_retry_test.go`

Confirm the pre-fix proof actually ran, both directions:
`grep -q 'to be true' /tmp/prefix-run.log`
`grep -q 'exit=1' /tmp/prefix-run.log`
`grep -q 'Ran 1 of' /tmp/postfix-run.log`
`grep -q 'exit=0' /tmp/postfix-run.log`
`! grep -q 'to be true' /tmp/postfix-run.log`

Confirm the production file was restored after the evidence step — run this **before** `make precommit`, since the `/tmp` baseline predates the formatter:
`cmp /tmp/planning_retry.go.fixed pkg/command/planning_retry.go`

Confirm the payload-phase read is still absent:
`! grep -q 'req\.Frontmatter\.Phase()' pkg/command/planning_retry.go`
⚠️ Scoped to the production file on purpose: requirements 2 and 3 add `req.Frontmatter.Phase()` assertions to the **test** file, so a repo-wide `--include='*.go' pkg/` grep would be non-zero on a correct implementation and this line would fail it.

Run `ROOTDIR=/workspace make precommit` — must exit 0 (includes `go test ./...`).

After `make precommit`, confirm the production file is still unmodified relative to git (this replaces `cmp` for the post-format state):
`test -z "$(git status --porcelain pkg/command/planning_retry.go)"`
</verification>
