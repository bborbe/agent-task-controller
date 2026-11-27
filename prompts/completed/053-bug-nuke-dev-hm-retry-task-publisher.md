---
status: completed
spec: [bug-nuke-dev-hm]
summary: Added a context-aware bounded-retry TaskPublisher decorator (1s/2s/4s/8s) with retry/failure Prometheus counters and five passing Ginkgo specs, plus a CHANGELOG entry; make precommit exits 0.
execution_id: 14db784a-3d7e-5b65-95cf-4df070e1e0b5-exec-053-bug-nuke-dev-hm-retry-task-publisher
dark-factory-version: dev
created: "2026-11-27T08:00:00Z"
queued: "2026-11-27T07:58:06Z"
started: "2026-11-27T07:58:07Z"
completed: "2026-11-27T08:14:03Z"
---

# Add a bounded-retry TaskPublisher decorator with retry/failure metrics

<summary>
- A transient Kafka produce failure no longer propagates straight out of a publish call.
- A failed publish is retried a bounded number of times with growing pauses before it gives up.
- When the retries are exhausted, the original error is still returned so the caller's existing fatal handling is unchanged.
- The pauses grow 1s, 2s, 4s, 8s, and the call waits at most about 15 seconds before giving up.
- A cancelled context stops the retries immediately, so shutdown is not delayed.
- Each retry and each exhausted publish is counted in two new Prometheus counters, labelled by event type.
- The retry delays are injectable, so tests prove the backoff schedule without actually sleeping.
- No new configuration knob, environment variable, or CLI flag is introduced.
- This prompt adds the decorator and its tests only; it is not wired into the application yet.

<objective>
Add a `TaskPublisher` decorator that wraps every publish in a context-aware bounded exponential-backoff retry (1s/2s/4s/8s), returns the last error on exhaustion so the existing fatal fallback still fires, and emits retry and failure counters — so a transient Sarama broker error (e.g. `ErrNotLeader`) is absorbed instead of terminating the controller at `pkg/sync/sync_loop.go`.
</objective>

<context>
Read these files before changing anything:

- `pkg/publisher/task_publisher.go` — the `TaskPublisher` interface (`PublishChanged(ctx, task lib.Task) error`, `PublishDeleted(ctx, id lib.TaskIdentifier) error`), its counterfeiter annotation, and the `errors.Wrapf` wrapping style used in this package. The decorator lives beside it.
- `pkg/publisher/task_publisher_test.go` and `pkg/publisher/publisher_suite_test.go` — the Ginkgo suite bootstrap (`package publisher_test`) and the existing spec style to match.
- `pkg/metrics/metrics.go` — the `Metrics` interface, the `defaultMetrics` accessor implementations, the `promauto` counter-vector declarations, and the `init()` block that pre-initialises every label series at 0. The two new counters follow `TasksPublishedTotal` exactly (a `promauto.NewCounterVec` with label `"type"`).
- `mocks/task_publisher.go` — the generated fake you will use as the inner publisher in tests (`PublishChangedReturns`, `PublishChangedReturnsOnCall`, `PublishChangedCallCount`, and the `PublishDeleted` equivalents).
- `mocks/metrics.go` — the generated fake for `metrics.Metrics`. Do NOT hand-edit it; `make precommit`'s `generate` target (`rm -rf mocks` + `go generate -mod=mod ./...`) regenerates it from the counterfeiter annotations after you extend the interface.

The `run` package already offers `run.Waiter` (`Wait(ctx context.Context, wait time.Duration) error`), `run.WaiterFunc`, and `run.NewWaiter()` at import path `github.com/bborbe/run`. Do NOT use `run.Retry`/`run.Backoff`: its delay formula is linear (`Delay + Delay*Factor*(counter-1)`), which produces 1s/3s/5s/7s, not the 1s/2s/4s/8s schedule this fix requires.

Relevant coding guides (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-prometheus-metrics-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-context-cancellation-in-loops.md`

<!-- OPEN QUESTION FOR REVIEWER (bug-nuke-dev-hm): the spec says "1s/2s/4s/8s, ~4 attempts".
     The four listed delays are four retries after the first attempt, i.e. five
     attempts in total. This prompt implements exactly the four listed delays
     (one immediate attempt + four retries = five attempts, at most 15s of waiting).
     If "~4 attempts" meant four total attempts, drop the trailing 8s entry from
     retryDelays — nothing else changes.

     Retry scope: this decorator retries EVERY error, not only errors classified as
     transient. The base TaskPublisher can fail permanently (for example a
     base.ParseEvent failure), and there is no in-tree classifier for Sarama produce
     errors, so a transient-only gate would either miss the ErrNotLeader case or need
     a new dependency. Bounded retry-all is the pragmatic choice for a bug fix; if the
     reviewer wants transient-only, add an IsRetryAble-style predicate and wire it here. -->
</context>

<requirements>
## 1. Add the two publish-retry counters to `pkg/metrics/metrics.go`

1a. In the `Metrics` interface, immediately after `TasksPublishedTotal(outcome string) prometheus.Counter`, add:

```go
	// KafkaPublishRetryTotal counts each retry the retry publisher performs, by
	// event type ("changed" | "deleted").
	KafkaPublishRetryTotal(outcome string) prometheus.Counter
	// KafkaPublishFailureTotal counts publishes that exhausted the retry budget,
	// by event type ("changed" | "deleted"). A non-zero value is the same failure
	// the sync loop treats as fatal, observed at the publisher instead.
	KafkaPublishFailureTotal(outcome string) prometheus.Counter
```

1b. In `defaultMetrics`, after the `TasksPublishedTotal` accessor, add the two accessors following the existing one-line `return Xxx.WithLabelValues(outcome)` shape:

```go
func (m *defaultMetrics) KafkaPublishRetryTotal(outcome string) prometheus.Counter {
	return KafkaPublishRetryTotal.WithLabelValues(outcome)
}

func (m *defaultMetrics) KafkaPublishFailureTotal(outcome string) prometheus.Counter {
	return KafkaPublishFailureTotal.WithLabelValues(outcome)
}
```

1c. After the `TasksPublishedTotal` `promauto.NewCounterVec` declaration, add two counter vectors with the `agent_controller_` prefix and label `"type"`, matching the existing style and doc-comment density:

```go
// KafkaPublishRetryTotal counts each retry the retry publisher performs, by
// event type ("changed" | "deleted"). It is the series that shows a transient
// Kafka outage being absorbed rather than reaching the sync loop's fatal path.
var KafkaPublishRetryTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "agent_controller_kafka_publish_retry_total",
		Help: "Total number of task-event publish retries performed, by event type.",
	},
	[]string{"type"},
)

// KafkaPublishFailureTotal counts publishes that exhausted the retry budget, by
// event type ("changed" | "deleted"). A non-zero reading is the same failure the
// sync loop treats as fatal, counted here at the point the budget ran out.
var KafkaPublishFailureTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "agent_controller_kafka_publish_failure_total",
		Help: "Total number of task-event publishes that exhausted the retry budget, by event type.",
	},
	[]string{"type"},
)
```

1d. In `init()`, next to the existing `TasksPublishedTotal.WithLabelValues(...)` pre-initialisation, add every label series at 0 so a fresh pod reads 0 rather than "no data":

```go
	KafkaPublishRetryTotal.WithLabelValues("changed").Add(0)
	KafkaPublishRetryTotal.WithLabelValues("deleted").Add(0)
	KafkaPublishFailureTotal.WithLabelValues("changed").Add(0)
	KafkaPublishFailureTotal.WithLabelValues("deleted").Add(0)
```

## 2. Create `pkg/publisher/retry_task_publisher.go`

Create the file with the BSD license header used by every Go file in this repo, `package publisher`, and this content. The retry loop is the crux of the fix — implement it exactly as written so the attempt count and metric ordering are correct:

```go
package publisher

import (
	"context"
	"time"

	lib "github.com/bborbe/agent"
	"github.com/bborbe/errors"
	"github.com/bborbe/run"
	"github.com/golang/glog"

	"github.com/bborbe/agent-task-controller/pkg/metrics"
)

// retryDelays is the exponential backoff applied between publish attempts:
// 1s, 2s, 4s, 8s. A publish is attempted once immediately and then retried
// after each delay, so one call makes at most len(retryDelays)+1 (5) attempts
// and waits at most 15s before giving up.
var retryDelays = []time.Duration{
	1 * time.Second,
	2 * time.Second,
	4 * time.Second,
	8 * time.Second,
}

// NewRetryTaskPublisher wraps inner with bounded exponential-backoff retries so
// a transient Kafka produce failure (for example Sarama's ErrNotLeader) does not
// reach the sync loop's fail-fast path and terminate the controller. On
// exhaustion it returns the last error unchanged in cause, so the caller's
// existing fatal handling still fires.
func NewRetryTaskPublisher(
	inner TaskPublisher,
	m metrics.Metrics,
	waiter run.Waiter,
) TaskPublisher {
	return &retryTaskPublisher{inner: inner, metrics: m, waiter: waiter}
}

type retryTaskPublisher struct {
	inner   TaskPublisher
	metrics metrics.Metrics
	waiter  run.Waiter
}

func (p *retryTaskPublisher) PublishChanged(ctx context.Context, task lib.Task) error {
	return p.withRetry(ctx, "changed", func(ctx context.Context) error {
		return p.inner.PublishChanged(ctx, task)
	})
}

func (p *retryTaskPublisher) PublishDeleted(ctx context.Context, id lib.TaskIdentifier) error {
	return p.withRetry(ctx, "deleted", func(ctx context.Context) error {
		return p.inner.PublishDeleted(ctx, id)
	})
}

// withRetry runs fn once, then up to len(retryDelays) more times, waiting the
// corresponding delay between attempts. A cancelled context during a wait stops
// the retries and returns the wait error, so shutdown is not delayed. Every
// completed retry increments the retry counter; exhausting the delays increments
// the failure counter and returns the last error.
func (p *retryTaskPublisher) withRetry(
	ctx context.Context,
	outcome string,
	fn func(context.Context) error,
) error {
	var err error
	for attempt := 0; ; attempt++ {
		if err = fn(ctx); err == nil {
			return nil
		}
		if attempt >= len(retryDelays) {
			p.metrics.KafkaPublishFailureTotal(outcome).Inc()
			return errors.Wrapf(
				ctx, err, "publish %s failed after %d attempts", outcome, attempt+1,
			)
		}
		glog.V(2).Infof(
			"publish %s attempt %d failed: %v; retrying in %v",
			outcome, attempt+1, err, retryDelays[attempt],
		)
		if waitErr := p.waiter.Wait(ctx, retryDelays[attempt]); waitErr != nil {
			return errors.Wrapf(ctx, waitErr, "publish %s retry wait interrupted", outcome)
		}
		p.metrics.KafkaPublishRetryTotal(outcome).Inc()
	}
}
```

Behavioral contract this code must satisfy:
- success on the first attempt calls the inner publisher exactly once, increments neither counter, and never calls the waiter;
- a publish that fails N<5 times then succeeds calls the inner publisher N+1 times, increments the retry counter N times, waits the first N delays, and never increments the failure counter;
- a publish that fails all attempts calls the inner publisher 5 times, increments the retry counter 4 times (after the 1s, 2s, 4s, 8s waits), increments the failure counter once, and returns a non-nil error whose cause is the last inner error;
- a cancelled context during a wait returns an error that satisfies `errors.Is(err, context.Canceled)` (stdlib), increments the retry counter 0 times for that wait, and performs no further attempts;
- `PublishDeleted` retries identically to `PublishChanged`, labelled `"deleted"`.

## 3. Add `pkg/publisher/retry_task_publisher_test.go`

Create the file in `package publisher_test`, following the existing Ginkgo/Gomega suite. Use the generated `mocks.TaskPublisher` (`github.com/bborbe/agent-task-controller/mocks`) as the inner publisher, a `mocks.Metrics` fake whose `KafkaPublishRetryTotalStub`/`KafkaPublishFailureTotalStub` return dedicated `prometheus.NewCounter(...)` instances (do NOT use `promauto`, so duplicate names across specs never panic), and a `run.WaiterFunc` that records the requested durations instead of sleeping. Read the counters with `prometheus/testutil.ToFloat64`. Construct the decorator with `publisher.NewRetryTaskPublisher(inner, fakeMetrics, waiter)`.

Cover, as separate `It` blocks (or a `DescribeTable`):

1. Success on the first attempt → inner `PublishChangedCallCount()` is 1; retry counter is 0; failure counter is 0; the waiter recorded no durations.
2. Retry then succeed → `PublishChangedReturnsOnCall(0, err)`, `PublishChangedReturnsOnCall(1, err)`, `PublishChangedReturns(nil)`; inner call count is 3; retry counter is 2; failure counter is 0; recorded durations equal `[]time.Duration{1 * time.Second, 2 * time.Second}`.
3. Exhaustion → `PublishChangedReturns(err)` (always); inner call count is 5; retry counter is 4; failure counter is 1; recorded durations equal `[]time.Duration{1 * time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}`; the returned error is non-nil and `errors.Is(returnedErr, err)` is true (stdlib `errors`).
4. Cancelled context during wait → inner always returns an error; the waiter returns `context.Canceled`; inner call count is 1; retry counter is 0; failure counter is 0; `errors.Is(returnedErr, context.Canceled)` is true.
5. `PublishDeleted` retries → mirror scenario 2 with `PublishDeletedReturnsOnCall`/`PublishDeletedReturns`; assert `PublishDeletedCallCount()` and that the retry/failure stubs received the `"deleted"` label (capture the argument in the stub closure).

These scenarios traverse the three boundaries the new code crosses: the `run.Waiter` injection point (proving the exact backoff schedule), the `metrics.Metrics` interface (proving the new methods are called with the right label), and context cancellation (proving shutdown is not delayed).

## 4. Self-check

Before finishing, re-run `<verification>` and confirm it passes. Walk each of the five test scenarios above against the code and confirm the attempt counts, the recorded delay slices, and the metric increments match exactly.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
- Preserve the existing fatal fallback: on exhaustion the decorator MUST return a non-nil error (wrapping the last inner error) so `pkg/sync`'s `processResult` still propagates it and `service.Run` still terminates the process on a genuine outage. Do not swallow the error and do not return nil after retries.
- Make the retries context-aware: a cancelled context during a wait must stop immediately and not delay shutdown.
- Do NOT use `run.Retry`/`run.Backoff` — its delay formula is linear, not the required 1s/2s/4s/8s.
- Do NOT add a configuration field, environment variable, or CLI flag for the retry count or delays. The schedule is a package constant.
- Do NOT implement the spec's optional "in-process re-queue of failed tasks" or "make the send blocking" — those are out of scope; the sync loop's publish is already synchronous, and a re-queue is a larger behavioural change not required to fix the transient-produce crash.
- Do NOT wire the decorator into `main.go` in this prompt — that is the next prompt.
- Do NOT hand-edit `mocks/metrics.go` or `mocks/task_publisher.go`; `make precommit` regenerates them from the counterfeiter annotations.
- Do NOT modify `pkg/publisher/task_publisher.go`'s existing behaviour; the decorator is additive.
</constraints>

<verification>
Run `ROOTDIR=/workspace make precommit` — must pass (its `generate` step regenerates `mocks/metrics.go` with the two new methods, then `test` compiles and runs the new specs).

Then confirm the new surface, which precommit alone does not spell out:
- `[ "$(grep -c 'agent_controller_kafka_publish_retry_total' pkg/metrics/metrics.go)" = 1 ]`
- `[ "$(grep -c 'agent_controller_kafka_publish_failure_total' pkg/metrics/metrics.go)" = 1 ]`
- `[ "$(grep -c 'func NewRetryTaskPublisher' pkg/publisher/retry_task_publisher.go)" = 1 ]`
- `[ "$(grep -c '1 \* time.Second' pkg/publisher/retry_task_publisher.go)" -ge 1 ]`
- `[ -f pkg/publisher/retry_task_publisher_test.go ]`
</verification>
