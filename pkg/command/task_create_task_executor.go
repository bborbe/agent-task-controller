// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package command

import (
	"context"
	"maps"
	"path/filepath"
	"strings"
	"time"

	lib "github.com/bborbe/agent"
	task "github.com/bborbe/agent/command/task"
	"github.com/bborbe/cqrs/base"
	"github.com/bborbe/cqrs/cdb"
	"github.com/bborbe/errors"
	libkv "github.com/bborbe/kv"
	"github.com/bborbe/validation"
	"github.com/golang/glog"
	libtime "github.com/bborbe/time"

	gitclient "github.com/bborbe/agent-task-controller/pkg/gitrestclient"
	"github.com/bborbe/agent-task-controller/pkg/routing"
	result "github.com/bborbe/agent-task-controller/pkg/result"
)

// NewCreateTaskExecutor creates a cdb.CommandObjectExecutorTx that materializes
// a new vault task file for the given task_identifier. If cmd.Title passes validation
// the file is written at tasks/{title}.md; otherwise it falls back to tasks/{task_identifier}.md.
// If a file already exists at the resolved path the command returns ErrTaskAlreadyExists
// (a benign Failure on the result topic — no overwrite, no git write).
// Frontmatter must include "assignee" and "status"; missing fields return a wrapped validation error.
// Commands whose effective target vault (cmd.TargetVault or the legacy fallback) does not
// match vaultName are skipped without side effects (no git write, no error, no result event).
func NewCreateTaskExecutor(
	gitClient gitclient.GitClient,
	taskDir string,
	vaultName string,
	currentDateTime libtime.CurrentDateTimeGetter,
) cdb.CommandObjectExecutorTx {
	return cdb.CommandObjectExecutorTxFunc(
		task.CreateCommandOperation,
		true,
		func(ctx context.Context, tx libkv.Tx, commandObject cdb.CommandObject) (*base.EventID, base.Event, error) {
			var cmd task.CreateCommand
			if err := commandObject.Command.Data.MarshalInto(ctx, &cmd); err != nil {
				return nil, nil, errors.Wrapf(
					ctx,
					cdb.ErrCommandObjectSkipped,
					"malformed CreateTaskCommand: %v",
					err,
				)
			}
			if err := cmd.TaskIdentifier.Validate(ctx); err != nil {
				return nil, nil, errors.Wrapf(ctx, err, "validate task_identifier")
			}
			if !routing.ShouldProcess(cmd, vaultName) {
				effective := cmd.TargetVault
				if effective == "" {
					effective = routing.LegacyDefaultVault
				}
				glog.V(2).Infof(
					"create-task: skipped vault mismatch target=%q effective=%q vault=%q task=%s",
					cmd.TargetVault, effective, vaultName, cmd.TaskIdentifier,
				)
				return nil, nil, nil
			}
			if err := validateCreateTaskFrontmatter(ctx, cmd.Frontmatter); err != nil {
				return nil, nil, errors.Wrapf(ctx, err, "validate frontmatter")
			}
			relPath := resolveCreateTaskRelPath(ctx, taskDir, cmd)
			if existing, err := gitClient.ReadFile(ctx, relPath); err == nil {
				// File present at the title path → collision. Write nothing; return the
				// sentinel so the CQRS framework emits a benign Failure on the result topic.
				glog.V(2).Infof(
					"create-task: title path %s already occupied (%d bytes), returning ErrTaskAlreadyExists for %s",
					relPath,
					len(existing),
					cmd.TaskIdentifier,
				)
				return nil, nil, errors.Wrapf(
					ctx, task.ErrTaskAlreadyExists, "title path %s occupied", relPath,
				)
			} else if !isNotFoundReadError(err) {
				// Transient / unexpected git-rest read error → propagate, do NOT write.
				return nil, nil, errors.Wrapf(
					ctx, err, "check existing task file at %s for %s", relPath, cmd.TaskIdentifier,
				)
			}
			// err is a "not found" read error → title path is free, proceed to write.

			content, err := buildCreateTaskContent(ctx, cmd)
			if err != nil {
				return nil, nil, errors.Wrapf(
					ctx,
					err,
					"build task file content for %s",
					cmd.TaskIdentifier,
				)
			}
			absPath := filepath.Join(gitClient.Path(), relPath)
			if err := gitClient.AtomicWriteAndCommitPush(
				ctx,
				absPath,
				content,
				"[agent-task-controller] create task "+string(cmd.TaskIdentifier),
			); err != nil {
				return nil, nil, errors.Wrapf(
					ctx,
					err,
					"atomic write and push for task %s",
					cmd.TaskIdentifier,
				)
			}
			glog.V(2).
				Infof("create-task: created task file at %s for %s", relPath, cmd.TaskIdentifier)
			supersedePriorRecurringTask(ctx, gitClient, taskDir, currentDateTime, cmd, relPath)
			return nil, nil, nil
		},
	)
}

// resolveCreateTaskRelPath returns the repo-root-relative path where the task
// file should be written. If cmd.Title passes validation and contains no path
// separators, the title-derived path is returned; otherwise a WARN is logged and
// the UUID-derived path is returned as fallback so the task is always materialized.
// Filename-collision detection is the caller's job (via gitClient.ReadFile) — this
// function no longer reads the vault or compares task_identifier.
func resolveCreateTaskRelPath(
	ctx context.Context,
	taskDir string,
	cmd task.CreateCommand,
) string {
	uuidRelPath := filepath.Join(taskDir, string(cmd.TaskIdentifier)+".md")

	// Re-validate the command (defense-in-depth: sender may have been bypassed).
	if err := cmd.Validate(ctx); err != nil {
		glog.Warningf(
			"create-task: Title validation failed for task %s (%v); falling back to UUID path",
			cmd.TaskIdentifier, err,
		)
		return uuidRelPath
	}

	// Reject titles containing path separators to prevent path traversal.
	if strings.ContainsAny(cmd.Title, "/\\") {
		glog.Warningf(
			"create-task: Title %q contains path separator; falling back to UUID path",
			cmd.Title,
		)
		return uuidRelPath
	}

	return filepath.Join(taskDir, cmd.Title+".md")
}

// isNotFoundReadError reports whether a gitClient.ReadFile error means the file
// does not exist (git-rest returns HTTP 404). git-rest's Get does not expose a
// typed not-found sentinel, so this matches the "404" status embedded in the
// wrapped error message produced by gitRestClient.Get
// ("GET <path> returned 404: ..."). A nil error is NOT a not-found error and must
// be handled by the caller before calling this helper.
func isNotFoundReadError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "returned 404")
}

func validateCreateTaskFrontmatter(ctx context.Context, fm lib.TaskFrontmatter) error {
	if fm.Assignee() == "" {
		return errors.Wrap(ctx, validation.Error, "frontmatter missing required field: assignee")
	}
	if s, _ := fm.String("status"); s == "" {
		return errors.Wrap(ctx, validation.Error, "frontmatter missing required field: status")
	}
	return nil
}

func buildCreateTaskContent(ctx context.Context, cmd task.CreateCommand) ([]byte, error) {
	fm := make(lib.TaskFrontmatter)
	maps.Copy(fm, cmd.Frontmatter)
	fm["task_identifier"] = string(cmd.TaskIdentifier)
	return marshalFileContent(ctx, fm, cmd.Body)
}

// supersedePriorRecurringTask transitions the prior-period recurring-task instance
// to status: aborted after a new instance is materialized. It is a best-effort
// hook: every failure path (ineligible, no prior, read/parse/write error) is logged
// and swallowed so the already-written new instance is never rolled back.
// newRelPath is the repo-root-relative path of the just-written new instance
// (used as the superseded_by back-pointer).
func supersedePriorRecurringTask(
	ctx context.Context,
	gitClient gitclient.GitClient,
	taskDir string,
	currentDateTime libtime.CurrentDateTimeGetter,
	cmd task.CreateCommand,
	newRelPath string,
) {
	// DB1: eligibility gate — only auto-supersede instances created by the recurring-task publisher.
	createdBy, _ := cmd.Frontmatter.String("created_by")
	if createdBy != "recurring-task-creator" {
		glog.V(3).Infof("auto-supersede: skip %s (created_by=%q != recurring-task-creator)", cmd.TaskIdentifier, createdBy)
		return
	}
	// audit_style true → opt-out (spec Non-goal: no per-feature tunable on controller side).
	if b, _ := cmd.Frontmatter["audit_style"].(bool); b {
		return
	}
	if s, _ := cmd.Frontmatter["audit_style"].(string); s == "true" {
		return
	}

	// DB2: compute prior title.
	priorTitle, err := decrementRecurringTaskTitle(ctx, cmd.Title)
	if err != nil {
		glog.Warningf("auto-supersede: cannot decrement title %q for %s: %v", cmd.Title, cmd.TaskIdentifier, err)
		return
	}

	// Security: reject path-separator-bearing prior title (defense-in-depth).
	if strings.ContainsAny(priorTitle, "/\\") {
		glog.Warningf("auto-supersede: computed prior title %q contains path separator; skipping for %s", priorTitle, cmd.TaskIdentifier)
		return
	}

	priorRelPath := filepath.Join(taskDir, priorTitle+".md")

	// DB3: read the prior file.
	content, err := gitClient.ReadFile(ctx, priorRelPath)
	if err != nil {
		if isNotFoundReadError(err) {
			glog.V(3).Infof("auto-supersede: no prior instance at %s for %s (first-ever instance)", priorRelPath, cmd.TaskIdentifier)
			return
		}
		glog.Warningf("auto-supersede: read prior %s failed for %s: %v", priorRelPath, cmd.TaskIdentifier, err)
		return
	}

	// DB4: parse prior frontmatter and check status.
	frontmatterStr, err := result.ExtractFrontmatter(ctx, content)
	if err != nil {
		glog.Warningf("auto-supersede: extract frontmatter from prior %s failed for %s: %v", priorRelPath, cmd.TaskIdentifier, err)
		return
	}
	priorFm, err := parseTaskFrontmatter(frontmatterStr)
	if err != nil {
		glog.Warningf("auto-supersede: parse prior frontmatter %s failed for %s: %v", priorRelPath, cmd.TaskIdentifier, err)
		return
	}
	status, _ := priorFm.String("status")
	if status != "in_progress" {
		glog.V(3).Infof("auto-supersede: prior %s status=%q (not in_progress), no-op for %s", priorRelPath, status, cmd.TaskIdentifier)
		return
	}

	// DB5/DB6: transition the prior file.
	ts := currentDateTime.Now().UTC().Format(time.RFC3339)
	priorAbsPath := filepath.Join(gitClient.Path(), priorRelPath)
	modify := buildSupersedeModifyFn(ctx, newRelPath, ts)
	msg := "[agent-task-controller] auto-supersede prior recurring task " + priorTitle
	if err := gitClient.AtomicReadModifyWriteAndCommitPush(ctx, priorAbsPath, modify, msg); err != nil {
		glog.Warningf("auto-supersede: write prior %s failed for %s: %v", priorRelPath, cmd.TaskIdentifier, err)
		return
	}
	glog.Infof("auto-supersede: %s -> %s (prior superseded by new instance)", priorRelPath, newRelPath)
}

// buildSupersedeModifyFn builds the modify closure for AtomicReadModifyWriteAndCommitPush
// that transitions a prior-period recurring-task instance to aborted.
func buildSupersedeModifyFn(
	ctx context.Context,
	newRelPath string,
	ts string,
) func([]byte) ([]byte, error) {
	return func(current []byte) ([]byte, error) {
		fmStr, err := result.ExtractFrontmatter(ctx, current)
		if err != nil {
			return nil, errors.Wrapf(ctx, err, "extract frontmatter")
		}
		body, err := result.ExtractBody(ctx, current)
		if err != nil {
			return nil, errors.Wrapf(ctx, err, "extract body")
		}
		fm, err := parseTaskFrontmatter(fmStr)
		if err != nil {
			return nil, errors.Wrapf(ctx, err, "parse frontmatter")
		}
		fm["status"] = "aborted"
		fm["phase"] = "done"
		fm["completed_date"] = ts
		fm["superseded_by"] = newRelPath
		fm["created_by"] = "recurring-task-creator"
		return marshalFileContent(ctx, fm, body)
	}
}
