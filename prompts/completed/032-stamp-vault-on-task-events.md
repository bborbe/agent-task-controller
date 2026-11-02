---
status: completed
summary: Stamped the controller's configured VAULT_NAME into every published task-changed event via Task.Vault, bumped github.com/bborbe/agent to v0.113.0, and added tests for empty and overriding input vault values.
execution_id: agent-task-controller-vault-stamp-exec-032-stamp-vault-on-task-events
dark-factory-version: v0.198.7
created: "2026-10-21T17:41:00Z"
queued: "2026-10-21T17:54:03Z"
started: "2026-10-21T17:54:25Z"
completed: "2026-10-21T17:59:22Z"
---

<summary>
- Every task event the controller publishes now carries the name of the vault the controller serves
- Consumers that share one Kafka topic across several vaults can filter for their own tasks from the event alone, without reading git
- The vault stamp always comes from the controller's own configuration, never from the task content, so a stale or wrong value in a task cannot leak through
- Delete events are unchanged (they carry no body)
- Routing of results by the task's target-vault frontmatter field is unchanged
- The agent library dependency is bumped to the release that introduced the vault field on tasks
- A new test proves the published event carries the configured vault even when the incoming task has an empty or different vault
- The changelog records the feature
</summary>

<objective>
Stamp the controller's configured `VAULT_NAME` into every published task-changed event (`Task.Vault`) so consumers that share one topic across vaults (agent-task-executor) can filter their own tasks without reading git.
</objective>

<context>
Read CLAUDE.md for project conventions.

Read these files before changing anything:
- `pkg/publisher/task_publisher.go` — `TaskPublisher` interface, `NewTaskPublisher`, `taskPublisher.PublishChanged`, `taskPublisher.PublishDeleted`.
- `pkg/publisher/task_publisher_test.go` — existing Ginkgo test; `BeforeEach` builds `tp` via `publisher.NewTaskPublisher(fakeSender, schemaID, currentDateTime)` and tests decode the sent event with `eventObject.Event.MarshalInto(ctx, &publishedTask)`.
- `pkg/publisher/topic_prefix_test.go` — `buildPublisher` helper calls `publisher.NewTaskPublisher(eventObjectSender, schemaID, currentDateTime)`.
- `main.go` — the `publisher.NewTaskPublisher(eventObjectSender, lib.TaskV1SchemaID, currentDateTime)` call inside the `pkgsync.NewSyncLoop(...)` construction. The `application` struct already has `VaultName string` (env `VAULT_NAME`), validated at startup by `routing.ValidateVaultName(ctx, a.VaultName)` earlier in `Run`.
- `CHANGELOG.md` — format of existing entries.

Library facts: `github.com/bborbe/agent` v0.113.0 adds `Task.Vault string` with JSON tag `json:"vault,omitempty"` — the slug of the vault the task file lives in. Current go.mod pins v0.112.0. `lib` is the import alias for `github.com/bborbe/agent` in this repo.

Sole call sites of `NewTaskPublisher` (verified by grep): `main.go`, `pkg/publisher/task_publisher_test.go`, `pkg/publisher/topic_prefix_test.go`. The `TaskPublisher` interface is unchanged, so the counterfeiter mock in `mocks/task_publisher.go` needs no regeneration.
</context>

<requirements>
1. Bump the dependency: run `go get github.com/bborbe/agent@v0.113.0`. Do NOT run `go mod tidy` yet and do NOT run `go mod vendor` (see requirement 6 for the order). Confirm go.mod now reads `github.com/bborbe/agent v0.113.0`.

2. In `pkg/publisher/task_publisher.go`:
   - Change the constructor signature to add `vaultName string` as the last parameter:
     `func NewTaskPublisher(eventObjectSender cdb.EventObjectSender, schemaID cdb.SchemaID, currentDateTimeGetter libtime.CurrentDateTimeGetter, vaultName string) TaskPublisher`
   - Add a `vaultName string` field to the `taskPublisher` struct and set it in the constructor.
   - In `PublishChanged`, set `task.Vault = p.vaultName` before `base.ParseEvent(ctx, task)`, next to where `task.Object` is assigned. The assignment is unconditional: the controller's configured vault always overwrites whatever `Vault` the incoming task value carried.
   - `PublishDeleted` is unchanged (tombstones carry no body).
   - Update the doc comments of `NewTaskPublisher` and/or `PublishChanged` to state: every changed-event is stamped with the controller's `VAULT_NAME` in `Task.Vault`; consumers sharing one topic across vaults (agent-task-executor) filter on it; `frontmatter["target_vault"]` remains the routing field for results.

3. In `main.go`, pass `a.VaultName` as the new last argument: `publisher.NewTaskPublisher(eventObjectSender, lib.TaskV1SchemaID, currentDateTime, a.VaultName)`. Do not add any new validation — `routing.ValidateVaultName` already runs at startup.

4. Update the two existing test call sites to compile with the new signature:
   - `pkg/publisher/task_publisher_test.go` `BeforeEach`: pass a package-level-in-the-Describe constant vault name, e.g. declare `vaultName string` in the `var (...)` block, set `vaultName = "test-vault"` in `BeforeEach`, and call `publisher.NewTaskPublisher(fakeSender, schemaID, currentDateTime, vaultName)`.
   - `pkg/publisher/topic_prefix_test.go` `buildPublisher`: pass `"test-vault"` as the last argument.

5. Add tests in `pkg/publisher/task_publisher_test.go` under `Describe("PublishChanged", ...)`, following the existing pattern (call `tp.PublishChanged`, read `fakeSender.SendUpdateArgsForCall(0)`, decode with `eventObject.Event.MarshalInto(ctx, &publishedTask)` — this is the real serialization round-trip, which exercises the `json:"vault,omitempty"` tag):
   - Input task with empty `Vault`: published task has `Vault == vaultName`.
   - Input task with `Vault: "some-other-vault"`: published task has `Vault == vaultName` (the configured vault overrides the input).
   Use `It` blocks or a `DescribeTable` with those two entries; keep the style of the surrounding file.

6. Finalize dependencies: after the code in requirements 2-5 is written (so the import is in use), run `go mod tidy` (or `ROOTDIR=/workspace make ensure`). Confirm go.mod still pins `github.com/bborbe/agent v0.113.0` and go.sum has the matching entries.

7. In `CHANGELOG.md`, add a `## Unreleased` section directly above the first `## vX.Y.Z` heading if none exists (currently the top section is `## v0.14.1`), with this bullet:
   `- feat: stamp `VAULT_NAME` into every published task event (`Task.Vault`) so consumers sharing one topic across vaults can filter their own tasks without reading git`
   If a `## Unreleased` section already exists, append the bullet to it.

8. Self-check: re-run `<verification>`, then walk each requirement against the diff: signature changed, `PublishChanged` stamps unconditionally, `PublishDeleted` untouched, all three call sites updated, both new test cases present, go.mod at v0.113.0, CHANGELOG bullet under `## Unreleased`.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
- `PublishDeleted` behavior and signature unchanged
- `frontmatter["target_vault"]` remains the routing field for results — do not touch routing code
- No new env vars, flags, config fields, or metrics; reuse the existing `VaultName` field and its existing startup validation
- Do NOT run `go mod vendor`; never use `-mod=vendor`
- Use `github.com/bborbe/errors` wrapping idioms already present in the file; do not add `fmt.Errorf`
</constraints>

<verification>
Run `ROOTDIR=/workspace make precommit` — must pass.

Run `grep -q 'github.com/bborbe/agent v0.113.0' go.mod` — must exit 0.

Run `grep -q 'a.VaultName)' main.go` — must exit 0 (the NewTaskPublisher call passes the vault name).

Run `awk '/^## /{sec=$0} /into every published task event/{print sec}' CHANGELOG.md` — must print `## Unreleased`.
</verification>
