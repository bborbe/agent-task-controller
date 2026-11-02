---
status: completed
spec: [010-durable-commands-raise-expiry]
summary: 'Wired the agent-task-v1-request command consumer in pkg/factory/factory.go through the explicit cqrs RunCommandConsumerTx form with a named 60-minute commandExpireDuration constant, added the expiry-window Ginkgo test (30-min executes, 61-min expires without executing), and recorded the change under a new ## Unreleased fix: bullet'
execution_id: agent-task-controller-expiry-exec-018-spec-010-durable-commands-raise-expiry
dark-factory-version: dev
created: "2026-09-06T11:32:28Z"
queued: "2026-09-06T13:22:39Z"
started: "2026-09-06T13:22:41Z"
completed: "2026-09-06T13:28:15Z"
branch: dark-factory/durable-commands-raise-expiry
---

<!-- OPEN QUESTIONS FOR THE REVIEWER (resolved by best-effort from the spec; verify at audit):
  Q1. The spec's CHANGELOG constraint says "an `## Unreleased` bullet (section exists — HEAD is v0.8.1)",
      but the current CHANGELOG.md has NO `## Unreleased` section (top entry is `## v0.8.1`). Requirement 4
      creates the section. If you prefer the bullet under v0.8.1 instead, adjust requirement 4.
  Q2. `.dark-factory.yaml` sets `validationPrompt: docs/dod.md`, but `docs/dod.md` does not exist in the
      repo. The executor falls back to the global definition-of-done (>=80% statement coverage for new code;
      tests are the new code here). Flag if a repo-local dod is expected.
  Q3. AC 2 / spec Verification list `git diff origin/master -- go.mod go.sum` as container-executable, but
      the execution container's `.git` is masked/unavailable (this workspace has no git repo). The container
      uses a non-git scope check (requirements 5); the git negative-evidence check is moved to the operator-side
      spec verification ladder. Confirm this split is acceptable.
  Q4. The spec Summary cites `kafka_message-handler-tx-skip.go` "in this repo" as the file logging
      `command expired`. That logic actually lives in the pinned `cqrs@v0.6.10`
      (`cdb/cdb_command-object-message-handler-tx.go`), not in this repo — no repo file to touch for it.
-->

# Raise command-consumer expiry from 5 minutes to 60 minutes

<summary>

- The command consumer that reads `<stage>.agent-task-v1-request` is switched from the cqrs default wiring (which hardcodes a 5-minute command-expiry window) to the explicit consumer form with a 60-minute expiry instead.
- The new wiring mirrors the default's exact behavior in every respect except the window: same batch size (1), same trigger, same unsupported-command handling, same topic prefix — only the expiry duration changes.
- The 60-minute value is a named package constant with a comment explaining the sizing rationale (worst-case queue residency of a 100+ task enqueue at a cap of 1 concurrent job, measured job runtimes 11-15 min), so a burst of agent results, a slow git-rest round-trip, or a controller restart cannot push queued commands past the window and silently drop their frontmatter writes.
- Stale-command protection is unchanged: a command older than 60 minutes still expires, still logs `command expired`, and still increments the existing request-topic kafka `failure_counter` — no new metric.
- A new Ginkgo unit test exercises the consumer's real message-handler path with the 60-minute duration: a 30-minute-old command reaches the executor and is executed, and a 61-minute-old command is dropped as expired without reaching the executor.
- No dependency version changes ship with this — `cqrs` stays pinned at `v0.6.10`; the change touches only the factory file, one new test file, and the changelog.
- CHANGELOG gains an `## Unreleased` `fix:` bullet describing the raised window.

</summary>

<objective>

Wire the command consumer in `pkg/factory/factory.go` through the explicit consumer form with a named 60-minute expiry constant (instead of the cqrs default's hardcoded 5 minutes), add a unit test proving the expiry window with the real message-handler path, and record the change in the changelog — all without touching any dependency.

</objective>

<context>

There is no CLAUDE.md in this repo; the global YOLO container CLAUDE.md (already in your context) governs project conventions. The repo's definition-of-done gate (`docs/dod.md`) does not exist on disk, so the global definition-of-done applies: new/modified code is tested and new code keeps >=80% statement coverage.

Read these repo files IN FULL before changing anything:

- `pkg/factory/factory.go` — the only file that wires the command consumer. Current call site is line 53: `cdb.RunCommandConsumerTxDefault(...)`. This is the ONLY call site of `RunCommandConsumerTxDefault` in the repo (verified). `CreateCommandConsumer` (line 24) keeps its signature — `main.go:174` is its single caller and needs no change. Imports `lib "github.com/bborbe/agent"`, `libkafka "github.com/bborbe/kafka"`, `"github.com/bborbe/run"` already exist; `"time"` does NOT and must be added.
- `pkg/factory/factory_suite_test.go` — declares `package factory_test`, the Ginkgo/Gomega suite bootstrap, the counterfeiter `//go:generate ... v6.12.2 -generate` directive, and the license header to copy into new files. `time.Local = time.UTC` is already set.
- `CHANGELOG.md` — top entry is `## v0.8.1`; there is NO `## Unreleased` section yet (create it; see Q1 above).
- `main.go` — lines around 174 to confirm `CreateCommandConsumer` usage (read-only reference; do not modify).

Read the pinned library sources to anchor signatures (all verified at the versions this repo pins):

- `RunCommandConsumerTxDefault` and `RunCommandConsumerTx` — module cache `cdb/cdb_run-command-consumer-tx.go` under `cqrs@v0.6.10`. The Default is exactly `RunCommandConsumerTx(saramaClientProvider, syncProducer, db, schemaID, libkafka.BatchSize(1), prefix, ignoreUnsupported, 5*time.Minute, run.NewTrigger(), commandObjectExecutors, options...)`. Mirror it, only replacing the duration.
- `NewCommandObjectMessageHandlerTx(schemaID SchemaID, commandObjectHandler CommandObjectHandlerTx, commandExpireDuration time.Duration) libkafka.MessageHandlerTx` — the real expiry gate used by the factory wiring; it returns `errors.Wrapf(ctx, cdb.CommandExpiredError, "command expired (expire %s < now %s)", ...)` when `now.After(RequestTime.Add(commandExpireDuration))`. This is the "consumer's real message-handler path" the test must drive.
- `NewCommandObjectHandlerTx(ignoreUnsupported bool, commandObjectExecutors ...CommandObjectExecutorTx) CommandObjectHandlerTx` — dispatches to the executor whose `CommandOperation()` matches the command.
- `CommandObjectExecutorTx` interface — `CommandOperation() base.CommandOperation`, `HandleCommand(ctx context.Context, tx libkv.Tx, commandObject CommandObject) (*base.EventID, base.Event, error)`, `SendResultEnabled() bool`.
- `libkafka.MessageHandlerTx` interface — `ConsumeMessage(ctx context.Context, tx libkv.Tx, msg *sarama.ConsumerMessage) error`.

Dependency-provided test doubles (import directly; do NOT generate new mocks in this repo — no new `//counterfeiter:generate` directives):

- `mocks.CDBCommandObjectExecutorTx` from `github.com/bborbe/cqrs/mocks` — counterfeiter fake with `CommandOperationReturns(base.CommandOperation)`, `HandleCommandReturns(*base.EventID, base.Event, error)`, `HandleCommandCallCount() int` (all verified present).
- `kvmocks.Tx` from `github.com/bborbe/kv/mocks` — implements `libkv.Tx`.

Reference guides (in-container paths; read before implementing):

- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo/Gomega conventions, external test packages, coverage rule.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — `github.com/bborbe/errors` wrapping and `errors.Cause` (the test asserts `errors.Cause(err) == cdb.CommandExpiredError`).
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — `## Unreleased` / prefix rules.
- `/home/node/.claude/plugins/marketplaces/coding/docs/definition-of-done.md` — coverage and completion rules.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-patterns.md` — interface/constructor/struct, error wrapping.

</context>

<requirements>

1. **Add the named expiry constant** to `pkg/factory/factory.go`, as a package-level unexported constant placed just above `CreateCommandConsumer` (or immediately below the imports). Use exactly this shape (the comment is load-bearing — it records the sizing rationale the spec's Desired Behavior 2 requires):

   ```go
   // commandExpireDuration is the maximum age of a queued agent-task-v1-request
   // command before it is dropped as expired. Sized at 60 minutes to absorb the
   // worst-case queue residency of a 100+ task enqueue against a cap of 1
   // concurrent job (measured job runtimes 11-15 min): a burst of agent
   // completions, a slow git-rest round-trip, or a controller restart must not
   // push tail commands past the window and silently drop their frontmatter
   // writes. Stale-command protection is unchanged — a command older than 60
   // minutes still expires rather than clobbering newer state.
   const commandExpireDuration = 60 * time.Minute
   ```

   Add `"time"` to the import block (it is not currently imported). Note `60 * time.Minute` is a constant expression — a `const` is correct here, not a `var`.

2. **Change the wiring** so `CreateCommandConsumer` returns the explicit consumer call instead of the Default, mirroring the Default's argument order exactly with the duration replaced. Replace the current return statement (lines 53-61):

   ```go
   	return cdb.RunCommandConsumerTx(
   		saramaClientProvider,
   		syncProducer,
   		db,
   		lib.TaskV1SchemaID,
   		libkafka.BatchSize(1),
   		topicPrefix,
   		true, // ignoreUnsupported: skip commands with unknown operations
   		commandExpireDuration,
   		run.NewTrigger(),
   		executors,
   	)
   ```

   The argument order MUST match the pinned `RunCommandConsumerTx` signature: `(saramaClientProvider, syncProducer, db, schemaID, batchSize, prefix, ignoreUnsupported, commandExpireDuration, trigger, commandObjectExecutors, options...)`. `libkafka.BatchSize(1)` and `run.NewTrigger()` are already imported. Do NOT change `lib.TaskV1SchemaID`, `topicPrefix`, `true`, or `executors` — only the call form and the duration differ from the old Default call. Do not remove or alter the `executors := cdb.CommandObjectExecutorTxs{...}` block above the return.

3. **Add the expiry-window unit test** in a new file `pkg/factory/command_consumer_expiry_test.go`, declared `package factory_test` (must match `factory_suite_test.go` so the existing suite runner picks up the specs). Copy the license header from `factory_suite_test.go`. The test drives the real message-handler path the factory wiring uses — `cdb.NewCommandObjectMessageHandlerTx(lib.TaskV1SchemaID, cdb.NewCommandObjectHandlerTx(true, executor), 60*time.Minute)` — and asserts the executor is invoked for a 30-minute-old command and NOT invoked for a 61-minute-old command (spec AC 3). Imports: `context`, `encoding/json`, `time`, `github.com/IBM/sarama`, `github.com/bborbe/errors`, `lib "github.com/bborbe/agent"`, `github.com/bborbe/cqrs/base`, `github.com/bborbe/cqrs/cdb`, `github.com/bborbe/cqrs/mocks`, `libkafka "github.com/bborbe/kafka"`, `kvmocks "github.com/bborbe/kv/mocks"`, and Ginkgo/Gomega (`. "github.com/onsi/ginkgo/v2"`, `. "github.com/onsi/gomega"`). Structure:

   ```go
   var _ = Describe("command consumer expiry window", func() {
   	const expiryWindow = 60 * time.Minute // must mirror pkg/factory commandExpireDuration

   	var (
   		ctx                   context.Context
   		commandMessageHandler libkafka.MessageHandlerTx
   		executor              *mocks.CDBCommandObjectExecutorTx
   	)

   	BeforeEach(func() {
   		ctx = context.Background()
   		executor = &mocks.CDBCommandObjectExecutorTx{}
   		executor.CommandOperationReturns(base.CommandOperation("any-op"))
   		executor.HandleCommandReturns(nil, base.Event{}, nil)
   		commandMessageHandler = cdb.NewCommandObjectMessageHandlerTx(
   			lib.TaskV1SchemaID,
   			cdb.NewCommandObjectHandlerTx(true, executor),
   			expiryWindow,
   		)
   	})

   	command := func(age time.Duration) base.Command {
   		return base.Command{
   			RequestID:   "1234567890",
   			RequestTime: time.Now().Add(-age),
   			Initiator:   "me",
   			Operation:   "any-op",
   		}
   	}

   	consume := func(command base.Command) error {
   		value, err := json.Marshal(command)
   		Expect(err).To(BeNil())
   		return commandMessageHandler.ConsumeMessage(ctx, &kvmocks.Tx{}, &sarama.ConsumerMessage{Value: value})
   	}

   	It("executes a command 30 minutes old", func() {
   		err := consume(command(30 * time.Minute))
   		Expect(err).To(BeNil())
   		Expect(executor.HandleCommandCallCount()).To(Equal(1))
   	})

   	It("drops a command 61 minutes old as expired without executing it", func() {
   		err := consume(command(61 * time.Minute))
   		Expect(err).NotTo(BeNil())
   		Expect(errors.Cause(err)).To(Equal(cdb.CommandExpiredError))
   		Expect(executor.HandleCommandCallCount()).To(Equal(0))
   	})
   })
   ```

   Notes for correctness:
   - `executor.CommandOperationReturns(base.CommandOperation("any-op"))` MUST be set BEFORE `cdb.NewCommandObjectHandlerTx` is constructed — the constructor calls `CommandOperation()` while building its dispatch map. `"any-op"` satisfies the `base.CommandOperation` regex `^[a-z][a-z-]*$`, and the same values (`RequestID: "1234567890"`, `Initiator: "me"`, `Operation: "any-op"`) are known-valid: they are exactly what the pinned `cqrs@v0.6.10`'s own `cdb_command-object-message-handler-tx_test.go` uses.
   - `time.Now()` is fine for both ages: the 30 vs 61 minute margins are huge relative to test execution time, so there is no flake window.
   - The 61-minute case must fail at the expiry gate (before `Validate`, before the executor), so `errors.Cause(err)` equals `cdb.CommandExpiredError` and `HandleCommandCallCount()` stays 0.
   - The 30-minute case passes expiry, validates, dispatches, and the mock executor's `HandleCommand` returns `(nil, base.Event{}, nil)` — the handler returns nil, `ConsumeMessage` returns nil, and `HandleCommandCallCount()` is 1.

4. **CHANGELOG**: add an `## Unreleased` section above `## v0.8.1` with one `fix:` bullet (a `fix:` prefix — this raises the window that caused the 2026-08-14 dropped-commands outage; dark-factory reads the prefix for the version bump). The bullet must describe what changed, not how it was verified:

   ```
   ## Unreleased

   - fix: raise the agent-task-v1-request command consumer expiry from the cqrs 5-minute default to 60 minutes — `pkg/factory/factory.go` now wires the explicit command consumer with a named `commandExpireDuration` constant (same batch size, trigger, and unsupported-command handling as the Default, only the window changes), so a burst of agent results, a slow git-rest round-trip, or a controller restart can no longer push queued commands past the window and silently drop their frontmatter writes; commands older than 60 minutes still expire (stale-command protection unchanged) and stay visible via the `command expired` warning log and the request-topic kafka `failure_counter` (spec 010)
   ```

   Do NOT alter any existing `## v0.x.y` entries.

5. **Do NOT change dependency versions.** Edit exactly three files: `pkg/factory/factory.go`, the new `pkg/factory/command_consumer_expiry_test.go`, and `CHANGELOG.md`. No dependency VERSION changes ship with this: `cqrs` stays pinned at `v0.6.10` and `kafka` at `v1.25.13`. Expected and acceptable: `go mod tidy` (run by `make precommit`'s ensure target) moves `github.com/IBM/sarama` from `// indirect` to a direct require, because the new test imports `sarama.ConsumerMessage` — do not revert that reclassification. `go.sum` must remain byte-identical. No new `//counterfeiter:generate` directive and no new mock file in this repo (the test reuses the dependency-provided `cqrs/mocks` and `kv/mocks` fakes).

6. **Failure-mode coverage** (spec Failure Modes → requirements mapping): the expiry of a genuinely stale command (trigger row 2) is exercised by the 61-minute case asserting `cdb.CommandExpiredError` and a zero executor call; the "wrong duration value at the call site" row (trigger row 4) is caught by requirement 2's exact call-shape plus the verification grep checks plus the 30/61-minute test boundaries. The restart/slow-git-residency rows (1 and 3) are queue-level behaviors absorbed by the widened window and need no new code — do not add retry or pause logic, and do NOT add any new metric or knob (spec Non-goals forbid both).

</requirements>

<constraints>

- Repo conventions frozen: Ginkgo/Gomega v2 tests, `github.com/bborbe/errors` wrapping (never `fmt.Errorf`, never `context.Background()` in pkg logic), counterfeiter mocks for new dependencies, glog `V(n)` gating. The test uses Ginkgo `Describe`/`It` in an external `factory_test` package with the existing suite bootstrap; do not create a second suite bootstrap.
- `cdb.RunCommandConsumerTx` requires the caller to also pass `batchSize` (`libkafka.BatchSize(1)`, matching what `RunCommandConsumerTxDefault` used) and `trigger` (`run.NewTrigger()`), plus `ignoreUnsupported` and `prefix` unchanged — mirror the Default's wiring exactly, only the duration changes.
- `cqrs` stays pinned at `v0.6.10` (its `RunCommandConsumerTx` already accepts the duration). No `cqrs` / `kafka` dependency change ships with this.
- Do NOT remove expiry entirely — stale-command protection stays (a command that sat for hours must not clobber newer state).
- Do NOT add a new expiry metric — the existing kafka `failure_counter` on the request topic + the `command expired` warning log already make expiry observable.
- Do NOT touch the executor or the ResourceQuota (capacity decision, out of scope).
- Do NOT commit — dark-factory handles git.

</constraints>

<verification>

All commands run from `/workspace`. Iterate with the fast loop first; run `make precommit` ONCE at the end.

```
cd /workspace && go test -mod=mod ./pkg/factory/... -count=1 -v
```
Must pass, and the output must show the two new specs ("executes a command 30 minutes old", "drops a command 61 minutes old as expired without executing it") plus the pre-existing suite specs.

```
cd /workspace && grep -n 'RunCommandConsumerTx\|commandExpireDuration\|60 \* time.Minute' pkg/factory/factory.go
```
Must return: the explicit consumer call, at least one line for the `commandExpireDuration` constant declaration, and at least one line for `60 * time.Minute` (spec AC 1 + AC 3). Confirm by inspection that the call passes `libkafka.BatchSize(1)`, `topicPrefix`, `true`, and `run.NewTrigger()` in the Default's order and that ONLY the duration argument changed.

```
cd /workspace && go vet ./pkg/factory/...
cd /workspace && make test
cd /workspace && make precommit
```
`make precommit` MUST exit 0 (it runs `ensure`, `format`, `generate`, `test`, `check`, `addlicense`). If it fails, fix the failing target and re-run only that target, then re-run `make precommit` once — never report success with a non-zero exit code. Note `generate` regenerates the repo `mocks/` dir idempotently (no new directives were added, so no new mock file appears).

Scope check: verify by inspection that ONLY `pkg/factory/factory.go`, `pkg/factory/command_consumer_expiry_test.go`, and `CHANGELOG.md` were modified, and that the only `go.mod` change is the expected `IBM/sarama` direct/indirect reclassification — e.g. `grep -E 'bborbe/(cqrs|kafka) ' go.mod` still shows `v0.6.10` / `v1.25.13`, and `cksum go.sum` before and after matches. This is the container-side substitute for the spec's `git diff origin/master -- go.mod go.sum` negative-evidence check (AC 2).

NOT run by this container — owned by the operator-side verification ladder on the spec (do not attempt, and do not stub them): the `git diff origin/master -- go.mod go.sum` diff check, and the post-deploy Rung-2 live check on dev (`kubectlnukedev`-based 100+ task enqueue at `maxConcurrentJobs: 1` asserting `grep -c 'command expired'` returns 0 in the controller log).

</verification>
