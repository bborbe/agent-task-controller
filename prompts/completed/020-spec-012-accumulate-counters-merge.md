---
status: completed
spec: [012-accumulate-agent-counters-on-write-back]
summary: Added the accumulate behaviour to MergeFrontmatter for metrics_agent_turns and metrics_interaction_count (sum when both sides numeric, keep on-disk on absent/non-numeric incoming, take incoming on absent on-disk), with decision-level and written-file specs; make precommit exits 0
execution_id: agent-task-controller-accumulate-exec-020-spec-012-accumulate-counters-merge
dark-factory-version: v0.193.0
created: "2026-09-14T20:40:02Z"
queued: "2026-09-14T20:57:47Z"
started: "2026-09-14T20:57:47Z"
completed: "2026-09-14T21:05:17Z"
branch: dark-factory/accumulate-agent-counters-on-write-back
---

# Accumulate the two agent counters in the result write-back merge

<summary>

- The result write-back merge gains a third behaviour beside "keep the on-disk value" and "incoming wins": **accumulate**.
- When both the task file and the agent's result payload carry a number for one of the two cumulative counters, the written value is their sum — after N runs the on-disk total is the sum of the N per-run emitted values.
- Two keys accumulate and only these two: the agent's own turn count for the run, and the run's human interaction count. Their spellings are a frozen cross-repo contract with the agent-side emitter.
- An emitted `0` (an evidenced unattended run) adds nothing — it can no longer wipe a lifetime total that a previous run built up.
- A payload that omits a counter leaves the on-disk value exactly as it was; the first run on a task that never carried the key writes the emitted value as the starting total.
- Numbers are compared and added by value, so a JSON-decoded decimal payload value adds correctly to a YAML-decoded whole number on disk, and no value type can panic the merge.
- A non-numeric value on either side never crashes and never overwrites: the on-disk value is kept verbatim, and the merge reports one guard decision naming the field when the two values differ (none when they are equal).
- Accumulation is a transform, not a rejection: a clean sum produces no guard decision and no new log line.
- The two existing guard lists keep exactly their current members — neither counter is added to them — and nothing else in the merge moves.
- Proven by unit specs that assert on the written file's bytes and on the merge's decision list.

</summary>

<objective>

Make the controller's single result write-back chokepoint treat `metrics_agent_turns` and `metrics_interaction_count` as cumulative counters — sum when both sides carry a numeric value, keep the on-disk value when the payload omits the key, take the emitted value when the file carries none, and keep the on-disk value verbatim (with one guard decision when the values differ) when either side is non-numeric — so the agent-side emitter cannot silently reset a task's lifetime total to one run's value, and neither counter key is added to the frozen guard lists (spec 012, Desired Behavior 1-5, AC 1-9).

</objective>

<context>

This repo has no root `CLAUDE.md`; the global YOLO container CLAUDE.md (already in your context) governs project conventions. Read the repo's own code and docs as the source of truth.

Read the spec first: `specs/in-progress/012-accumulate-agent-counters-on-write-back.md` — Summary, Problem, Goal, Non-goals, Acceptance Criteria, Desired Behavior 1-5, Constraints, Failure Modes, Security / Abuse Cases, and the "Suggested Decomposition" table (this prompt is row 1).

Read these files fully before changing anything:

- `pkg/result/result_writer.go` — the whole file. The pieces this prompt touches:
  - `MergeFrontmatter(existing, incoming lib.TaskFrontmatter) (lib.TaskFrontmatter, []GuardDecision)` — the merge. Its ONLY production call site is `buildResultModifyFn` (`merged, decisions := MergeFrontmatter(currentOnDisk, req.Frontmatter)`); the merge keeps exactly one call site (spec Constraint).
  - `GuardDecision` (`Field`, `Kept`, `Rejected`) — the record of one guard discard.
  - `controllerOwnedFields = []string{"trigger_count", "retry_count"}` and `operatorOwnedFields = []string{"assignee", "previous_assignee"}` — FROZEN. Neither list gains a member (spec AC 8).
  - `applyOperatorOwnedFields(existing, incoming, merged lib.TaskFrontmatter, decisions []GuardDecision) []GuardDecision` — the helper shape to mirror: it takes the merged map and the decision list, applies its rule in place, and returns the (possibly extended) decision list. The accumulation helper follows this exact shape.
  - `frontmatterValueEqual(a, b any) bool` and `numericValue(v any) (float64, bool)` — reuse both, do not modify either.
  - The guard-log loop in `buildResultModifyFn` (`glog.Infof("ownership guard kept on-disk: task %s field %s kept %v rejected %v", ...)`) — unchanged; it stays the only deployed-side signal, and it logs exactly what `MergeFrontmatter` returns as decisions.
- `pkg/result/result_writer_guard_test.go` (218 lines, `package result_test`, `Describe("MergeFrontmatter", ...)`) — the decision-level specs. Your new value/decision specs go here.
- `docs/controller-design.md` § "Frontmatter Merge" (lines 56-95) — the ownership doctrine this change extends: the guard tables, the `frontmatterValueEqual` rule, and the sentence that `MergeFrontmatter` has exactly one call site. Prompt 3 adds the accumulated-counter row to its table; your code must match what that row will claim.
- `pkg/result/result_writer_test.go` — read the `Context("field ownership guard")` block in full (it starts around line 1595 and ends after `It("still lets the agent own phase, status, and new keys on a non-terminal task (negative control)")`). It holds the two `DescribeTable`s and the shared harness (`writeTaskFile`, `taskFile`, `ContainSubstring` assertions on the written file). Your new file-content specs go at the end of that Context.

Read the coding-plugin docs that apply:

- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` (Ginkgo/Gomega, `DescribeTable`, coverage ≥80% for changed code, error paths)
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-patterns.md` (package-level declaration style, doc comments)
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-doc-best-practices.md` (GoDoc comment starts with the name, full sentences, behaviour not implementation)
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-glog-guide.md` (why this change adds NO log line)
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-precommit.md` (funlen 80, golines 100, tab indentation)
- `/home/node/.claude/plugins/marketplaces/coding/docs/definition-of-done.md`

Verified facts (checked against the module sources and by execution on 2026-09-14 — do not re-derive, and do not contradict them):

- `type TaskFrontmatter map[string]interface{}` from `github.com/bborbe/agent` (imported as `lib`); `pkg/result/result_writer.go` already imports it.
- On-disk frontmatter is parsed with `gopkg.in/yaml.v3`, which decodes `metrics_agent_turns: 3` as `int(3)`; the incoming `req.Frontmatter` reaches `WriteResult` after a JSON round-trip through cqrs, which decodes the same number as `float64(3)`. `numericValue` already normalises `int` / `int64` / `float64` to `float64` for exactly this reason.
- `yaml.Marshal` renders a whole-number `float64` without a decimal point: `yaml.Marshal(map[string]any{"metrics_agent_turns": float64(10)})` produces the bytes `metrics_agent_turns: 10\n` (verified by execution against `gopkg.in/yaml.v3@v3.0.1`). So a float64 sum is written to the file as an integer-looking scalar and `ContainSubstring("metrics_agent_turns: 10")` is the correct assertion.
- The accumulated value in the returned map is therefore a `float64`. Never assert it with `Equal(10)` (a bare `int`); assert on the written file bytes, or use `BeNumerically("==", 10)`, which is type-agnostic.
- `metrics_agent_turns` and `metrics_interaction_count` appear nowhere in this repo's code today — these are the first occurrences, and their spellings are frozen (lowercase, snake_case, no alias, no version suffix).

Environment facts that shape this prompt:

- The execution container's `.git` is masked (the daemon runs with `hideGit=true`; `git status` fails with `fatal: not a git repository`). Do NOT run any `git` command — a failed git command in `<verification>` would look like a pass. The spec's `git diff -U0 ...` ACs (AC 9, AC 10) are host-side evaluations; the non-git equivalents in `<verification>` below are what you run.
- Do NOT run `kubectl*`, `docker`, `make build`, or any deploy command — the spec's Post-Deploy ACs (15, 16) live on the spec's operator-executable rung.

</context>

<requirements>

All production changes are in `pkg/result/result_writer.go`; test changes go in `pkg/result/result_writer_guard_test.go` and `pkg/result/result_writer_test.go`. Match the files' existing TAB indentation, `glog`/`errors.Wrapf` idioms, and doc-comment voice.

1. **Add the frozen counter-name declaration.** Place it immediately after the existing `operatorOwnedFields` declaration, mirroring that declaration's shape (doc comment explaining the rule, then a fixed literal list):

   ```go
   // accumulatedCounters lists frontmatter keys that are cumulative in the result
   // write-back merge: when both the on-disk frontmatter and the incoming payload
   // carry a numeric value for one of them, the written value is their sum. These
   // keys are neither controller-owned (that guard would discard the emitted value,
   // so the counter would never move) nor agent-owned (incoming-wins would reset a
   // lifetime total to one run's value) — accumulation is the third ownership
   // behaviour. The spellings are a frozen cross-repo contract with the agent-side
   // emitter (spec 012): a renamed emitter key silently stops accumulating.
   var accumulatedCounters = []string{
       "metrics_agent_turns",
       "metrics_interaction_count",
   }
   ```

   The two spellings are exact and frozen — lowercase, snake_case, no alias, no version suffix. The list is a fixed literal, never derived from the incoming payload.

2. **Add the `applyAccumulatedCounters` helper.** Place it directly after `applyOperatorOwnedFields` (same argument shape and return shape as that helper, so the two rules read as siblings):

   ```go
   // applyAccumulatedCounters applies the cumulative-counter rule to the merged
   // frontmatter. When both sides carry a numeric value for one of the accumulated
   // counters, the written value is their sum — compared and added by numeric value,
   // so a JSON-decoded incoming float64 adds correctly to a YAML-decoded on-disk int —
   // and NO guard decision is produced: accumulation is a transform, not a discard.
   // When either side is non-numeric the on-disk value is kept verbatim and the
   // incoming value is discarded, producing one decision naming the field when the two
   // values differ and none when they are equal. A key absent on either side keeps the
   // base merge's outcome: an absent incoming key leaves the on-disk value untouched,
   // and an absent on-disk key takes the incoming value. Returns the decision list with
   // any discards appended.
   func applyAccumulatedCounters(
       existing, incoming, merged lib.TaskFrontmatter,
       decisions []GuardDecision,
   ) []GuardDecision {
       for _, field := range accumulatedCounters {
           diskValue, onDisk := existing[field]
           incomingValue, inIncoming := incoming[field]
           if !onDisk || !inIncoming {
               // The base merge already produced the right value: an absent incoming
               // key leaves the on-disk value untouched, and an absent on-disk key
               // takes the incoming value as the starting total.
               continue
           }
           diskNumber, diskIsNumber := numericValue(diskValue)
           incomingNumber, incomingIsNumber := numericValue(incomingValue)
           if diskIsNumber && incomingIsNumber {
               merged[field] = diskNumber + incomingNumber
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

   Semantics that must hold exactly:
   - `!onDisk || !inIncoming` → no action. Both sides present is the only case this helper changes (the base merge already handles the other two: absent incoming leaves the on-disk value, absent on-disk takes the incoming value).
   - Both numeric → `merged[field]` is the float64 sum, and NO decision. `0` incoming is numeric, so it adds nothing — an evidenced unattended run leaves the total unchanged.
   - Either side non-numeric → the on-disk value is written back verbatim, and `frontmatterValueEqual` decides whether a decision is appended (differing → exactly one decision naming the field; equal → none, the existing equal-values rule).
   - Never use `==` on two `any` values anywhere in this path (spec Constraint) — `frontmatterValueEqual` is the comparison mechanism, and `numericValue` is the numeric-conversion mechanism. Do not add a type assertion that can fail hard, and do not touch `numericValue` or `frontmatterValueEqual` themselves.

3. **Wire the helper into `MergeFrontmatter`.** The call goes after the operator-owned call, so the rule order stays controller-owned → terminal pin → operator-owned → accumulated:

   ```go
       decisions = applyOperatorOwnedFields(existing, incoming, merged, decisions)
       decisions = applyAccumulatedCounters(existing, incoming, merged, decisions)
       return merged, decisions
   ```

   The controller-owned loop, the terminal-status pin block, `applyOperatorOwnedFields`, and the returned decision list for every non-counter key are unchanged. The accumulated rule is additive: it must not disturb any existing decision, and for the two counter keys it is the last rule that runs.

4. **Extend `MergeFrontmatter`'s doc comment** with one sentence stating the third behaviour, in the existing voice, e.g.: "The accumulated counters (`metrics_agent_turns`, `metrics_interaction_count`) are summed when both sides carry a numeric value, keep the on-disk value when the incoming key is absent, and take the incoming value when the key is absent on disk; a non-numeric value on either side keeps the on-disk value verbatim and is reported as a decision when the two values differ." Do not rewrite the rest of the comment.

5. **Add the decision-level specs to `pkg/result/result_writer_guard_test.go`.** Append them inside `Describe("MergeFrontmatter", ...)`, after the last existing `It` (the non-string-assignee panic spec). Do NOT modify, reorder, or weaken any existing spec or `Expect(` line in this file — additions only. Use `result.MergeFrontmatter(existing, incoming)` directly (external test package `result_test`) and `BeNumerically("==", ...)` for numeric values:

   ```go
   DescribeTable(
       "an accumulated counter adds the incoming value to the on-disk total with no decision",
       func(existing, incoming lib.TaskFrontmatter, field string, want float64) {
           merged, decisions := result.MergeFrontmatter(existing, incoming)
           Expect(decisions).To(HaveLen(0))
           Expect(merged[field]).To(BeNumerically("==", want))
       },
       Entry(
           "on-disk int 7 plus incoming float64 3 accumulates to 10",
           lib.TaskFrontmatter{"status": "in_progress", "metrics_agent_turns": 7},
           lib.TaskFrontmatter{"status": "in_progress", "metrics_agent_turns": float64(3)},
           "metrics_agent_turns",
           float64(10),
       ),
       Entry(
           "on-disk int64 3 plus incoming int 4 accumulates to 7",
           lib.TaskFrontmatter{"status": "in_progress", "metrics_agent_turns": int64(3)},
           lib.TaskFrontmatter{"status": "in_progress", "metrics_agent_turns": 4},
           "metrics_agent_turns",
           float64(7),
       ),
       Entry(
           "on-disk int 116 plus incoming float64 41 accumulates to 157",
           lib.TaskFrontmatter{"status": "in_progress", "metrics_interaction_count": 116},
           lib.TaskFrontmatter{"status": "in_progress", "metrics_interaction_count": float64(41)},
           "metrics_interaction_count",
           float64(157),
       ),
       Entry(
           "an evidenced unattended run (incoming 0) leaves the on-disk total unchanged",
           lib.TaskFrontmatter{"status": "in_progress", "metrics_interaction_count": 116},
           lib.TaskFrontmatter{"status": "in_progress", "metrics_interaction_count": float64(0)},
           "metrics_interaction_count",
           float64(116),
       ),
   )
   ```

   The `int64` entry lives here rather than in the file-content table on purpose: a YAML fixture cannot produce an `int64` on-disk value for a small number (`gopkg.in/yaml.v3` decodes it as `int`), so the cross-type matrix is exercised by calling the merge directly with the value types the boundary can produce, and `BeNumerically("==", ...)` keeps the assertion type-agnostic.

   Then three plain `It` blocks:

   ```go
   It("leaves both on-disk counters untouched with no decision when the incoming payload carries neither key", func() {
       existing := lib.TaskFrontmatter{
           "status":                    "in_progress",
           "metrics_agent_turns":       7,
           "metrics_interaction_count": 116,
       }
       incoming := lib.TaskFrontmatter{"status": "in_progress"}
       merged, decisions := result.MergeFrontmatter(existing, incoming)
       Expect(decisions).To(HaveLen(0))
       Expect(merged["metrics_agent_turns"]).To(Equal(7))
       Expect(merged["metrics_interaction_count"]).To(Equal(116))
   })

   It("takes the incoming value with no decision when the on-disk frontmatter carries no counter key (first run)", func() {
       existing := lib.TaskFrontmatter{"status": "in_progress"}
       incoming := lib.TaskFrontmatter{
           "status":                    "in_progress",
           "metrics_agent_turns":       3,
           "metrics_interaction_count": 0,
       }
       merged, decisions := result.MergeFrontmatter(existing, incoming)
       Expect(decisions).To(HaveLen(0))
       Expect(merged["metrics_agent_turns"]).To(Equal(3))
       Expect(merged["metrics_interaction_count"]).To(Equal(0))
   })

   It("keeps a non-numeric counter verbatim and reports exactly one decision naming the field", func() {
       existing := lib.TaskFrontmatter{"status": "in_progress", "metrics_agent_turns": "unparseable"}
       incoming := lib.TaskFrontmatter{"status": "in_progress", "metrics_agent_turns": 3}
       merged, decisions := result.MergeFrontmatter(existing, incoming)
       Expect(decisions).To(HaveLen(1))
       Expect(decisions[0].Field).To(Equal("metrics_agent_turns"))
       Expect(decisions[0].Kept).To(Equal("unparseable"))
       Expect(decisions[0].Rejected).To(Equal(3))
       Expect(merged["metrics_agent_turns"]).To(Equal("unparseable"))
   })

   It("keeps a numeric on-disk counter when the incoming value is non-numeric and reports one decision", func() {
       existing := lib.TaskFrontmatter{"status": "in_progress", "metrics_agent_turns": 3}
       incoming := lib.TaskFrontmatter{"status": "in_progress", "metrics_agent_turns": "unparseable"}
       merged, decisions := result.MergeFrontmatter(existing, incoming)
       Expect(decisions).To(HaveLen(1))
       Expect(decisions[0].Field).To(Equal("metrics_agent_turns"))
       Expect(decisions[0].Kept).To(Equal(3))
       Expect(decisions[0].Rejected).To(Equal("unparseable"))
       Expect(merged["metrics_agent_turns"]).To(Equal(3))
   })

   It("keeps the on-disk value verbatim with zero decisions when both sides hold the same non-numeric value", func() {
       existing := lib.TaskFrontmatter{"status": "in_progress", "metrics_agent_turns": "unparseable"}
       incoming := lib.TaskFrontmatter{"status": "in_progress", "metrics_agent_turns": "unparseable"}
       merged, decisions := result.MergeFrontmatter(existing, incoming)
       Expect(decisions).To(HaveLen(0))
       Expect(merged["metrics_agent_turns"]).To(Equal("unparseable"))
   })

   It("keeps a non-numeric slice counter verbatim without panicking (spec Security)", func() {
       existing := lib.TaskFrontmatter{"status": "in_progress", "metrics_agent_turns": []any{"x"}}
       incoming := lib.TaskFrontmatter{"status": "in_progress", "metrics_agent_turns": 3}
       merged, decisions := result.MergeFrontmatter(existing, incoming)
       Expect(decisions).To(HaveLen(1))
       Expect(decisions[0].Field).To(Equal("metrics_agent_turns"))
       Expect(merged["metrics_agent_turns"]).To(Equal([]any{"x"}))
   })
   ```

   Do not add a decision to these specs by way of `status` — both sides carry `status: in_progress`, which is not terminal, so the pin produces nothing and `HaveLen(0)` holds for a clean accumulation.

6. **Add the file-content specs to `pkg/result/result_writer_test.go`.** Append them at the END of `Context("field ownership guard")`, after the existing `It("still lets the agent own phase, status, and new keys on a non-terminal task (negative control)")`, reusing the existing table shape and the `writeTaskFile` / `taskFile` / `ContainSubstring` harness. Do NOT modify any existing block, fixture, or `Expect(` line in this file — additions only.

   6a. The accumulation table — one fixture per behaviour, positive and negative file-content assertions on the written bytes (spec AC 1, 2, 4, 5, 6):

   ```go
   DescribeTable(
       "the accumulated counters add the incoming value to the on-disk total",
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
       Entry(
           "accumulates metrics_agent_turns (7 on disk + 3 incoming = 10)",
           "task_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: ai_review\nmetrics_agent_turns: 7\n",
           lib.TaskFrontmatter{
               "task_identifier":     "test-task-uuid-1234",
               "status":              "in_progress",
               "phase":               "ai_review",
               "metrics_agent_turns": float64(3),
           },
           []string{"metrics_agent_turns: 10"},
           []string{"metrics_agent_turns: 3", "metrics_agent_turns: 7"},
       ),
       Entry(
           "accumulates metrics_interaction_count (116 on disk + 41 incoming = 157)",
           "task_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: ai_review\nmetrics_interaction_count: 116\n",
           lib.TaskFrontmatter{
               "task_identifier":           "test-task-uuid-1234",
               "status":                    "in_progress",
               "phase":                     "ai_review",
               "metrics_interaction_count": float64(41),
           },
           []string{"metrics_interaction_count: 157"},
           []string{"metrics_interaction_count: 41", "metrics_interaction_count: 116"},
       ),
       Entry(
           "an evidenced unattended run (incoming 0) does not wipe the total",
           "task_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: ai_review\nmetrics_interaction_count: 116\n",
           lib.TaskFrontmatter{
               "task_identifier":           "test-task-uuid-1234",
               "status":                    "in_progress",
               "phase":                     "ai_review",
               "metrics_interaction_count": float64(0),
           },
           []string{"metrics_interaction_count: 116"},
           []string{"metrics_interaction_count: 0"},
       ),
       Entry(
           "the first run on a task that never carried the counters writes the emitted values",
           "task_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: ai_review\n",
           lib.TaskFrontmatter{
               "task_identifier":           "test-task-uuid-1234",
               "status":                    "in_progress",
               "phase":                     "ai_review",
               "metrics_agent_turns":       float64(3),
               "metrics_interaction_count": float64(0),
           },
           []string{"metrics_agent_turns: 3", "metrics_interaction_count: 0"},
           []string{},
       ),
       Entry(
           "keeps a non-numeric on-disk counter verbatim over a numeric incoming value",
           "task_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: ai_review\nmetrics_agent_turns: unparseable\n",
           lib.TaskFrontmatter{
               "task_identifier":     "test-task-uuid-1234",
               "status":              "in_progress",
               "phase":               "ai_review",
               "metrics_agent_turns": float64(3),
           },
           []string{"metrics_agent_turns: unparseable"},
           []string{"metrics_agent_turns: 3"},
       ),
       Entry(
           "keeps the on-disk counter when the incoming value is non-numeric",
           "task_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: ai_review\nmetrics_agent_turns: 3\n",
           lib.TaskFrontmatter{
               "task_identifier":     "test-task-uuid-1234",
               "status":              "in_progress",
               "phase":               "ai_review",
               "metrics_agent_turns": "unparseable",
           },
           []string{"metrics_agent_turns: 3"},
           []string{"metrics_agent_turns: unparseable"},
       ),
   )
   ```

   6b. One `It` for the absent-incoming case (spec AC 3) — the payload omits both keys while the file carries both:

   ```go
   It("leaves both on-disk counters verbatim when the incoming payload carries neither key", func() {
       writeTaskFile(
           "my-task.md",
           "---\ntask_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: ai_review\nmetrics_agent_turns: 7\nmetrics_interaction_count: 116\n---\n## Result\nStatus: failed\n",
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
       Expect(s).To(ContainSubstring("metrics_agent_turns: 7"))
       Expect(s).To(ContainSubstring("metrics_interaction_count: 116"))
   })
   ```

   Every on-disk fixture must carry `task_identifier: test-task-uuid-1234` (the writer locates the file by it) and every incoming payload must carry the same `task_identifier`, `status: in_progress` and `phase: ai_review` so no other rule contributes a decision or an overwrite.

7. **Do not add a log line, a metric, a config field, or a flag** (spec Constraints). `applyAccumulatedCounters` and the declaration contain no `glog.`/`log.`/`fmt.Print` call: accumulation is a transform and reports nothing — the existing `ownership guard kept on-disk` line stays the only deployed-side signal, and it already covers the non-numeric branch because that branch produces a `GuardDecision`.

8. **Self-check before finishing.** Re-run `<verification>` and confirm every count; walk spec AC 1-9 against the change one by one (accumulate both keys; unattended `0`; absent incoming; first run; numeric types incl. `int64` on disk; non-numeric never crashes with the right decision counts; clean accumulation → `HaveLen(0)`; guard lists unchanged; no new log line). Confirm the only production function you changed is `MergeFrontmatter` plus the two new declarations, and that no other production behaviour moved.

</requirements>

<constraints>

- **Both counter keys accumulate, and only these two** (spec Desired Behavior 1). `accumulatedCounters` holds exactly `metrics_agent_turns` and `metrics_interaction_count` — exactly these spellings, lowercase snake_case, no alias, no version suffix. The names are a frozen cross-repo contract with the agent-side emitter; this spec consumes them and renames nothing.
- **The guard lists are frozen** at `controllerOwnedFields = {trigger_count, retry_count}` and `operatorOwnedFields = {assignee, previous_assignee}`, with the terminal-`status` pin unchanged. Adding either counter key to `controllerOwnedFields` is the trap this spec exists to avoid: that guard discards rather than accumulates, so the counter would never move. Adding it to `operatorOwnedFields` is equally forbidden.
- **No new knob** (spec Non-goal, invariant): no opt-out flag, no per-key allowlist, no configurable cap, no config field, no env var, no new metric, no new log line. An escape hatch that disables accumulation would reintroduce the silent reset this spec removes.
- **No clamping or bounds-checking** of the counters: the merge adds what it is given; it does not police the emitter's contract. A payload can inflate a counter without limit — that is accepted.
- **`frontmatterValueEqual` remains the comparison mechanism** — never `==` on two `any` values. `==` panics on two values holding the same uncomparable dynamic type (`map`/`slice`), and that panic would kill the single result-write chokepoint. `numericValue` is the only numeric-conversion path; a `map` or `slice` value must fall to the keep-verbatim branch, never to an arithmetic path.
- **The merge keeps exactly one call site** — the result write-back path. No new exported entry point, no new call site, no change to `buildResultModifyFn`'s guard-log loop, `HealTargetVault`, `applyRetryCounter`, `applyTriggerCap`, `applyRetryCap`, `clearAssignee`, `ClearAssigneeIfHumanReview`, `restoreExistingPhase`, `containsEscalationSection`, `mergeBody`, `splitBody`, `ExtractFrontmatter`, `ExtractBody`, or the marshalling step.
- **Single writer**: the Job never writes frontmatter; the controller's result write-back stays the only writer for these keys. Routing the counters through `IncrementFrontmatterCommand` / `UpdateFrontmatterCommand` is rejected and must stay rejected — `buildUpdateModifyFn` applies updates straight onto the on-disk frontmatter with no guard, making it a second writer by construction. Do NOT touch `pkg/command/`.
- **The write stays inside the existing atomic read-modify-write-and-commit-push primitive**; no new write path. The spec's write-path Failure Modes (git-rest unavailable or its push stuck, a crash between reading the file and committing, vault storage exhausted) keep their current behaviour unchanged: the write retries in-process, the readiness probe and the held Kafka offset are untouched, and nothing is silently dropped. Because `buildResultModifyFn` re-reads the on-disk frontmatter on every attempt, a retry or a later delivery accumulates onto the current on-disk value rather than onto a stale snapshot — do not introduce a cached snapshot, a retry counter, or a timeout in this path.
- **Existing specs pass unmodified.** In `pkg/result/result_writer_guard_test.go` and `pkg/result/result_writer_test.go` your change is additive only: no existing `It`/`DescribeTable`/`Expect(` line is deleted, reordered, or weakened. The existing specs for the controller-owned counters, the terminal-`status` pin, the operator-owned `assignee`/`previous_assignee` rules, the body section merge, the preamble rules and the escalation dedup must stay green as written.
- Do NOT commit — dark-factory handles git.
- Do NOT run any `git` command — the container's `.git` is masked, and a failed git command would be read as a pass. The spec's `git diff -U0 ...` ACs are host-side; use the non-git equivalents in `<verification>`.
- Do NOT run `kubectl*`, `docker`, `make build`, `make buca`, or any operator/deploy command. The spec's Post-Deploy ACs (15, 16) are operator-side.
- Do NOT use `PIt`, `XIt`, `FIt`, `Skip(`, or `Pending` anywhere. Keep each `It` closure under 80 lines so `funlen` stays green.
- Do NOT run `go mod vendor`, and never write `-mod=vendor` in a verification command — use `-mod=mod`.

</constraints>

<verification>

Fast loop while implementing (repo root):

```
cd /workspace && go test -mod=mod -count=1 ./pkg/result/...
cd /workspace && make test
```

Expect the Ginkgo suite to report SUCCESS with 0 Skipped and 0 Pending, including the new accumulation specs and every pre-existing `pkg/result` spec.

New specs are present (each `grep -c` wrapped in `|| true` because `grep -c` exits 1 on a zero count):

```
cd /workspace && grep -c 'accumulated counters add the incoming value' pkg/result/result_writer_test.go || true
cd /workspace && grep -c 'accumulated counter adds the incoming value to the on-disk total with no decision' pkg/result/result_writer_guard_test.go || true
cd /workspace && grep -c 'leaves both on-disk counters verbatim when the incoming payload carries neither key' pkg/result/result_writer_test.go || true
cd /workspace && grep -c 'HaveLen(0)' pkg/result/result_writer_guard_test.go || true
```

Expect the first three to print `1` and the last to print `≥9` (5 today; this prompt adds 4 — a floor of `≥1` can never fail and so is not a check).

Production anchors:

```
cd /workspace && grep -n 'accumulatedCounters' pkg/result/result_writer.go || true
cd /workspace && grep -n 'applyAccumulatedCounters' pkg/result/result_writer.go || true
```

Expect `accumulatedCounters` on the declaration, its doc comment and the loop in `applyAccumulatedCounters`; expect `applyAccumulatedCounters` on its doc comment, its `func` line, and its call inside `MergeFrontmatter` (immediately after the `applyOperatorOwnedFields` call).

Spec AC 8 — the guard lists keep their members (the grep must print `0`):

```
cd /workspace && grep -n 'controllerOwnedFields = \|operatorOwnedFields = ' -A 4 pkg/result/result_writer.go | grep -c 'metrics_' || true
```

Expect `0`. Never make this pass by editing the guard lists.

Spec AC 9 — no new log line, expressed without git (the container has no `.git`). The file's log-line count must be exactly the pre-change baseline `17`, and no other file in `pkg/result/` may carry any:

```
cd /workspace && grep -c 'glog\.' pkg/result/result_writer.go || true
cd /workspace && grep -rcE 'glog\.|log\.|fmt\.Print' pkg/result/*.go | grep -v ':0$' || true
cd /workspace && sed -n '/^func applyAccumulatedCounters/,/^}/p' pkg/result/result_writer.go | wc -l
cd /workspace && sed -n '/^func applyAccumulatedCounters/,/^}/p' pkg/result/result_writer.go | grep -cE 'glog\.|log\.|fmt\.Print' || true
```

The `wc -l` line is the sanity check for the two greps above it: the extracted helper body must be non-empty (expect `≥ 10` lines), so a zero from the following `grep -c` cannot come from a `sed` range that matched nothing. Expect the first grep to print `17` (unchanged — the same count as before this change), the second to print only `pkg/result/result_writer.go:17`, and the third to print `0`. The pattern is deliberately broad (`glog.`, `log.`, `slog.`, `fmt.Print`); if any of these counts grows, you added a log line — remove it. The spec states this check as a host-side diff over `pkg/result/`; the count-equality form above is its in-container equivalent, because the container has no `.git` to diff against.

Nothing outside the two source files changed:

```
cd /workspace && ls pkg/result/
```

Expect the same file list as before, with no new non-test file.

Run ONCE at the end, at repo root:

```
cd /workspace && make precommit
```

Expect exit `0` with the full suite green. If it fails, fix and re-run ONLY the failing target (`make test`, `make check`, `make lint`), then re-run `make precommit` once the individual targets pass. Note that `make precommit` runs `make format` (goimports-reviser, `golines --max-len=100 -w`, `gofmt`) over every non-vendor `.go` file on every run — files being reformatted in place is expected, not a failure. Never make a target green by deleting or weakening an assertion.

</verification>
