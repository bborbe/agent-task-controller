// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package command

import (
	"context"
	"strings"

	task "github.com/bborbe/agent/command/task"
	libtime "github.com/bborbe/time"
	"github.com/golang/glog"

	"github.com/bborbe/agent-task-controller/pkg/vaultapi"
)

const (
	// updateGoTaskType is the task_type whose queue this collapses. Only the plain
	// update task counts: `github-update-go-decision` rows are operator questions
	// and are never superseded by an update run.
	updateGoTaskType = "github-update-go"

	// updateGoTitlePrefix is the emitter's title shape
	// (`Update Go <owner>-<repo> <sha>`, see /github-update-go-repo-trigger § 6).
	updateGoTitlePrefix = "Update Go "
)

// supersedePriorUpdateGoTasks collapses the github-update-go queue to one open task
// per repo. After a new update task is materialized, every other still-open update
// task for the same repo is transitioned to aborted with a `superseded_by`
// back-pointer to the new one, capped at k candidates.
//
// This is the task-side counterpart to the watcher's `openUpdatePRGate`: that gate
// drops a repo with an open update **PR** from the emit cycle, but nothing guards
// the **task** side, so a repo that never gets as far as opening a PR accumulates
// one task per commit. Measured 2026-10-20: 309 open update-go tasks across 68
// repos, 217 of them excess.
//
// The candidate set is a typed listing question, not a glob: status and task_type
// are pushed to the API, and the repo is re-checked against each returned task's
// own frontmatter, so a page whose title merely looks like an update task is
// never aborted. k bounds how many OPEN priors this call aborts — the API
// returns only open rows, so no budget is spent on the closed history the old
// glob scan had to read past.
//
// Best-effort: a list or write error is logged and swallowed, and the
// already-written new task is never rolled back.
func supersedePriorUpdateGoTasks(
	ctx context.Context,
	client vaultapi.Client,
	currentDateTime libtime.CurrentDateTimeGetter,
	k int,
	cmd task.CreateCommand,
	newRelPath string,
) {
	if taskType, _ := cmd.Frontmatter.String("task_type"); taskType != updateGoTaskType {
		return
	}
	repo, _ := cmd.Frontmatter.String("repo")
	if repo == "" {
		glog.V(3).Infof("auto-supersede(update-go): %s has no repo, skipping", cmd.TaskIdentifier)
		return
	}
	prefix := updateGoTitlePrefix + strings.ReplaceAll(repo, "/", "-") + " "
	priors, err := client.ListTasks(ctx, vaultapi.Filter{
		Status:    "in_progress",
		TaskTypes: []string{updateGoTaskType},
	})
	if err != nil {
		glog.Warningf("auto-supersede(update-go): list candidates failed for %q: %v", repo, err)
		return
	}
	openFound := 0
	for _, prior := range priors {
		if err := ctx.Err(); err != nil {
			return
		}
		if openFound >= k {
			break
		}
		// The task just created is itself an open update task for this repo; it is
		// the back-pointer target, never a candidate.
		if prior.ID == string(cmd.TaskIdentifier) {
			continue
		}
		if !hasTitlePrefix(prior, prefix) {
			continue
		}
		if frontmatterString(prior, "repo") != repo {
			continue
		}
		openFound++
		// setCreatedBy=false: an update-go prior was emitted by the update-go
		// watcher, not the recurring publisher, so stamping
		// created_by: recurring-task-creator would replace its real attribution
		// with a false one. Same four frozen fields the build-fix retire path
		// writes, and the same reason.
		transitionPriorTyped(
			ctx,
			client,
			currentDateTime,
			prior,
			newRelPath,
			cmd.TaskIdentifier,
			false,
		)
	}
}
