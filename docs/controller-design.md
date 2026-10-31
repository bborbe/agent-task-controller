# Controller Design (task/controller)

The controller is the single writer to the vault git repo. It has two responsibilities: detecting task changes in git and publishing them to Kafka, and consuming commands from Kafka and writing results back to git. It has no K8s API access.

## Inputs / Outputs

| Direction | Topic | Purpose |
|-----------|-------|---------|
| Produces | `agent-task-v1-event` | Task created or status changed in git |
| Consumes | `agent-task-v1-request` | Update task commands (from agents) |
| Produces | `agent-task-v1-result` | Command processing confirmation (CQRS auto) |

## Core Logic

### 1. Change Detection (git → Kafka)

```
Poll loop:
  │
  ├── Pull() — no-op (git-rest handles pulls internally)
  ├── gitClient.ListFiles(taskDir/*.md) → enumerate task files via HTTP
  ├── sha256-hash each file's content
  ├── compare with previous hashes
  │
  ├── changed file → parse frontmatter + body → publish agent-task-v1-event
  ├── deleted file → publish agent-task-v1-event (deleted)
  └── publish identifier→path index (task_identifier → task file path) for the command consumer

> The scanner increments `agent_controller_vault_scanner_skipped_files_total{reason=<closed enum>}` at every skip site (broken frontmatter, unreadable file, empty status, injection failure, unresolvable duplicate frontmatter, auto-inject disabled, repair did not converge). The counter is pre-initialised at zero for every reason label so dashboards see the whole closed set before the first skip. Operators alert on `rate(agent_controller_vault_scanner_skipped_files_total{reason!="repair_not_converging"}[5m]) > 0`; a positive rate means a broken file is currently in the vault and is not being scanned. The `repair_not_converging` label is an edge rather than a level — the scanner's content-hash short-circuit means it fires once per distinct file content — so it is alerted on separately with `increase(agent_controller_vault_scanner_skipped_files_total{reason="repair_not_converging"}[6h]) > 0`.

**Identifier→path index (spec 016).** Once a cycle's file walk and deleted-file collection have completed, the scanner publishes an in-process `task_identifier` → task-file-path index derived from the same per-file bookkeeping that decides whether a file changed. The map is built whole and swapped under a write lock, so a reader observes either the previous cycle's mapping or the new one — never a partially built map and never one being mutated — and the scanner's own bookkeeping stays single-goroutine and unguarded. The index is replaced wholesale once per cycle, so a deleted or renamed file stops being reported on the next cycle; an entry whose `task_identifier` is empty — the halted-repair shape — is never indexed, because an empty identifier is not a task. Two files sharing one identifier keep both paths, so the ambiguity can be reported rather than silently collapsed. The index holds only files under this controller's own `taskDir`, is in-process only, is never serialized or persisted, and adds no config field, env var, CLI flag or metric.

**Index bootstrap in trigger-only mode.** A cycle is the only publisher of that index, and a controller with a non-positive `POLL_INTERVAL` runs no cycle: the vault is scanned only on a trigger, and nothing in that deployment fires one. The index then stays empty for the pod's whole life and every command pays the full walk — measured on prod `agent-task-controller-openclaw-0` at 0 hits / 606 misses and a median 18.3 s per lookup over 17 h, which serialized the single-threaded consumer and dropped 85 commands as expired. `vaultScanner.Run` therefore calls `buildIndex` once before it starts waiting, in the non-positive-interval branch only: one read-only pass that lists `taskDir/*.md`, reads and parses each file's `task_identifier`, and swaps the finished map in under the same write lock. It is deliberately narrower than `scanFiles` — no `Pull`, no `auto-inject`, no write, no commit, no changed-task publish — so a trigger-only deployment still writes nothing from the scanner and republishes no vault's full task set on restart; it also leaves `hashes` untouched, so a later cycle's change detection is unchanged. A file without a valid identifier is skipped rather than repaired, and a file created after the pass misses once, falls back to the walk, and is then recorded. One `glog.V(2)` line per build names the identifier count, file count, duration and skipped count. Between rebuilds the index is kept current by `Record`: the create-task executor records the path it just wrote (only when the index reports a clean miss for that identifier), and `FindTaskFilePath` records the single file a walk found, so a task this controller creates costs no walk and any other file created after pod start costs at most one — measured 2026-10-25 on openclaw-0 at 155 of 159 walked identifiers created after start, each walked ~14 times, the consumer 60 minutes behind and dropping commands as expired. `Record` never replaces an entry holding two or more paths, so a known duplicate stays an error; a `Record` that lands just before a rebuild swap is lost with the old map and costs at most one later walk.

### 2. Command Processing (Kafka → git)

```
On agent-task-v1-request (operation: "update"):
  │
  ├── deserialize lib.Task from command payload
  ├── validate: TaskIdentifier and Content must be non-empty
  │
  ├── resolve task_identifier → task file path (index hit: one read, no listing; miss: full directory walk as before)
  ├── merge frontmatter + apply escalation check (counter set by executor, not incremented here)
  │     ├── read retry_count from the ON-DISK frontmatter (legacy records only — no deployed component writes the field since its retirement, so the merge is incoming-wins and a stale echo must not suppress an earned escalation)
  │     ├── if trigger_count >= max_triggers → clear assignee: "" unless it is a configured human owner, preserve lifecycle phase, append ## Trigger Cap Escalation (once)
  │     ├── if retry_count >= max_retries   → clear assignee: "" unless it is a configured human owner, preserve lifecycle phase, append ## Retry Escalation (once)
  │     └── if agent emits needs_input → clear assignee: "" (phase unchanged; spec-039 supersedes spec-021 for this row)
  ├── sanitize content (escape bare --- lines to prevent YAML corruption)
  ├── write frontmatter + content to file
  ├── git add + commit + push
  └── CQRS framework publishes success/failure result to agent-task-v1-result
```

The controller reads a required `VAULT_NAME` env var (CLI flag `--vault-name`) at startup naming the single Obsidian vault it serves. Every CreateCommand is checked against `VAULT_NAME` via the `pkg/routing.ShouldProcess` predicate: the effective target is `cmd.targetVault` if non-empty, otherwise the legacy fallback `openclaw`; commands whose effective target is not `VAULT_NAME` are skipped without side effects (no git write, no result publish, no error) and emit a single `glog.V(2)` line naming the command's `targetVault`, the effective target, and `VAULT_NAME` so operators can confirm routing decisions. Two controllers (e.g. one per vault) can therefore share the `agent-task-v1-request` topic without duplicating task materializations. The `targetVault` field is added to `task.CreateCommand` with `omitempty`; legacy producers that emit no `targetVault` continue to flow to the `openclaw` controller.

The frontmatter commands (`update-frontmatter`, `increment-frontmatter`, `complete-task`) carry the same `targetVault` (lib v0.86.0) and are guarded by `pkg/routing.ShouldProcessFrontmatterCommand` with the **result semantics, not the create semantics**: an empty `targetVault` falls through to `true` — every controller attempts the command, the owning vault finds the file and heals it, the non-owning vault drops it; a non-empty `targetVault` mismatch is skipped. The routing guard runs **before** the task-file lookup, so a cross-vault command performs no vault scan, increments no metric, writes nothing, and publishes no result event — it returns an error wrapping `cdb.ErrCommandObjectSkipped` (a nil return with `SendResultEnabled` would publish a spurious Success event on the shared result topic). The empty-`targetVault` fall-through is deliberately NOT defaulted to `openclaw`: defaulting would route legacy personal-vault tasks to the wrong controller permanently. The create path's `ShouldProcess` legacy default (`openclaw`) is untouched.

**Identifier→path index lookup (spec 016).** The task-file lookup consults the scanner's published identifier→path index before walking the task directory: an identifier the index holds resolves to its file with one read and no directory listing, and an identifier the index does not hold — a file the scanner skipped, or one created since the last cycle — falls back to the existing full walk with its 3-attempt retry loop unchanged. A resolver error is returned as an error and never treated as a miss, so an ambiguous identifier cannot be resolved by re-deriving the ambiguity through the walk; a hit whose single read or parse fails falls back to the walk, and so does a hit whose file no longer carries the requested identifier, because the snapshot is rebuilt every cycle and a stale entry lives at most one cycle — the identifier is re-verified against the file's own frontmatter before the hit is accepted, so a path repurposed since the last cycle can never receive another task's result. The lookup's position relative to the vault-routing guard is unchanged — the guard still runs first, so a cross-vault command still performs no lookup at all. A hit emits one `glog.V(2)` line containing `index hit for task`, so a deployed controller's behaviour is readable in the pod log. A miss logs `index miss for task` and each of the three index-hit fallbacks logs its reason, all at `glog.V(2)`. The index adds no config surface: there is no switch that restores the per-command walk.

**Heal-on-write.** The heal is the other half of the frontmatter routing rule. Every write path that touches a task file — result write-back (`buildResultModifyFn`), `update-frontmatter`, `increment-frontmatter`, `complete-task`, and both planning-retry-gate writes (`buildRetryModifyFn`, `buildEscalationModifyFn`) — stamps `target_vault` onto the file when the key is absent, recording the writing controller's `VAULT_NAME` in the same write via `result.HealTargetVault`. An existing `target_vault` (any value) is never overridden. A legacy unstamped file therefore falls through to both controllers exactly once: the owner writes and stamps it, after which `ShouldProcessResult` and `ShouldProcessFrontmatterCommand` return `false` for the non-owner forever, permanently stopping the double-scan and the spurious `not_found` counts. The planning-retry-gate writes need the heal inside their own modify functions because the gate runs after the routing predicate in `NewTaskResultExecutor` and returns `handled=true`, so the executor returns before `WriteResult` and the result-path heal never fires for those writes. The supersede prior-file write (`buildSupersedeModifyFn`) intentionally does not heal: prior files are written to terminal `aborted` status and create commands already route through `ShouldProcess` into the owning vault.

**Build-fix supersede (spec 014).** A failed-build task is re-emitted whenever a build is re-classified or its log fetch recovers, and the re-emission carries two markers: `supersedes_task_id` names the `task_identifier` of the task it replaces, and `supersedes_build_id` names the build. After `writeTaskFile`, the create callback calls `retireSupersededBuildFixTasks`, which lists `tasks/*.md`, excludes the task just created, and retires every still-live candidate whose parsed `task_identifier` equals `supersedes_task_id` or whose parsed `build_id` equals `supersedes_build_id`; matching is always on the parsed value, never by joining a marker into a path, and an absent or empty marker matches nothing. A retired task carries the same frozen four-field transition the recurring supersede writes (`status: aborted`, `phase: done`, `completed_date`, and `superseded_by` naming the created task's relPath) and deliberately does not carry `created_by`, which names the recurring publisher and would be false here. The retirement is best-effort: a list, read, parse or write failure on any single file is logged and swallowed, the already-created task is never rolled back, and no retry loop is added — the handler's context deadline is the bound. Every line it emits, and the commit message it passes to `AtomicReadModifyWriteAndCommitPush`, carries the frozen prefix `auto-supersede build-fix:`, so an operator can separate this mechanism's record from the recurring mechanism's `auto-supersede:` in the pod log. The message is deliberately not described as readable after the fact: `gitRestGitClientAdapter.AtomicReadModifyWriteAndCommitPush` uses it only for log lines (the `glog.V(3)` POST line, and the `glog.V(2)` `gitrest: skip unchanged read-modify-write` line when the modify closure returns the bytes it read, in which case no POST is made) and `gitRestClient.Post` sends path and content only, so it never reaches git; the operator-facing record is the log lines — the four failures at `glog.Warningf` and the success line at `glog.V(2)`, both emitted at the deployed `-v=2`. The prefix is on the message as well because that is the mechanically assertable half.

## Frontmatter Merge

When writing a result back, the ResultWriter merges frontmatter from the existing task file with frontmatter provided by the agent. Existing keys are preserved and agent keys override on conflict — but only for agent-owned keys. This ensures fields like `assignee`, `tags`, and `task_identifier` survive result writeback even though agents don't receive frontmatter, while two field classes stay under controller control regardless of what the agent publishes.

The agent's payload is built from the `TASK_CONTENT` snapshot injected at spawn, so it always describes the task as it looked *before* the run. Without an ownership rule a stale snapshot silently rolls back anything the controller changed in the meantime.

| Ownership | Fields | Rule |
|---|---|---|
| Controller-owned | `trigger_count` | The on-disk value always wins. An incoming value can never introduce a controller-owned key that is absent on disk. `retry_count` was removed from this list when the field was retired: no deployed component writes it any more, so there is nothing for the guard to protect, and an incoming value is now applied like any agent-owned key. The retry cap still reads whatever a legacy record carries. |
| Controller-owned (terminal pin) | `status` | A terminal on-disk status (`completed` or `aborted`, decided by the normalizing `Status()` accessor) is pinned and the incoming status is discarded. The write is a pin, not a freeze — `phase`, the agent's result fields, and the body still land. |
| Operator-owned | `assignee`, `previous_assignee` | The on-disk value always wins when the key exists on disk. An incoming value may introduce a key absent on disk (a spawn/claim names an assignee on a task that never carried one) — unlike controller-owned counters. Two exceptions, both for `assignee` only and both applied rather than discarded, so both produce no guard decision and no log line: an incoming empty string is always applied, as the deliverer's deliberate Failed/needs_input clear (spec 039) rather than a stale snapshot; and an incoming non-empty assignee is applied when the payload carries a reassignment declaration (`assignee_reassigned_by`, consumed once honoured — see below). |
| Operator-pinned | `cluster` | The on-disk value always wins, and an incoming value can never introduce the key when it is absent on disk — an operator who removes `cluster:` from a running task keeps it removed. Every differing discarded incoming value is a guard decision and logged, including when the key is absent on disk. The agent only echoes `cluster` from its spawn-time snapshot; the operator is its only legitimate writer. |
| Accumulated counters | `metrics_agent_turns`, `metrics_interaction_count` | The written value is the sum of the on-disk value and the incoming value when both carry a number — `int`, `int64` and `float64` are compared and added by value, so a JSON-decoded incoming `float64` adds correctly to a YAML-decoded on-disk `int`. An incoming key absent from the payload leaves the on-disk value untouched (an emitted `0` adds nothing), and an absent on-disk key takes the incoming value as the starting total. A non-numeric value on either side keeps the on-disk value verbatim and is reported as a guard decision when the two values differ. These keys are neither controller-owned (that guard discards the emitted value, so the counter would never move) nor operator-owned; adding either key to a guard list is the failure this row exists to prevent. |
| Agent-owned | everything else | Incoming value wins on conflict (unchanged). |

```
Existing file:  {status: aborted, trigger_count: 5, phase: ai_review}
Agent provides: {status: in_progress, trigger_count: 1, phase: execution}
Merged result:  {status: aborted, trigger_count: 5, phase: execution}
```

The terminal `status` is pinned and the controller-owned counter keeps its on-disk value, while the agent-owned `phase` still lands.

**Reassignment declaration.** `assignee_reassigned_by` is the one key that lets an agent change the routing assignee. The guard's premise is that an agent authors `assignee` from its spawn-time snapshot, so a differing incoming value is a stale echo; a deliberate agent-to-agent handoff — the sentry deep-to-fix handoff is the first — writes exactly such a differing value on purpose, and the two cases are indistinguishable from the values alone. The declaration makes the intent explicit rather than inferred. It is **consumed**: deleted from the merged frontmatter once honoured, so it never lands on the task. That is what makes a declaration unambiguous — the task file never carries the key, so an agent's spawn-time snapshot can never echo one, and a declaration in the incoming payload is always this run's deliberate write. Keeping it on disk instead would make a *repeat* handoff indistinguishable from an echo: a task re-delegated to the analyzer and re-analysed would write the value the earlier handoff left behind, and a value-differs test would discard it — re-creating the stranding the key exists to fix, on exactly the re-drive path the parked-task runbooks use. Like the accumulated counters, the spelling is a frozen cross-repo contract with the emitter (the sentry-fix handoff sets it to its own step name), so a renamed emitter key silently reverts every reassignment to a discard — no error, no log line, and the task fails the executor's `task_type` filter somewhere else entirely.

**Accumulated counters.** `metrics_agent_turns` and `metrics_interaction_count` are cumulative: the agent's payload carries one run's contribution while the task file carries the lifetime total, so the merge adds rather than overwrites. After N runs the on-disk total is the sum of the N per-run emitted values, and an emitted `0` — an evidenced unattended run — adds nothing, so it can never wipe a total an earlier run built up. Accumulation is a transform, not a discard: a clean sum produces no guard decision and no log line, and for these two keys the `ownership guard kept on-disk` line appears only on the non-numeric branch, where the on-disk value is genuinely kept and an incoming value is rejected. The two key names are a frozen cross-repo contract with the agent-side emitter — the merge knows exactly these spellings, so a renamed emitter key silently stops accumulating instead of erroring.

**Body merge.** The body is merged by heading rather than replaced wholesale: an on-disk heading absent from the incoming body is preserved in place with its content, so an operator `## Parked` section recording a park reason and resume options survives the write; a heading present in both bodies is replaced in place by the incoming content, so the agent's fresh `## Result` lands; and a heading present only in the incoming body is appended after the last on-disk section. The preamble follows the same rule: text before the first `## ` heading is preserved from the on-disk file only when the incoming body starts with a heading (no preamble); an incoming body that carries its own preamble replaces the on-disk preamble; and a body with no `## ` heading at all is preamble-only on both sides, so an incoming preamble-only body still replaces an on-disk preamble-only body. A bare `---` line is never treated as a heading and is preserved unescaped, and both `\n` and `\r\n` line endings are tolerated. Escalation sections still append exactly once: an on-disk `## Trigger Cap Escalation` or `## Retry Escalation` section survives the merge, so the dedup check still sees it and does not append a duplicate.

**Terminal short-circuit.** A terminal on-disk `status` takes the task out of the escalation machinery uniformly for both terminal statuses: no `## Trigger Cap Escalation` or `## Retry Escalation` section is appended, `assignee` is not cleared, `previous_assignee` is not written, `phase` is not restored by `restoreExistingPhase`, and an inherited `spawn_notification: true` key survives the write. Escalation exists to park a live runaway task, and a task an operator has already ended is not that.

**Guard logging.** When the guard discards an incoming value that differs from the kept on-disk value, the writer emits one unconditional INFO line containing `ownership guard kept on-disk`, naming the task, the field, and both values. Equal values produce no log line, so steady-state publishes stay silent (a JSON-decoded incoming `float64` counter compares equal to a YAML-decoded on-disk `int`, and a status alias that normalizes to the same value is likewise silent).

Comparison uses `result.FrontmatterValueEqual`, never `==`. Frontmatter values are `any` decoded from YAML (on disk) or JSON (incoming), either of which can yield a map or a slice, and `==` on two `any` values holding the same uncomparable dynamic type panics at runtime with `comparing uncomparable type map[string]interface {}`. A panic here would kill the single result-write chokepoint, so the helper compares numerics by value across int/float representations and falls back to `reflect.DeepEqual`, which never panics. It is exported because the typed frontmatter executors need the same comparison: `buildUpdatePatch` diffs the merged copy against the task as read, over every frontmatter key, so a task carrying `tags:` would panic there for exactly the same reason.

**What the guard does and does not cover.** The ownership guard is applied by `MergeFrontmatter`, which has exactly one call site: the result write-back path in `resultWriter`. The typed frontmatter commands (`## Typed Frontmatter Commands`) take a different route — `buildUpdatePatch` PATCHes only the fields the command names, plus the two doctrines — and are therefore *not* subject to the guard.

That separation is deliberate rather than an oversight. The guard exists to reject a stale spawn-time snapshot that would silently roll back concurrent controller writes; it is not meant to freeze the file against the system's own deliberate, explicitly-addressed writes. An `UpdateFrontmatterCommand` naming a field is an intentional write; an agent result payload carrying that field is a side effect of when the job happened to start.

Two mechanisms may therefore legitimately lower a controller-owned counter, and both write to disk outside the guard:

- the scanner's Empty-to-Named Reset (see `## Empty-to-Named Reset (spec 021)`), which writes `trigger_count: 0` when a task is re-delegated to a named assignee; and
- a **trigger-scope reset**, published by the executor as an `UpdateFrontmatterCommand` when a task's `trigger_scope` (`<phase>:<ref[:8]>`) changes. It writes the new scope and `trigger_count: 1` in one atomic write — 1 rather than 0 because the spawn it precedes is the first attempt in the new scope. This is what lets the executor's trigger cap run by default instead of opt-in: a re-dispatch representing real progress (new phase, or a new commit on the target repo) earns a fresh budget, while repeated attempts at the same phase and ref burn the existing one down.

What remains true in every case is the guard's actual purpose: no *agent result payload* can raise or lower a controller-owned counter, or revive a terminal status.

**Single writer for `trigger_count`.** The controller is the only service that sets `trigger_count`; the executor only publishes commands, and the result merge pins the on-disk value. Two write paths carry a controller counter write, and neither can rewrite a field it did not name: the increment executor (`task_increment_frontmatter_executor.go`) and the update executor's trigger-scope write (`task_update_frontmatter_executor.go`) both go through the typed vault API's field-level `PATCH /tasks/{id}`, which replaces only the keys in the request; the scanner's Empty-to-Named Reset (`vault_scanner.go` `writeCounterReset`) still goes through `AtomicReadModifyWriteAndCommitPush`, whose modify closure re-reads the live file and changes only the `trigger_count` field. The reset must never rewrite the whole frontmatter from its scan-time snapshot — doing so raced the executor's increment and produced the frontmatter conflict git-rest quarantined on 2026-10-19 (merge `622e8c24b0`, `trigger_count: 3` vs `0`).

## Terminal Task Status (create-task dedup)

The vault has exactly two terminal task statuses: `completed` and `aborted`. A task in a terminal status is finished — no longer a live duplicate — so `create-task`'s dedup rule keys on whether the title path is occupied by a *live* task rather than on file existence alone.

`done` is a `phase` value, not a `status`, and is NOT terminal for the dedup rule: a file can carry `phase: done` while its `status` is still live (status/phase merge semantics documented in `## Frontmatter Merge`), so `phase` plays no part in the dedup decision.

`create-task`'s dedup rule is "the title path is occupied by a live task":

- a create command whose title path holds a terminal task (`completed` or `aborted`) frees the slot and materializes a fresh non-terminal task at that path — an in-place overwrite whose prior instance remains recoverable via `git show <sha>~1:<path>`, recorded by a `[agent-task-controller] reopen terminal task <id>` commit and an unconditional `create-task: reopening terminal task` INFO log;
- any non-terminal status, or any existing file whose status cannot be read (absent/empty/unknown status, missing frontmatter delimiters, unparseable YAML), holds the path and the create is dropped with `ErrTaskAlreadyExists`.

**Identifier redirect on create.** Before a create is posted, the executor resolves the command's `task_identifier` against the identifier→path index and, on a clean hit, posts the create under the title stem of the file that already carries it. A re-emit of the same deterministic key under a different title — a retitled PR, or an `Update Go` record whose title SHA diverged from its rewritten `ref` — therefore lands on the occupied path and is answered by the server's existing conflict or reopen logic instead of materializing a second file that shares one identity. A miss, a nil resolver and a resolver error all keep the command's own title; the error is logged as an ambiguity and never redirects, because a known-duplicate index entry must not choose a path. The redirect returns a title stem, never a path, and reads the identifier→path index only — never the index's shared identifier/name/slug space — so the vault-api#28 shadowing class stays closed, and the ` - <id[:8]>` disambiguation for two different identifiers colliding on one title is unaffected.

## Assignee-Clear on Escalation (spec 021, refined by spec 039, completed by spec 042)

Every escalation path writes `assignee: ""` so the task surfaces in operator inbox —
**unless the assignee is a configured human owner**, which is retained (see the
exception below). All four rows route through the single chokepoint
`result.ClearAssigneeIfHumanReview` (for `human_review` paths) or
`result.ClearAssigneeUnlessHuman` (for cap paths) in
`task/controller/pkg/result/result_writer.go`:

| Escalation trigger | `phase` written | `assignee` written | Enforcement point |
|---|---|---|---|
| `trigger_count >= max_triggers` | unchanged (lifecycle stage preserved) | `""`, unless the assignee is a configured human owner | `applyTriggerCap` → `ClearAssigneeUnlessHuman` |
| `retry_count >= max_retries` | unchanged (lifecycle stage preserved) | `""`, unless the assignee is a configured human owner | `applyRetryCap` → `ClearAssigneeUnlessHuman` |
| Agent emits `Result.NextPhase: human_review` (legitimate handoff) | `human_review` (from `resolveNextPhase`) | `""`, unless the assignee is a configured human owner | `applyRetryCounter` → `ClearAssigneeIfHumanReview` |
| Agent emits `UpdateFrontmatterCommand` with merged `phase: human_review` (spec 042) | `human_review` | `""`, unless the assignee is a configured human owner | `buildUpdatePatch` → `ClearAssigneeIfHumanReview` |

**The human-owner exception (`HUMAN_ASSIGNEES`).** The two `human_review` rows above
retain the assignee when it is one of the values in `HUMAN_ASSIGNEES` (comma-separated,
parsed once at startup). The doctrine's purpose is that an *agent* must not keep a record
it parked; clearing a human's owner instead removes the record from that human's queue,
which is the failure the routing stamp writes the owner to prevent. The set is
configuration rather than inference because this package cannot tell a human name from an
agent name — every assignee is a free-form string and the controller holds no Config
lister, so the identities an agent answers to (a Config's `spec.assignee`) are not
readable here. An **empty** set retains nothing and reproduces the pre-existing behaviour
exactly; that default is deliberate — no person's name is hard-coded in this repository —
and `main` logs the effective set at startup, warning when it is empty, so an
unconfigured deployment is visible in the log rather than inferred later from records
that lost their owner. ⚠️ Membership is exact string equality: an assignee not in the
set is treated as an agent and cleared, so a typo in `HUMAN_ASSIGNEES` silently removes a
human's record from their queue.

**The cap rows carry the same exception, and they must.** `applyTriggerCap` runs *before*
the `human_review` chokepoint inside one `applyRetryCounter` call and `applyRetryCap`
runs *after* it, so a record that is human-owned **and** at its trigger or retry cap
would lose its owner on that same write — the chokepoint's retention silently undone.
Both now call `clearAssigneeUnlessHuman`, which captures the name for the escalation body
and clears only a non-human assignee. The escalation section is still appended: a cap
escalation parks a runaway *agent* task in the operator inbox, and a record a human
already owns is already in a queue. **The escalation *notification* is not published in
that case** — its trigger is "prior assignee non-empty AND merged assignee empty", so a
retained human owner suppresses it. That is intended, for the same reason the section is
harmless: the record is already in the human's queue, so there is nothing new to surface.

Once a task is parked (escalation section present, `assignee: ""` — or the retained human
owner, which is the exception above), repeated stale agent result publishes are idempotent:
the escalation section is not duplicated, the lifecycle phase is restored from the on-disk
value, and the assignee stays as it was written.

The `phase == "human_review"` assignee-clear guard in `resultWriter.applyRetryCounter`
runs BEFORE the `spawn_notification` early return. This ordering is load-bearing: on
a pr-reviewer agent's first post-spawn write, the merged frontmatter carries
`spawn_notification: true` (inherited from the executor's spawn-time
`UpdateFrontmatterCommand`) AND incoming `phase: human_review` (from
`Result.NextPhase` via `resolveNextPhase`). The guard fires regardless of
`spawn_notification` state — see spec 041 for the 2026-05-25 prod incident reproducer
and prompt 075 for the same reorder pattern applied to `applyTriggerCap` on
2026-04-24.

**Coalescing the repeat escalation ping (spec 013).** The publish is keyed on the underlying PR issue, not on the task file. `resultWriter.publishEscalation` derives the coalescing key `(repo, PR number)` from the task name with the anchored pattern `^PR \S+ \S+ - (\S+) - ([0-9]+) - \S+` — only the owner-qualified repo token and the PR number are captured, because everything after the short SHA (the title slug, the ` - retry-<taskid[:8]>` suffix, the task kind word and the provider word) varies between retries of one PR while those two do not. Inside a **30-minute** window measured on the injected `libtime.CurrentDateTimeGetter` from the last *published* ping for that key, a further escalation of the same key is written, committed and parked exactly as before but publishes nothing, with one `glog.V(1)` line carrying `escalation notification coalesced` and naming the key and the task name. The record holds the time of the last published ping, so a suppressed escalation does not extend the window and a PR that escalates every few minutes is re-announced once per window rather than silenced for as long as it keeps escalating. The check and the record are one atomic step under a mutex, so two concurrent escalations of one key cannot both publish, and the claim is released when the sender rejects the command, so a broker outage is followed by the next escalation for that key publishing normally. Distinct keys never coalesce: the key is repo-qualified, so the same PR number in two repositories, two numbers in one repository and two tasks carrying the same head SHA in different repositories each publish their own notification. A task name the parse does not recognise publishes uncoalesced and creates no window state, so a format change degrades to one ping per file and never to silence. The suppression sits at the publish point rather than in the write because the modify closure re-runs on every git retry and the four escalation rows share one write path that must never be suppressed — the park, the `previous_assignee` write, the escalation section, the message, the metadata and the deeplink are unchanged, and only the repeat ping is dropped.

**Deeplink encoding (spec 015).** The escalation message's final line is an `https://redirect.benjamin-borbe.de/obsidian?vault=<vault>&file=<path>` link built by `result.vaultDeeplink`; the redirector turns it into an `obsidian://open` URI on the tap, and Obsidian's URI handler resolves `file=` as a vault-relative path. The message cannot carry the custom scheme directly: Telegram accepts only `http`, `https` and `tg://` in a `text_link` entity URL and answers any other scheme with `400 … entity URL … is invalid: Unsupported URL protocol`, which fails the whole message rather than degrading the link to plain text. An `https://` URL is auto-linked by the client with no entity at all, so the message carries one and the client follows the redirector's `302` out of the in-app browser into the custom scheme — verified by tapping a throwaway redirect from the phone before the redirector was built. Both query values are percent-encoded, and a space is emitted as `%20` and never as `+`. Form encoding would render a space as `+`, which Obsidian does not decode, so the path it resolved carried literal `+` characters and matched no file — inert for every escalation whose task name contains a space and for every escalation from a vault whose task directory contains one. The rewrite runs on the already-escaped result, so a literal `+` in a task name still round-trips as `%2B` and the path separator still encodes as `%2F`; a name with no spaces emits neither `+` nor `%20`, and a non-ASCII character keeps its percent-encoded UTF-8 bytes. The encoding is not configurable — no env var, config field, CLI flag or allowlist. The V(1) `assignee cleared → notification published for task …` line is extended to carry the emitted deeplink after its existing prefix, so the encoding a deployed controller produces can be read from the pod log rather than inferred from the delivered message.

## Empty-to-Named Reset (spec 021)

When the vault scanner observes a task file whose `assignee` transitions from empty (or absent) to a non-empty agent name, it writes `trigger_count: 0` back to the file atomically and queues a git commit. The write goes through `AtomicReadModifyWriteAndCommitPush`: its modify closure re-reads the live file and changes only the `trigger_count` field, so a concurrent write to any other field survives. (The reset no longer touches `retry_count`, which is retired — a legacy record's counter is left exactly as the file carries it.) (Before 2026-10-19 this was a whole-file write from the scan-time snapshot, which raced the executor's `trigger_count` increment and produced the frontmatter conflict git-rest quarantined — merge `622e8c24b0`, `trigger_count: 3` vs `0`.) This refills the per-attempt budgets for the re-delegated agent without requiring manual counter edits. The reset fires exactly once per empty-to-named transition (named→named and named→empty transitions do not trigger a reset).

## Typed Frontmatter Commands

In addition to the `"update"` operation (full result write), the controller handles three frontmatter operations on `agent-task-v1-request`. Each is guarded by `pkg/routing.ShouldProcessFrontmatterCommand` — a non-empty `TargetVault` differing from `VAULT_NAME` is skipped before the task read (one `glog.V(2)` line, no metric increment, no write, error wrapping `cdb.ErrCommandObjectSkipped`); an empty `TargetVault` falls through (legacy unstamped commands are attempted by every controller, the owner heals, the non-owner drops). The `increment-frontmatter` and `update-frontmatter` executors address the task by `task_identifier` through the typed vault API and write with a field-level `PATCH`, so they hold no clone, resolve no path, and cannot rewrite a frontmatter key they did not name. Each of them heals `target_vault` when the key is absent (stamping `VAULT_NAME`, never overriding an existing value).

### `"increment-frontmatter"` (IncrementFrontmatterExecutor)

Payload: `lib.IncrementFrontmatterCommand{TaskIdentifier, Field, Delta, TargetVault}`

```
On agent-task-v1-request (operation: "increment-frontmatter"):
  │
  ├── deserialize IncrementFrontmatterCommand
  ├── routing guard: non-empty TargetVault != VAULT_NAME → skip (glog.V(2), no counter, no write, ErrCommandObjectSkipped)
  ├── ReadTask(task_identifier) through the typed vault API — the identifier is the address
  ├── if not found → log warning, return nil (no error)
  ├── build the patch from the task as read:
  │     ├── Field = currentValue + Delta (default 0 if absent)
  │     ├── cap escalation: if Field == "trigger_count" AND newVal >= max_triggers
  │     │     └── add assignee: "" to the same patch (phase unchanged; spec-039 supersedes spec-021 for this row)
  │     └── heal-on-write: if target_vault absent → add target_vault: VAULT_NAME (never overrides an existing value)
  ├── PatchTask(task.ID, patch) — one field-level request, no path, no clone, no commit
  └── increment FrontmatterCommandsTotal{operation, outcome}
```

Delta may be negative (decrement). Cap escalation only fires for `trigger_count` reaching `max_triggers`.

### `"update-frontmatter"` (UpdateFrontmatterExecutor)

Payload: `lib.UpdateFrontmatterCommand{TaskIdentifier, Updates map[string]any, Body, TargetVault}`

```
On agent-task-v1-request (operation: "update-frontmatter"):
  │
  ├── deserialize UpdateFrontmatterCommand
  ├── routing guard: non-empty TargetVault != VAULT_NAME → skip (glog.V(2), no counter, no write, ErrCommandObjectSkipped)
  ├── if Updates is empty AND Body is nil → return nil (no-op, no write)
  ├── ReadTask(task_identifier) through the typed vault API — the identifier is the address
  ├── if not found → log warning, return nil
  ├── buildUpdatePatch: merge Updates onto a copy of the task's frontmatter
  │     ├── if merged phase == "human_review" → result.ClearAssigneeIfHumanReview (spec 042)
  │     ├── heal-on-write: if target_vault absent → stamp VAULT_NAME (never overrides an existing value)
  │     └── diff the merged copy against the task as read (result.FrontmatterValueEqual)
  │           → only the changed keys go into the PATCH, so untouched frontmatter is never restated
  ├── PatchTask(task.ID, patch) — skipped entirely when the diff is empty
  ├── if Body section provided → PutBody(task.ID, body) with the section appended/replaced (spec 016)
  │     ⚠️ two requests, not one atomic write: a failure between PATCH and PUT leaves
  │        frontmatter updated and the body stale. The command is redelivered (the
  │        offset is not committed) and both halves are idempotent, so the window self-heals.
  └── increment FrontmatterCommandsTotal{operation, outcome}
```

### `"complete-task"` (CompleteTaskExecutor)

Payload: `lib.CompleteCommand{TaskIdentifier, RecoverySHA, TargetVault}`

```
On agent-task-v1-request (operation: "complete-task"):
  │
  ├── deserialize CompleteCommand
  ├── routing guard: non-empty TargetVault != VAULT_NAME → skip (glog.V(2), no counter, no write, ErrCommandObjectSkipped)
  ├── find task file by task_identifier (WalkDir)
  ├── if not found → log warning, return nil
  ├── AtomicReadModifyWriteAndCommitPush:
  │     ├── read current file bytes (under mutex)
  │     ├── if a # Resolution / ## Resolution section is already present → return nil, nil (idempotent no-op)
  │     ├── set status: completed, phase: done, completed_date/processed_at: now, recovery_sha (when provided)
  │     ├── heal-on-write: if target_vault absent → stamp VAULT_NAME (never overrides an existing value)
  │     ├── append ## Resolution body section (verdict, recovery SHA, closed-at)
  │     ├── write updated file (under mutex)
  │     └── git commit + push (under mutex)
  └── increment FrontmatterCommandsTotal{operation, outcome}
```

The complete executor has no subsection of its own elsewhere in this document; the routing-guard and heal steps above are the same ones applied by the two frontmatter executors.

## Vault Writes via git-rest

The controller holds no local git clone. All vault file operations flow through the
`vault-obsidian-openclaw` git-rest StatefulSet via HTTP:

| Operation | HTTP call | Who commits |
|-----------|-----------|-------------|
| Read file | `GET /api/v1/files/{relPath}` | N/A |
| Write file | `POST /api/v1/files/{relPath}` | git-rest (auto-commit) |
| Delete file | `DELETE /api/v1/files/{relPath}` | git-rest (auto-commit) |
| List files | `GET /api/v1/files/?glob={pattern}` | N/A |

git-rest ensures one commit per write. The controller's `/readiness` endpoint reflects
git-rest readiness: if git-rest returns 503 (push stuck), the controller reports 503
and the Kafka consumer goroutine blocks inside the write retry loop until git-rest
recovers. Kafka offsets are not advanced during this block.

BoltDB (at `/data/bolt` on the `datadir` PVC) continues to track Kafka consumer
offsets — unchanged from the pre-migration architecture.

## Content Sanitization

Agent output may contain bare `---` lines that would corrupt YAML frontmatter boundaries. The ResultWriter escapes these to `\-\-\-` before writing.

## HTTP Endpoints

| Endpoint | Purpose |
|----------|---------|
| `/healthz` | Liveness probe |
| `/readiness` | Readiness probe |
| `/metrics` | Prometheus metrics |
| `/setloglevel` | Temporary log level change (5-min auto-reset) |
| `/trigger` | On-demand vault scan cycle |

## Task Age Metrics

Two gauges describe the non-terminal part of the board: its oldest age and its age distribution, scraped from `/metrics`. Both are **levels**, not counters: the ticker recomputes them and `Set`s the result, so a value falls back to 0 as soon as the condition clears. That is what makes a threshold expression alertable.

| Series | Labels | Meaning |
|---|---|---|
| `agent_controller_task_oldest_age_seconds` | `vault` | Age in whole seconds of the oldest dated non-terminal record; 0 when no dated non-terminal record exists. The alert threshold is 7 days (`604800`), compared as `> 604800`. |
| `agent_controller_task_age_bucket_tasks` | `vault`, `bucket` | Number of non-terminal records per bucket. `bucket` is a closed set of exactly four values. |

`vault` is the controller's own configured `VAULT_NAME`, passed through verbatim; no display name or slug mapping is applied, so the dashboard selector that matches both spellings needs no change. **Non-terminal** is `status` neither `completed` nor `aborted` — an empty or unknown status is non-terminal.

**Age precedence — first parseable value wins:** `created`, then `created_date`, then `queued_at`, then `job_started_at`. A value parses as RFC3339 (with or without fractional seconds) or as date-only `YYYY-MM-DD` read as midnight UTC. A value that is present but unparseable is skipped and the next field is tried. A record with none of the four parseable is **undated** and contributes nothing to the oldest age. A timestamp in the future clamps to an age of 0 and lands in `lt_1d`; no negative value is ever published.

The published age is truncated to whole seconds, and bucket classification uses that same truncated value. A record whose untruncated age is 7 days plus a fraction of a second therefore publishes `604800` and lands in `1d_7d`, not in `gt_7d` — so the value a scrape reads and the bucket it lands in agree at both the 24-hour and the 7-day boundary.

**Buckets** (half-open where it matters):

| `bucket` | Boundary |
|---|---|
| `lt_1d` | age < 24h, including every age clamped to 0 |
| `1d_7d` | 24h ≤ age ≤ 7 days |
| `gt_7d` | age > 7 days |
| `undated` | none of the four fields parseable |

**Alert comparison.** The alert rule compares `agent_controller_task_oldest_age_seconds > 604800`, matching the `gt_7d` bucket and `1d_7d`'s inclusive 7-day upper bound. A `>=` would put a record exactly 7 days old in both `1d_7d` and the alert, double-reporting it.

**Recompute.** A dedicated ticker, independent of `POLL_INTERVAL`, runs one recompute at start-up and then every **60 seconds** when its interval is positive — a package constant in `main.go`, not configuration, because the alert contract is "at most 60 s" and a knob that could widen or disable it would be an opt-out on the shipped behaviour. A non-positive interval disables both the start-up recompute and the ticker, so the series are never published; configuration cannot produce that value, only a caller can. The recompute lists `TASK_DIR/*.md`, reads and parses each file, and `Set`s all five series — the empty buckets at 0 included — so after a successful pass a scrape never sees a missing series. It does not pull, scan, write or commit, and it sends nothing on the results channel. The scan cycle never writes these series (single writer): `scanFiles` is dead on a trigger-only controller, and a second writer would make the value depend on which path ran last.

A file that cannot be read, carries no frontmatter, or carries frontmatter that will not unmarshal — including duplicate keys that will not re-parse — is skipped entirely: it lands in no bucket and never affects the oldest age, so one broken file cannot blank the instrument. A file whose duplicate keys do deduplicate cleanly is not skipped; it is counted once, with the last value winning for each key. A failed listing writes nothing: the values from the previous successful pass stay, and on a pod's first failed recompute no series exists yet.

The dashboard panel and the alert rule live in `bborbe/nuke`; this section is the contract they consume. A change to a name, a label, a bucket boundary, the precedence or the cadence is a cross-repo change and needs its own spec that updates this section in the same change.

## What the Controller Does NOT Do

- No K8s API calls (task/executor handles job spawning)
- No domain logic (doesn't know what a backtest is)
- No job management (doesn't know about pods)
- No prompt conversion (removed in v0.17.0)
