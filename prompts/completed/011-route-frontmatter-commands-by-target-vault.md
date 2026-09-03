---
status: completed
summary: Routed frontmatter commands by target vault and added heal-on-write target_vault stamping across all task-file write paths
execution_id: agent-task-controller-target-vault-echo-exec-011-route-frontmatter-commands-by-target-vault
dark-factory-version: dev
created: "2026-09-03T17:17:20Z"
queued: "2026-09-03T17:17:20Z"
started: "2026-09-03T17:44:00Z"
completed: "2026-09-03T17:55:22Z"
cancelled: "2026-09-03T17:42:55Z"
---
# Route frontmatter commands by target vault and heal legacy files

---
status: draft
---

<summary>
- Frontmatter commands carrying a vault name are skipped by controllers serving a different vault, before any vault lookup
- Commands without a vault name keep the current fall-through behavior, so legacy unstamped tasks keep working
- When the owning controller writes a task file that has no vault stamp, it records its own vault name in the same write
- The result path gains the same heal, so legacy files permanently stop falling through to both controllers
- Routing decisions are logged the same way create-command routing is, and the routing doc stays truthful
</summary>

<objective>
Extend per-vault routing to the frontmatter-command paths (update-frontmatter, increment-frontmatter, complete) and add heal-on-write stamping of target_vault, so the non-owning controller neither scans its vault nor counts not_found for cross-vault traffic, and legacy files created before target_vault stamping permanently stop double-scanning results. This eliminates the false AgentControllerResultNotFound alerts (10 frontmatter-command drops + 2 legacy-file drops observed 2026-09-03 on nuke-prod).
</objective>

<context>
There is no CLAUDE.md in this repo; the container's global CLAUDE.md governs project conventions.

Pattern references (read before writing):
- pkg/routing/routing.go — ShouldProcess (create commands: legacy default openclaw) and ShouldProcessResult (results: absent target_vault falls through true). Frontmatter commands use the RESULT semantics: empty target vault falls through and processes; non-empty mismatch skips.
- pkg/command/task_result_executor.go — the skip precedent: return an error wrapping cdb.ErrCommandObjectSkipped on mismatch (a nil return with SendResultEnabled publishes a spurious Success event on the shared result topic); log the skip at glog.V(2) naming the command's target vault and VAULT_NAME
- pkg/command/task_update_frontmatter_executor.go, pkg/command/task_increment_frontmatter_executor.go, pkg/command/task_complete_task_executor.go — the three executors to guard
- pkg/factory/factory.go around the executor wiring — VaultName is already resolved in main; thread it into the three constructors and into the result writer
- pkg/result/result_writer.go — buildResultModifyFn is where the result write merges frontmatter; the heal helper belongs beside MergeFrontmatter
- docs/controller-design.md "Command Processing" paragraph and "Atomic Frontmatter Commands" section — update to describe the frontmatter-command routing + heal rule
- UpdateFrontmatterCommand / IncrementFrontmatterCommand / CompleteCommand now (or after the next lib release) carry TargetVault

Dependency: requires the github.com/bborbe/agent release exposing TargetVault on the frontmatter commands (go get in requirement 1).
</context>

<requirements>
1. Bump the dependency: `go get github.com/bborbe/agent@v0.86.0` (the release with TargetVault on UpdateFrontmatterCommand/IncrementFrontmatterCommand). Write the importing code in the same change so `go mod tidy` keeps the dep.
2. Add a routing predicate in pkg/routing (e.g. ShouldProcessFrontmatterCommand(targetVault, vaultName string) bool): empty targetVault → true (fall through, matching ShouldProcessResult); non-empty → equal to vaultName. Unit tests: empty/absent falls through; matching passes; mismatch fails.
3. Thread VaultName into NewUpdateFrontmatterExecutor, NewIncrementFrontmatterExecutor, NewCompleteTaskExecutor (pkg/factory/factory.go) and apply the predicate before the task-file lookup in each executor. On mismatch: one glog.V(2) line, increment no counter, write nothing, and return an error wrapping cdb.ErrCommandObjectSkipped — never nil, never a plain error.
4. Add heal-on-write in pkg/result (beside MergeFrontmatter): when the write path's merged frontmatter has no target_vault key, set it to the controller's vault name in the same write; an existing value (any value) is never overridden. Apply it in the result write path (buildResultModifyFn), in the update-frontmatter and increment-frontmatter modify functions, in the complete executor's write (buildCompleteModifyFn), and in BOTH planning-retry-gate write paths (buildRetryModifyFn and buildEscalationModifyFn in pkg/command/planning_retry.go — the gate runs after the routing predicate in NewTaskResultExecutor and returns handled=true, so the executor returns before WriteResult and the result-path heal never fires for these writes). The result writer is constructed in main.go (result.NewResultWriter, not in pkg/factory/factory.go) and the three executors are wired in pkg/factory/factory.go — both need the vault name for this. The supersede prior-file write (buildSupersedeModifyFn in pkg/command/task_create_task_executor.go) intentionally does not heal: prior files are written to terminal status and create commands already route through ShouldProcess into the owning vault.
5. Tests (update the existing constructor call sites in pkg/command/task_update_frontmatter_executor_test.go, task_increment_frontmatter_executor_test.go, task_complete_task_executor_test.go, pkg/result/result_writer_test.go, result_writer_guard_test.go for the new vaultName parameters): (a) each guarded executor skips a mismatched-vault command with zero git writes and zero not_found counter increments, and processes an empty-vault command; (b) heal-on-write stamps target_vault on a file lacking it during result, update, increment, complete, and planning-retry/escalation writes, and the stamped file read back through ShouldProcessResult returns false for a different vaultName (the heal exists so the other controller stops falling through); (c) an existing target_vault value survives every write unchanged.
6. Update docs/controller-design.md: extend the Command Processing routing paragraph (frontmatter commands + heal rule, empty-targetVault fall-through semantics) and the Atomic Frontmatter Commands section (routing guard step) — note that section today documents only increment-frontmatter and update-frontmatter; state the routing-guard + heal step for the complete executor there too, since it has no subsection of its own.
7. Add a CHANGELOG.md entry under `## Unreleased` for the routing guard and heal-on-write — create the `## Unreleased` section directly after the header block if absent (the file currently begins at `## v0.6.6`).
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
- Skip semantics: error wrapping cdb.ErrCommandObjectSkipped, one glog.V(2) line, no metric increment, no git write — a nil return would publish a spurious Success event on the shared result topic
- Heal-on-write never overrides an existing target_vault — stamp only when the key is absent
- The empty-targetVault fall-through is load-bearing for legacy unstamped tasks: both controllers attempt, the owner finds and heals the file, the non-owner drops — do NOT "fix" it by defaulting to openclaw (that would route legacy personal-vault tasks to the wrong controller permanently)
- Never run `go mod vendor`; use `-mod=mod` for any go test command that needs it
</constraints>

<verification>
Run `make precommit`; must pass.
</verification>
