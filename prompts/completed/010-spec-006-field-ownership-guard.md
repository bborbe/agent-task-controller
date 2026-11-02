---
status: completed
spec: [006-bug-frontmatter-field-ownership]
summary: 'Implemented the field-ownership guard in the result-write chokepoint: on-disk trigger_count/retry_count always win (never introduced from the incoming payload), terminal on-disk status is pinned via the normalizing Status() accessor, the applyRetryCounter early return covers both terminal statuses, each differing-value discard emits one unconditional ''ownership guard kept on-disk'' INFO log, proven by new unit specs in pkg/result, with the CHANGELOG Unreleased entry added.'
execution_id: agent-task-controller-fieldmerge-exec-010-spec-006-field-ownership-guard
dark-factory-version: dev
created: "2026-08-31T11:56:38Z"
queued: "2026-08-31T12:18:44Z"
started: "2026-08-31T12:19:11Z"
completed: "2026-08-31T12:32:25Z"
branch: dark-factory/bug-frontmatter-field-ownership
---

# Field-ownership guard, terminal pin, guard log, unit specs, changelog

<summary>

- A result publish can no longer roll back `trigger_count` / `retry_count`: the on-disk counter always wins, and an incoming payload can never introduce a controller-owned counter that is absent on disk — so the trigger and retry caps compare against real spawn counts and can fire.
- A terminal on-disk `status` (`aborted` / `completed`) is pinned: the incoming `status` is discarded, so an operator's abort survives every publish.
- A pinned-terminal task short-circuits the escalation machinery uniformly for both terminal statuses: no Trigger Cap / Retry Escalation section, no assignee clear, no `previous_assignee`, no phase restore, and an inherited `spawn_notification: true` survives — escalation exists to park a live task, not one an operator already ended.
- Everything the agent legitimately authors — `phase`, arbitrary keys, the body — keeps today's incoming-wins behavior on terminal and non-terminal tasks alike.
- Every guard discard of a differing incoming value emits one unconditional INFO log line containing `ownership guard kept on-disk` (task, field, kept value, rejected value); equal values stay silent, so steady-state publishes produce no log noise.
- The controller-owned field names and the terminal status set are each declared once at package level, so adding a controller-owned field later is a one-line change.
- The one pre-existing spec that models an incoming counter reset is rewritten to model the reset the way the system actually performs it — the scanner's Empty-to-Named Reset writing `trigger_count: 0` to disk — while preserving its intent (`previous_assignee` persistence, `assignee: backtest-agent`).
- New unit specs prove: counters survive, absent counters are never introduced, the cap fires because the on-disk counter won, terminal statuses pin, escalation short-circuits, the merge reports exactly the right guard decisions, and agent-owned fields still win on non-terminal tasks.
- The changelog records the fix under `## Unreleased` naming the counter rollback and the reverted terminal status.

</summary>

<objective>

Make the single result-write chokepoint (`pkg/result`) field-ownership-aware: `trigger_count` / `retry_count` always take the on-disk value (and are never introduced from the incoming payload), a terminal on-disk `status` is pinned and discards the incoming status, the escalation early return covers both terminal statuses, every differing-value discard emits one unconditional `ownership guard kept on-disk` INFO log line, and all of it is proven by unit specs in `pkg/result/result_writer_test.go`. Record the behavior change in `CHANGELOG.md`.

</objective>

<context>

There is no CLAUDE.md in this repo; the global YOLO container CLAUDE.md (already in your context) governs project conventions. Read the repo's own code as the source of truth for style.

Read the coding-plugin docs that apply to this change:
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-glog-guide.md` (unconditional `glog.Infof` vs `glog.V(n).Infof`)
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` (Ginkgo/Gomega, `DescribeTable`, coverage ≥80% for changed code)
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/definition-of-done.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md`

Read `pkg/result/result_writer.go` IN FULL (15309 bytes). The functions you change:
- `func mergeFrontmatter(existing, incoming lib.TaskFrontmatter) lib.TaskFrontmatter` — currently at the bottom of the file (around line 353-364); the ONLY call site is `buildResultModifyFn` (line 171), so changing its signature is safe. It is the result-write chokepoint this spec targets.
- `func (r *resultWriter) buildResultModifyFn(ctx context.Context, req lib.Task) func(current []byte) ([]byte, error)` (line 151) — the modify callback re-reads on-disk frontmatter per git retry and calls the merge. This is where the guard log lines are emitted (it holds `req.TaskIdentifier`).
- `func (r *resultWriter) applyRetryCounter(merged, existing lib.TaskFrontmatter, body string) string` (line 188) — its terminal early return is currently `if string(merged.Status()) == "completed" { return body }` (line 189).
- `applyTriggerCap`, `applyRetryCap`, `clearAssignee`, `ClearAssigneeIfHumanReview`, `restoreExistingPhase` — NOT changed by this spec; they simply become unreachable for pinned-terminal tasks because the widened early return fires first.

Verified library facts (grep-checked against module sources; do not re-derive):
- `type TaskFrontmatter map[string]interface{}` from `github.com/bborbe/agent` (imported as `lib`).
- `func (f TaskFrontmatter) Status() domain.TaskStatus` — the normalizing accessor; canonicalises aliases via `domain.NormalizeTaskStatus` (e.g. `done` → `completed`), and returns `domain.TaskStatus(v)` for unknown values. Terminal detection MUST use this accessor, never a raw string comparison.
- `domain.TaskStatusCompleted TaskStatus = "completed"` and `domain.TaskStatusAborted TaskStatus = "aborted"` from `github.com/bborbe/vault-cli/pkg/domain` (already a direct require in `go.mod`; `result_writer.go` does NOT yet import it — add the import).
- `func (f TaskFrontmatter) TriggerCount() int` / `RetryCount() int` — both accept `int` and `float64` underlying values.
- `github.com/golang/glog` is already imported; `glog.Infof(format string, args ...any)` is the unconditional INFO call.
- On-disk frontmatter is parsed with `gopkg.in/yaml.v3` into `map[string]interface{}`, which decodes `trigger_count: 3` as `int(3)`; the incoming `req.Frontmatter` reaches `WriteResult` after a `json.Marshal`+`json.Unmarshal` round-trip through cqrs (`base/base_event.go MarshalInto`, standard `encoding/json`, no `UseNumber`), which decodes the same number as `float64(3)`. The guard's value-equality test MUST therefore treat `int` and `float64` as numerically equal, or every steady-state publish would log a spurious guard line, violating DB 6 ("Equal values produce no log line, so a steady-state publish is silent").

Read `pkg/result/result_writer_test.go` IN FULL (1748 lines, package `result_test`, external test package). The harness you reuse:
- `BeforeEach` (line 57-105): builds `fakeGit *mocks.GitClient` with a real-filesystem `AtomicReadModifyWriteAndCommitPushStub`, `fakeTime`, `identifier`, and `writer = result.NewResultWriter(fakeGit, taskDir, fakeTime, metrics.New())`.
- `writeTaskFile := func(name, content string) string` (line 111) writes an on-disk file; `taskFile = lib.Task{...}` models the agent's incoming payload; assertions read the written file and use `ContainSubstring` / `NotTo(ContainSubstring)`.
- The existing spec `It("agent keys override existing keys")` (line 186) MUST keep passing with unmodified `Expect(` lines — it is the non-terminal agent-wins control.
- Two pre-existing blocks may be modified, and only these two: `It("previous_assignee persists when operator re-delegates by setting a non-empty assignee")` (line 1351-1404, Requirement 7) and `It("fully replaces content on second call")` (line 215, Requirement 7b). Every other existing `It(`/`Expect(` in this file must remain byte-identical. Neither carve-out deletes or weakens an `Expect(` line.
- Every existing spec that carries `trigger_count` / `retry_count` in its incoming payload already uses on-disk == incoming values, so the guard changes none of their outcomes — this is deliberate; the only incoming-carried counter reset in the file is inside the carved block.

The spec's Test-style directive: use `DescribeTable` for the counter rows and for the two terminal-status rows (same fixture shape, differing values), and plain `It` blocks for the pin-not-freeze, short-circuit, log (DB 6), and negative-control cases, matching the surrounding file. `DescribeTable` style precedent lives in `pkg/command/command_operations_test.go` (line 21): `DescribeTable("...", func(...) {...}, Entry("name", args...), ...)`.

Read `CHANGELOG.md` — a `## Unreleased` section already exists (line 8) with one `fix:` bullet (terminal-task reopens); append a NEW `fix:` bullet to that existing section (do not create a second `## Unreleased`, do not touch the top version heading `## v0.6.1`).

Spec cross-references to keep straight:
- DB 1-2: counters read only from disk → caps see real spawn counts.
- DB 3: terminal pin via normalizing `Status()` vs the two `domain` constants.
- DB 4: pin happens during the merge, before `applyRetryCounter`, whose early return is widened from `completed` to BOTH terminal statuses; consequence = pinned-terminal tasks never reach the cap/assignee/`spawn_notification` machinery.
- DB 5: agent-owned keys keep incoming-wins on terminal and non-terminal tasks alike.
- DB 6: the merge helper reports guard decisions to the caller; the caller emits one unconditional INFO line per decision naming task, field, kept value, rejected value; equal values → no decision → silent.
- DB 7: controller-owned field names and terminal statuses each declared once at package level.

</context>

<requirements>

All changes below are in `pkg/result/result_writer.go` unless stated otherwise.

1. **Add the `domain` import.** Add `domain "github.com/bborbe/vault-cli/pkg/domain"` to the import block (already a direct `go.mod` dependency; `github.com/bborbe/agent` imports the same package, so no cycle). goimports-reviser in `make precommit` will place it; match the grouping style of the existing imports.

2. **Add the two package-level ownership declarations (DB 7).** Place them near the top of the file, after the `resultWriter` struct (around line 52), before `FindTaskFilePath`. The set of controller-owned field names and the set of terminal statuses each live here ONCE — a future controller-owned field is a one-line addition to `controllerOwnedFields`:
   ```go
   // controllerOwnedFields lists frontmatter keys whose on-disk value always wins:
   // the result writer never lets the agent's incoming value overwrite them, and an
   // incoming value can never introduce a controller-owned key that is absent on disk.
   var controllerOwnedFields = []string{
       "trigger_count",
       "retry_count",
   }

   // terminalStatuses lists the statuses that end a task's lifecycle. When the on-disk
   // status is terminal, the merge pins it (discarding the incoming status) and the
   // escalation machinery short-circuits. Terminal is decided via the normalizing
   // TaskFrontmatter.Status() accessor compared against these constants.
   var terminalStatuses = []domain.TaskStatus{
       domain.TaskStatusCompleted,
       domain.TaskStatusAborted,
   }
   ```
   The string literal `"trigger_count"` MUST appear in this declaration (spec AC 12 greps for it).

3. **Replace `mergeFrontmatter` with the exported `MergeFrontmatter` and add the `GuardDecision` type.** Delete the existing `mergeFrontmatter` function and its doc comment (currently around line 353-364). Add the decision type and the new exported function in its place (the export is REQUIRED: `result_writer_test.go` is `package result_test`, so the DB 6 decision-reporting unit specs can only reach the helper through an exported symbol — `FindTaskFilePath` and `ExtractBody` are already exported in this package, so this matches surrounding style):
   ```go
   // GuardDecision records a single ownership-guard discard: the merge kept the
   // on-disk value of a controller-owned field (or the pinned terminal status) and
   // rejected a differing incoming value. Equal values produce no decision.
   type GuardDecision struct {
       Field    string
       Kept     any
       Rejected any
   }

   // MergeFrontmatter returns a new frontmatter map with all keys from existing,
   // overridden by all keys from incoming, then applies the field-ownership guard:
   // controller-owned counters take the on-disk value when present on disk and are
   // absent from the result when not; a terminal on-disk status is pinned. The
   // returned decisions name every field whose differing incoming value was discarded
   // (equal values produce no decision). Neither input map is modified.
   func MergeFrontmatter(existing, incoming lib.TaskFrontmatter) (lib.TaskFrontmatter, []GuardDecision) {
       merged := make(lib.TaskFrontmatter, len(existing)+len(incoming))
       for k, v := range existing {
           merged[k] = v
       }
       for k, v := range incoming {
           merged[k] = v
       }
       var decisions []GuardDecision

       // Controller-owned counters: the on-disk value always wins when the key exists
       // on disk (kept verbatim, whatever its type); a key absent on disk stays absent
       // even if the incoming payload carries it.
       for _, field := range controllerOwnedFields {
           diskValue, onDisk := existing[field]
           incomingValue, inIncoming := incoming[field]
           if !onDisk {
               if inIncoming {
                   delete(merged, field)
               }
               continue
           }
           merged[field] = diskValue
           if inIncoming && !frontmatterValueEqual(diskValue, incomingValue) {
               decisions = append(decisions, GuardDecision{Field: field, Kept: diskValue, Rejected: incomingValue})
           }
       }

       // Terminal on-disk status is pinned: the on-disk status value is kept and the
       // incoming status is discarded. Terminal is decided by the normalizing Status()
       // accessor compared against terminalStatuses, never by raw string equality on
       // the unparsed value. The status decision is reported only when the normalized
       // values differ (an incoming alias such as "done" against on-disk "completed"
       // is normalized-equal and produces no decision).
       if diskStatus, onDisk := existing["status"]; onDisk && isTerminalStatus(existing.Status()) {
           merged["status"] = diskStatus
           if incomingStatus, ok := incoming["status"]; ok && existing.Status() != incoming.Status() {
               decisions = append(decisions, GuardDecision{Field: "status", Kept: diskStatus, Rejected: incomingStatus})
           }
       }

       return merged, decisions
   }
   ```
   All other keys follow today's incoming-wins merge, on terminal and non-terminal tasks alike (DB 5) — no extra branching.

4. **Add the three package-private helpers** `isTerminalStatus`, `frontmatterValueEqual`, `numericValue` (place near `MergeFrontmatter`):
   ```go
   func isTerminalStatus(s domain.TaskStatus) bool {
       for _, t := range terminalStatuses {
           if s == t {
               return true
           }
       }
       return false
   }

   // frontmatterValueEqual reports whether two frontmatter values compare equal,
   // treating numeric int/float64 pairs as equal so a JSON-decoded incoming counter
   // (float64) equals a YAML-decoded on-disk counter (int) at steady state — equal
   // counters must produce no guard log line (DB 6).
   func frontmatterValueEqual(a, b any) bool {
       if a == b {
           return true
       }
       af, aOK := numericValue(a)
       bf, bOK := numericValue(b)
       return aOK && bOK && af == bf
   }

   func numericValue(v any) (float64, bool) {
       switch n := v.(type) {
       case int:
           return float64(n), true
       case int64:
           return float64(n), true
       case float64:
           return n, true
       default:
           return 0, false
       }
   }
   ```
   Do NOT coerce the on-disk counter value to an integer when keeping it: a non-integer on-disk value (e.g. the string `"3"` or `null`) is kept verbatim, and the cap arithmetic reads it through the existing `TriggerCount()`/`RetryCount()` accessors (which yield `0` for non-integers, so no escalation fires — unchanged from today, spec Failure-Modes row "On-disk counter is not an integer").

5. **Update `buildResultModifyFn` to consume the decisions and emit the guard log.** Replace the single merge call at line 171 with the two-value return and a loop that emits one unconditional INFO line per decision. The frozen substring `ownership guard kept on-disk` MUST appear verbatim in the `glog.Infof` call, which MUST be unconditional (`glog.Infof`, NOT `glog.V(n).Infof`), naming the task identifier, the field, the kept on-disk value and the rejected incoming value (spec AC 13 + DB 6; the Rung-3 post-deploy AC greps prod logs for it at default verbosity):
   ```go
   merged, decisions := MergeFrontmatter(currentOnDisk, req.Frontmatter)
   for _, d := range decisions {
       glog.Infof(
           "ownership guard kept on-disk: task %s field %s kept %v rejected %v",
           req.TaskIdentifier,
           d.Field,
           d.Kept,
           d.Rejected,
       )
   }
   body := r.applyRetryCounter(merged, currentOnDisk, string(req.Content))
   ```
   The log line must name only the task identifier, the field, and the two values — NEVER the body or the full frontmatter (spec Security / Log safety). Do not change anything else in `buildResultModifyFn` (the per-retry re-read, the body-replacement semantics, and the marshal/write steps stay exactly as they are).

6. **Widen the terminal early return in `applyRetryCounter` (DB 4).** Change:
   ```go
   if string(merged.Status()) == "completed" {
       return body
   }
   ```
   (currently at line 189) to:
   ```go
   if isTerminalStatus(merged.Status()) {
       return body
   }
   ```
   This is the intended control-flow change: the pin during the merge means `merged.Status()` is already terminal for a pinned-terminal task, so the early return fires before `applyTriggerCap`, `ClearAssigneeIfHumanReview`, the `spawn_notification` delete, and `applyRetryCap` — a pinned-terminal task accrues no escalation section, its `assignee` is not cleared, `previous_assignee` is not written, `phase` is not restored by `restoreExistingPhase`, and an inherited `spawn_notification: true` key survives. Both terminal statuses take one uniform control flow. Do not change any other statement in `applyRetryCounter`.

> Indentation in the code blocks below is normalized to spaces for readability — match the file's existing TAB indentation when applying them.

7. **Rewrite the carved-out spec** `It("previous_assignee persists when operator re-delegates by setting a non-empty assignee")` (currently line 1351-1403) in `pkg/result/result_writer_test.go`. This is one of exactly TWO existing blocks you may modify (see Requirement 7b for the second). The first write (lines 1354-1377, cap fires → `previous_assignee: claude` written, `assignee` cleared) stays unchanged. Rewrite the second-write portion to model the reset the way the system actually performs it — the scanner's Empty-to-Named Reset (spec 021) writes `trigger_count: 0` to DISK on an empty→named assignee transition, it is NOT carried in the incoming payload:
   - Delete `"trigger_count":   0, // operator reset` from the second-write incoming `lib.Task{...}`.
   - BEFORE the second `Expect(writer.WriteResult(ctx, taskFile)).To(Succeed())`, add a `writeTaskFile("my-task.md", ...)` call that rewrites the on-disk file to `status: in_progress`, `phase: planning`, `trigger_count: 0`, `max_triggers: 3`, `assignee: backtest-agent`, AND `previous_assignee: claude`, body `## Task\nRetrying with backtest-agent.\n`. The rewritten on-disk content MUST itself retain `previous_assignee: claude` so the persistence assertion tests persistence rather than re-creation.
   - The second-write incoming `lib.Task{...}` keeps `status: in_progress`, `phase: planning`, `max_triggers: 3`, `assignee: backtest-agent` (no `trigger_count` key).
   - Keep both existing post-write assertions unchanged: `Expect(s2).To(ContainSubstring("previous_assignee: claude"))` and `Expect(s2).To(ContainSubstring("assignee: backtest-agent"))`.
   The spec's intent is preserved: `previous_assignee: claude` persists (retained on disk through the reset, then preserved by the merge because the incoming payload does not carry it) and `assignee: backtest-agent` is set. With `trigger_count: 0` on disk, the second publish does not re-escalate.


7b. **Second carve-out — `It("fully replaces content on second call")`** (`pkg/result/result_writer_test.go`, line 215, inside `Context("overwrite")`). This spec writes twice. Its FIRST write carries `"status": "done"`, and the normalizing accessor canonicalises `done` → `completed`, which is terminal. Under the new pin the second write's incoming `"status": "closed"` is therefore correctly discarded and `Expect(string(written)).To(ContainSubstring("status: closed"))` fails. The spec's intent is body replacement across two writes, not terminal-status semantics, so restore it by making the FIRST write's status non-terminal. Change ONLY that one fixture value — do not touch any `Expect(` line (AC 14 must stay clean).

    Find:

    ```go
    				taskFile = lib.Task{
    					TaskIdentifier: identifier,
    					Frontmatter: lib.TaskFrontmatter{
    						"task_identifier": "test-task-uuid-1234",
    						"status":          "done",
    					},
    					Content: lib.TaskContent("First result\n"),
    				}
    ```

    Replace with:

    ```go
    				taskFile = lib.Task{
    					TaskIdentifier: identifier,
    					Frontmatter: lib.TaskFrontmatter{
    						"task_identifier": "test-task-uuid-1234",
    						"status":          "in_progress",
    					},
    					Content: lib.TaskContent("First result\n"),
    				}
    ```

8. **Add the new unit specs to `pkg/result/result_writer_test.go`.** Do not modify any existing block other than the carve-out. Follow the spec's Test-style directive (`DescribeTable` for the counter rows and the terminal-status rows; plain `It` for the rest), and reuse the `writeTaskFile` / `lib.Task` / `ContainSubstring` harness from `BeforeEach`. Assert on the WRITTEN FILE BYTES (the full serialize path through `MergeFrontmatter` → `yaml.Marshal` → write), not on struct shapes:

   8a. Inside `Context("trigger_count cap escalation")` (which closes around line 1494), add the load-bearing plain `It` (spec AC 4):
   ```go
   It("escalates on the on-disk trigger_count even when the incoming payload carries a stale lower value", func() {
       writeTaskFile(
           "my-task.md",
           "---\ntask_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: ai_review\ntrigger_count: 3\nmax_triggers: 3\nassignee: claude\n---\n## Result\nStatus: failed\n",
       )
       taskFile = lib.Task{
           TaskIdentifier: identifier,
           Frontmatter: lib.TaskFrontmatter{
               "task_identifier": "test-task-uuid-1234",
               "status":          "in_progress",
               "phase":           "ai_review",
               "trigger_count":   1, // stale snapshot — on-disk is 3
               "max_triggers":    3,
               "assignee":        "claude",
           },
           Content: lib.TaskContent("## Result\nStatus: failed\n"),
       }
       Expect(writer.WriteResult(ctx, taskFile)).To(Succeed())
       written, _ := os.ReadFile(filepath.Join(tmpDir, taskDir, "my-task.md"))
       s := string(written)
       Expect(s).To(ContainSubstring("## Trigger Cap Escalation"))
       Expect(s).To(ContainSubstring("trigger_count: 3"))
       Expect(s).NotTo(ContainSubstring("trigger_count: 1"))
       Expect(s).To(ContainSubstring("assignee: \"\""))
   })
   ```
   The cap fires BECAUSE the on-disk counter won (AC 4 is the load-bearing case).

   8b. Inside `Describe("WriteResult")`, add a new `Context("field ownership guard")` (place it right after `Context("trigger_count cap escalation")` closes and before `Context("atomic write and push error")` around line 1589). It contains:
   - A `DescribeTable` for the counter rows (ACs 1, 2, 3) with this shared body:
     ```go
     DescribeTable(
         "the on-disk counter always wins and an absent counter stays absent",
         func(onDiskFM string, incomingFM lib.TaskFrontmatter, present, absent []string) {
             writeTaskFile("my-task.md", "---\n"+onDiskFM+"---\n## Result\nStatus: failed\n")
             taskFile = lib.Task{
                 TaskIdentifier: identifier,
                 Frontmatter:    incomingFM,
                 Content:        lib.TaskContent("## Result\nStatus: failed\n"),
             }
             Expect(writer.WriteResult(ctx, taskFile)).To(Succeed())
             written, _ := os.ReadFile(filepath.Join(tmpDir, taskDir, "my-task.md"))
             s := string(written)
             for _, want := range present {
                 Expect(s).To(ContainSubstring(want))
             }
             for _, unwanted := range absent {
                 Expect(s).NotTo(ContainSubstring(unwanted))
             }
         },
         Entry("keeps on-disk trigger_count over a stale incoming value",
             "task_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: ai_review\ntrigger_count: 5\n",
             lib.TaskFrontmatter{"task_identifier": "test-task-uuid-1234", "status": "in_progress", "phase": "ai_review", "trigger_count": 1},
             []string{"trigger_count: 5"},
             []string{"trigger_count: 1"},
         ),
         Entry("keeps on-disk retry_count over a stale incoming value",
             "task_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: ai_review\nretry_count: 4\n",
             lib.TaskFrontmatter{"task_identifier": "test-task-uuid-1234", "status": "in_progress", "phase": "ai_review", "retry_count": 0},
             []string{"retry_count: 4"},
             []string{"retry_count: 0"},
         ),
         Entry("incoming counters absent on disk are never written",
             "task_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: ai_review\n",
             lib.TaskFrontmatter{"task_identifier": "test-task-uuid-1234", "status": "in_progress", "phase": "ai_review", "trigger_count": 1, "retry_count": 0},
             []string{},
             []string{"trigger_count", "retry_count"},
         ),
     )
     ```
     (The third row's negative assertions `NotTo(ContainSubstring("trigger_count"))` and `NotTo(ContainSubstring("retry_count"))` prove an incoming value can never introduce a controller-owned counter absent on disk — AC 3.)
   - A `DescribeTable` for the two terminal-status pin rows (ACs 5 and 6, same fixture shape, differing terminal status value):
     ```go
     DescribeTable(
         "a terminal on-disk status is pinned and the incoming status is discarded",
         func(terminalStatus string) {
             writeTaskFile(
                 "my-task.md",
                 "---\ntask_identifier: test-task-uuid-1234\nstatus: "+terminalStatus+"\nphase: ai_review\n---\n## Result\nStatus: failed\n",
             )
             taskFile = lib.Task{
                 TaskIdentifier: identifier,
                 Frontmatter: lib.TaskFrontmatter{
                     "task_identifier": "test-task-uuid-1234",
                     "status":          "in_progress",
                     "phase":           "ai_review",
                 },
                 Content: lib.TaskContent("## Result\nStatus: failed\n"),
             }
             Expect(writer.WriteResult(ctx, taskFile)).To(Succeed())
             written, _ := os.ReadFile(filepath.Join(tmpDir, taskDir, "my-task.md"))
             s := string(written)
             Expect(s).To(ContainSubstring("status: " + terminalStatus))
             Expect(s).NotTo(ContainSubstring("status: in_progress"))
         },
         Entry("aborted", "aborted"),
         Entry("completed", "completed"),
         Entry("done (alias normalized to completed)", "done"),
     )
     ```
   - A plain `It` for the pin-not-freeze case (AC 7, four assertions in one spec):
     ```go
     It("pins only status and still records the agent payload on a terminal task", func() {
         writeTaskFile(
             "my-task.md",
             "---\ntask_identifier: test-task-uuid-1234\nstatus: aborted\n---\nOld body\n",
         )
         taskFile = lib.Task{
             TaskIdentifier: identifier,
             Frontmatter: lib.TaskFrontmatter{
                 "task_identifier": "test-task-uuid-1234",
                 "status":          "in_progress",
                 "phase":           "execution",
             },
             Content: lib.TaskContent("## Result\nStatus: failed\n"),
         }
         Expect(writer.WriteResult(ctx, taskFile)).To(Succeed())
         written, _ := os.ReadFile(filepath.Join(tmpDir, taskDir, "my-task.md"))
         s := string(written)
         Expect(s).To(ContainSubstring("status: aborted"))
         Expect(s).To(ContainSubstring("phase: execution"))
         Expect(s).To(ContainSubstring("## Result"))
         Expect(s).NotTo(ContainSubstring("status: in_progress"))
     })
     ```
   - Two plain `It` blocks for the terminal short-circuit (ACs 8 and 9 — DB 4), identical fixtures except the on-disk terminal status, identical assertions (one positive, three negative). The aborted row:
     ```go
     It("short-circuits the escalation machinery when the on-disk status is aborted (DB 4)", func() {
         writeTaskFile(
             "my-task.md",
             "---\ntask_identifier: test-task-uuid-1234\nstatus: aborted\nphase: ai_review\ntrigger_count: 3\nmax_triggers: 3\nassignee: claude\nspawn_notification: true\n---\n## Result\nStatus: failed\n",
         )
         taskFile = lib.Task{
             TaskIdentifier: identifier,
             Frontmatter: lib.TaskFrontmatter{
                 "task_identifier": "test-task-uuid-1234",
                 "status":          "in_progress",
                 "phase":           "ai_review",
             },
             Content: lib.TaskContent("## Result\nStatus: failed\n"),
         }
         Expect(writer.WriteResult(ctx, taskFile)).To(Succeed())
         written, _ := os.ReadFile(filepath.Join(tmpDir, taskDir, "my-task.md"))
         s := string(written)
         Expect(s).NotTo(ContainSubstring("## Trigger Cap Escalation"))
         Expect(s).To(ContainSubstring("\nassignee: claude"))
         Expect(s).NotTo(ContainSubstring("previous_assignee"))
         Expect(s).To(ContainSubstring("spawn_notification: true"))
     })
     ```
     Add a second `It` with the identical body and assertions but on-disk `status: completed` instead of `aborted` (AC 9 — both terminal statuses take one uniform control flow). Keep the incoming payload identical (do NOT carry `trigger_count`/`max_triggers`/`assignee`/`spawn_notification`, so those on-disk values survive the merge and the terminal early return is what protects them).
   - A plain `It` for the negative control (AC 10 — the fix is NOT "existing always wins"):
     ```go
     It("still lets the agent own phase, status, and new keys on a non-terminal task (negative control)", func() {
         writeTaskFile(
             "my-task.md",
             "---\ntask_identifier: test-task-uuid-1234\nstatus: next\nphase: ai_review\n---\nOld body\n",
         )
         taskFile = lib.Task{
             TaskIdentifier: identifier,
             Frontmatter: lib.TaskFrontmatter{
                 "task_identifier": "test-task-uuid-1234",
                 "status":          "in_progress",
                 "phase":           "execution",
                 "custom_field":    "from-agent",
             },
             Content: lib.TaskContent("New body\n"),
         }
         Expect(writer.WriteResult(ctx, taskFile)).To(Succeed())
         written, _ := os.ReadFile(filepath.Join(tmpDir, taskDir, "my-task.md"))
         s := string(written)
         Expect(s).To(ContainSubstring("status: in_progress"))
         Expect(s).To(ContainSubstring("phase: execution"))
         Expect(s).To(ContainSubstring("custom_field: from-agent"))
         Expect(s).NotTo(ContainSubstring("phase: ai_review"))
     })
     ```

   8c. Add a new `Describe("MergeFrontmatter", func() { ... })` after `Describe("ExtractBody")` closes and before the file's final closing `})` (around line 1747-1748). This is the DB 6 decision-reporting spec, calling the exported helper directly (AC 11):
   ```go
   Describe("MergeFrontmatter", func() {
       It("reports zero guard decisions when the incoming counter equals the on-disk counter", func() {
           existing := lib.TaskFrontmatter{"status": "in_progress", "trigger_count": 3}
           incoming := lib.TaskFrontmatter{"status": "in_progress", "trigger_count": 3}
           _, decisions := result.MergeFrontmatter(existing, incoming)
           Expect(decisions).To(HaveLen(0))
       })

       It("reports zero decisions when a JSON-decoded float64 counter equals the YAML-decoded int counter", func() {
           existing := lib.TaskFrontmatter{"status": "in_progress", "trigger_count": 3}
           incoming := lib.TaskFrontmatter{"status": "in_progress", "trigger_count": float64(3)}
           _, decisions := result.MergeFrontmatter(existing, incoming)
           Expect(decisions).To(HaveLen(0))
       })

       It("reports exactly one decision naming the field and both values when the incoming counter differs", func() {
           existing := lib.TaskFrontmatter{"status": "in_progress", "trigger_count": 3}
           incoming := lib.TaskFrontmatter{"status": "in_progress", "trigger_count": 1}
           _, decisions := result.MergeFrontmatter(existing, incoming)
           Expect(decisions).To(HaveLen(1))
           Expect(decisions[0].Field).To(Equal("trigger_count"))
           Expect(decisions[0].Kept).To(Equal(3))
           Expect(decisions[0].Rejected).To(Equal(1))
       })
   })
   ```

9. **Add the `CHANGELOG.md` entry (spec AC 19).** Append a new `fix:` bullet to the EXISTING `## Unreleased` section (line 8), below the existing terminal-reopen bullet. Per `changelog-guide.md`: one bullet, prefix required, specific (name the types/fields — never `- fix: fix bug`). The bullet MUST name the counter rollback AND the reverted terminal status, and MUST contain the substring `trigger_count` (the AC's position-aware `awk` check requires it to appear above the first `## v` heading). Example shape:
   ```
   - fix: stop result writes from rolling back controller-owned state — the result writer now keeps the on-disk `trigger_count`/`retry_count` (an incoming payload can never reset them, so the trigger/retry caps compare against real spawn counts) and pins a terminal on-disk `status` (`aborted`/`completed`), so an operator abort survives every publish and a pinned-terminal task no longer accrues escalation sections (spec 006)
   ```

10. **Self-check before finishing:** verify each of the new specs above lands in the file, run `make test` after each meaningful change, then run the `<verification>` block. Confirm the spec's Constraint is honored: `pkg/command/task_increment_frontmatter_executor.go` is FROZEN — do not touch it, do not add any new write site or Kafka operation, do not change `ClearAssigneeIfHumanReview`, `restoreExistingPhase`, `applyTriggerCap`, `applyRetryCap`, or the escalation-section writers. The Rung-3 post-deploy AC (AC 20) is operator-side and lives in the spec's Verification ladder — do NOT attempt kubectl/cluster steps in this container.

</requirements>

<constraints>

- The change stays inside the existing `pkg/result` result-write chokepoint (`MergeFrontmatter` — the renamed `mergeFrontmatter` — invoked from `buildResultModifyFn`) plus the terminal-status early return in `applyRetryCounter`. No new write site, no new Kafka operation, no new command executor.
- `pkg/command/task_increment_frontmatter_executor.go` is FROZEN — do not modify it in any way.
- Terminal detection uses `github.com/bborbe/agent`'s normalizing `TaskFrontmatter.Status()` accessor compared against `domain.TaskStatusCompleted` / `domain.TaskStatusAborted` — never a raw string equality on the unparsed value. `phase: done` is NOT a status and plays no part.
- Terminal statuses are exactly `aborted` and `completed` (plus whatever the normalizing accessor canonicalises into them).
- The guard is unconditional: no config flag, env var, or per-task opt-out.
- The `WriteResult` body semantics are frozen: the on-disk body is fully replaced by the agent's content, exactly as today.
- `ClearAssigneeIfHumanReview` keeps its exported signature and semantics; only the set of tasks that reach it changes (pinned-terminal tasks no longer do).
- The incoming payload is never a valid counter-reset channel — the only mechanism that may lower a counter is the scanner's Empty-to-Named Reset, which is NOT modified by this spec.
- Every existing spec in `pkg/result/result_writer_test.go` must pass with its `Expect(` lines unmodified, with exactly TWO carve-outs: (a) `It("previous_assignee persists when operator re-delegates by setting a non-empty assignee")` (line 1351) — rewritten per Requirement 7; (b) `It("fully replaces content on second call")` (line 215) — one fixture value changed per Requirement 7b. Neither carve-out deletes or weakens an `Expect(` line. No other existing `It(`/`Context`/`Expect(` may be changed. Do NOT add the `phase`+`ref`-scoped counter, and do NOT flip the trigger cap from opt-in to default-on.
- `buildUpdateModifyFn` in `pkg/command/task_update_frontmatter_executor.go` is NOT guarded — it is the controller/executor/operator write channel (increment + Empty-to-Named Reset + operator status sets). Do not apply the ownership guard to it; doing so would break the very reset this spec depends on.
- Per `go-error-wrapping-guide.md`: `errors.Wrapf(ctx, ...)` only in `pkg/` — never `fmt.Errorf`, never `context.Background()`.
- Per `go-glog-guide.md`: the guard log is unconditional `glog.Infof`, not `glog.V(n).Infof`, and names only the task identifier, the field, and the two values — never the body or the full frontmatter.
- Per `go-security-linting.md`: gosec rules apply; the new code introduces no new file writes or input evaluation.
- Do NOT commit — dark-factory handles git.
- Do NOT run kubectl/cluster/operator commands — the container has no cluster access; the Rung-3 post-deploy AC lives in the spec's operator-executable Verification rung.

</constraints>

<verification>

Run iteratively while implementing (fast loop):

```
cd /workspace && make test
```

Frozen-substring / declaration checks (spec ACs 12-13; every `grep` wrapped in `|| true` because `grep -c`/`grep -n` exit 1 on a zero count):

```
cd /workspace && grep -n -B3 'ownership guard kept on-disk' pkg/result/result_writer.go || true
cd /workspace && grep -c '"trigger_count"' pkg/result/result_writer.go || true
cd /workspace && grep -n 'controllerOwnedFields\|terminalStatuses' pkg/result/result_writer.go || true
```
Expect: ≥1 line containing `glog.Infof` on the `ownership guard kept on-disk` line; `"trigger_count"` count ≥1 (the package-level `controllerOwnedFields` declaration); the declaration grep returns ≥2 lines (each identifier's declaration line plus its use in `MergeFrontmatter` / `isTerminalStatus`).

Carved-out spec integrity (spec AC 15):

```
cd /workspace && grep -c 'previous_assignee persists when operator re-delegates' pkg/result/result_writer_test.go || true
```
Expect exactly 1 — the carved block still exists once and still asserts both `previous_assignee: claude` and `assignee: backtest-agent` after the second write.

Test-suite-integrity note (spec AC 14): the machine-decidable evidence for "no `Expect(` line weakened outside the carved block" is a `git diff -U0 pkg/result/result_writer_test.go` evaluation. The execution container's `.git` is masked, so do NOT attempt git here — the auditor runs the diff on the host at review time. In the container, enforce AC 14 by the Requirement-8 discipline: only the carved block may differ; every other existing `It(`/`Expect(` is byte-identical; and the full suite passing proves no behavioral regression.

CHANGELOG AC (spec AC 19):

```
cd /workspace && awk '/^## v/{exit} /trigger_count/{f=1} END{exit !f}' CHANGELOG.md
```
Expect exit 0 (the new `## Unreleased` bullet containing `trigger_count` appears above the first `## v` heading; it exits 1 today).

Run ONCE at the end:

```
cd /workspace && make precommit
```

Expected: exit 0 with the full Ginkgo suite green — all new specs pass, the rewritten carved spec passes, and every pre-existing spec in `pkg/result/result_writer_test.go` passes with unmodified `Expect(` lines.

</verification>
