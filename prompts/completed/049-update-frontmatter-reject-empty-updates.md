---
status: completed
summary: update-frontmatter now validates commands via cmd.Validate, rejecting no-update/no-body and empty-identifier commands as failures instead of silent successes, with tests, docs, and changelog updated
execution_id: agent-task-controller-empty-update-exec-049-update-frontmatter-reject-empty-updates
dark-factory-version: v0.198.17
created: "2026-11-08T19:19:27Z"
queued: "2026-11-08T19:19:27Z"
started: "2026-11-08T19:39:35Z"
completed: "2026-11-08T19:45:58Z"
---

# Reject an update-frontmatter command that carries no updates and no body

<summary>
- An update-frontmatter command that carries no field updates and no body section is now reported as a failure instead of a success
- The typical cause is a producer sending the field map under the wrong key, which today decodes to an empty update and silently writes nothing while the result says it succeeded
- A command with no task identifier is also reported as a failure instead of being looked up
- The failure is reported on the normal command result, so the sender sees why its write did not happen
- The command is not retried — a malformed command cannot succeed on a second attempt
- Well-formed commands (field updates only, body only, or both) behave exactly as before, and commands for another vault are still skipped silently
- The failure is counted in the existing update-frontmatter error metric
- The design doc and changelog record the behaviour change
</summary>

<objective>
Make the controller's update-frontmatter executor report a command that fails the command type's own `Validate` (neither `updates` nor `body`, or an empty task identifier) as a failed command — a `success:false` result naming the problem — instead of returning success without writing, so a producer that sends the wrong key (e.g. `frontmatter` instead of `updates`) is told its write never happened.
</objective>

<context>
Read CLAUDE.md if present, and `docs/controller-design.md` — the `"update-frontmatter"` flow diagram.

Read `pkg/command/task_update_frontmatter_executor.go` — `NewUpdateFrontmatterExecutor`. After decoding and the `frontmatterCommandVaultMismatch` check, it has:

```go
// Empty updates with no body section is a no-op — nothing to write.
if len(cmd.Updates) == 0 && cmd.Body == nil {
    return nil, nil, nil
}
```

The executor registers with result sending enabled (`true` second argument). The cqrs result wrapper (`github.com/bborbe/cqrs/cdb`, `NewCommandObjectExecutorTxResultSender`, applied to every executor via `cdb.CreateCommandMessageHandlerBatch` in `pkg/factory/factory.go`) behaves as follows:
- handler returns `nil` error → a SUCCESS result is published (this is the bug: the no-op reports success)
- handler returns an error wrapping `cdb.ErrCommandObjectSkipped` → NO result is published at all (silent — must NOT be used here)
- handler returns any other error → a FAILURE result (`success:false`, message = error text) is published, and the wrapper returns nil, so the command is NOT retried

The command type `task.UpdateFrontmatterCommand` (`github.com/bborbe/agent/command/task`, pinned v0.136.0) has JSON tags `taskIdentifier`, `updates`, `body` (omitempty), `targetVault` (omitempty), and a `Validate(ctx) error` that enforces:
- `TaskIdentifier` non-empty
- at least one of `Updates` (non-empty map) or `Body` (non-nil) set — error message contains `at least one of Updates or Body must be set`
- `TargetVault`, when set, matches `^[a-z][a-z0-9-]*$` (cannot fire here in practice: the vault-mismatch guard only lets through an empty or exactly-matching vault name)

Tests that encode the old no-op and must change:
- `pkg/command/task_update_frontmatter_executor_test.go` — `Context("empty updates", ...)`, two `It`s asserting `Expect(err).NotTo(HaveOccurred())` for `Updates: nil` and `Updates: lib.TaskFrontmatter{}`.
- `pkg/command/task_frontmatter_sequence_test.go` — `Context("Test C — UpdateFrontmatterCommand with empty Updates is a no-op", ...)`, asserting no error for `Updates: nil, Body: nil`.

Test helpers: `buildCmdObj` in `task_update_frontmatter_executor_test.go` builds `Data` with `base.ParseEvent(ctx, cmd)`. The counterfeiter fake for `cdb.ResultObjectSender` is `cqrsmocks.CDBResultObjectSender` (`cqrsmocks "github.com/bborbe/cqrs/mocks"`), already imported in `pkg/factory/command_consumer_expiry_test.go`. The metric-assert pattern is in the same test file's "vault routing" Context.

Error style: `github.com/bborbe/errors` (`errors.Wrapf(ctx, err, ...)`), as used throughout `pkg/command/`. Metrics: `m.FrontmatterCommandsTotal("update-frontmatter", "error").Inc()` as used further down the same handler.
</context>

<requirements>
1. In `NewUpdateFrontmatterExecutor` (`pkg/command/task_update_frontmatter_executor.go`), replace the empty-updates no-op branch with a call to `cmd.Validate(ctx)` placed after `frontmatterCommandVaultMismatch` (so a command for another vault is still skipped silently by that check). On a validation error: increment `m.FrontmatterCommandsTotal("update-frontmatter", "error")`, log once at `glog.Warningf` naming the task identifier and the error, and return `errors.Wrapf(ctx, err, "invalid update-frontmatter command for %s", cmd.TaskIdentifier)`. The returned error MUST NOT wrap `cdb.ErrCommandObjectSkipped`.
2. Remove the now-dead `// Empty updates with no body section is a no-op` comment. Leave every other branch (decode failure, vault mismatch, task not found, apply) unchanged.
3. In `pkg/command/task_update_frontmatter_executor_test.go`, rewrite `Context("empty updates", ...)`:
   - `Updates: nil, Body: nil` → `HandleCommand` returns an error; `errors.Is(err, cdb.ErrCommandObjectSkipped)` is false; the error text contains `Updates or Body`; `fakeVault.ReadTaskCallCount()`, `PatchTaskCallCount()` and `PutBodyCallCount()` are 0; the `FrontmatterCommandsTotal("update-frontmatter","error")` counter increased by 1 (follow the "vault routing" Context's metric-assert pattern).
   - `Updates: lib.TaskFrontmatter{}`, `Body: nil` → same error and zero-write assertions.
   - `TaskIdentifier: ""` with non-empty `Updates` → error not wrapping `cdb.ErrCommandObjectSkipped`; `ReadTaskCallCount()` is 0.
   - `Updates: nil` with a non-nil `Body` → no error, and the body write happens: first write a task file whose `task_identifier` matches the command and whose body does NOT already contain the section heading (`PutBody` is skipped when the body is unchanged), then assert `PutBodyCallCount()` is 1.
4. In `pkg/command/task_frontmatter_sequence_test.go`, rewrite `Context("Test C — UpdateFrontmatterCommand with empty Updates is a no-op", ...)`: rename it to state the command is rejected, assert `HandleCommand` returns a non-nil error that does not wrap `cdb.ErrCommandObjectSkipped`, and keep its assertions that the file is unchanged.
5. Add a boundary test in `task_update_frontmatter_executor_test.go` that traverses the real decode path with the wrong key. Build `Data` the way `buildCmdObj` does (`base.ParseEvent(ctx, <value>)`), but from `map[string]interface{}{"taskIdentifier": "wrongkey-uuid", "frontmatter": map[string]interface{}{"status": "in_progress"}}` instead of a typed command, wrap it in the same `cdb.CommandObject` shape, and assert `HandleCommand` returns an error that does not wrap `cdb.ErrCommandObjectSkipped`, with `ReadTaskCallCount()`, `PatchTaskCallCount()` and `PutBodyCallCount()` all 0.
6. Add a test through the real result wrapper: `cdb.NewCommandObjectExecutorTxResultSender(executor, resultSender, log.DefaultSamplerFactory)` (`github.com/bborbe/log`), with `resultSender := &cqrsmocks.CDBResultObjectSender{}`. Send an empty-updates command and assert: the wrapped `HandleCommand` returns nil (proves no retry), `resultSender.SendCallCount()` is 1, and `SendArgsForCall(0)` carries `Result.Success == false` with a `Result.Message` containing `Updates or Body`.
7. In `docs/controller-design.md`, `"update-frontmatter"` flow diagram, replace the line `├── if Updates is empty AND Body is nil → return nil (no-op, no write)` with `├── cmd.Validate: Updates empty AND Body nil, or empty TaskIdentifier → count error, return plain error → failure result published, not retried (never ErrCommandObjectSkipped)`.
8. Add a `## Unreleased` section at the top of `CHANGELOG.md` (above `## v0.26.6`) with one bullet: `- fix: update-frontmatter now reports a command with no updates and no body (e.g. a field map sent under the wrong key), or with no task identifier, as a failed result instead of a silent success`.
9. Before you finish, re-run the `<verification>` commands and confirm they pass; walk each requirement above against the diff.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Do NOT use `cdb.ErrCommandObjectSkipped` for the validation failure — it suppresses the result, which is the silent behaviour being removed.
- Do NOT change the decode-failure, vault-mismatch, task-not-found or apply branches.
- Only the rules `UpdateFrontmatterCommand.Validate` already enforces are in scope. Do NOT add strict unknown-field decoding to the command type.
- Do NOT edit `vendor/` or run `go mod vendor`; do not change `go.mod` dependency versions.
- Existing tests other than `Context("empty updates", ...)` in `task_update_frontmatter_executor_test.go` and "Test C" in `task_frontmatter_sequence_test.go` must still pass unchanged.
</constraints>

<verification>
Run `ROOTDIR=/workspace make precommit` — must pass.
Run `! grep -n 'Empty updates with no body section is a no-op' pkg/command/task_update_frontmatter_executor.go` — must exit 0.
Run `! grep -n 'no-op, no write' docs/controller-design.md` — must exit 0.
Run `awk '/^## /{sec=$0} /update-frontmatter now reports a command with no updates/{print sec}' CHANGELOG.md` — must print `## Unreleased`.
</verification>
