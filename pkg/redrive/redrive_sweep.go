// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package redrive

import (
	"context"
	"sync"
	"time"

	lib "github.com/bborbe/agent"
	"github.com/bborbe/cqrs/base"
	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
	"github.com/bborbe/vault-cli/pkg/domain"
	"github.com/golang/glog"

	"github.com/bborbe/agent-task-controller/pkg/gitrestclient"
	"github.com/bborbe/agent-task-controller/pkg/metrics"
	"github.com/bborbe/agent-task-controller/pkg/publisher"
	"github.com/bborbe/agent-task-controller/pkg/scanner"
)

// redriveTriggerPhases is the phase allow-list a task must be in to be re-driven.
// Mirrors the executor's default trigger phases — anything past planning is
// already agent-runnable, so re-emitting its TaskUpdated is what the executor
// is waiting on.
var redriveTriggerPhases = map[domain.TaskPhase]struct{}{
	domain.TaskPhasePlanning:  {},
	domain.TaskPhaseExecution: {},
	domain.TaskPhaseAIReview:  {},
}

//counterfeiter:generate -o ../../mocks/redrive_sweep.go --fake-name RedriveSweep . RedriveSweep

// RedriveSweep is a background loop that force re-publishes TaskUpdated events
// for eligible tasks that have never spawned a Job. The change-detecting scanner
// only emits an event when a task file's content hash changes; a task that sits
// unchanged since creation (e.g. a stub a collector wrote and then went quiet
// on) never produces a new event, so the executor — purely event-driven — never
// re-evaluates it. This sweep is the re-drive path: a task matching the
// executor's spawn filter but with no current_job (never spawned) gets its
// TaskUpdated re-emitted on a schedule, and the executor's own guards (active
// job, trigger cap, grace window) decide whether a spawn actually happens.
type RedriveSweep interface {
	// Run blocks until ctx is cancelled, sweeping once per interval.
	Run(ctx context.Context) error
	// SweepOnce performs a single sweep pass. Exposed for unit tests so they do
	// not have to manage tickers. Returns an error only on git/pull failures;
	// per-task errors are logged.
	SweepOnce(ctx context.Context) error
}

// NewRedriveSweep creates a RedriveSweep. The backoff for re-emitting the same
// task is the sweep interval itself, so an eligible task that cannot spawn
// (executor down, config missing) produces at most one event per interval
// rather than a flood.
func NewRedriveSweep(
	gitClient gitrestclient.GitClient,
	publisher publisher.TaskPublisher,
	taskDir string,
	branch base.Branch,
	interval time.Duration,
	currentDateTime libtime.CurrentDateTimeGetter,
	m metrics.Metrics,
) RedriveSweep {
	return &redriveSweep{
		gitClient:       gitClient,
		publisher:       publisher,
		taskDir:         taskDir,
		branch:          branch,
		interval:        interval,
		backoff:         interval,
		currentDateTime: currentDateTime,
		metrics:         m,
		published:       make(map[lib.TaskIdentifier]time.Time),
	}
}

type redriveSweep struct {
	gitClient       gitrestclient.GitClient
	publisher       publisher.TaskPublisher
	taskDir         string
	branch          base.Branch
	interval        time.Duration
	backoff         time.Duration
	currentDateTime libtime.CurrentDateTimeGetter
	metrics         metrics.Metrics
	publishedMu     sync.Mutex
	published       map[lib.TaskIdentifier]time.Time
}

func (s *redriveSweep) Run(ctx context.Context) error {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	glog.V(2).
		Infof("redrive sweep started interval=%s task_dir=%s branch=%s", s.interval, s.taskDir, s.branch)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := s.SweepOnce(ctx); err != nil {
				// Per-tick failures (transient git errors) must not kill the
				// sweeper goroutine — that would tear down the controller via
				// service.Run. Log and continue.
				glog.Errorf("redrive sweep tick: %v", err)
			}
		}
	}
}

// SweepOnce pulls the vault, scans every task file, and re-publishes a
// TaskUpdated event for each eligible never-spawned task. It bypasses the
// scanner's hash cache on purpose: re-drive exists precisely for tasks whose
// file hash has not changed since creation.
func (s *redriveSweep) SweepOnce(ctx context.Context) error {
	s.metrics.ScanCyclesTotal("redrive").Inc()
	if err := s.gitClient.Pull(ctx); err != nil {
		return errors.Wrapf(ctx, err, "redrive sweep git pull")
	}
	paths, err := s.gitClient.ListFiles(ctx, s.taskDir+"/*.md")
	if err != nil {
		return errors.Wrapf(ctx, err, "redrive sweep list tasks")
	}
	now := s.currentDateTime.Now().Time()

	s.publishedMu.Lock()
	defer s.publishedMu.Unlock()
	// Prune entries whose backoff has elapsed — they no longer suppress a
	// re-emit, and pruning keeps the map bounded as tasks spawn and leave the
	// eligible set while keeping a stale entry.
	for id, last := range s.published {
		if now.Sub(last) >= s.backoff {
			delete(s.published, id)
		}
	}
	for _, relPath := range paths {
		content, err := s.gitClient.ReadFile(ctx, relPath)
		if err != nil {
			glog.Warningf("redrive sweep: read %s: %v", relPath, err)
			continue
		}
		task := scanner.ParseTask(ctx, content)
		if task == nil || !s.eligible(*task) {
			continue
		}
		if last, seen := s.published[task.TaskIdentifier]; seen && now.Sub(last) < s.backoff {
			continue
		}
		if err := s.publisher.PublishChanged(ctx, *task); err != nil {
			glog.Errorf("redrive sweep: publish %s: %v", task.TaskIdentifier, err)
			continue
		}
		s.published[task.TaskIdentifier] = now
		s.metrics.TasksPublishedTotal("redrive").Inc()
		glog.V(2).Infof(
			"redrive sweep: re-published %s (phase=%v assignee=%s)",
			task.TaskIdentifier, task.Frontmatter.Phase(), task.Frontmatter.Assignee(),
		)
	}
	return nil
}

// eligible reports whether a task should be re-driven: it passes the executor's
// spawn filters (status, phase, assignee, stage) and has never spawned a Job
// (no current_job — the executor's spawn notification writes it at first spawn).
func (s *redriveSweep) eligible(task lib.Task) bool {
	if task.Frontmatter.Status() != domain.TaskStatusInProgress {
		return false
	}
	phase := task.Frontmatter.Phase()
	if phase == nil {
		return false
	}
	if _, ok := redriveTriggerPhases[*phase]; !ok {
		return false
	}
	if task.Frontmatter.Assignee() == "" {
		return false
	}
	if task.Frontmatter.Stage() != string(s.branch) {
		return false
	}
	if task.Frontmatter.CurrentJob() != "" {
		return false
	}
	return true
}
