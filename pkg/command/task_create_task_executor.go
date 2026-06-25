// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package command

import (
	"context"
	"maps"
	"path/filepath"
	"strings"

	lib "github.com/bborbe/agent"
	task "github.com/bborbe/agent/command/task"
	"github.com/bborbe/cqrs/base"
	"github.com/bborbe/cqrs/cdb"
	"github.com/bborbe/errors"
	libkv "github.com/bborbe/kv"
	"github.com/bborbe/validation"
	"github.com/golang/glog"

	gitclient "github.com/bborbe/agent-task-controller/pkg/gitrestclient"
	"github.com/bborbe/agent-task-controller/pkg/routing"
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
