---
status: approved
spec: [bug-nuke-dev-hm]
created: "2026-11-27T08:00:00Z"
queued: "2026-11-27T08:01:46Z"
---

# Wire the retry publisher into main and record the fix in the changelog

<summary>
- The controller now runs every task-event publish through the bounded-retry decorator.
- A transient Kafka produce failure on the change-detection path is absorbed instead of terminating the process.
- The same retrying publisher is shared by the sync loop, the re-drive sweep, and the result writer, so all three inherit the retries.
- A genuine, persistent outage still terminates the controller, because the decorator returns the last error when its retries run out.
- The retry delays and the production waiter are supplied at the single wiring point, with no new configuration surface.
- The changelog records the fix under a new Unreleased section.
- No existing command-processing, routing, or scanner behaviour changes.

<objective>
Wire `publisher.NewRetryTaskPublisher` around the shared `publisher.NewTaskPublisher` in `main.go` so a transient Sarama broker error on the `pkg/sync` publish path no longer reaches `run.CancelOnFirstErrorWait`/`service.Run` and kills the controller, while preserving the fatal fallback on true exhaustion; then record the fix in `CHANGELOG.md`.
</objective>

<context>
Read these files before changing anything:

- `main.go` — the `application.Run` method. Find where `taskPublisher := publisher.NewTaskPublisher(eventObjectSender, lib.TaskV1SchemaID, currentDateTime, a.VaultName)` is created, and note that this single value is then passed to `pkgsync.NewSyncLoop`, `redrive.NewRedriveSweep`, and `result.NewResultWriter` (via `result.WithKeptStatePublisher(taskPublisher)`). The imports already include `"github.com/bborbe/run"` and `"github.com/bborbe/agent-task-controller/pkg/metrics"` and `"github.com/bborbe/agent-task-controller/pkg/publisher"`, so no new imports are needed.
- `pkg/publisher/retry_task_publisher.go` — the decorator added by the previous prompt: `NewRetryTaskPublisher(inner TaskPublisher, m metrics.Metrics, waiter run.Waiter) TaskPublisher`.
- `pkg/sync/sync_loop.go` — `processResult` returns `errors.Wrapf(ctx, err, "publish changed task %s", ...)` on a publish failure; `Run` propagates it through `run.CancelOnFirstErrorWait`. Read only; do not modify.
- `CHANGELOG.md` — it currently opens with `## v0.38.0` (there is no `## Unreleased` section). The format is Keep a Changelog.

Relevant coding guides (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-factory-pattern.md`

<!-- DECISION FOR REVIEWER (bug-nuke-dev-hm): the decorator is wired ONCE around the
     shared publisher, so the sync loop (the fatal path named in the spec), the
     re-drive sweep, and the result writer all inherit the retries. This is the
     minimal wiring that matches "at the main.go wiring" and keeps one publisher
     instance, rather than wrapping only the sync loop's copy. -->
</context>

<requirements>
## 1. Wrap the shared task publisher in `main.go`

In `application.Run`, replace the `taskPublisher := publisher.NewTaskPublisher(...)` construction with the decorator wrapping the existing constructor, so every downstream consumer keeps receiving the same single `publisher.TaskPublisher` value:

```go
	taskPublisher := publisher.NewRetryTaskPublisher(
		publisher.NewTaskPublisher(
			eventObjectSender,
			lib.TaskV1SchemaID,
			currentDateTime,
			a.VaultName,
		),
		metrics.New(),
		run.NewWaiter(),
	)
```

Do not change the three consumers (`pkgsync.NewSyncLoop`, `redrive.NewRedriveSweep`, `result.WithKeptStatePublisher`) — they receive `taskPublisher` unchanged and therefore get the retrying decorator automatically. Do not add any new struct field, argument, environment variable, or CLI flag.

## 2. Record the fix in `CHANGELOG.md`

Insert a new `## Unreleased` section immediately above the existing `## v0.38.0` line (leave the header preamble intact). Add one `- fix:` bullet describing the user-visible change. It must contain the exact phrase `no longer terminates the controller` (used by the verification check). Follow the house style of the existing bullets (bold one-line summary, then the mechanism). Suggested content:

```markdown
## Unreleased

- fix: **a transient Kafka produce failure on the change-detection publish path no longer terminates the controller.** `pkg/sync`'s fail-fast `PublishChanged` path propagated a Sarama broker error (most consistent with `ErrNotLeader`) through `run.CancelOnFirstErrorWait` into `service.Run`, killing the process. The shared `publisher.TaskPublisher` is now wrapped in `publisher.NewRetryTaskPublisher`, which retries a failed publish after 1s, 2s, 4s and 8s (five attempts, at most 15s) before returning the last error unchanged in cause, so the existing fatal fallback still fires on true exhaustion. Retries and exhaustion are counted by `agent_controller_kafka_publish_retry_total` and `agent_controller_kafka_publish_failure_total`, labelled by event type. The decorator is wired once in `main.go`, so the sync loop, the re-drive sweep and the result writer all inherit it. A cancelled context during a wait stops retrying immediately, so shutdown is not delayed.
```

## 3. Self-check

Before finishing, re-run `<verification>` and confirm it passes. Confirm the sync loop, re-drive sweep, and result writer all still receive the same `taskPublisher` value (now the decorator) and that no other wiring changed.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
- Preserve the existing fatal fallback: the decorator returns the last error on exhaustion, so `pkg/sync`'s error propagation and `service.Run`'s termination are unchanged for a genuine outage.
- Do NOT modify `pkg/sync/sync_loop.go`, `pkg/redrive/redrive_sweep.go`, or `pkg/result/result_writer.go` — the wiring change is confined to `main.go`.
- Do NOT add a configuration field, environment variable, or CLI flag; `run.NewWaiter()` and the package-constant delays are supplied at the wiring point.
- Do NOT add any other `## Unreleased` heading; exactly one section is inserted.
</constraints>

<verification>
Run `ROOTDIR=/workspace make precommit` — must pass.

Then confirm the wiring and changelog, which precommit does not prove:
- `[ "$(grep -c 'publisher.NewRetryTaskPublisher' main.go)" = 1 ]`
- `[ "$(grep -c 'run.NewWaiter()' main.go)" = 1 ]`
- `[ "$(grep -c '^## Unreleased$' CHANGELOG.md)" = 1 ]`
- `awk '/^## /{sec=$0} /no longer terminates the controller/{print sec}' CHANGELOG.md` prints `## Unreleased`
</verification>
