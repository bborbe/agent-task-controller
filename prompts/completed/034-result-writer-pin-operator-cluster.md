---
status: completed
summary: Made cluster an operator-pinned frontmatter key in MergeFrontmatter so result writes can never change or reintroduce it, with guard specs, a write-path table, design-doc row and changelog entry.
execution_id: agent-task-controller-cluster-operator-owned-exec-034-result-writer-pin-operator-cluster
dark-factory-version: v0.198.7
created: "2026-10-22T17:25:56Z"
queued: "2026-10-22T17:28:08Z"
started: "2026-10-22T17:28:55Z"
completed: "2026-10-22T17:36:08Z"
---

<summary>
- An agent's result write can no longer change a task's `cluster:` frontmatter key — the value already in the task file always wins
- An operator who removes `cluster:` from a running task keeps it removed: the agent's late result write can no longer put it back
- An operator who re-pins a running task to a different cluster keeps that pin: the agent's stale value is discarded
- Every discarded incoming `cluster` value appears in the existing "ownership guard kept on-disk" log line, including the case where the key was absent on disk
- The existing guards for the trigger and retry counters, the assignee fields, the terminal status and the accumulated metrics counters behave exactly as before
- New unit tests cover the untag-while-running incident, a differing pin, equal values and an absent incoming key; a write-path test proves the written file carries no `cluster:` line
- The design doc's ownership table and the changelog record the change
</summary>

<objective>
Make the `cluster` frontmatter key un-writable by an agent's result write-back: in `MergeFrontmatter`, the on-disk `cluster` value always wins, and an incoming `cluster` can never introduce the key when it is absent on disk. The `cluster` key is the operator's pin; agents only echo it from their spawn-time task snapshot, and that echo re-tagged tasks onto a cluster that was being shut down.
</objective>

<context>
Read CLAUDE.md for project conventions, if it exists. This repo has no root CLAUDE.md today, so do not treat its absence as an error.

Relevant coding guides (read the ones that apply):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md`

Live incident this fixes (2026-10-21): a `pr-reviewer` job (`pr-reviewer-agent-8ba6f21c-20261021214249`) was spawned at 21:42:50Z from a task snapshot carrying `cluster: sun`, for the task "PR Review github - bborbe-teamvault-docker - 10". The operator removed `cluster:` from the task file at 21:48:57Z (vault commit `d107bbdb`). The job's result write at 21:53:32Z restored `cluster: sun` (vault commit `83a2ab1a`). Cause: `MergeFrontmatter` overlays every incoming key (`for k, v := range incoming { merged[k] = v }`) and `cluster` is in neither `controllerOwnedFields` nor `operatorOwnedFields`. 27 tasks were re-tagged this way, routing work to a cluster that was being shut down.

Read these files before changing anything:

- `pkg/result/result_writer.go`:
  - `type GuardDecision struct { Field string; Kept any; Rejected any }` — one record per discarded incoming value.
  - `var controllerOwnedFields = []string{"trigger_count", "retry_count"}` with its doc comment.
  - `var operatorOwnedFields = []string{"assignee", "previous_assignee"}` with its long doc comment. Its rule lets an incoming value INTRODUCE a key absent on disk — which is exactly what `cluster` must NOT allow. Do not add `cluster` to this list.
  - `func MergeFrontmatter(existing, incoming lib.TaskFrontmatter) (lib.TaskFrontmatter, []GuardDecision)`. It builds `merged` from `existing` then overlays `incoming`, then runs, in order: the `controllerOwnedFields` loop (on disk → `merged[field] = diskValue` plus a decision when `inIncoming && !frontmatterValueEqual(diskValue, incomingValue)`; absent on disk → `delete(merged, field)` when incoming carries it, with NO decision), the terminal-status pin, `decisions = applyOperatorOwnedFields(existing, incoming, merged, decisions)`, `decisions = applyAccumulatedCounters(existing, incoming, merged, decisions)`, then `return merged, decisions`.
  - `func applyOperatorOwnedFields(existing, incoming, merged lib.TaskFrontmatter, decisions []GuardDecision) []GuardDecision` and `func applyAccumulatedCounters(...)` — follow this helper shape (same parameter list, returns the decision slice with discards appended) for the new helper.
  - `func frontmatterValueEqual(a, b any) bool` — the panic-free equality helper; use it, never `==` on two `any` values.
  - The modify closure inside the result writer calls `merged, decisions := MergeFrontmatter(currentOnDisk, req.Frontmatter)` and logs each decision with `glog.Infof("ownership guard kept on-disk: task %s field %s kept %v rejected %v", ...)`. No change is needed there — a decision with `Kept: nil` logs as `kept <nil>`.
- `pkg/result/result_writer_guard_test.go`: `Describe("MergeFrontmatter", ...)` with flat `It(...)` specs calling `result.MergeFrontmatter(existing, incoming)`. The `previous_assignee` specs are `It("keeps the on-disk previous_assignee over a differing incoming value and reports one decision", ...)` and `It("does not treat an empty incoming previous_assignee as a clear — the on-disk value wins and a decision is reported", ...)`. Imports are `lib "github.com/bborbe/agent"`, Ginkgo/Gomega dot-imports, and `"github.com/bborbe/agent-task-controller/pkg/result"`.
- `pkg/result/result_writer_test.go`: inside `Context("field ownership guard", ...)` the existing pattern for asserting written file content is:
  ```go
  writeTaskFile("my-task.md", "---\n"+onDiskFM+"---\n## Result\nStatus: failed\n")
  taskFile = lib.Task{
  	TaskIdentifier: identifier,
  	Frontmatter:    incomingFM,
  	Content:        lib.TaskContent("## Result\nStatus: failed\n"),
  }
  Expect(writer.WriteResult(ctx, taskFile)).To(Succeed())
  written, _ := os.ReadFile(filepath.Join(tmpDir, taskDir, "my-task.md"))
  s := string(written)
  ```
  The on-disk fixtures carry `task_identifier: test-task-uuid-1234`. See the `DescribeTable("the on-disk counter always wins and an absent counter stays absent", ...)` entries.
- `docs/controller-design.md`, section `## Frontmatter Merge`: the ownership table with rows `Controller-owned`, `Controller-owned (terminal pin)`, `Operator-owned`, `Accumulated counters`, `Agent-owned`.
- `CHANGELOG.md`: the top section is `## v0.16.0`; there is no `## Unreleased` section. Bullet style is `- fix: **bold lead sentence.** explanation...`.
- `.golangci.yml`: `funlen` is `lines: 80`, `statements: 50`. Keep `MergeFrontmatter` small by putting the new loop in a helper.
</context>

<requirements>
1. In `pkg/result/result_writer.go`, add a new package-level list directly after `operatorOwnedFields` (before `reassignmentDeclarationKey`):
   ```go
   // operatorPinnedFields lists frontmatter keys that only the operator may write.
   // The agent's result payload is built from the TASK_CONTENT snapshot injected
   // at spawn, so any value it carries for these keys is an echo of how the task
   // looked before the run, never a decision the agent made. The on-disk value
   // therefore always wins — and, unlike operatorOwnedFields, an incoming value can
   // never introduce a pinned key that is absent on disk: an operator who removed
   // the key while the job ran must not see a late result write put it back.
   //
   // cluster is the operator's routing pin (which controller publishes the task).
   // The operator is its only legitimate writer; the automatic cluster assigner is
   // being retired. Observed live 2026-10-21: a pr-reviewer job spawned from a
   // snapshot carrying cluster: sun, the operator removed cluster: six minutes
   // later, and the job's result write restored cluster: sun — 27 tasks were
   // re-tagged onto a cluster that was being shut down.
   //
   // Unlike controllerOwnedFields, a discarded incoming value is reported as a
   // guard decision even when the key is absent on disk (Kept: nil), so every
   // dropped echo shows up in the ownership-guard log line.
   var operatorPinnedFields = []string{
   	"cluster",
   }
   ```

2. In `pkg/result/result_writer.go`, add a helper next to `applyOperatorOwnedFields`, following its signature shape exactly:
   ```go
   // applyOperatorPinnedFields applies the operator-pinned rule to the merged
   // frontmatter: the on-disk value of each operatorPinnedFields key always wins.
   // A key present on disk keeps its on-disk value and a differing incoming value is
   // reported as a decision. A key absent on disk stays absent even when incoming
   // carries it, and the discarded incoming value is reported as a decision with
   // Kept: nil. Equal values and an incoming payload without the key produce no
   // decision. Returns the decision list with any discards appended.
   func applyOperatorPinnedFields(
   	existing, incoming, merged lib.TaskFrontmatter,
   	decisions []GuardDecision,
   ) []GuardDecision {
   	for _, field := range operatorPinnedFields {
   		diskValue, onDisk := existing[field]
   		incomingValue, inIncoming := incoming[field]
   		if !inIncoming {
   			continue // base merge already kept the on-disk value (or its absence)
   		}
   		if !onDisk {
   			delete(merged, field)
   			decisions = append(
   				decisions,
   				GuardDecision{Field: field, Kept: nil, Rejected: incomingValue},
   			)
   			continue
   		}
   		merged[field] = diskValue
   		if !frontmatterValueEqual(diskValue, incomingValue) {
   			decisions = append(
   				decisions,
   				GuardDecision{Field: field, Kept: diskValue, Rejected: incomingValue},
   			)
   		}
   	}
   	return decisions
   }
   ```

3. In `MergeFrontmatter`, call the helper on the line directly before `decisions = applyOperatorOwnedFields(existing, incoming, merged, decisions)` (i.e. after the terminal-status pin block):
   ```go
   decisions = applyOperatorPinnedFields(existing, incoming, merged, decisions)
   ```
   Do not change the `controllerOwnedFields` loop, the terminal-status pin, `applyOperatorOwnedFields`, `applyAccumulatedCounters`, or the `GuardDecision` type. `trigger_count` / `retry_count` keep their current behaviour, including producing NO decision when the key is absent on disk.

4. Update the `MergeFrontmatter` doc comment: after the sentence about controller-owned counters, add that operator-pinned fields (cluster) always take the on-disk value, are absent from the result when absent on disk, and report every differing discarded incoming value as a decision (Kept nil when absent on disk). Also update the `GuardDecision` doc comment so it no longer implies decisions come only from controller-owned fields or the status pin — e.g. "the merge kept the on-disk value of a guarded field (Kept is nil when the field was absent on disk and stays absent) and rejected a differing incoming value."

5. In `pkg/result/result_writer_guard_test.go`, add four `It(...)` specs directly after the two existing `previous_assignee` specs:

   a. Untag while running — comment it with the live incident:
   ```go
   // Live incident 2026-10-21, task "PR Review github - bborbe-teamvault-docker - 10":
   // the job spawned from a snapshot carrying cluster: sun, the operator removed
   // cluster: while it ran (vault commit d107bbdb), and the result write restored
   // cluster: sun (vault commit 83a2ab1a). An incoming cluster must never
   // reintroduce a key the operator removed.
   ```
   existing `lib.TaskFrontmatter{"status": "in_progress", "phase": "planning", "spawn_notification": true, "current_job": "pr-reviewer-agent-8ba6f21c-20261021214249"}`, incoming `lib.TaskFrontmatter{"status": "in_progress", "phase": "done", "cluster": "sun"}`. Assert `merged` does not have key `"cluster"` (`Expect(merged).NotTo(HaveKey("cluster"))`), `merged["phase"]` equals `"done"`, `decisions` has length 1, `decisions[0].Field == "cluster"`, `decisions[0].Kept` is nil (`BeNil()`), `decisions[0].Rejected == "sun"`.

   b. Differing pin: existing `{"status": "in_progress", "cluster": "nuke"}`, incoming `{"status": "in_progress", "cluster": "sun"}` → `merged["cluster"] == "nuke"`, exactly 1 decision with Field `"cluster"`, Kept `"nuke"`, Rejected `"sun"`.

   c. Equal values: existing and incoming both `{"status": "in_progress", "cluster": "sun"}` → `merged["cluster"] == "sun"`, `decisions` has length 0.

   d. Incoming without the key: existing `{"status": "in_progress", "cluster": "sun"}`, incoming `{"status": "in_progress", "phase": "done"}` → `merged["cluster"] == "sun"`, `decisions` has length 0.

6. In `pkg/result/result_writer_test.go`, inside `Context("field ownership guard", ...)`, add a `DescribeTable` after the existing `"the on-disk counter always wins and an absent counter stays absent"` table, using the identical body (write fixture, `WriteResult`, read file, `present` / `absent` substring lists):
   ```go
   DescribeTable(
   	"the on-disk cluster pin always wins and an incoming cluster never introduces the key",
   	func(onDiskFM string, incomingFM lib.TaskFrontmatter, present, absent []string) { /* same body as the counter table */ },
   	Entry(
   		"operator removed cluster while the job ran: the result write does not restore it",
   		"task_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: planning\n",
   		lib.TaskFrontmatter{
   			"task_identifier": "test-task-uuid-1234",
   			"status":          "in_progress",
   			"phase":           "ai_review",
   			"cluster":         "sun",
   		},
   		[]string{"phase: ai_review"},
   		[]string{"cluster:"},
   	),
   	Entry(
   		"keeps the on-disk cluster over a stale incoming value",
   		"task_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: planning\ncluster: nuke\n",
   		lib.TaskFrontmatter{
   			"task_identifier": "test-task-uuid-1234",
   			"status":          "in_progress",
   			"phase":           "ai_review",
   			"cluster":         "sun",
   		},
   		[]string{"cluster: nuke"},
   		[]string{"cluster: sun"},
   	),
   )
   ```
   This exercises the real write path (modify closure + YAML marshal), not only the merge function. If the shared body can be extracted into a local closure in the `Context` without changing the existing table's behaviour, do so; otherwise duplicate the body.

7. In `docs/controller-design.md`, `## Frontmatter Merge` table: add a row directly after the `Operator-owned` row:
   `| Operator-pinned | `cluster` | The on-disk value always wins, and an incoming value can never introduce the key when it is absent on disk — an operator who removes `cluster:` from a running task keeps it removed. Every differing discarded incoming value is a guard decision and logged, including when the key is absent on disk. The agent only echoes `cluster` from its spawn-time snapshot; the operator is its only legitimate writer. |`
   Also change the intro sentence "while two field classes stay under controller control" only if it would now be inaccurate; keep the edit minimal.

8. In `CHANGELOG.md`, insert a `## Unreleased` section directly above `## v0.16.0` (blank line between sections) with exactly one bullet:
   `- fix: **an agent's result write can no longer change a task's `cluster:` pin.** `MergeFrontmatter` overlaid every incoming key, and `cluster` belonged to no ownership guard, so a result built from the spawn-time task snapshot echoed the snapshot's `cluster` back onto the task. Measured 2026-10-21: an operator removed `cluster: sun` from a running pr-reviewer task and the job's result write six minutes later restored it; 27 tasks were re-tagged this way onto a cluster being shut down. `cluster` is now operator-pinned: the on-disk value always wins, an incoming value can never introduce the key when it is absent on disk, and every discarded incoming value is logged on the `ownership guard kept on-disk` line.`
   Do not bump a version or create a tag.
</requirements>

<constraints>
- Do NOT add `cluster` to `operatorOwnedFields` — that rule lets an incoming value introduce a key absent on disk, which is the bug.
- Do NOT change behaviour or reported decisions for `trigger_count`, `retry_count`, `status`, `assignee`, `previous_assignee`, `task_type`, `metrics_agent_turns`, `metrics_interaction_count`. All existing tests must pass unchanged.
- Use `frontmatterValueEqual` for value comparison; never `==` on two `any` values (panics on maps/slices).
- `MergeFrontmatter` must not modify either input map.
- No new config field, env var, flag or metric.
- Keep `funlen` (80 lines / 50 statements) satisfied — the new loop lives in the helper, not inline in `MergeFrontmatter`.
- Do NOT commit — dark-factory handles git.
- No version bump, no tag; the CHANGELOG entry goes under `## Unreleased`.
</constraints>

<verification>
Run `make precommit` in the repo root — must pass.

Run `go test ./pkg/result/...` — must pass, including the four new `MergeFrontmatter` specs and the two new `WriteResult` table entries.

Run `grep -n 'operatorPinnedFields' pkg/result/result_writer.go` — must show the declaration and its use in `applyOperatorPinnedFields`.

Run `grep -n '^## Unreleased' CHANGELOG.md` — must print one line, above the `## v0.16.0` line.
</verification>
