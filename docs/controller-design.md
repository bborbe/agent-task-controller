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
  └── deleted file → publish agent-task-v1-event (deleted)

> The scanner increments `agent_controller_vault_scanner_skipped_files_total{reason=<closed enum>}` at every skip site (broken frontmatter, unreadable file, empty status, injection failure, unresolvable duplicate frontmatter, auto-inject disabled, repair did not converge). The counter is pre-initialised at zero for every reason label so dashboards see the whole closed set before the first skip. Operators alert on `rate(agent_controller_vault_scanner_skipped_files_total{reason!="repair_not_converging"}[5m]) > 0`; a positive rate means a broken file is currently in the vault and is not being scanned. The `repair_not_converging` label is an edge rather than a level — the scanner's content-hash short-circuit means it fires once per distinct file content — so it is alerted on separately with `increase(agent_controller_vault_scanner_skipped_files_total{reason="repair_not_converging"}[6h]) > 0`.

### 2. Command Processing (Kafka → git)

```
On agent-task-v1-request (operation: "update"):
  │
  ├── deserialize lib.Task from command payload
  ├── validate: TaskIdentifier and Content must be non-empty
  │
  ├── walk task directory, find file matching task_identifier in frontmatter
  ├── merge frontmatter + apply escalation check (counter set by executor, not incremented here)
  │     ├── read retry_count from merged frontmatter (set by executor at spawn time, spec 011)
  │     ├── if trigger_count >= max_triggers → clear assignee: "", preserve lifecycle phase, append ## Trigger Cap Escalation (once)
  │     ├── if retry_count >= max_retries   → clear assignee: "", preserve lifecycle phase, append ## Retry Escalation (once)
  │     └── if agent emits needs_input → clear assignee: "" (phase unchanged; spec-039 supersedes spec-021 for this row)
  ├── sanitize content (escape bare --- lines to prevent YAML corruption)
  ├── write frontmatter + content to file
  ├── git add + commit + push
  └── CQRS framework publishes success/failure result to agent-task-v1-result
```

The controller reads a required `VAULT_NAME` env var (CLI flag `--vault-name`) at startup naming the single Obsidian vault it serves. Every CreateCommand is checked against `VAULT_NAME` via the `pkg/routing.ShouldProcess` predicate: the effective target is `cmd.targetVault` if non-empty, otherwise the legacy fallback `openclaw`; commands whose effective target is not `VAULT_NAME` are skipped without side effects (no git write, no result publish, no error) and emit a single `glog.V(2)` line naming the command's `targetVault`, the effective target, and `VAULT_NAME` so operators can confirm routing decisions. Two controllers (e.g. one per vault) can therefore share the `agent-task-v1-request` topic without duplicating task materializations. The `targetVault` field is added to `task.CreateCommand` with `omitempty`; legacy producers that emit no `targetVault` continue to flow to the `openclaw` controller.

The frontmatter commands (`update-frontmatter`, `increment-frontmatter`, `complete-task`) carry the same `targetVault` (lib v0.86.0) and are guarded by `pkg/routing.ShouldProcessFrontmatterCommand` with the **result semantics, not the create semantics**: an empty `targetVault` falls through to `true` — every controller attempts the command, the owning vault finds the file and heals it, the non-owning vault drops it; a non-empty `targetVault` mismatch is skipped. The routing guard runs **before** the task-file lookup, so a cross-vault command performs no vault scan, increments no metric, writes nothing, and publishes no result event — it returns an error wrapping `cdb.ErrCommandObjectSkipped` (a nil return with `SendResultEnabled` would publish a spurious Success event on the shared result topic). The empty-`targetVault` fall-through is deliberately NOT defaulted to `openclaw`: defaulting would route legacy personal-vault tasks to the wrong controller permanently. The create path's `ShouldProcess` legacy default (`openclaw`) is untouched.

**Heal-on-write.** The heal is the other half of the frontmatter routing rule. Every write path that touches a task file — result write-back (`buildResultModifyFn`), `update-frontmatter`, `increment-frontmatter`, `complete-task`, and both planning-retry-gate writes (`buildRetryModifyFn`, `buildEscalationModifyFn`) — stamps `target_vault` onto the file when the key is absent, recording the writing controller's `VAULT_NAME` in the same write via `result.HealTargetVault`. An existing `target_vault` (any value) is never overridden. A legacy unstamped file therefore falls through to both controllers exactly once: the owner writes and stamps it, after which `ShouldProcessResult` and `ShouldProcessFrontmatterCommand` return `false` for the non-owner forever, permanently stopping the double-scan and the spurious `not_found` counts. The planning-retry-gate writes need the heal inside their own modify functions because the gate runs after the routing predicate in `NewTaskResultExecutor` and returns `handled=true`, so the executor returns before `WriteResult` and the result-path heal never fires for those writes. The supersede prior-file write (`buildSupersedeModifyFn`) intentionally does not heal: prior files are written to terminal `aborted` status and create commands already route through `ShouldProcess` into the owning vault.

**Build-fix supersede (spec 014).** A failed-build task is re-emitted whenever a build is re-classified or its log fetch recovers, and the re-emission carries two markers: `supersedes_task_id` names the `task_identifier` of the task it replaces, and `supersedes_build_id` names the build. After `writeTaskFile`, the create callback calls `retireSupersededBuildFixTasks`, which lists `tasks/*.md`, excludes the task just created, and retires every still-live candidate whose parsed `task_identifier` equals `supersedes_task_id` or whose parsed `build_id` equals `supersedes_build_id`; matching is always on the parsed value, never by joining a marker into a path, and an absent or empty marker matches nothing. A retired task carries the same frozen four-field transition the recurring supersede writes (`status: aborted`, `phase: done`, `completed_date`, and `superseded_by` naming the created task's relPath) and deliberately does not carry `created_by`, which names the recurring publisher and would be false here. The retirement is best-effort: a list, read, parse or write failure on any single file is logged and swallowed, the already-created task is never rolled back, and no retry loop is added — the handler's context deadline is the bound. Every line it emits, and the commit message it passes to `AtomicReadModifyWriteAndCommitPush`, carries the frozen prefix `auto-supersede build-fix:`, so an operator can separate this mechanism's record from the recurring mechanism's `auto-supersede:` in the pod log. The message is deliberately not described as readable after the fact: `gitRestGitClientAdapter.AtomicReadModifyWriteAndCommitPush` uses it only for a `glog.V(3)` line and `gitRestClient.Post` sends path and content only, so it never reaches git; the operator-facing record is the log lines — the four failures at `glog.Warningf` and the success line at `glog.V(2)`, both emitted at the deployed `-v=2`. The prefix is on the message as well because that is the mechanically assertable half.

## Frontmatter Merge

When writing a result back, the ResultWriter merges frontmatter from the existing task file with frontmatter provided by the agent. Existing keys are preserved and agent keys override on conflict — but only for agent-owned keys. This ensures fields like `assignee`, `tags`, and `task_identifier` survive result writeback even though agents don't receive frontmatter, while two field classes stay under controller control regardless of what the agent publishes.

The agent's payload is built from the `TASK_CONTENT` snapshot injected at spawn, so it always describes the task as it looked *before* the run. Without an ownership rule a stale snapshot silently rolls back anything the controller changed in the meantime.

| Ownership | Fields | Rule |
|---|---|---|
| Controller-owned | `trigger_count`, `retry_count` | The on-disk value always wins. An incoming value can never introduce a controller-owned key that is absent on disk. |
| Controller-owned (terminal pin) | `status` | A terminal on-disk status (`completed` or `aborted`, decided by the normalizing `Status()` accessor) is pinned and the incoming status is discarded. The write is a pin, not a freeze — `phase`, the agent's result fields, and the body still land. |
| Operator-owned | `assignee`, `previous_assignee` | The on-disk value always wins when the key exists on disk. An incoming value may introduce a key absent on disk (a spawn/claim names an assignee on a task that never carried one) — unlike controller-owned counters. Exception for `assignee` only: an incoming empty string is always applied, as the deliverer's deliberate Failed/needs_input clear (spec 039) rather than a stale snapshot, and produces no guard decision or log line. |
| Accumulated counters | `metrics_agent_turns`, `metrics_interaction_count` | The written value is the sum of the on-disk value and the incoming value when both carry a number — `int`, `int64` and `float64` are compared and added by value, so a JSON-decoded incoming `float64` adds correctly to a YAML-decoded on-disk `int`. An incoming key absent from the payload leaves the on-disk value untouched (an emitted `0` adds nothing), and an absent on-disk key takes the incoming value as the starting total. A non-numeric value on either side keeps the on-disk value verbatim and is reported as a guard decision when the two values differ. These keys are neither controller-owned (that guard discards the emitted value, so the counter would never move) nor operator-owned; adding either key to a guard list is the failure this row exists to prevent. |
| Agent-owned | everything else | Incoming value wins on conflict (unchanged). |

```
Existing file:  {status: aborted, trigger_count: 5, phase: ai_review}
Agent provides: {status: in_progress, trigger_count: 1, phase: execution}
Merged result:  {status: aborted, trigger_count: 5, phase: execution}
```

The terminal `status` is pinned and the controller-owned counter keeps its on-disk value, while the agent-owned `phase` still lands.

**Accumulated counters.** `metrics_agent_turns` and `metrics_interaction_count` are cumulative: the agent's payload carries one run's contribution while the task file carries the lifetime total, so the merge adds rather than overwrites. After N runs the on-disk total is the sum of the N per-run emitted values, and an emitted `0` — an evidenced unattended run — adds nothing, so it can never wipe a total an earlier run built up. Accumulation is a transform, not a discard: a clean sum produces no guard decision and no log line, and for these two keys the `ownership guard kept on-disk` line appears only on the non-numeric branch, where the on-disk value is genuinely kept and an incoming value is rejected. The two key names are a frozen cross-repo contract with the agent-side emitter — the merge knows exactly these spellings, so a renamed emitter key silently stops accumulating instead of erroring.

**Body merge.** The body is merged by heading rather than replaced wholesale: an on-disk heading absent from the incoming body is preserved in place with its content, so an operator `## Parked` section recording a park reason and resume options survives the write; a heading present in both bodies is replaced in place by the incoming content, so the agent's fresh `## Result` lands; and a heading present only in the incoming body is appended after the last on-disk section. The preamble follows the same rule: text before the first `## ` heading is preserved from the on-disk file only when the incoming body starts with a heading (no preamble); an incoming body that carries its own preamble replaces the on-disk preamble; and a body with no `## ` heading at all is preamble-only on both sides, so an incoming preamble-only body still replaces an on-disk preamble-only body. A bare `---` line is never treated as a heading and is preserved unescaped, and both `\n` and `\r\n` line endings are tolerated. Escalation sections still append exactly once: an on-disk `## Trigger Cap Escalation` or `## Retry Escalation` section survives the merge, so the dedup check still sees it and does not append a duplicate.

**Terminal short-circuit.** A terminal on-disk `status` takes the task out of the escalation machinery uniformly for both terminal statuses: no `## Trigger Cap Escalation` or `## Retry Escalation` section is appended, `assignee` is not cleared, `previous_assignee` is not written, `phase` is not restored by `restoreExistingPhase`, and an inherited `spawn_notification: true` key survives the write. Escalation exists to park a live runaway task, and a task an operator has already ended is not that.

**Guard logging.** When the guard discards an incoming value that differs from the kept on-disk value, the writer emits one unconditional INFO line containing `ownership guard kept on-disk`, naming the task, the field, and both values. Equal values produce no log line, so steady-state publishes stay silent (a JSON-decoded incoming `float64` counter compares equal to a YAML-decoded on-disk `int`, and a status alias that normalizes to the same value is likewise silent).

Comparison uses `frontmatterValueEqual`, never `==`. Frontmatter values are `any` decoded from YAML (on disk) or JSON (incoming), either of which can yield a map or a slice, and `==` on two `any` values holding the same uncomparable dynamic type panics at runtime with `comparing uncomparable type map[string]interface {}`. A panic here would kill the single result-write chokepoint, so the helper compares numerics by value across int/float representations and falls back to `reflect.DeepEqual`, which never panics.

**What the guard does and does not cover.** The ownership guard is applied by `MergeFrontmatter`, which has exactly one call site: the result write-back path in `resultWriter`. The atomic frontmatter commands (`## Atomic Frontmatter Commands`) take a different route — `buildUpdateModifyFn` applies its `Updates` straight onto the on-disk frontmatter — and are therefore *not* subject to the guard.

That separation is deliberate rather than an oversight. The guard exists to reject a stale spawn-time snapshot that would silently roll back concurrent controller writes; it is not meant to freeze the file against the system's own deliberate, explicitly-addressed writes. An `UpdateFrontmatterCommand` naming a field is an intentional write; an agent result payload carrying that field is a side effect of when the job happened to start.

Two mechanisms may therefore legitimately lower a controller-owned counter, and both write to disk outside the guard:

- the scanner's Empty-to-Named Reset (see `## Empty-to-Named Reset (spec 021)`), which writes `trigger_count: 0` / `retry_count: 0` when a task is re-delegated to a named assignee; and
- a **trigger-scope reset**, published by the executor as an `UpdateFrontmatterCommand` when a task's `trigger_scope` (`<phase>:<ref[:8]>`) changes. It writes the new scope and `trigger_count: 1` in one atomic write — 1 rather than 0 because the spawn it precedes is the first attempt in the new scope. This is what lets the executor's trigger cap run by default instead of opt-in: a re-dispatch representing real progress (new phase, or a new commit on the target repo) earns a fresh budget, while repeated attempts at the same phase and ref burn the existing one down.

What remains true in every case is the guard's actual purpose: no *agent result payload* can raise or lower a controller-owned counter, or revive a terminal status.

## Terminal Task Status (create-task dedup)

The vault has exactly two terminal task statuses: `completed` and `aborted`. A task in a terminal status is finished — no longer a live duplicate — so `create-task`'s dedup rule keys on whether the title path is occupied by a *live* task rather than on file existence alone.

`done` is a `phase` value, not a `status`, and is NOT terminal for the dedup rule: a file can carry `phase: done` while its `status` is still live (status/phase merge semantics documented in `## Frontmatter Merge`), so `phase` plays no part in the dedup decision.

`create-task`'s dedup rule is "the title path is occupied by a live task":

- a create command whose title path holds a terminal task (`completed` or `aborted`) frees the slot and materializes a fresh non-terminal task at that path — an in-place overwrite whose prior instance remains recoverable via `git show <sha>~1:<path>`, recorded by a `[agent-task-controller] reopen terminal task <id>` commit and an unconditional `create-task: reopening terminal task` INFO log;
- any non-terminal status, or any existing file whose status cannot be read (absent/empty/unknown status, missing frontmatter delimiters, unparseable YAML), holds the path and the create is dropped with `ErrTaskAlreadyExists`.

## Assignee-Clear on Escalation (spec 021, refined by spec 039, completed by spec 042)

Every escalation path writes `assignee: ""` so the task surfaces in operator inbox.
All four rows route through the single chokepoint `result.ClearAssigneeIfHumanReview`
(for `human_review` paths) or `result.clearAssignee` (for cap paths) in
`task/controller/pkg/result/result_writer.go`:

| Escalation trigger | `phase` written | `assignee` written | Enforcement point |
|---|---|---|---|
| `trigger_count >= max_triggers` | unchanged (lifecycle stage preserved) | `""` | `applyTriggerCap` → `clearAssignee` |
| `retry_count >= max_retries` | unchanged (lifecycle stage preserved) | `""` | `applyRetryCap` → `clearAssignee` |
| Agent emits `Result.NextPhase: human_review` (legitimate handoff) | `human_review` (from `resolveNextPhase`) | `""` | `applyRetryCounter` → `ClearAssigneeIfHumanReview` |
| Agent emits `UpdateFrontmatterCommand` with merged `phase: human_review` (spec 042) | `human_review` | `""` | `buildUpdateModifyFn` → `ClearAssigneeIfHumanReview` |

Once a task is parked (escalation section present, `assignee: ""`), repeated stale agent
result publishes are idempotent: the escalation section is not duplicated, the lifecycle
phase is restored from the on-disk value, and assignee stays empty.

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

**Deeplink encoding (spec 015).** The escalation message's final line is an `obsidian://open?vault=<vault>&file=<path>` link built by `result.vaultDeeplink`, and Obsidian's URI handler resolves `file=` as a vault-relative path. Both query values are percent-encoded, and a space is emitted as `%20` and never as `+`. Form encoding would render a space as `+`, which Obsidian does not decode, so the path it resolved carried literal `+` characters and matched no file — inert for every escalation whose task name contains a space and for every escalation from a vault whose task directory contains one. The rewrite runs on the already-escaped result, so a literal `+` in a task name still round-trips as `%2B` and the path separator still encodes as `%2F`; a name with no spaces emits neither `+` nor `%20`, and a non-ASCII character keeps its percent-encoded UTF-8 bytes. The encoding is not configurable — no env var, config field, CLI flag or allowlist. The V(1) `assignee cleared → notification published for task …` line is extended to carry the emitted deeplink after its existing prefix, so the encoding a deployed controller produces can be read from the pod log rather than inferred from the delivered message.

## Empty-to-Named Reset (spec 021)

When the vault scanner observes a task file whose `assignee` transitions from empty (or absent) to a non-empty agent name, it writes `trigger_count: 0` and `retry_count: 0` back to the file atomically and queues a git commit. This refills the per-attempt budgets for the re-delegated agent without requiring manual counter edits. The reset fires exactly once per empty-to-named transition (named→named and named→empty transitions do not trigger a reset).

## Atomic Frontmatter Commands

In addition to the `"update"` operation (full result write), the controller handles three atomic frontmatter operations on `agent-task-v1-request`. Each is guarded by `pkg/routing.ShouldProcessFrontmatterCommand` — a non-empty `TargetVault` differing from `VAULT_NAME` is skipped before the task-file lookup (one `glog.V(2)` line, no metric increment, no git write, error wrapping `cdb.ErrCommandObjectSkipped`); an empty `TargetVault` falls through (legacy unstamped commands are attempted by every controller, the owner heals, the non-owner drops). Each write path heals `target_vault` when the key is absent (stamping `VAULT_NAME`, never overriding an existing value).

### `"increment-frontmatter"` (IncrementFrontmatterExecutor)

Payload: `lib.IncrementFrontmatterCommand{TaskIdentifier, Field, Delta, TargetVault}`

```
On agent-task-v1-request (operation: "increment-frontmatter"):
  │
  ├── deserialize IncrementFrontmatterCommand
  ├── routing guard: non-empty TargetVault != VAULT_NAME → skip (glog.V(2), no counter, no write, ErrCommandObjectSkipped)
  ├── find task file by task_identifier (WalkDir)
  ├── if not found → log warning, return nil (no error)
  ├── AtomicReadModifyWriteAndCommitPush:
  │     ├── read current file bytes (under mutex)
  │     ├── parse frontmatter, read Field value (default 0 if absent)
  │     ├── newVal = currentVal + Delta
  │     ├── set Field = newVal
  │     ├── cap escalation: if Field == "trigger_count" AND newVal >= max_triggers
  │     │     └── clear assignee in the same write (phase unchanged; spec-039 supersedes spec-021 for this row)
  │     ├── heal-on-write: if target_vault absent → stamp VAULT_NAME (never overrides an existing value)
  │     ├── write updated file (under mutex)
  │     └── git commit + push (under mutex)
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
  ├── if Updates is empty → return nil (no-op, no write)
  ├── find task file by task_identifier (WalkDir)
  ├── if not found → log warning, return nil
  ├── AtomicReadModifyWriteAndCommitPush:
  │     ├── read current file bytes (under mutex)
  │     ├── parse existing frontmatter
  │     ├── merge only the keys in Updates (all other keys unchanged)
  │     ├── if Body section provided → append/replace section in body (spec 016)
  │     ├── if merged phase == "human_review" → result.ClearAssigneeIfHumanReview clears assignee in the same write (spec 042)
  │     ├── heal-on-write: if target_vault absent → stamp VAULT_NAME (never overrides an existing value)
  │     ├── write updated file (under mutex)
  │     └── git commit + push (under mutex)
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

## What the Controller Does NOT Do

- No K8s API calls (task/executor handles job spawning)
- No domain logic (doesn't know what a backtest is)
- No job management (doesn't know about pods)
- No prompt conversion (removed in v0.17.0)
