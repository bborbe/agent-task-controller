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

	"github.com/bborbe/agent-task-controller/pkg/metrics"
	"github.com/bborbe/agent-task-controller/pkg/prcomment"
	"github.com/bborbe/agent-task-controller/pkg/result"
	"github.com/bborbe/agent-task-controller/pkg/vaultapi"
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

// NewPlanningRetryGate constructs the gate. vaultName is the controller's
// VAULT_NAME, used to heal legacy task files that lack a target_vault stamp.
// taskDir is only used to render the human-facing PR comment, which names the
// task by a path-shaped string; the write itself addresses the task by
// identifier.
func NewPlanningRetryGate(
	client vaultapi.Client,
	taskDir string,
	vaultName string,
	currentDateTime libtime.CurrentDateTimeGetter,
	prCommenter prcomment.PRCommenter,
	m metrics.Metrics,
	humans result.HumanAssignees,
) PlanningRetryGate {
	return &planningRetryGate{
		client:          client,
		taskDir:         taskDir,
		vaultName:       vaultName,
		currentDateTime: currentDateTime,
		prCommenter:     prCommenter,
		metrics:         m,
		humanAssignees:  humans,
	}
}

type planningRetryGate struct {
	client          vaultapi.Client
	taskDir         string
	vaultName       string
	currentDateTime libtime.CurrentDateTimeGetter
	prCommenter     prcomment.PRCommenter
	metrics         metrics.Metrics
	// humanAssignees names the assignee values that are human owners, so the
	// human_review chokepoint retains them instead of clearing them.
	humanAssignees result.HumanAssignees
}

func (g *planningRetryGate) Handle(ctx context.Context, req lib.Task) (handled bool, err error) {
	if !g.matchesRetryGate(req) {
		return false, nil
	}

	task, err := g.client.ReadTask(ctx, string(req.TaskIdentifier))
	if err != nil {
		if errors.Is(err, vaultapi.ErrNotFound) {
			glog.Warningf(
				"planning-retry: task file not found for %s; skipping",
				req.TaskIdentifier,
			)
			return false, nil
		}
		return false, errors.Wrapf(ctx, err, "planning-retry: read task")
	}

	existingFrontmatter := lib.TaskFrontmatter(task.Frontmatter)

	phase := existingFrontmatter.Phase()
	phaseValue := ""
	if phase != nil {
		phaseValue = phase.String()
	}
	// phaseValue is "" when phase is nil, and "" never equals planning, so the
	// single comparison covers the absent-phase case too.
	if phaseValue != domain.TaskPhasePlanning.String() {
		glog.V(3).
			Infof("planning-retry: task %s on-disk phase=%s; skipping gate", req.TaskIdentifier, phaseValue)
		return false, nil
	}

	count, _ := existingFrontmatter.Int("planning_retry_count")
	if count < 0 {
		count = 0
	}

	if count >= maxControllerPlanningRetries {
		return g.escalate(ctx, req, task)
	}

	// Re-read before writing: the typed PATCH carries no compare-and-swap of its
	// own, so this second read is the baseline the retry change is checked
	// against. A task that moved between the two reads must not be forced back
	// to planning.
	current, err := g.client.ReadTask(ctx, task.ID)
	if err != nil {
		if errors.Is(err, vaultapi.ErrNotFound) {
			glog.Warningf(
				"planning-retry: task file not found for %s; skipping",
				req.TaskIdentifier,
			)
			return false, nil
		}
		return false, errors.Wrapf(ctx, err, "planning-retry: re-read task")
	}

	reason := g.extractReason(string(req.Content))
	ts := g.currentDateTime.Now().UTC().Format(time.RFC3339)

	fields, newBody, changed := g.buildRetryChange(current, count, reason, ts)
	if !changed {
		return true, nil
	}

	if _, err := g.client.PatchTaskWithBody(ctx, current.ID, fields, newBody); err != nil {
		return false, errors.Wrapf(
			ctx,
			err,
			"planning-retry: write retry for task %s",
			req.TaskIdentifier,
		)
	}

	g.metrics.PlanningRetryTotal("retry").Inc()
	glog.V(2).Infof(
		"planning-retry: attempt %d/3 for task %s (reason=%q)",
		count+1,
		req.TaskIdentifier,
		reason,
	)
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

// buildRetryChange computes the field set and body for one retry write against
// the freshly-read task. count is the baseline Handle decided on: a task whose
// counter or phase moved since that read produces changed=false, so a stale read
// never forces a task back into planning. The same read supplies the body, so
// the duplicate-bullet guard sees the bytes the write is about to replace.
func (g *planningRetryGate) buildRetryChange(
	current *vaultapi.Task,
	count int,
	reason string,
	ts string,
) (fields map[string]any, body string, changed bool) {
	fm := lib.TaskFrontmatter(current.Frontmatter)

	onDiskCount, _ := fm.Int("planning_retry_count")
	if onDiskCount != count {
		return nil, "", false
	}
	// Re-check the phase against THIS read, not the one Handle saw: the task may
	// have moved past planning between the two reads, and a stale read must never
	// force it back to planning.
	if phase := fm.Phase(); phase == nil ||
		phase.String() != domain.TaskPhasePlanning.String() {
		return nil, "", false
	}

	retryLine := "retry " + strconv.Itoa(count+1) + "/3: " + reason + " at " + ts
	retryLineBullet := "- " + retryLine
	for _, line := range strings.Split(current.Body, "\n") {
		if strings.TrimSpace(line) == retryLineBullet {
			return nil, "", false
		}
	}

	fields = map[string]any{
		"planning_retry_count": count + 1,
		"phase":                "planning",
		"task_identifier":      uuid.NewString(),
	}
	g.healTargetVault(fields, fm)

	return fields, appendProgressLine(current.Body, retryLineBullet), true
}

func (g *planningRetryGate) escalate(
	ctx context.Context,
	req lib.Task,
	task *vaultapi.Task,
) (bool, error) {
	reason := g.extractReason(string(req.Content))
	ts := g.currentDateTime.Now().UTC().Format(time.RFC3339)

	// Re-read for the same reason buildRetryChange does: the typed PATCH carries
	// no compare-and-swap, so the write is checked against a read taken here.
	current, err := g.client.ReadTask(ctx, task.ID)
	if err != nil {
		if errors.Is(err, vaultapi.ErrNotFound) {
			glog.Warningf(
				"planning-retry: task file not found for %s; skipping",
				req.TaskIdentifier,
			)
			return false, nil
		}
		return false, errors.Wrapf(ctx, err, "planning-retry: re-read task for escalation")
	}

	fields, newBody, firstEscalation, changed := g.buildEscalationChange(current, reason, ts)
	if !changed {
		return true, nil
	}

	if _, err := g.client.PatchTaskWithBody(ctx, current.ID, fields, newBody); err != nil {
		return false, errors.Wrapf(
			ctx,
			err,
			"planning-retry: escalation write for task %s",
			req.TaskIdentifier,
		)
	}

	if firstEscalation {
		commentBody := "Automated pr-review planning failed after 3 controller retries and 3 in-agent retries. Last error: " + reason + ". Please investigate " + g.taskRelPath(
			current,
		) + "."
		if commentErr := g.prCommenter.PostComment(
			ctx,
			lib.TaskFrontmatter(current.Frontmatter),
			commentBody,
		); commentErr != nil {
			glog.Warningf(
				"planning-retry: github COMMENT post failed: task=%s err=%v",
				req.TaskIdentifier,
				commentErr,
			)
		}
		g.metrics.PlanningRetryTotal("exhausted").Inc()
		glog.V(2).Infof(
			"planning-retry: exhausted after 3 retries for task %s; escalated to human_review",
			req.TaskIdentifier,
		)
	}

	return true, nil
}

// buildEscalationChange computes the field set and body for the escalation
// write. firstEscalation is false when the terminal retry bullet is already in
// the body, which keeps the caller from posting the comment or bumping the
// "exhausted" metric for a write that repeats an earlier one.
func (g *planningRetryGate) buildEscalationChange(
	current *vaultapi.Task,
	reason, ts string,
) (fields map[string]any, body string, firstEscalation bool, changed bool) {
	fm := lib.TaskFrontmatter(current.Frontmatter)

	// Re-check the phase against THIS read: a task that moved past planning
	// between the two reads must not be escalated back into the human queue.
	if phase := fm.Phase(); phase == nil ||
		phase.String() != domain.TaskPhasePlanning.String() {
		return nil, "", false, false
	}

	firstEscalation = true
	// Idempotency: check if terminal retry line already present
	for _, line := range strings.Split(current.Body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "retry 3/3:") {
			firstEscalation = false
			break
		}
	}

	body = current.Body
	if firstEscalation {
		body = appendProgressLine(body, "- retry 3/3: "+reason+" at "+ts)
	}

	// The phase is set on the map BEFORE the assignee decision: the chokepoint
	// only clears an assignee whose phase is already human_review.
	fm["phase"] = "human_review"
	fields = map[string]any{"phase": "human_review"}
	before, hadBefore := fm["assignee"]
	result.ClearAssigneeIfHumanReview(fm, g.humanAssignees)
	after, hasAfter := fm["assignee"]
	if hadBefore != hasAfter || before != after {
		fields["assignee"] = fm["assignee"]
	}
	// The chokepoint captures the outgoing assignee into previous_assignee before
	// clearing assignee (result.clearAssignee). A field PATCH writes only the keys
	// it names, so the capture has to be named here too — the whole-document
	// rewrite this replaced carried it implicitly, and dropping it loses the only
	// record of who held the task before it was parked.
	if prev, ok := fm["previous_assignee"]; ok {
		fields["previous_assignee"] = prev
	}

	g.healTargetVault(fields, fm)

	return fields, body, firstEscalation, true
}

// healTargetVault adds the target_vault stamp to a patch whose task file lacks
// it. The original heal-on-write mutated the whole frontmatter document before
// rewriting it; a field PATCH only writes the keys it names, so the stamp has to
// be named explicitly. A present value (any value) is never overridden — the
// same rule result.HealTargetVault enforces.
func (g *planningRetryGate) healTargetVault(fields map[string]any, fm lib.TaskFrontmatter) {
	if _, ok := fm["target_vault"]; !ok {
		fields["target_vault"] = g.vaultName
	}
}

// taskRelPath renders the vault-relative path a human reading the escalation PR
// comment can act on. The typed read reports no path, so it is derived from the
// task's title — the comment is for a human, and an identifier is less useful
// there than a path.
func (g *planningRetryGate) taskRelPath(t *vaultapi.Task) string {
	if g.taskDir == "" {
		return t.Title + ".md"
	}
	return filepath.Join(g.taskDir, t.Title+".md")
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
