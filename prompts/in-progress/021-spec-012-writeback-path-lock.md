---
status: approved
spec: [012-accumulate-agent-counters-on-write-back]
created: "2026-09-14T20:40:02Z"
queued: "2026-09-14T20:57:47Z"
branch: dark-factory/accumulate-agent-counters-on-write-back
---

# Lock the accumulated-counter sum invariant across two write-backs

<summary>

- The accumulation is now proven across two sequential write-backs of the same task, through the real result-write path rather than the merge helper in isolation.
- The first run's emitted turn count lands in the file; the second run's write-back reads that file back and writes the sum, so the on-disk total after two runs is the sum of the two per-run emissions.
- The second run's value is never the written value on its own, and the first run's value is not lost.
- One further lock proves the terminal-`status` pin does not suppress the accumulation: a late result on an already-completed task still moves the counter while the terminal status stays pinned.
- The existing merge behaviours are re-verified as untouched: the guard lists, the terminal pin, the operator-owned rules, the body section merge, the preamble rules and the escalation dedup all keep their current specs, with no assertion deleted or weakened.
- An agent-owned key that is not a counter still takes the incoming value — the existing `phase` fixture stays the negative control.
- No production code changes in this step; the deliverable is proof, not behaviour.
- Adds nothing to the merge: no dedup key, no per-run identity, no idempotency guarantee for redelivered results (that is a separate spec).

</summary>

<objective>

Prove the accumulation through the real write-back path — two sequential `WriteResult` calls against the same task file leave `3` in the bytes written by the first call and `7` in the bytes written by the second (spec AC 11) — and lock the untouched merge behaviours, including the terminal-`status` pin not suppressing the accumulation (spec AC 10, Desired Behavior 6).

</objective>

<context>

This repo has no root `CLAUDE.md`; the global YOLO container CLAUDE.md (already in your context) governs project conventions.

Read the spec: `specs/in-progress/012-accumulate-agent-counters-on-write-back.md` — Goal, Desired Behavior 2-6, Acceptance Criteria (AC 10 and AC 11), Constraints, Failure Modes (especially the redelivery row), and the "Suggested Decomposition" table (this prompt is row 2).

DEPENDENCY — prompt 1 must have shipped before this prompt runs. Verify first:

```
cd /workspace && grep -n 'applyAccumulatedCounters' pkg/result/result_writer.go || true
```

Expect the doc comment, the `func` line, and the call inside `MergeFrontmatter`. If it returns nothing, STOP and report `status: failed` with message `"accumulation not yet deployed (prompt 1)"` — do not write a lock for behaviour that did not ship.

Read these files before writing anything:

- `pkg/result/result_writer_test.go` — the harness and the surrounding style. The relevant pieces:
  - the outer `BeforeEach`: `tmpDir`, `taskDir = "tasks"`, `fakeGit *mocks.GitClient` (whose `ListFilesStub` globs the real filesystem, `ReadFileStub` reads from disk, and `AtomicReadModifyWriteAndCommitPushStub` reads `absPath`, calls `modify`, and writes the returned bytes back to `absPath`), `fakeTime`, `identifier = lib.TaskIdentifier("test-task-uuid-1234")`, `writer = result.NewResultWriter(...)`.
  - `writeTaskFile := func(name, content string) string` — writes the on-disk fixture.
  - `Context("field ownership guard")` — prompt 1 appended the accumulation table and the absent-incoming spec at its end; the existing negative control `It("still lets the agent own phase, status, and new keys on a non-terminal task (negative control)")` — the last of the pre-existing specs in that block — stays exactly as written; prompt 1's additions now follow it.
  - The `DescribeTable("a terminal on-disk status is pinned and the incoming status is discarded", ...)` table — the pin's existing coverage.
- `pkg/result/result_writer.go` — `MergeFrontmatter` and `applyAccumulatedCounters` (prompt 1), the terminal pin block, and `applyRetryCounter`'s terminal early return. Read them to confirm what "the pin does not suppress the accumulation" means in code: the pin only rewrites `status`, and the accumulation helper runs after it, so a terminal on-disk status does not stop the counters from moving.
- `docs/controller-design.md` § "Frontmatter Merge" — the doctrine whose existing rows (controller-owned, terminal pin, operator-owned, agent-owned) these specs lock. Prompt 3 adds the accumulated-counter row; this prompt locks the rows around it.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` (Ginkgo/Gomega style, coverage) and `/home/node/.claude/plugins/marketplaces/coding/docs/definition-of-done.md`.

Environment facts that shape this prompt:

- The execution container's `.git` is masked (the daemon runs with `hideGit=true`; `git status` fails with `fatal: not a git repository`). Do NOT run any `git` command — a failed git command in `<verification>` reads as a pass. Spec AC 10's `git diff -U0 <test files> | grep '^-[^-]' | grep -c 'Expect('` = 0 check is a host-side evaluation performed by the auditor; the in-container proxy is the per-file `Expect(` floor counts plus the full suite staying green.
- Do NOT run `kubectl*`, `docker`, `make build`, or any deploy command — the spec's Post-Deploy ACs (15, 16) are operator-side.

</context>

<requirements>

All changes are in `pkg/result/result_writer_test.go`. This prompt adds specs only — no production code changes, and no modification to any existing block, fixture, or `Expect(` line.

1. **Add the two-run sum-invariant spec (spec AC 11).** Insert a new `Context("accumulation across runs (spec 012)")` inside `Describe("WriteResult")`, between the `Context("field ownership guard")` block and the `Context("interleaved partial update between read and write (race-fix regression)")` block. It drives two sequential `WriteResult` calls through the real modify path, reading the fixture file after each:

   ```go
   Context("accumulation across runs (spec 012)", func() {
       It("accumulates the second run's emission onto the first run's total", func() {
           writeTaskFile(
               "my-task.md",
               "---\ntask_identifier: test-task-uuid-1234\nstatus: in_progress\nphase: ai_review\n---\n## Result\nStatus: failed\n",
           )

           // First run: the payload emits 3 turns onto a task that never carried the key.
           Expect(writer.WriteResult(ctx, lib.Task{
               TaskIdentifier: identifier,
               Frontmatter: lib.TaskFrontmatter{
                   "task_identifier":     "test-task-uuid-1234",
                   "status":              "in_progress",
                   "phase":               "ai_review",
                   "metrics_agent_turns": float64(3),
               },
               Content: lib.TaskContent("## Result\nfirst run\n"),
           })).To(Succeed())

           afterFirst, err := os.ReadFile(filepath.Join(tmpDir, taskDir, "my-task.md"))
           Expect(err).NotTo(HaveOccurred())
           Expect(string(afterFirst)).To(ContainSubstring("metrics_agent_turns: 3"))

           // Second run: the payload emits 4 turns; the write-back reads the file the
           // first call wrote and accumulates onto it.
           Expect(writer.WriteResult(ctx, lib.Task{
               TaskIdentifier: identifier,
               Frontmatter: lib.TaskFrontmatter{
                   "task_identifier":     "test-task-uuid-1234",
                   "status":              "in_progress",
                   "phase":               "ai_review",
                   "metrics_agent_turns": float64(4),
               },
               Content: lib.TaskContent("## Result\nsecond run\n"),
           })).To(Succeed())

           afterSecond, err := os.ReadFile(filepath.Join(tmpDir, taskDir, "my-task.md"))
           Expect(err).NotTo(HaveOccurred())
           Expect(string(afterSecond)).To(ContainSubstring("metrics_agent_turns: 7"))
           Expect(string(afterSecond)).NotTo(ContainSubstring("metrics_agent_turns: 4"))
           Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(2))
       })
   })
   ```

   The invariant: after the first call the file carries `3` (the emitted value, no on-disk key existed); after the second call it carries `7` — the sum — never the second run's `4` alone. Two real write-backs must have happened, which is what `AtomicReadModifyWriteAndCommitPushCallCount()` proves; the stub writes the modified bytes back to disk, so the second call genuinely reads the first call's output.

2. **Add the terminal-pin lock (spec Desired Behavior 6).** In the same new `Context`, add one spec proving the terminal-`status` pin does not suppress the accumulation — the pin pins `status` only, and the counters still move:

   ```go
   It("accumulates the counter on a terminal task while the on-disk status stays pinned", func() {
       writeTaskFile(
           "my-task.md",
           "---\ntask_identifier: test-task-uuid-1234\nstatus: completed\nphase: done\nmetrics_agent_turns: 7\n---\n## Result\nStatus: failed\n",
       )
       Expect(writer.WriteResult(ctx, lib.Task{
           TaskIdentifier: identifier,
           Frontmatter: lib.TaskFrontmatter{
               "task_identifier":     "test-task-uuid-1234",
               "status":              "in_progress",
               "phase":               "ai_review",
               "metrics_agent_turns": float64(3),
           },
           Content: lib.TaskContent("## Result\nlate result\n"),
       })).To(Succeed())

       written, err := os.ReadFile(filepath.Join(tmpDir, taskDir, "my-task.md"))
       Expect(err).NotTo(HaveOccurred())
       s := string(written)
       Expect(s).To(ContainSubstring("metrics_agent_turns: 10"))
       Expect(s).NotTo(ContainSubstring("metrics_agent_turns: 7"))
       Expect(s).To(ContainSubstring("status: completed"))
       Expect(s).NotTo(ContainSubstring("status: in_progress"))
   })
   ```

   The incoming `status: in_progress` is discarded by the terminal pin while the incoming counter value is still accumulated onto the on-disk total — one fixture proves both halves. Do NOT extend this into new behaviour: a terminal task's escalation machinery stays short-circuited exactly as it is today.

3. **Lock the untouched merge behaviours (spec AC 10).** This step adds no code; it is the discipline plus the evidence. Confirm and report:
   - No existing spec or `Expect(` line in any of these five files is deleted, reordered, or weakened: `pkg/result/result_writer_test.go`, `pkg/result/result_writer_guard_test.go`, `pkg/result/result_writer_body_merge_test.go`, `pkg/result/result_writer_escalation_test.go`, `pkg/result/result_writer_unowned_test.go`. Your change is additive: the two new `It` blocks above and nothing else.
   - The existing negative control `It("still lets the agent own phase, status, and new keys on a non-terminal task (negative control)")` is present exactly once and passes unmodified — an agent-owned key that is not a counter still takes the incoming value (`phase: execution` lands).
   - The existing guard-decision ordering specs (counter decision first, operator-owned decision second) and the controller-owned counter / terminal pin / operator-owned / body merge / preamble / escalation dedup specs all pass unmodified under `make test`.
   - Record `grep -c 'Expect('` for each of the five files BEFORE your first edit. After the change each count must be ≥ its recorded start value plus the number of `Expect(` sites this prompt adds (15 in `result_writer_test.go`, 0 in the other four). A fixed floor is too loose to be a lock: prompt 1 also raises `result_writer_test.go`, so a static baseline would still clear with roughly 20 assertions deleted. This is the in-container proxy for the spec's host-side deleted-line count over the same five files.

4. **Do NOT add deduplication, per-run identity, or idempotency machinery.** The spec names exactly-once accumulation under Kafka redelivery as a Non-goal and records the double-add in Failure Modes: a redelivered result is indistinguishable from a second run and adds its value twice, and building a dedup key is a separate spec. Do not add a run-id check, a timestamp guard, a clamp, or a retry-detection heuristic — and do not write a spec that asserts a redelivered result is idempotent.

5. **Self-check before finishing.** Re-run `<verification>`; walk spec AC 10 and AC 11 against the change; confirm the two new specs pass, that the `Expect(` floors hold, and that no existing spec was edited. Confirm this prompt changed no production file (record `shasum -a 256 pkg/result/result_writer.go` before your first edit and re-run it after `make precommit`; the two hashes must match — only `pkg/result/result_writer_test.go` may differ).

</requirements>

<constraints>

- **Test-only prompt.** No production code changes. `pkg/result/result_writer.go` is untouched by this prompt; the only file you edit is `pkg/result/result_writer_test.go`, and only additively.
- **Both counter keys accumulate, and only these two** (spec Desired Behavior 1): `metrics_agent_turns` and `metrics_interaction_count` — exactly these spellings, a frozen cross-repo contract with the agent-side emitter.
- **The guard lists are frozen** at `controllerOwnedFields = {trigger_count, retry_count}` and `operatorOwnedFields = {assignee, previous_assignee}`, with the terminal-`status` pin unchanged. Neither counter key may be added to either list — that guard discards rather than accumulates.
- **Nothing else in the merge moves** (spec Desired Behavior 6): the guard lists keep exactly their current members; the terminal-`status` pin keeps pinning `status` without suppressing the accumulation; the body section merge, the preamble rules and the escalation-section dedup are untouched; every non-counter key still takes the incoming value on conflict.
- **No new knob, no new log line, no new metric, no new config field, no new flag** (spec Constraints, invariant). This prompt adds none.
- **Exactly-once accumulation under Kafka redelivery is a Non-goal**: a re-delivered result adds its value twice, and the existing `scenarios/002-result-writeback-idempotency.md` covers counter-free payloads only. Do not add dedup machinery and do not assert redelivery idempotency.
- **`frontmatterValueEqual` remains the comparison mechanism** — never `==` on two `any` values. This prompt adds no comparison logic at all; it must not introduce one.
- **The merge keeps exactly one call site** — the result write-back path. No new call site, no new exported entry point.
- **No clamping or bounds-checking** of the counters.
- Do NOT commit — dark-factory handles git.
- Do NOT run any `git` command — the container's `.git` is masked; the spec's `git diff` regression-lock grep is host-side, and a failed git command in `<verification>` would read as a pass.
- Do NOT run `kubectl*`, `docker`, `make build`, `make buca`, or any operator/deploy command.
- Do NOT use `PIt`, `XIt`, `FIt`, `Skip(`, or `Pending` anywhere. Keep each `It` closure under 80 lines so `funlen` stays green.
- Do NOT run `go mod vendor`, and never write `-mod=vendor` in a verification command — use `-mod=mod`.

</constraints>

<verification>

Fast loop while implementing (repo root):

```
cd /workspace && go test -mod=mod -count=1 ./pkg/result/...
cd /workspace && make test
```

Expect SUCCESS with 0 Skipped and 0 Pending, including the two new specs and every pre-existing `pkg/result` spec.

The new specs are present and the production file is unchanged by this prompt:

```
cd /workspace && grep -c 'accumulation across runs (spec 012)' pkg/result/result_writer_test.go || true
cd /workspace && grep -c 'accumulates the counter on a terminal task while the on-disk status stays pinned' pkg/result/result_writer_test.go || true
cd /workspace && grep -c 'metrics_agent_turns: 7' pkg/result/result_writer_test.go || true
```

Expect `1`, `1` and `≥1`.

Spec AC 10 — the regression lock, expressed without git. Each `grep -c` is wrapped in `|| true` because `grep -c` exits 1 on a zero count. The floors are MEASURED, not fixed — record each count before your first edit; the numbers must not fall below the recorded start plus this prompt's additions, and a fall means an assertion was deleted:

```
cd /workspace && grep -c 'Expect(' pkg/result/result_writer_test.go || true
cd /workspace && grep -c 'Expect(' pkg/result/result_writer_guard_test.go || true
cd /workspace && grep -c 'Expect(' pkg/result/result_writer_body_merge_test.go || true
cd /workspace && grep -c 'Expect(' pkg/result/result_writer_escalation_test.go || true
cd /workspace && grep -c 'Expect(' pkg/result/result_writer_unowned_test.go || true
```

Expect each count to be ≥ the value you recorded for that file before your first edit, plus the number of `Expect(` sites this prompt adds (`15` for `result_writer_test.go`, `0` for the other four). For orientation only — not as the floor — the pre-prompt-1 baselines measured today are `340`, `42`, `31`, `24`, `9`, and prompt 1 raises the first of them. The spec states this lock as a host-side deleted-line count over the five test files; that evaluation runs at audit time, and these measured floors plus the green suite are the in-container evidence.

The agent-owned non-counter control is intact:

```
cd /workspace && grep -c 'still lets the agent own phase, status, and new keys on a non-terminal task (negative control)' pkg/result/result_writer_test.go || true
```

Expect `1` — the spec must exist exactly once and pass unmodified.

No new skipped or pending specs, and no new log lines anywhere in the package:

```
cd /workspace && grep -c 'PIt(\|XIt(\|FIt(\|Skip(\|Pending(' pkg/result/result_writer_test.go || true
cd /workspace && grep -rcE 'glog\.|log\.|fmt\.Print' pkg/result/*.go | grep -v ':0$' || true
```

Expect `0` for the first, and only `pkg/result/result_writer.go:17` for the second (the log-line count is unchanged from before this spec).

Run ONCE at the end, at repo root:

```
cd /workspace && make precommit
```

Expect exit `0` with the full suite green. If it fails, fix and re-run ONLY the failing target (`make test`, `make check`, `make lint`), then re-run `make precommit` once the individual targets pass. `make precommit` runs `make format` over every non-vendor `.go` file on every run — files being reformatted in place is expected, not a failure. Never make a target green by deleting or weakening an assertion.

</verification>
