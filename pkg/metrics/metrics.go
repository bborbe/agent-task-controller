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
	GitRestCallsTotal(operation, status string) prometheus.Counter
	KafkaConsumePausedTotal() prometheus.Counter
	CommandExpiredTotal(operation string) prometheus.Counter
	SkippedFilesTotal(reason SkipReason) prometheus.Counter
	UpdateGoStuckPlanningTasks() prometheus.Gauge
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

// CommandExpiredTotal counts commands dropped by the expiry filter, by operation.
//
// It is the counter the 60-minute window could not previously report: an expired command was
// dropped silently, so "a published create never reached the controller" had no series to read.
// A non-zero value is loss, not slowness — the command's effect never happened — so the alert is
// a level test on increase() over a window wide enough to cover a burst, not a rate over 5m.
var CommandExpiredTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "agent_controller_commands_expired_total",
		Help: "Total number of commands dropped as expired before execution, by operation.",
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
// It is a level, not an edge: the scanner recomputes it once per scan cycle and
// Set()s the result, so the series falls back to 0 as soon as the queue drains.
// That is what makes it alertable — `agent_controller_update_go_stuck_planning_tasks >= 10`
// is true only while at least ten tasks are actually stuck, unlike a counter which
// would stay positive forever after the backlog was cleared.
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

func init() {
	ScanCyclesTotal.WithLabelValues("changes").Add(0)
	ScanCyclesTotal.WithLabelValues("no_changes").Add(0)
	ScanCyclesTotal.WithLabelValues("error").Add(0)

	TasksPublishedTotal.WithLabelValues("changed").Add(0)
	TasksPublishedTotal.WithLabelValues("deleted").Add(0)

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

	// A gauge has no label combinations to pre-initialise, but setting it to 0
	// publishes the series before the first scan completes, so an alert query
	// reads 0 rather than "no data" on a freshly started pod.
	UpdateGoStuckPlanningTasks.Set(0)
}
