---
status: completed
spec: [007-bug-writeback-clobbers-operator-edits]
summary: 'Made assignee/previous_assignee operator-owned in MergeFrontmatter (on-disk value wins over stale snapshots, with the empty-assignee deliverer-clear exception) and merged the result body by markdown heading so operator-authored sections like ## Parked survive every write, proven by 16 new unit/guard specs through the full WriteResult path; make precommit exits 0'
execution_id: agent-task-controller-writeback-merge-exec-012-spec-007-operator-owned-assignee-and-body-merge
dark-factory-version: dev
created: "2026-09-03T18:15:22Z"
queued: "2026-09-03T18:23:07Z"
started: "2026-09-03T19:03:53Z"
completed: "2026-09-03T19:16:46Z"
branch: dark-factory/bug-writeback-clobbers-operator-edits
---

# Operator-owned assignee/previous_assignee guard, empty-clear exception, body section-merge, unit specs, carve-outs, PIt rewrite, changelog

<summary>

- An operator park (`assignee: ""` plus a preserved `previous_assignee`) now survives every agent write-back: the on-disk values of `assignee` and `previous_assignee` win over a stale spawn-time snapshot, and the discard is logged through the existing `ownership guard kept on-disk` path.
- An incoming empty `assignee` — the deliverer's deliberate Failed/needs_input clear, not a stale snapshot — is still applied even when the on-disk value is non-empty, and produces no guard decision and no log line (the spec-039 contract is preserved).
- A task that never carried an `assignee` can still be assigned by a spawn/claim: the new operator-owned rule introduces an absent key instead of deleting it (unlike controller-owned counters), so introducibility does not regress.
- The body write is now a section-level merge instead of full replacement: on-disk-only headings like `## Parked` are preserved in place with their content, same-named headings are replaced by the agent's fresh content, and the on-disk preamble survives when the incoming body starts with a heading.
- Heading-less bodies keep today's full-replacement behavior; a bare `---` line is never treated as a heading and stays unescaped; CRLF line endings in the on-disk body are tolerated.
- Escalation sections still append exactly once: an on-disk `## Trigger Cap Escalation` / `## Retry Escalation` section now survives the merge, so the duplicate-escalation guard keeps working and no duplicate is appended.
- Existing tests pass with `Expect(` lines unmodified, with exactly two carve-outs: the already-parked-task fixture gains its realistic `previous_assignee: claude`, and the pending `## Review` spec is un-pended and rewritten to assert the section-merge doctrine.
- New unit specs prove every row of the operator-owned matrix, every body-merge case, the CRLF tolerance, and the guard decisions — all through the full `WriteResult` file-write path.
- The changelog records the fix under `## Unreleased` naming both the assignee ownership guard and the body section merge.

</summary>

<objective>

Make the result-write chokepoint stop clobbering operator edits: `assignee` and `previous_assignee` become operator-owned (the on-disk value always wins, with the deliverer's empty-`assignee` clear as the only exception), and the body is merged by heading so operator-authored sections survive every write-back — proven by unit specs through the existing mock-git harness.

</objective>

<context>

There is no CLAUDE.md in this repo; the global YOLO container CLAUDE.md (already in your context) governs project conventions. Read the repo's own code as the source of truth for style.

Read the coding-plugin docs that apply to this change:
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-glog-guide.md` (unconditional `glog.Infof` vs `glog.V(n).Infof`; the guard log stays unconditional)
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` (Ginkgo/Gomega, `DescribeTable`, coverage ≥80% for changed code)
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/definition-of-done.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md`

Read `pkg/result/result_writer.go` IN FULL (22KB). The functions you touch:

- `func MergeFrontmatter(existing, incoming lib.TaskFrontmatter) (lib.TaskFrontmatter, []GuardDecision)` — the exported merge helper; the ONLY call site is `buildResultModifyFn`. It currently handles controller-owned counters (`controllerOwnedFields`) and the terminal `status` pin. You ADD operator-owned handling to this same function; you do NOT rename it.
- `var controllerOwnedFields = []string{"trigger_count", "retry_count"}` — the package-level declaration whose shape your new `operatorOwnedFields` mirrors. Place the new declaration immediately after it.
- `func (r *resultWriter) buildResultModifyFn(ctx context.Context, req lib.Task) func(current []byte) ([]byte, error)` — the modify callback. It currently emits the guard log lines and calls `body := r.applyRetryCounter(merged, currentOnDisk, string(req.Content))`, and at the end discards the on-disk body with `_ = bodyStr` (the full-replacement semantics). You wire the body merge here.
- `func (r *resultWriter) applyRetryCounter(merged, existing lib.TaskFrontmatter, body string) string` — FROZEN except that it now receives the MERGED body instead of the raw incoming content. It still appends escalation sections, clears assignee via `clearAssignee` / `ClearAssigneeIfHumanReview`, and restores the on-disk phase.
- `func clearAssignee(merged lib.TaskFrontmatter) string`, `func ClearAssigneeIfHumanReview(merged lib.TaskFrontmatter) string`, `func restoreExistingPhase(existing, merged lib.TaskFrontmatter)`, `func containsEscalationSection(body, header string) bool`, `applyTriggerCap`, `applyRetryCap`, `escalationSection`, `triggerEscalationSection` — ALL FROZEN, do not modify.

Verified library facts (grep-checked against `github.com/bborbe/agent@v0.83.0` module source; do not re-derive):
- `type TaskFrontmatter map[string]interface{}` from `github.com/bborbe/agent` (imported as `lib`).
- `func (f TaskFrontmatter) Assignee() TaskAssignee` — returns `TaskAssignee` (a string type); `string(f.Assignee())` yields the raw value, `""` when absent/non-string.
- `func (f TaskFrontmatter) Status() domain.TaskStatus`, `func (f TaskFrontmatter) Phase() *domain.TaskPhase`, `func (f TaskFrontmatter) TriggerCount() int`, `func (f TaskFrontmatter) RetryCount() int`, `func (f TaskFrontmatter) MaxTriggers() int`, `func (f TaskFrontmatter) SpawnNotification() bool` — used only by the frozen functions, not by your new code.
- `func frontmatterValueEqual(a, b any) bool` already exists in this package (it treats int/float64 pairs as equal and falls back to `reflect.DeepEqual`, never panicking). REUSE it for operator-owned value comparison.
- On-disk frontmatter is parsed with `gopkg.in/yaml.v3` into `map[string]interface{}`; incoming values arrive after a JSON round-trip through cqrs. `assignee`/`previous_assignee` are strings on both sides in every realistic fixture, but `frontmatterValueEqual` handles any value type safely.

Read `pkg/result/result_writer_test.go` (1920 lines, package `result_test`, external test package). The harness you reuse:
- `BeforeEach` (line 57): builds `fakeGit *mocks.GitClient` with a real-filesystem `AtomicReadModifyWriteAndCommitPushStub`, `fakeTime`, `identifier`, and `writer = result.NewResultWriter(fakeGit, taskDir, fakeTime, metrics.New(), libtime.NewWaiterDuration())`.
- `writeTaskFile := func(name, content string) string` (line 117) writes an on-disk file; `taskFile = lib.Task{...}` models the agent's incoming payload; assertions read the written file with `ContainSubstring` / `NotTo(ContainSubstring)`.
- `Context("field ownership guard")` (opens line 1537, closes line 1724) — contains the spec-006 `DescribeTable`s and `It`s. Your operator-owned `DescribeTable` and two `It`s go inside it, after `It("still lets the agent own phase, status, and new keys on a non-terminal task (negative control)")` (which closes at line 1723).
- `Context("interleaved partial update between read and write (race-fix regression)")` opens at line 1726 — your new `Context("body section merge")` goes between line 1724 and 1726.
- `Context("Review section preservation")` (opens line 1849) contains the pending `PIt("preserves prior ## Review content when writing a new result")` (line 1852) — carve-out 2.

Read `pkg/result/result_writer_guard_test.go` (115 lines) — the `Describe("MergeFrontmatter", func() { ... })` block of guard-decision specs. You APPEND new `It`s to it (spec AC 5 mirrors the existing `trigger_count` decision specs here).

`DescribeTable` style precedent: the counter table at `result_writer_test.go:1538` (`func(onDiskFM string, incomingFM lib.TaskFrontmatter, present, absent []string)` with `Entry(...)` rows) — mirror that shape.

Spec cross-references to keep straight:
- DB 1: operator-owned with empty-clear exception; discarded differing value → `GuardDecision` + `ownership guard kept on-disk` INFO log naming `field assignee`; empty-clear exception → NO decision, NO log line; absent-on-disk keys may be introduced by the incoming payload (unlike counters).
- DB 2: `clearAssignee` / `ClearAssigneeIfHumanReview` keep signatures, call sites, and post-merge execution inside `applyRetryCounter` — untouched.
- DB 3: body sections merge by heading in `buildResultModifyFn`.
- DB 4: preamble rule (incoming preamble wins when present; otherwise the on-disk preamble is preserved when the incoming body starts with a heading; heading-less bodies on both sides → incoming replaces on-disk).
- DB 5: escalation sections append exactly once (dedup via `containsEscalationSection` on the merged body).
- DB 6: delimiter validation unchanged — `ExtractFrontmatter`/`ExtractBody` errors still refuse the write.

</context>

<requirements>

All changes below are in `pkg/result/result_writer.go`, `pkg/result/result_writer_test.go`, `pkg/result/result_writer_guard_test.go`, and `CHANGELOG.md`. Match the file's existing TAB indentation and `glog`/`errors.Wrapf` idioms.

1. **Add the `operatorOwnedFields` package-level declaration.** Immediately after the existing `controllerOwnedFields` declaration (around line 452), add:
   ```go
   // operatorOwnedFields lists frontmatter keys that are the operator's routing
   // surface: the result writer keeps the on-disk value over a differing incoming
   // snapshot (the agent authors these keys from its spawn-time snapshot, not live
   // state), but — unlike controllerOwnedFields — an incoming value may introduce
   // an operator-owned key that is absent on disk (a spawn/claim names an assignee
   // on a task that never carried one). One exception, for "assignee" only: an
   // incoming empty string is always applied — the deliverer's deliberate
   // Failed/needs_input clear (spec 039), never a stale snapshot — and produces no
   // guard decision.
   var operatorOwnedFields = []string{
       "assignee",
       "previous_assignee",
   }
   ```
   The field names are a FIXED literal list (spec Security: never derived from the incoming payload). The string literal `"assignee"` MUST appear in this declaration.

2. **Extend `MergeFrontmatter` with the operator-owned rule.** Insert the following block AFTER the terminal-status pin block (i.e. after the `if diskStatus, onDisk := existing["status"]; ...` block, immediately before `return merged, decisions`). The rule is additive: the counter loop and the terminal pin are unchanged, and the returned decision list must still contain every counter/status decision exactly as before (spec Constraint: "the `operatorOwnedFields` rule is additive and must not disturb the decision list for counters or status").
   ```go
   // Operator-owned routing fields: assignee and previous_assignee are the
   // operator's routing surface. When the key exists on disk, the on-disk value
   // always wins over a differing incoming snapshot; unlike controller-owned
   // counters, an incoming value may introduce a key that is absent on disk (the
   // base merge above already wrote it). Exception for "assignee" only: an
   // incoming empty string is always applied (the deliverer's deliberate
   // Failed/needs_input clear) and produces no decision.
   for _, field := range operatorOwnedFields {
       diskValue, onDisk := existing[field]
       incomingValue, inIncoming := incoming[field]
       if !onDisk {
           continue // incoming may introduce the key (already merged above)
       }
       if inIncoming && field == "assignee" && incomingValue == "" {
           merged[field] = incomingValue
           continue
       }
       merged[field] = diskValue
       if inIncoming && !frontmatterValueEqual(diskValue, incomingValue) {
           decisions = append(
               decisions,
               GuardDecision{Field: field, Kept: diskValue, Rejected: incomingValue},
           )
       }
   }
   ```
   Semantics to preserve exactly: (a) on-disk `assignee: ""` (park) beats an incoming non-empty name AND produces a decision (so the guard logs `field assignee`); (b) on-disk `previous_assignee` beats a differing incoming value AND produces a decision; (c) an incoming `assignee: ""` against a non-empty on-disk value is applied with NO decision (empty-clear exception — this is what keeps `result_writer_test.go:939` green with unmodified `Expect(` lines); (d) an incoming `previous_assignee: ""` is NOT a clear — the on-disk value wins and a decision is reported (the exception applies to `assignee` only); (e) a key absent on disk is introduced by the incoming payload with no decision.

3. **Add the body-merge helpers.** Add to `pkg/result/result_writer.go` (place them near `buildResultModifyFn`, below `containsEscalationSection` is fine):
   ```go
   // bodySection is one markdown heading and the content that follows it.
   type bodySection struct {
       heading string // the full heading line, e.g. "## Result\n" (line ending included)
       content string // everything after the heading line up to the next heading line or end of body
   }

   // splitBody splits a markdown body into a preamble (text before the first "## "
   // heading line) and an ordered list of sections. A heading line is a line
   // starting with the exact prefix "## " (hash-hash-space). A bare "---" line is
   // never a heading. Both "\n" and "\r\n" line endings are tolerated: a trailing
   // "\r" before "\n" is part of the terminator, so the heading name of the line
   // "## Parked\r\n" is "## Parked". Returns ("", nil) for an empty body.
   func splitBody(body string) (preamble string, sections []bodySection) { ... }

   // mergeBody merges the on-disk body with the incoming body by heading: an
   // on-disk-only heading is preserved in place with its content, a heading
   // present in both is replaced in place by the incoming content, a heading
   // present only in the incoming body is appended after the last on-disk section,
   // and the preamble follows the DB-4 rule (incoming preamble wins when it has
   // text; otherwise the on-disk preamble is preserved). Preserved sections keep
   // their original line endings verbatim.
   func mergeBody(existingBody, incomingBody string) string { ... }
   ```

   Implement `splitBody` and `mergeBody` to satisfy EXACTLY this algorithm:
   - **splitBody:** scan the body line-by-line. A line is a maximal run ending in `\n` (the final line may lack a newline); a trailing `\r` directly before `\n` is part of the line terminator. The first line whose text starts with the prefix `## ` opens the first section; everything before it (including leading blank lines) is the preamble, kept verbatim. Each subsequent line starting with `## ` closes the previous section and opens a new one; every other line (including any `---` line) is ordinary content. Each section records the full heading line (with its own line ending) and the verbatim content up to the next heading line or end of body.
   - **mergeBody:** (1) split both bodies. (2) Build a map from incoming heading NAME → incoming section; two headings match when their names — the heading line with its terminator (`\n`, `\r\n`, or nothing) removed — are byte-identical. (3) Result preamble: if `incomingPreamble` contains any non-whitespace text, use it; else use `existingPreamble`. (4) For each existing section in on-disk order: if the incoming map holds a section with the same heading name, emit the incoming section (heading + content, i.e. the fresh content lands in place); else emit the existing section verbatim (the on-disk-only heading is preserved in place). (5) Append, after all emitted sections and in incoming order, every incoming section whose heading name was NOT present among the existing sections. (6) Return `resultPreamble` + the concatenated sections. Do NOT normalise line endings — each emitted section keeps its original line endings (a preserved on-disk `## Parked\r\n` stays `\r\n`).

4. **Wire the body merge into `buildResultModifyFn`.** Inside the modify callback, replace:
   ```go
   body := r.applyRetryCounter(merged, currentOnDisk, string(req.Content))
   ```
   with:
   ```go
   mergedBody := mergeBody(bodyStr, string(req.Content))
   body := r.applyRetryCounter(merged, currentOnDisk, mergedBody)
   ```
   and DELETE the trailing discard block (currently at the end of the callback):
   ```go
   // Discard the on-disk body — WriteResult fully replaces body with req.Content
   // (post-applyRetryCounter modifications), matching the prior single-write
   // semantics. bodyStr is read above only to validate the file has well-formed
   // delimiters; an extraction error must surface so we do not silently overwrite a
   // corrupted file.
   _ = bodyStr
   ```
   `bodyStr` is now consumed by `mergeBody` (DB 6 still holds: the `ExtractFrontmatter`/`ExtractBody` calls above it remain, so missing/corrupt delimiters still refuse the write via the existing error returns). Keep the guard-log loop, the marshal step, and the final `return []byte("---\n" + string(marshaledFrontmatter) + "---\n" + body), nil` byte-identical. Do NOT change `applyRetryCounter`, `clearAssignee`, `ClearAssigneeIfHumanReview`, `applyTriggerCap`, `applyRetryCap`, `restoreExistingPhase`, `containsEscalationSection`, `escalationSection`, or `triggerEscalationSection`.

5. **Carve-out 1 — the already-parked-task fixture gains `previous_assignee: claude`.** In `pkg/result/result_writer_test.go`, the spec `It("keeps assignee empty and phase unchanged when stale result arrives at already-parked task")` (line 1288) currently writes its on-disk file with:
   ```go
   "---\ntask_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: ai_review\ntrigger_count: 3\nmax_triggers: 3\nassignee: \"\"\n---\n"+existingEscalationBody,
   ```
   Change ONLY that one on-disk fixture string to:
   ```go
   "---\ntask_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: ai_review\ntrigger_count: 3\nmax_triggers: 3\nassignee: \"\"\nprevious_assignee: claude\n---\n"+existingEscalationBody,
   ```
   Do NOT touch any `Expect(` line in that spec. WHY: under the new guard the on-disk `assignee: ""` wins, so `clearAssignee` sees a pre-clear assignee of `""` and no longer writes `previous_assignee`; the spec's `Expect(s).To(ContainSubstring("previous_assignee: claude"))` then only passes because `previous_assignee: claude` is already on disk (a real parked task carries it — the 2026-08-19 evidence) and is preserved by the merge. All five `Expect(` lines in the spec must pass UNMODIFIED. The body merge keeps this spec green: the on-disk `## Trigger Cap Escalation` section survives the merge (replaced in place by the incoming copy), so `containsEscalationSection` sees it and `applyTriggerCap` does not append a duplicate — `strings.Count(s, "## Trigger Cap Escalation")` stays 1 — and `restoreExistingPhase` restores `phase: ai_review`.

6. **Carve-out 2 — un-pend and rewrite the `## Review` PIt.** In `pkg/result/result_writer_test.go`, the block inside `Context("Review section preservation")` (currently `PIt("preserves prior ## Review content when writing a new result", func() { ... })` at line 1852) becomes a passing `It`. Its current `SatisfyAny(Prior review content, ## Outdated by)` assertion encodes the pre-doctrine hope (operator annotations inside a same-named heading surviving); under the adopted section-merge doctrine a same-named heading is REPLACED, so those annotations do not survive (spec Design Decision 2). Keep the on-disk fixture, the incoming payload, the `Expect(err).NotTo(HaveOccurred())`, the `Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(1))`, and the file-read lines byte-identical. Replace only the `PIt(` keyword with `It(` (drop the two `// PIt:` comment lines above it) and replace the final assertion block:
   ```go
   s := string(written)
   // Section-merge doctrine: a same-named ## Review heading is replaced in place
   // by the incoming content, the on-disk preamble (# Body) survives, and the
   // write commits exactly once. Prior review content is superseded by design.
   Expect(s).To(ContainSubstring("# Body"))
   Expect(s).To(ContainSubstring("## Review"))
   Expect(s).To(ContainSubstring("New review content"))
   Expect(s).NotTo(ContainSubstring("Prior review content"))
   ```

7. **Add the new unit specs to `pkg/result/result_writer_test.go`.** Do NOT modify any existing block other than the two carve-outs. Follow the spec's Test-style directive (`DescribeTable` for the operator-owned-field rows; plain `It` blocks for the empty-clear, introducibility, body-merge, preamble, and CRLF cases), reuse the `writeTaskFile` / `lib.Task` / `ContainSubstring` harness, and assert on the WRITTEN FILE BYTES (the full serialize path through `MergeFrontmatter` → body merge → `yaml.Marshal` → write).

   7a. Inside `Context("field ownership guard")`, after `It("still lets the agent own phase, status, and new keys on a non-terminal task (negative control)")`, add this `DescribeTable` (ACs 1 and 4 — on-disk wins for both operator-owned keys; the shape mirrors the counter table at line 1538):
   ```go
   DescribeTable(
       "the on-disk operator-owned field always wins over a stale incoming snapshot",
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
       Entry("keeps the on-disk empty assignee (operator park) over a stale incoming name",
           "task_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: ai_review\nassignee: \"\"\nprevious_assignee: github-update-go-agent\n",
           lib.TaskFrontmatter{
               "task_identifier": "test-task-uuid-1234",
               "status":          "in_progress",
               "phase":           "ai_review",
               "assignee":        "github-update-go-agent",
           },
           []string{"assignee: \"\"", "previous_assignee: github-update-go-agent"},
           []string{"assignee: github-update-go-agent"},
       ),
       Entry("keeps the on-disk previous_assignee over a stale incoming value",
           "task_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: ai_review\nprevious_assignee: A\n",
           lib.TaskFrontmatter{
               "task_identifier":   "test-task-uuid-1234",
               "status":            "in_progress",
               "phase":             "ai_review",
               "previous_assignee": "B",
           },
           []string{"previous_assignee: A"},
           []string{"previous_assignee: B"},
       ),
   )
   ```

   7b. In the same `Context("field ownership guard")`, add the empty-clear `It` (AC 2 — positive + negative file-content assertions). The on-disk file keeps `assignee: claude`; the incoming `assignee: ""` must be applied (deliverer Failed/needs_input clear):
   ```go
   It("applies an incoming empty assignee even when the on-disk assignee is non-empty (deliverer clear)", func() {
       writeTaskFile(
           "my-task.md",
           "---\ntask_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: ai_review\nassignee: claude\n---\n## Result\nStatus: failed\n",
       )
       taskFile = lib.Task{
           TaskIdentifier: identifier,
           Frontmatter: lib.TaskFrontmatter{
               "task_identifier": "test-task-uuid-1234",
               "status":          "in_progress",
               "phase":           "ai_review",
               "assignee":        "",
           },
           Content: lib.TaskContent("## Result\nStatus: failed\n"),
       }
       Expect(writer.WriteResult(ctx, taskFile)).To(Succeed())
       written, _ := os.ReadFile(filepath.Join(tmpDir, taskDir, "my-task.md"))
       s := string(written)
       Expect(s).To(ContainSubstring("assignee: \"\""))
       Expect(s).NotTo(ContainSubstring("\nassignee: claude"))
   })
   ```

   7c. In the same `Context("field ownership guard")`, add the introducibility `It` (AC 3 — the fix is NOT "existing always wins"; the negative check proves an absent-on-disk assignee is introduced):
   ```go
   It("introduces an incoming assignee when the on-disk frontmatter has no assignee key (spawn/claim)", func() {
       writeTaskFile(
           "my-task.md",
           "---\ntask_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: ai_review\n---\n## Result\nStatus: failed\n",
       )
       taskFile = lib.Task{
           TaskIdentifier: identifier,
           Frontmatter: lib.TaskFrontmatter{
               "task_identifier": "test-task-uuid-1234",
               "status":          "in_progress",
               "phase":           "ai_review",
               "assignee":        "backtest-agent",
           },
           Content: lib.TaskContent("## Result\nStatus: failed\n"),
       }
       Expect(writer.WriteResult(ctx, taskFile)).To(Succeed())
       written, _ := os.ReadFile(filepath.Join(tmpDir, taskDir, "my-task.md"))
       s := string(written)
       Expect(s).To(ContainSubstring("assignee: backtest-agent"))
   })
   ```

   7d. Add a new `Context("body section merge")` inside `Describe("WriteResult")`, positioned between where `Context("field ownership guard")` closes (line 1724) and `Context("interleaved partial update between read and write (race-fix regression)")` (line 1726). It contains the following plain `It` blocks:
   - AC 6 — an on-disk-only operator heading survives the merge in place:
     ```go
     It("preserves an on-disk-only heading and its content when the incoming body lacks it (operator ## Parked)", func() {
         writeTaskFile(
             "my-task.md",
             "---\ntask_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: ai_review\n---\n## Parked\n\nOperator park reason, resume options.\n\n## Result\nStatus: failed\n",
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
         Expect(s).To(ContainSubstring("## Parked"))
         Expect(s).To(ContainSubstring("Operator park reason, resume options."))
         Expect(s).To(ContainSubstring("## Result"))
         Expect(s).To(ContainSubstring("Status: failed"))
     })
     ```
   - AC 7 — a same-named heading is replaced in place by the incoming content:
     ```go
     It("replaces a same-named heading in place with the incoming content (fresh ## Result lands)", func() {
         writeTaskFile(
             "my-task.md",
             "---\ntask_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: ai_review\n---\n## Result\nOld content\n",
         )
         taskFile = lib.Task{
             TaskIdentifier: identifier,
             Frontmatter: lib.TaskFrontmatter{
                 "task_identifier": "test-task-uuid-1234",
                 "status":          "in_progress",
                 "phase":           "ai_review",
             },
             Content: lib.TaskContent("## Result\nNew content\n"),
         }
         Expect(writer.WriteResult(ctx, taskFile)).To(Succeed())
         written, _ := os.ReadFile(filepath.Join(tmpDir, taskDir, "my-task.md"))
         s := string(written)
         Expect(s).To(ContainSubstring("New content"))
         Expect(s).NotTo(ContainSubstring("Old content"))
     })
     ```
   - AC 8 — the on-disk preamble survives when the incoming body starts with a heading (note the bare `---` line inside the preamble must survive verbatim):
     ```go
     It("preserves the on-disk preamble when the incoming body starts with a heading", func() {
         writeTaskFile(
             "my-task.md",
             "---\ntask_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: ai_review\n---\nTags: [[Task]]\n\n---\n\ndescription\n\n## Details\n- x\n",
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
         Expect(s).To(ContainSubstring("Tags: [[Task]]"))
         Expect(s).To(ContainSubstring("description"))
         Expect(s).To(ContainSubstring("## Result"))
     })
     ```
   - AC 9 — heading-less bodies on both sides keep today's full-replacement behavior (incoming preamble replaces on-disk preamble):
     ```go
     It("replaces a preamble-only on-disk body with an incoming preamble-only body (no headings)", func() {
         writeTaskFile(
             "my-task.md",
             "---\ntask_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: ai_review\n---\nFirst content\n",
         )
         taskFile = lib.Task{
             TaskIdentifier: identifier,
             Frontmatter: lib.TaskFrontmatter{
                 "task_identifier": "test-task-uuid-1234",
                 "status":          "in_progress",
                 "phase":           "ai_review",
             },
             Content: lib.TaskContent("Second result\n"),
         }
         Expect(writer.WriteResult(ctx, taskFile)).To(Succeed())
         written, _ := os.ReadFile(filepath.Join(tmpDir, taskDir, "my-task.md"))
         s := string(written)
         Expect(s).To(ContainSubstring("Second result\n"))
         Expect(s).NotTo(ContainSubstring("First content"))
     })
     ```
   - Failure-mode row "Task file uses CRLF line endings" — the section split tolerates `\r\n` line endings and preserves the on-disk-only section. The fixture uses `\r\n` INSIDE the body while keeping the `\n---\n` closing frontmatter delimiter (the form `ExtractBody` can actually parse), so the on-disk heading lines carry a trailing `\r`:
     ```go
     It("tolerates CRLF line endings in the on-disk body and preserves an on-disk-only section", func() {
         writeTaskFile(
             "my-task.md",
             "---\ntask_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: ai_review\n---\n## Parked\r\n\r\nOperator park reason.\r\n\r\n## Result\r\nStatus: failed\r\n",
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
         Expect(s).To(ContainSubstring("## Parked"))
         Expect(s).To(ContainSubstring("Operator park reason."))
         Expect(s).To(ContainSubstring("## Result"))
     })
     ```
     (This passes only if `splitBody` strips the trailing `\r` from the heading NAME so `## Parked\r\n` matches the incoming `## Parked`/`## Result` names — the `\r` is part of the terminator, not the name.)

8. **Add the guard-decision specs to `pkg/result/result_writer_guard_test.go`.** Append these `It`s inside the existing `Describe("MergeFrontmatter", func() { ... })` block, after its last existing `It` (AC 5 plus the DB 1 edge cases, mirroring the `trigger_count` decision specs):
   ```go
   It("reports exactly one decision naming assignee with the kept and rejected values when the on-disk assignee differs", func() {
       existing := lib.TaskFrontmatter{"status": "in_progress", "assignee": "claude"}
       incoming := lib.TaskFrontmatter{"status": "in_progress", "assignee": "other"}
       merged, decisions := result.MergeFrontmatter(existing, incoming)
       Expect(decisions).To(HaveLen(1))
       Expect(decisions[0].Field).To(Equal("assignee"))
       Expect(decisions[0].Kept).To(Equal("claude"))
       Expect(decisions[0].Rejected).To(Equal("other"))
       Expect(merged["assignee"]).To(Equal("claude"))
   })

   It("reports zero decisions when the on-disk assignee equals the incoming assignee", func() {
       existing := lib.TaskFrontmatter{"status": "in_progress", "assignee": "claude"}
       incoming := lib.TaskFrontmatter{"status": "in_progress", "assignee": "claude"}
       merged, decisions := result.MergeFrontmatter(existing, incoming)
       Expect(decisions).To(HaveLen(0))
       Expect(merged["assignee"]).To(Equal("claude"))
   })

   It("applies an incoming empty assignee over a non-empty on-disk value without reporting a decision (deliverer clear exception)", func() {
       existing := lib.TaskFrontmatter{"status": "in_progress", "assignee": "claude"}
       incoming := lib.TaskFrontmatter{"status": "in_progress", "assignee": ""}
       merged, decisions := result.MergeFrontmatter(existing, incoming)
       Expect(decisions).To(HaveLen(0))
       Expect(merged["assignee"]).To(Equal(""))
   })

   It("reports zero decisions when an incoming assignee introduces a key absent on disk", func() {
       existing := lib.TaskFrontmatter{"status": "in_progress"}
       incoming := lib.TaskFrontmatter{"status": "in_progress", "assignee": "backtest-agent"}
       merged, decisions := result.MergeFrontmatter(existing, incoming)
       Expect(decisions).To(HaveLen(0))
       Expect(merged["assignee"]).To(Equal("backtest-agent"))
   })

   It("keeps the on-disk previous_assignee over a differing incoming value and reports one decision", func() {
       existing := lib.TaskFrontmatter{"status": "in_progress", "previous_assignee": "A"}
       incoming := lib.TaskFrontmatter{"status": "in_progress", "previous_assignee": "B"}
       merged, decisions := result.MergeFrontmatter(existing, incoming)
       Expect(decisions).To(HaveLen(1))
       Expect(decisions[0].Field).To(Equal("previous_assignee"))
       Expect(decisions[0].Kept).To(Equal("A"))
       Expect(decisions[0].Rejected).To(Equal("B"))
       Expect(merged["previous_assignee"]).To(Equal("A"))
   })

   It("does not treat an empty incoming previous_assignee as a clear — the on-disk value wins and a decision is reported", func() {
       existing := lib.TaskFrontmatter{"status": "in_progress", "previous_assignee": "A"}
       incoming := lib.TaskFrontmatter{"status": "in_progress", "previous_assignee": ""}
       merged, decisions := result.MergeFrontmatter(existing, incoming)
       Expect(decisions).To(HaveLen(1))
       Expect(decisions[0].Field).To(Equal("previous_assignee"))
       Expect(merged["previous_assignee"]).To(Equal("A"))
   })

   It("reports both the counter decision and the operator-owned decision when both differ (additive rule)", func() {
       existing := lib.TaskFrontmatter{"status": "in_progress", "trigger_count": 3, "assignee": "claude"}
       incoming := lib.TaskFrontmatter{"status": "in_progress", "trigger_count": 1, "assignee": "other"}
       merged, decisions := result.MergeFrontmatter(existing, incoming)
       Expect(decisions).To(HaveLen(2))
       Expect(decisions[0].Field).To(Equal("trigger_count"))
       Expect(decisions[1].Field).To(Equal("assignee"))
       Expect(merged["trigger_count"]).To(Equal(3))
       Expect(merged["assignee"]).To(Equal("claude"))
   })
   ```

9. **Add the `CHANGELOG.md` entry (spec AC 15).** Append a NEW `fix:` bullet to the EXISTING `## Unreleased` section (currently the first section, holding one `chore:` bullet), BELOW the existing chore bullet. Do NOT create a second `## Unreleased`, do NOT touch the `# Changelog` preamble or the `## v0.6.6` heading. Per `changelog-guide.md`: one bullet, `fix:` prefix, specific (name the fields and the merge — never `- fix: fix bug`). The bullet MUST name the assignee ownership guard AND the body section merge, and MUST contain the substring `assignee` (spec AC 15's position-aware `awk` check requires it to appear above the first `## v` heading). Example shape:
   ```
   - fix: stop result writes from clobbering operator edits — `assignee`/`previous_assignee` are now operator-owned in the frontmatter merge (the on-disk value always wins over a stale spawn-time snapshot, an incoming value may introduce an absent key, and an incoming empty `assignee` is always honored as the deliverer's Failed/needs_input clear), and the body is merged by section instead of replaced wholesale — an on-disk `## Parked` and other operator-authored headings survive every write, same-named headings are replaced by the fresh incoming content, and the on-disk preamble survives when the incoming body starts with a heading (spec 007)
   ```

10. **Self-check before finishing:** verify each of the new specs above lands in the file, run `make test` after each meaningful change, then run the `<verification>` block. Confirm the spec's Constraints are honored: `pkg/command/task_create_task_executor.go` (the create-task reopen `writeTaskFile`) is FROZEN — do not touch it; do not add any new write site or Kafka operation; do not change `clearAssignee`, `ClearAssigneeIfHumanReview`, `applyTriggerCap`, `applyRetryCap`, `restoreExistingPhase`, `containsEscalationSection`, `applyRetryCounter`, or the escalation-section writers. The Rung-2 / Rung-3 post-deploy ACs (spec ACs 17-18) are operator-side and live in the spec's Verification ladder — do NOT attempt kubectl/cluster steps in this container.

</requirements>

<constraints>

- The change stays inside `pkg/result`: `MergeFrontmatter` (extended, not renamed) plus the new package-level `operatorOwnedFields` declaration (distinct from `controllerOwnedFields`), the body-merge helpers wired into `buildResultModifyFn`, and the test files. No new write site, no new Kafka operation, no command-executor change. `pkg/command/task_create_task_executor.go` is FROZEN — the 2026-08-30 reopen harm is explicitly out of scope for this spec.
- The test suite must pass with `Expect(` lines unmodified, with exactly TWO carve-outs in `pkg/result/result_writer_test.go`: (1) the on-disk fixture string of `It("keeps assignee empty and phase unchanged when stale result arrives at already-parked task")` (line 1288) gains `previous_assignee: claude`; (2) the pending `PIt("preserves prior ## Review content when writing a new result")` (line 1852) is un-pended and its expectations rewritten to the section-merge doctrine. No other spec may lose or weaken an assertion. The existing `It("preserves phase and keeps assignee cleared when deliverer published needs_input (post-spec-039 shape)")` (line 939) and `It("fully replaces content on second call")` (line 221) must pass with unmodified `Expect(` lines. The existing `It("preserves bare --- lines in body without escaping")` (line 344) must stay green — bare `---` lines are never treated as headings and never escaped.
- The guard is unconditional: no config flag, env var, or per-task opt-out. The empty-clear exception applies to `assignee` only, never to `previous_assignee` — the deliverer never clears `previous_assignee`, and `clearAssignee` remains the sole writer of it, post-merge.
- Heading matching is an exact string comparison on the heading NAME (heading line with the line terminator removed). The body merge must tolerate both `\n` and `\r\n` line endings and must NOT treat a bare `---` line as a heading delimiter.
- The terminal `status` pin and the `controllerOwnedFields` counter rules from spec 006 are FROZEN; the `operatorOwnedFields` rule is additive and must not disturb the decision list for counters or status.
- The incoming payload is never a valid re-assignment channel for a task that already has an `assignee` on disk; the empty-clear exception is the only agent-controlled assignee write and it is the deliverer's documented contract, unchanged from today.
- `applyRetryCounter` keeps its existing signature; the MERGED body is passed to it after the body merge, and it continues to append escalation sections and restore the on-disk phase on repeated parks.
- Per `go-error-wrapping-guide.md`: `errors.Wrapf(ctx, ...)` only in `pkg/` — never `fmt.Errorf`, never `context.Background()`. Your new helpers (`splitBody`, `mergeBody`) return plain strings and need no error wrapping; the existing error paths in `buildResultModifyFn` are unchanged.
- Per `go-glog-guide.md`: the guard log stays unconditional `glog.Infof` naming only the task identifier, the field, and the two values — NEVER the body or the full frontmatter (spec Security / Log safety). Your body-merge code adds NO new log lines.
- Per `go-security-linting.md`: the operator-owned field list is a fixed package-level declaration, never derived from the incoming payload; the merge compares parsed YAML values and heading names by exact string comparison and never evaluates agent-supplied strings as paths, commands, or field names.
- Do NOT commit — dark-factory handles git. Do NOT run kubectl/cluster/operator commands — the container has no cluster access; the Rung-2 / Rung-3 post-deploy ACs live in the spec's operator-executable Verification rung.
- The execution container's `.git` is masked (this repo mounts a null device over `.git`) — do NOT attempt git commands, including `git diff`; AC 13's machine-decidable diff greps are evaluated by the auditor on the host at review time. Enforce AC 13 in-container by the Requirement-5/6 discipline (only the two named carve-outs differ) plus the full suite passing.

</constraints>

<verification>

Run iteratively while implementing (fast loop):

```
cd /workspace && make test
```

Frozen-substring / declaration checks (every `grep -c` wrapped in `|| true` because `grep -c` exits 1 on a zero count):

```
cd /workspace && grep -n 'operatorOwnedFields' pkg/result/result_writer.go || true
cd /workspace && grep -n 'mergeBody\|splitBody' pkg/result/result_writer.go || true
cd /workspace && grep -n 'ownership guard kept on-disk' pkg/result/result_writer.go || true
cd /workspace && grep -c 'PIt(' pkg/result/result_writer_test.go || true
cd /workspace && grep -c 'previous_assignee: claude' pkg/result/result_writer_test.go || true
```

Expect: the `operatorOwnedFields` declaration line and its use in `MergeFrontmatter`; the `mergeBody`/`splitBody` declarations and the `mergeBody(` call site in `buildResultModifyFn`; ≥1 `glog.Infof` line containing `ownership guard kept on-disk` (unchanged); `PIt(` count 0 (spec AC 12 — the pending spec is un-pended); the `previous_assignee: claude` grep returns ≥1 (the carve-out-1 fixture and its assertion).

CHANGELOG AC (spec AC 15):

```
cd /workspace && awk '/^## v/{exit} /assignee/{f=1} END{exit !f}' CHANGELOG.md
```

Expect exit 0 (the new `## Unreleased` bullet containing `assignee` appears above the first `## v` heading; it exits 1 today).

Test-suite-integrity note (spec AC 13): the machine-decidable evidence for "no `Expect(` line weakened outside the two carve-out blocks" is a `git diff -U0 pkg/result/result_writer_test.go` evaluation. The execution container's `.git` is masked, so do NOT attempt git here — the auditor runs the diff on the host at review time. In the container, enforce AC 13 by the Requirement-5/6 discipline: only the two named carve-out blocks may differ; every other existing `It(`/`Expect(` is byte-identical; and the full suite passing proves no behavioral regression.

Run ONCE at the end:

```
cd /workspace && make precommit
```

Expected: exit 0 with the full Ginkgo suite green — all new specs pass, both carve-out specs pass, and every pre-existing spec in `pkg/result/result_writer_test.go` passes with unmodified `Expect(` lines.

</verification>
