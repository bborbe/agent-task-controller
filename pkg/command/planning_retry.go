// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package command

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	lib "github.com/bborbe/agent"
	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
	domain "github.com/bborbe/vault-cli/pkg/domain"
	"github.com/golang/glog"
	"github.com/google/uuid"
	"gopkg.in/yaml.v3"

	gitclient "github.com/bborbe/agent-task-controller/pkg/gitrestclient"
	"github.com/bborbe/agent-task-controller/pkg/metrics"
	"github.com/bborbe/agent-task-controller/pkg/prcomment"
	"github.com/bborbe/agent-task-controller/pkg/result"
)

// maxControllerPlanningRetries is the frozen cap on controller-side planning
// retries. Invariant per spec 003 — never an env var, CLI flag, config field,
// or CRD field.
const maxControllerPlanningRetries = 3

//counterfeiter:generate -o ../../mocks/planning_retry_gate.go --fake-name PlanningRetryGate . PlanningRetryGate

// PlanningRetryGate decides whether a failed pr-review planning result should be
// retried controller-side. Handle inspects the incoming result and the on-disk
// task state and, when the retry gate matches, performs the retry write (counter
// bump + ## Progress append + phase reset + fresh task_identifier) and returns
// handled=true. When the gate does not match (non-pr-review, non-planning,
// success/skipped, no failure marker, or counter already at cap in this prompt),
// it returns handled=false and the caller falls through to the default
// WriteResult path.
type PlanningRetryGate interface {
	Handle(ctx context.Context, req lib.Task) (handled bool, err error)
}

// NewPlanningRetryGate constructs the gate.
func NewPlanningRetryGate(
	gitClient gitclient.GitClient,
	taskDir string,
	currentDateTime libtime.CurrentDateTimeGetter,
	prCommenter prcomment.PRCommenter,
	m metrics.Metrics,
) PlanningRetryGate {
	return &planningRetryGate{
		gitClient:       gitClient,
		taskDir:         taskDir,
		currentDateTime: currentDateTime,
		prCommenter:     prCommenter,
		metrics:         m,
	}
}

type planningRetryGate struct {
	gitClient       gitclient.GitClient
	taskDir         string
	currentDateTime libtime.CurrentDateTimeGetter
	prCommenter     prcomment.PRCommenter
	metrics         metrics.Metrics
}

func (g *planningRetryGate) Handle(ctx context.Context, req lib.Task) (handled bool, err error) {
	if !g.matchesRetryGate(req) {
		return false, nil
	}

	relPath, existingFrontmatter, findErr := result.FindTaskFilePath(
		ctx, g.gitClient, g.taskDir, req.TaskIdentifier,
	)
	if findErr != nil {
		return false, errors.Wrapf(ctx, findErr, "planning-retry: find task file")
	}
	if relPath == "" {
		glog.Warningf("planning-retry: task file not found for %s; skipping", req.TaskIdentifier)
		return false, nil
	}

	count, _ := existingFrontmatter.Int("planning_retry_count")
	if count < 0 {
		count = 0
	}

	if count >= maxControllerPlanningRetries {
		return g.escalate(ctx, req, relPath, existingFrontmatter)
	}

	reason := g.extractReason(string(req.Content))
	bump := false
	ts := g.currentDateTime.Now().UTC().Format("2006-01-02T15:04:05Z07:00")

	msg := "[agent-task-controller] planning retry " + strconv.Itoa(
		count+1,
	) + "/3 for task " + string(
		req.TaskIdentifier,
	)
	absPath := filepath.Join(g.gitClient.Path(), relPath)

	modifyErr := g.gitClient.AtomicReadModifyWriteAndCommitPush(
		ctx, absPath, g.buildRetryModifyFn(ctx, count, reason, ts, &bump), msg,
	)
	if modifyErr != nil {
		return false, errors.Wrapf(
			ctx,
			modifyErr,
			"planning-retry: write retry for task %s",
			req.TaskIdentifier,
		)
	}

	if bump {
		g.metrics.PlanningRetryTotal("retry").Inc()
		glog.Infof(
			"planning-retry: attempt %d/3 for task %s (reason=%q)",
			count+1,
			req.TaskIdentifier,
			reason,
		)
	}
	return true, nil
}

func (g *planningRetryGate) matchesRetryGate(req lib.Task) bool {
	taskType := req.Frontmatter.TaskType()
	if taskType != lib.TaskTypePRReview &&
		req.Frontmatter.Assignee() != lib.TaskAssignee("pr-reviewer-agent") {
		glog.V(3).
			Infof("planning-retry: task %s is not pr-review (task_type=%q, assignee=%q); skipping gate", req.TaskIdentifier, taskType, req.Frontmatter.Assignee())
		return false
	}
	phase := req.Frontmatter.Phase()
	if phase == nil || phase.String() != domain.TaskPhasePlanning.String() {
		glog.V(3).
			Infof("planning-retry: task %s phase is not planning; skipping gate", req.TaskIdentifier)
		return false
	}
	if !hasFailureMarker(string(req.Content)) {
		glog.V(2).
			Infof("planning-retry: no failure signal; skipping retry gate for task %s", req.TaskIdentifier)
		return false
	}
	return true
}

func hasFailureMarker(content string) bool {
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "Status: failed") {
			return true
		}
	}
	return false
}

func (g *planningRetryGate) extractReason(content string) string {
	afterMarker := false
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if afterMarker {
			if trimmed != "" {
				return sanitizeReason(trimmed)
			}
		}
		if strings.HasPrefix(trimmed, "Status: failed") {
			afterMarker = true
		}
	}
	return sanitizeReason(strings.TrimSpace(content))
}

func sanitizeReason(raw string) string {
	var builder strings.Builder
	for _, r := range raw {
		if r == '\r' || r == '\n' {
			builder.WriteRune(' ')
			continue
		}
		if r < 0x20 && r != ' ' {
			continue
		}
		builder.WriteRune(r)
	}
	sanitized := strings.TrimSpace(builder.String())
	if utf8.RuneCountInString(sanitized) > 200 {
		return string([]rune(sanitized)[:200])
	}
	return sanitized
}

func (g *planningRetryGate) buildRetryModifyFn(
	ctx context.Context,
	count int,
	reason string,
	ts string,
	bump *bool,
) func(current []byte) ([]byte, error) {
	return func(current []byte) ([]byte, error) {
		*bump = false
		fmStr, err := result.ExtractFrontmatter(ctx, current)
		if err != nil {
			return nil, errors.Wrapf(ctx, err, "extract frontmatter")
		}
		body, err := result.ExtractBody(ctx, current)
		if err != nil {
			return nil, errors.Wrapf(ctx, err, "extract body")
		}
		var fm lib.TaskFrontmatter
		if err := yaml.Unmarshal([]byte(fmStr), &fm); err != nil {
			return nil, errors.Wrapf(ctx, err, "unmarshal frontmatter")
		}

		onDiskCount, _ := fm.Int("planning_retry_count")
		if onDiskCount != count {
			return current, nil
		}

		retryLine := "retry " + strconv.Itoa(count+1) + "/3: " + reason + " at " + ts
		retryLineBullet := "- " + retryLine
		for _, line := range strings.Split(body, "\n") {
			if strings.TrimSpace(line) == retryLineBullet {
				return current, nil
			}
		}

		*bump = true

		fm["planning_retry_count"] = count + 1
		fm["phase"] = "planning"
		fm["task_identifier"] = uuid.NewString()

		newBody := appendProgressLine(body, retryLineBullet)

		marshaled, err := yaml.Marshal(map[string]any(fm))
		if err != nil {
			return nil, errors.Wrapf(ctx, err, "marshal frontmatter")
		}
		return []byte("---\n" + string(marshaled) + "---\n" + newBody), nil
	}
}

func (g *planningRetryGate) escalate(
	ctx context.Context,
	req lib.Task,
	relPath string,
	existingFrontmatter lib.TaskFrontmatter,
) (bool, error) {
	reason := g.extractReason(string(req.Content))
	ts := g.currentDateTime.Now().UTC().Format(time.RFC3339)

	msg := "[agent-task-controller] planning retry exhausted for task " + string(req.TaskIdentifier)
	absPath := filepath.Join(g.gitClient.Path(), relPath)

	firstEscalation := true
	modifyErr := g.gitClient.AtomicReadModifyWriteAndCommitPush(
		ctx, absPath, g.buildEscalationModifyFn(ctx, &firstEscalation, reason, ts), msg,
	)
	if modifyErr != nil {
		return false, errors.Wrapf(
			ctx,
			modifyErr,
			"planning-retry: escalation write for task %s",
			req.TaskIdentifier,
		)
	}

	if firstEscalation {
		commentBody := "Automated pr-review planning failed after 3 controller retries and 3 in-agent retries. Last error: " + reason + ". Please investigate " + relPath + "."
		if commentErr := g.prCommenter.PostComment(ctx, existingFrontmatter, commentBody); commentErr != nil {
			glog.Warningf(
				"planning-retry: github COMMENT post failed: task=%s err=%v",
				req.TaskIdentifier,
				commentErr,
			)
		}
		g.metrics.PlanningRetryTotal("exhausted").Inc()
		glog.Infof(
			"planning-retry: exhausted after 3 retries for task %s; escalated to human_review",
			req.TaskIdentifier,
		)
	}

	return true, nil
}

func (g *planningRetryGate) buildEscalationModifyFn(
	ctx context.Context,
	firstEscalation *bool,
	reason, ts string,
) func(current []byte) ([]byte, error) {
	return func(current []byte) ([]byte, error) {
		fmStr, err := result.ExtractFrontmatter(ctx, current)
		if err != nil {
			return nil, errors.Wrapf(ctx, err, "extract frontmatter")
		}
		body, err := result.ExtractBody(ctx, current)
		if err != nil {
			return nil, errors.Wrapf(ctx, err, "extract body")
		}
		var fm lib.TaskFrontmatter
		if err := yaml.Unmarshal([]byte(fmStr), &fm); err != nil {
			return nil, errors.Wrapf(ctx, err, "unmarshal frontmatter")
		}

		// Idempotency: check if terminal retry line already present
		for _, line := range strings.Split(body, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "retry 3/3:") {
				*firstEscalation = false
				break
			}
		}

		if *firstEscalation {
			body = appendProgressLine(body, "- retry 3/3: "+reason+" at "+ts)
		}

		fm["phase"] = "human_review"
		result.ClearAssigneeIfHumanReview(fm)

		marshaled, err := yaml.Marshal(map[string]any(fm))
		if err != nil {
			return nil, errors.Wrapf(ctx, err, "marshal frontmatter")
		}
		return []byte("---\n" + string(marshaled) + "---\n" + body), nil
	}
}

func appendProgressLine(body string, line string) string {
	if body != "" && !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	if strings.Contains(body, "## Progress") {
		return body + line + "\n"
	}
	return body + "## Progress\n\n" + line + "\n"
}
