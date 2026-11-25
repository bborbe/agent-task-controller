// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6@v6.12.2 -generate

// Metrics defines the interface for accessing Prometheus metrics.
// Use this interface in business logic packages to enable mock injection in tests.
//
//counterfeiter:generate -o ../../mocks/metrics.go --fake-name Metrics . Metrics
type Metrics interface {
	ScanCyclesTotal(outcome string) prometheus.Counter
	TasksPublishedTotal(outcome string) prometheus.Counter
	PlanningRetryTotal(result string) prometheus.Counter
	ResultsWrittenTotal(outcome string) prometheus.Counter
	GitPushTotal(outcome string) prometheus.Counter
	ConflictResolutionsTotal() prometheus.Counter
	FrontmatterCommandsTotal(operation, outcome string) prometheus.Counter
	// ReadbackMismatchTotal counts commands that reported success while their
	// record had not moved by the verification deadline. It is incremented by the
	// post-write readback comparison itself — never by a test hook and never by a
	// gauge set by hand — so a non-zero reading means a real acknowledgement
	// preceded a write that had not reached the store.
	ReadbackMismatchTotal(operation string) prometheus.Counter
	GitRestCallsTotal(operation, status string) prometheus.Counter
	KafkaConsumePausedTotal() prometheus.Counter
	CommandExpiredTotal(operation string) prometheus.Counter
	SkippedFilesTotal(reason SkipReason) prometheus.Counter
	UpdateGoStuckPlanningTasks() prometheus.Gauge
	HealthcheckParkedProbes() prometheus.Gauge
	TaskOldestAgeSeconds(vault string) prometheus.Gauge
	TaskAgeBucketTasks(vault string, bucket TaskAgeBucket) prometheus.Gauge
	// OpenTasks returns the open-task census vector, labelled by vault, phase and
	// task_type in that declaration order — the same order WithLabelValues and
	// DeleteLabelValues take. It is a *prometheus.GaugeVec rather than a single
	// prometheus.Gauge because the caller both Set()s every present pair and
	// DeleteLabelValues()s the pairs that vanished, and only the vector exposes both.
	OpenTasks() *prometheus.GaugeVec
}

// defaultMetrics implements Metrics using promauto-registered counters.
type defaultMetrics struct{}

var _ Metrics = &defaultMetrics{}

// New returns a new default Metrics implementation.
func New() Metrics {
	return &defaultMetrics{}
}

func (m *defaultMetrics) ScanCyclesTotal(outcome string) prometheus.Counter {
	return ScanCyclesTotal.WithLabelValues(outcome)
}

func (m *defaultMetrics) TasksPublishedTotal(outcome string) prometheus.Counter {
	return TasksPublishedTotal.WithLabelValues(outcome)
}

func (m *defaultMetrics) PlanningRetryTotal(result string) prometheus.Counter {
	return PlanningRetryTotal.WithLabelValues(result)
}

func (m *defaultMetrics) ResultsWrittenTotal(outcome string) prometheus.Counter {
	return ResultsWrittenTotal.WithLabelValues(outcome)
}

func (m *defaultMetrics) GitPushTotal(outcome string) prometheus.Counter {
	return GitPushTotal.WithLabelValues(outcome)
}

func (m *defaultMetrics) ConflictResolutionsTotal() prometheus.Counter {
	return ConflictResolutionsTotal.WithLabelValues()
}

func (m *defaultMetrics) FrontmatterCommandsTotal(operation, outcome string) prometheus.Counter {
	return FrontmatterCommandsTotal.WithLabelValues(operation, outcome)
}

func (m *defaultMetrics) ReadbackMismatchTotal(operation string) prometheus.Counter {
	return ReadbackMismatchTotal.WithLabelValues(operation)
}

func (m *defaultMetrics) GitRestCallsTotal(operation, status string) prometheus.Counter {
	return GitRestCallsTotal.WithLabelValues(operation, status)
}

func (m *defaultMetrics) KafkaConsumePausedTotal() prometheus.Counter {
	return KafkaConsumePausedTotal
}

func (m *defaultMetrics) CommandExpiredTotal(operation string) prometheus.Counter {
	return CommandExpiredTotal.WithLabelValues(operation)
}

func (m *defaultMetrics) SkippedFilesTotal(reason SkipReason) prometheus.Counter {
	return SkippedFilesTotal.WithLabelValues(reason.String())
}

func (m *defaultMetrics) UpdateGoStuckPlanningTasks() prometheus.Gauge {
	return UpdateGoStuckPlanningTasks
}

func (m *defaultMetrics) HealthcheckParkedProbes() prometheus.Gauge {
	return HealthcheckParkedProbes
}

func (m *defaultMetrics) TaskOldestAgeSeconds(vault string) prometheus.Gauge {
	return TaskOldestAgeSeconds.WithLabelValues(vault)
}

func (m *defaultMetrics) TaskAgeBucketTasks(vault string, bucket TaskAgeBucket) prometheus.Gauge {
	return TaskAgeBucketTasks.WithLabelValues(vault, bucket.String())
}

func (m *defaultMetrics) OpenTasks() *prometheus.GaugeVec {
	return OpenTasks
}

// ScanCyclesTotal counts scan cycle completions by result.
var ScanCyclesTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "agent_controller_scan_cycles_total",
		Help: "Total number of scan cycles completed.",
	},
	[]string{"result"},
)

// TasksPublishedTotal counts task events published by type.
var TasksPublishedTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "agent_controller_tasks_published_total",
		Help: "Total number of task events published.",
	},
	[]string{"type"},
)

// PlanningRetryTotal counts controller-side pr-review planning-retry gate outcomes
// by result ("retry" | "exhausted"). "passthrough" is intentionally NOT a label —
// the metric fires only when the retry gate matches (spec DB 7).
var PlanningRetryTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "agent_controller_planning_retry_total",
		Help: "Total number of controller-side pr-review planning-retry gate outcomes, by result.",
	},
	[]string{"result"},
)

// ResultsWrittenTotal counts result write attempts by outcome
// ("success" | "not_found" | "unowned" | "error"). `unowned` is a miss on a
// result carrying no target_vault, which every controller consumes by design —
// routine fan-out, not data loss. `not_found` is a miss on a result this
// controller was routed, and is the series AgentControllerResultNotFound alerts on.
var ResultsWrittenTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "agent_controller_results_written_total",
		Help: "Total number of task result write attempts.",
	},
	[]string{"result"},
)

// GitPushTotal counts git push attempts by outcome.
var GitPushTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "agent_controller_git_push_total",
		Help: "Total number of git push attempts.",
	},
	[]string{"result"},
)

// ConflictResolutionsTotal counts per-file conflict resolution attempts by outcome.
var ConflictResolutionsTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "agent_controller_conflict_resolutions_total",
		Help: "Total number of per-file conflict resolution attempts.",
	},
	[]string{"result"},
)

// FrontmatterCommandsTotal counts atomic frontmatter command executions
// by operation ("increment-frontmatter" | "update-frontmatter") and
// outcome ("success" | "error" | "not_found").
var FrontmatterCommandsTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "agent_controller_frontmatter_commands_total",
		Help: "Total number of atomic frontmatter commands processed, by operation and outcome.",
	},
	[]string{"operation", "outcome"},
)

// ReadbackMismatchTotal counts commands whose post-write readback found the
// record unchanged past its verification deadline.
//
// It exists because a command's success result is emitted when the write is
// accepted, and git-rest batches writes into a ~30 s commit window, so an
// acknowledgement precedes the record's arrival in the store. A non-zero reading
// means a command reported success and the record had still not moved by the
// deadline — the gap no other series exposes. It is incremented at the readback
// comparison, so the detector and the fix share one site.
var ReadbackMismatchTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "agent_controller_readback_mismatch_total",
		Help: "Total number of commands whose record had not moved by the readback verification deadline, by operation.",
	},
	[]string{"operation"},
)

// GitRestCallsTotal counts git-rest HTTP API calls by operation and outcome.
var GitRestCallsTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "agent_controller_git_rest_calls_total",
		Help: "Total number of git-rest HTTP API calls by operation and outcome.",
	},
	[]string{"op", "status"},
)

// KafkaConsumePausedTotal counts times a Kafka command executor blocked
// waiting for git-rest to become available (i.e. retry attempts after the first).
var KafkaConsumePausedTotal = promauto.NewCounter(prometheus.CounterOpts{
	Name: "controller_kafka_consume_paused_total",
	Help: "Total number of times Kafka consumption was paused waiting for git-rest.",
})

// CommandExpiredTotal counts commands dropped as expired, by operation.
//
// It is the counter the 60-minute window could not previously report: an expired command was
// dropped silently, so "a published create never reached the controller" had no series to read.
// A non-zero value is loss, not slowness — the command's effect never happened — so the alert is
// a level test on increase() over a window wide enough to cover a burst, not a rate over 5m.
//
// Two drops share it, because they are the same loss seen from two places: the expiry filter
// dropping a message that was never executed, and the park re-driver dropping a command whose
// re-drive window closed. Both mean the command's effect never happened.
var CommandExpiredTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "agent_controller_commands_expired_total",
		Help: "Total number of commands dropped as expired without ever executing, by operation.",
	},
	[]string{"operation"},
)

// availableCommandOperations is the closed set of command operations this controller consumes,
// used for counter pre-initialisation so every operation series exists at 0 before the first drop.
// An operation missing here still counts on first increment; it just starts at "no data".
var availableCommandOperations = []string{
	"create-task",
	"complete-task",
	"increment-frontmatter",
	"update-frontmatter",
	"update",
}

// SkipReason is the closed set of structured reasons the vault scanner skips a task file.
type SkipReason string

// String returns the Prometheus label value for the reason.
func (r SkipReason) String() string { return string(r) }

const (
	ReasonInvalidFrontmatter          SkipReason = "invalid_frontmatter"
	ReasonDuplicateFrontmatterInvalid SkipReason = "duplicate_frontmatter_invalid"
	ReasonEmptyStatus                 SkipReason = "empty_status"
	ReasonInjectTaskIdentifierFailed  SkipReason = "inject_task_identifier_failed"
	ReasonReadFailed                  SkipReason = "read_failed"
	ReasonAutoInjectDisabled          SkipReason = "auto_inject_disabled"
	ReasonRepairNotConverging         SkipReason = "repair_not_converging"
)

// AvailableSkipReasons is the full closed set of skip reasons, used for counter
// pre-initialisation so every reason series exists at 0 before the first skip.
var AvailableSkipReasons = []SkipReason{
	ReasonInvalidFrontmatter,
	ReasonDuplicateFrontmatterInvalid,
	ReasonEmptyStatus,
	ReasonInjectTaskIdentifierFailed,
	ReasonReadFailed,
	ReasonAutoInjectDisabled,
	ReasonRepairNotConverging,
}

// TaskAgeBucket is the closed set of bucket label values for TaskAgeBucketTasks.
type TaskAgeBucket string

// String returns the Prometheus label value for the bucket.
func (b TaskAgeBucket) String() string { return string(b) }

const (
	TaskAgeBucketLt1d    TaskAgeBucket = "lt_1d"
	TaskAgeBucket1d7d    TaskAgeBucket = "1d_7d"
	TaskAgeBucketGt7d    TaskAgeBucket = "gt_7d"
	TaskAgeBucketUndated TaskAgeBucket = "undated"
)

// AvailableTaskAgeBuckets is the full closed set of bucket values, used by the
// scanner to publish every bucket on every pass so each series exists at 0.
var AvailableTaskAgeBuckets = []TaskAgeBucket{
	TaskAgeBucketLt1d,
	TaskAgeBucket1d7d,
	TaskAgeBucketGt7d,
	TaskAgeBucketUndated,
}

// SkippedFilesTotal counts vault task files the scanner skipped during a scan cycle,
// labelled by the structured reason for the skip. A non-zero value on any label
// indicates operator-actionable vault health issues (broken frontmatter, empty status,
// unreadable files, injection failures). For every label EXCEPT repair_not_converging,
// a stuck broken file is re-read every cycle and keeps its label rate-positive until
// repaired, so rate(agent_controller_vault_scanner_skipped_files_total[5m]) > 0 is the
// detection query.
//
// repair_not_converging is the documented exception: it is an edge, not a level. The
// scanner short-circuits on the file's content hash, so a file whose repair was halted is
// not re-processed while its bytes are unchanged, and the counter increments exactly once
// per distinct file content. Detect it with:
//
//	increase(agent_controller_vault_scanner_skipped_files_total{reason="repair_not_converging"}[6h]) > 0
//
// A short-window rate is wrong for this label (it goes flat while the file stays broken),
// and a bare level check is wrong too (it survives forever after the file is repaired and
// resets on pod restart).
//
// The closed set of reason values is declared as constants above and pre-initialised in init().
var SkippedFilesTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "agent_controller_vault_scanner_skipped_files_total",
		Help: "Total number of vault task files the scanner skipped during a scan cycle, by reason. Increments once per skipped file per cycle for every reason except repair_not_converging, which is an edge that fires once per distinct file content and then goes flat while the file stays broken; see the doc comment for its increase(...[6h]) detection query.",
	},
	[]string{"reason"},
)

// UpdateGoStuckPlanningTasks is the number of github-update-go tasks sitting at
// phase: planning whose last job started more than 24 hours ago and whose body
// carries a `## Failure` entry.
//
// It is a level, not an edge: the metrics pass recomputes it and Set()s the result,
// so the series falls back to 0 as soon as the queue drains. That is what makes it
// alertable — `agent_controller_update_go_stuck_planning_tasks >= 10` is true only
// while at least ten tasks are actually stuck, unlike a counter which would stay
// positive forever after the backlog was cleared.
//
// The writer is the metrics ticker and not the scan cycle because the scan cycle is
// dead on a trigger-only controller (POLL_INTERVAL=0s, which every controller runs),
// so a gauge set there kept its init value forever and the series read 0 on every
// controller.
//
// The 24 h age comes from the task's `job_started_at` frontmatter field; a task
// with no recorded job start is not counted, because "stuck" is defined by a run
// that began and never finished rather than by the file merely existing.
var UpdateGoStuckPlanningTasks = promauto.NewGauge(
	prometheus.GaugeOpts{
		Name: "agent_controller_update_go_stuck_planning_tasks",
		Help: "Number of github-update-go tasks at phase planning with a job started more than 24h ago and a ## Failure entry.",
	},
)

// HealthcheckParkedProbes is the number of healthcheck probe records that are
// parked: neither their phase nor their status is terminal, and no job is running
// them.
//
// It is the sibling of UpdateGoStuckPlanningTasks, not a widening of it, and the two
// watch disjoint populations. That gauge measures a run that started and stalled —
// `task_type: github-update-go` at `phase: planning` with a job older than 24 h and a
// `## Failure` body. This one measures a record that can never be re-triggered once
// it parks: `task_type: healthcheck` at ANY non-terminal phase and ANY non-terminal
// status, with age and failure markers deliberately not read, because every row it
// measures is permanently non-advancing and those clauses would carry no signal.
//
// It is a level, not an edge: the metrics pass recomputes it and Set()s the result, so
// the series falls back to 0 once every probe record is terminal. That is what makes
// it alertable — the value is only positive while a probe is actually parked. The
// writer is the metrics ticker and not the scan cycle because the scan cycle is dead
// on a trigger-only controller (POLL_INTERVAL=0s, which every controller runs), so a
// gauge set there would keep its init value forever.
var HealthcheckParkedProbes = promauto.NewGauge(
	prometheus.GaugeOpts{
		Name: "agent_controller_healthcheck_parked_probes",
		Help: "Number of healthcheck probe records parked non-terminal (phase and status both non-terminal, no current_job). Sibling of agent_controller_update_go_stuck_planning_tasks, which instead watches task_type github-update-go at phase planning with a job older than 24h.",
	},
)

// TaskOldestAgeSeconds is the age in seconds of the oldest dated non-terminal
// task record, labelled by vault.
//
// It is a level, not a counter: the dedicated metrics ticker recomputes it and
// Set()s the result, so the series falls back to 0 as soon as the last dated
// record turns terminal. That is what makes a threshold expression alertable —
// the alert rule in bborbe/nuke compares it against the 7-day threshold
// `> 604800` seconds.
var TaskOldestAgeSeconds = promauto.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "agent_controller_task_oldest_age_seconds",
		Help: "Age in seconds of the oldest dated non-terminal task record, by vault.",
	},
	[]string{"vault"},
)

// TaskAgeBucketTasks is the number of non-terminal task records per age bucket,
// labelled by vault and bucket.
//
// It is a level, not a counter: every successful recompute Set()s all four
// bucket series, including the empty ones, so a scrape never sees a missing
// series after a pass and each bucket reads 0 when it is empty.
var TaskAgeBucketTasks = promauto.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "agent_controller_task_age_bucket_tasks",
		Help: "Number of non-terminal task records per age bucket, by vault and bucket.",
	},
	[]string{"vault", "bucket"},
)

// OpenTasks is the number of open task records in each (phase, task_type) pair
// of this controller's own vault.
//
// It is a level, not a counter: the dedicated metrics ticker recomputes it from
// scratch on every pass and Set()s each pair's count, so the value is the
// board's count rather than a running total, and two passes over an unchanged
// board publish the same value.
//
// A pair that empties disappears from the exposition entirely rather than
// lingering at 0: the recompute deletes the series for every pair the previous
// pass published and this pass did not see, scoped to this controller's own
// vault label so another controller's series are never touched.
//
// There is no init() pre-initialisation. The pair set is data-dependent, so no
// fixed series can be seeded at registration; a fresh pod publishes no census
// series until its first successful pass.
var OpenTasks = promauto.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "agent_controller_open_tasks",
		Help: "Number of open task records per (phase, task_type) pair, by vault. A pair that empties disappears from the exposition rather than lingering at 0.",
	},
	[]string{"vault", "phase", "task_type"},
)

func init() {
	ScanCyclesTotal.WithLabelValues("changes").Add(0)
	ScanCyclesTotal.WithLabelValues("no_changes").Add(0)
	ScanCyclesTotal.WithLabelValues("error").Add(0)
	ScanCyclesTotal.WithLabelValues("redrive").Add(0)

	TasksPublishedTotal.WithLabelValues("changed").Add(0)
	TasksPublishedTotal.WithLabelValues("deleted").Add(0)
	TasksPublishedTotal.WithLabelValues("redrive").Add(0)

	ResultsWrittenTotal.WithLabelValues("success").Add(0)
	ResultsWrittenTotal.WithLabelValues("not_found").Add(0)
	ResultsWrittenTotal.WithLabelValues("unowned").Add(0)
	ResultsWrittenTotal.WithLabelValues("error").Add(0)

	PlanningRetryTotal.WithLabelValues("retry").Add(0)
	PlanningRetryTotal.WithLabelValues("exhausted").Add(0)

	GitPushTotal.WithLabelValues("success").Add(0)
	GitPushTotal.WithLabelValues("retry_success").Add(0)
	GitPushTotal.WithLabelValues("conflict_resolved").Add(0)
	GitPushTotal.WithLabelValues("error").Add(0)

	ConflictResolutionsTotal.WithLabelValues("success").Add(0)
	ConflictResolutionsTotal.WithLabelValues("error").Add(0)

	for _, op := range []string{"increment-frontmatter", "update-frontmatter", "complete-task"} {
		for _, outcome := range []string{"success", "error", "not_found"} {
			FrontmatterCommandsTotal.WithLabelValues(op, outcome).Add(0)
		}
	}

	for _, op := range []string{"get", "post", "delete", "list", "readiness"} {
		for _, status := range []string{"success", "error"} {
			GitRestCallsTotal.WithLabelValues(op, status).Add(0)
		}
	}

	for _, reason := range AvailableSkipReasons {
		SkippedFilesTotal.WithLabelValues(reason.String()).Add(0)
	}

	for _, operation := range availableCommandOperations {
		CommandExpiredTotal.WithLabelValues(operation).Add(0)
	}

	// The readback verifier labels by the same closed operation set, so every
	// series exists at 0 before the first mismatch. An alert query then reads 0
	// rather than "no data" on a freshly started pod — which matters more here
	// than for a drop counter, because "no data" and "nothing mismatched" are the
	// two readings this series exists to tell apart.
	for _, operation := range availableCommandOperations {
		ReadbackMismatchTotal.WithLabelValues(operation).Add(0)
	}

	// A gauge has no label combinations to pre-initialise, but setting it to 0
	// publishes the series before the first scan completes, so an alert query
	// reads 0 rather than "no data" on a freshly started pod.
	UpdateGoStuckPlanningTasks.Set(0)
	HealthcheckParkedProbes.Set(0)
}
