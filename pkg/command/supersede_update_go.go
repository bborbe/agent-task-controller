// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package command

import (
	"context"
	"path/filepath"
	"strings"

	task "github.com/bborbe/agent/command/task"
	libtime "github.com/bborbe/time"
	"github.com/golang/glog"

	gitclient "github.com/bborbe/agent-task-controller/pkg/gitrestclient"
	result "github.com/bborbe/agent-task-controller/pkg/result"
)

const (
	// updateGoTaskType is the task_type whose queue this collapses. Only the plain
	// update task counts: `github-update-go-decision` rows are operator questions
	// and are never superseded by an update run.
	updateGoTaskType = "github-update-go"

	// updateGoTitlePrefix is the emitter's filename shape
	// (`Update Go <owner>-<repo> <sha>.md`, see /github-update-go-repo-trigger § 6).
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
// Best-effort: list/read/parse/write errors on any single file are logged and
// swallowed, and the already-written new task is never rolled back.
func supersedePriorUpdateGoTasks(
	ctx context.Context,
	gitClient gitclient.GitClient,
	taskDir string,
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
	titles, err := listUpdateGoCandidateTitles(ctx, gitClient, taskDir, repo)
	if err != nil {
		glog.Warningf("auto-supersede(update-go): list candidates failed for %q: %v", repo, err)
		return
	}
	inspected := 0
	for _, title := range titles {
		if err := ctx.Err(); err != nil {
			return
		}
		if inspected >= k {
			break
		}
		if strings.ContainsAny(title, "/\\") {
			glog.Warningf(
				"auto-supersede(update-go): candidate title %q contains path separator; skipping",
				title,
			)
			continue
		}
		candidateRelPath := filepath.Join(taskDir, title+".md")
		if candidateRelPath == newRelPath {
			continue
		}
		inspected++
		content, err := readPriorForSupersede(ctx, gitClient, candidateRelPath, cmd.TaskIdentifier)
		if err != nil || content == nil {
			continue
		}
		if !isOpenUpdateGoTaskForRepo(ctx, content, repo) {
			continue
		}
		transitionPrior(
			ctx,
			gitClient,
			currentDateTime,
			candidateRelPath,
			title,
			newRelPath,
			cmd.TaskIdentifier,
		)
	}
}

// listUpdateGoCandidateTitles lists the task titles for one repo. The repo slug in
// the filename replaces "/" with "-", so the glob is repo-scoped; a repo carrying a
// glob metacharacter falls back to listing every task file and filtering in memory,
// the same injection defense listSameSlugCandidateTitles applies.
func listUpdateGoCandidateTitles(
	ctx context.Context,
	gitClient gitclient.GitClient,
	taskDir string,
	repo string,
) ([]string, error) {
	prefix := updateGoTitlePrefix + strings.ReplaceAll(repo, "/", "-") + " "
	var glob string
	if strings.ContainsAny(prefix, "*?[]\\") {
		glob = filepath.Join(taskDir, "*.md")
	} else {
		glob = filepath.Join(taskDir, prefix+"*.md")
	}
	relPaths, err := gitClient.ListFiles(ctx, glob)
	if err != nil {
		return nil, err
	}
	var titles []string
	for _, relPath := range relPaths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		title := strings.TrimSuffix(filepath.Base(relPath), ".md")
		if strings.HasPrefix(title, prefix) {
			titles = append(titles, title)
		}
	}
	return titles, nil
}

// isOpenUpdateGoTaskForRepo reports whether one task file is a still-open update
// task for the given repo. Both the type and the repo are re-checked against the
// parsed frontmatter rather than trusted from the filename, so a file that merely
// looks like an update task is never aborted.
func isOpenUpdateGoTaskForRepo(ctx context.Context, content []byte, repo string) bool {
	fmStr, err := result.ExtractFrontmatter(ctx, content)
	if err != nil {
		return false
	}
	fm, err := parseTaskFrontmatter(fmStr)
	if err != nil {
		return false
	}
	if taskType, _ := fm["task_type"].(string); taskType != updateGoTaskType {
		return false
	}
	if fmRepo, _ := fm["repo"].(string); fmRepo != repo {
		return false
	}
	status, _ := fm["status"].(string)
	return status == "in_progress"
}
