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
	libtime "github.com/bborbe/time"
	"github.com/bborbe/validation"
	"github.com/golang/glog"

	gitclient "github.com/bborbe/agent-task-controller/pkg/gitrestclient"
	result "github.com/bborbe/agent-task-controller/pkg/result"
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
			if err := checkTitlePathFree(ctx, gitClient, relPath, cmd.TaskIdentifier); err != nil {
				return nil, nil, err
			}
			if err := writeTaskFile(ctx, gitClient, relPath, cmd); err != nil {
				return nil, nil, err
			}
			supersedePriorRecurringTask(ctx, gitClient, taskDir, currentDateTime, cmd, relPath)
			return nil, nil, nil
		},
	)
}

// checkTitlePathFree returns ErrTaskAlreadyExists when the title path is
// already occupied (benign Failure on the result topic — no overwrite, no
// git write). A transient git-rest read error is propagated. A "not found"
// read error is swallowed (title path is free).
func checkTitlePathFree(
	ctx context.Context,
	gitClient gitclient.GitClient,
	relPath string,
	taskIdentifier lib.TaskIdentifier,
) error {
	existing, err := gitClient.ReadFile(ctx, relPath)
	if err == nil {
		glog.V(2).Infof(
			"create-task: title path %s already occupied (%d bytes), returning ErrTaskAlreadyExists for %s",
			relPath,
			len(existing),
			taskIdentifier,
		)
		return errors.Wrapf(
			ctx, task.ErrTaskAlreadyExists, "title path %s occupied", relPath,
		)
	}
	if !isNotFoundReadError(err) {
		return errors.Wrapf(
			ctx, err, "check existing task file at %s for %s", relPath, taskIdentifier,
		)
	}
	return nil
}

// writeTaskFile builds the task content and writes it atomically to the vault
// via git-rest, then logs the creation.
func writeTaskFile(
	ctx context.Context,
	gitClient gitclient.GitClient,
	relPath string,
	cmd task.CreateCommand,
) error {
	content, err := buildCreateTaskContent(ctx, cmd)
	if err != nil {
		return errors.Wrapf(ctx, err, "build task file content for %s", cmd.TaskIdentifier)
	}
	absPath := filepath.Join(gitClient.Path(), relPath)
	if err := gitClient.AtomicWriteAndCommitPush(
		ctx,
		absPath,
		content,
		"[agent-task-controller] create task "+string(cmd.TaskIdentifier),
	); err != nil {
		return errors.Wrapf(ctx, err, "atomic write and push for task %s", cmd.TaskIdentifier)
	}
	glog.V(2).Infof("create-task: created task file at %s for %s", relPath, cmd.TaskIdentifier)
	return nil
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
//
//nolint:unparam // Parameters kept for prompt 2 rewrite; body is a transitional no-op.
func supersedePriorRecurringTask(
	ctx context.Context,
	gitClient gitclient.GitClient,
	taskDir string,
	currentDateTime libtime.CurrentDateTimeGetter,
	cmd task.CreateCommand,
	newRelPath string,
) {
	if !isEligibleForSupersede(cmd) {
		return
	}
	// Scan-and-collapse lands in spec-004 prompt 2; until then this hook is a no-op.
	glog.V(3).
		Infof("auto-supersede: scan-and-collapse not yet wired for %s (spec-004 prompt 2)", cmd.TaskIdentifier)
	// Interim references so `unused`/`unparam` stay green while the scan (prompt 2)
	// is not yet wired. Prompt 2 rewrites this whole body and deletes this block.
	_, _, _, _, _ = ctx, gitClient, taskDir, currentDateTime, newRelPath
	_ = []any{readPriorForSupersede, priorIsInProgress, transitionPrior, buildSupersedeModifyFn}
}

// isEligibleForSupersede reports whether cmd is a recurring-task instance
// that should auto-supersede its prior. Returns true only when created_by is
// the recurring-task publisher AND auto_abort_prior is explicitly true (opt-in).
// Returns false (no supersede) when auto_abort_prior is absent or false — the
// safe default that prevents accidental supersede of audit-style tasks.
func isEligibleForSupersede(cmd task.CreateCommand) bool {
	createdBy, _ := cmd.Frontmatter.String("created_by")
	if createdBy != "recurring-task-creator" {
		glog.V(3).
			Infof("auto-supersede: skip %s (created_by=%q != recurring-task-creator)", cmd.TaskIdentifier, createdBy)
		return false
	}
	if b, _ := cmd.Frontmatter["auto_abort_prior"].(bool); b {
		return true
	}
	if s, _ := cmd.Frontmatter["auto_abort_prior"].(string); s == "true" {
		return true
	}
	glog.V(3).
		Infof("auto-supersede: skip %s (auto_abort_prior not true — opt-in required)", cmd.TaskIdentifier)
	return false
}

// readPriorForSupersede reads the prior file. Returns (nil, nil) on a
// not-found (first-ever instance — benign no-op). Returns (nil, err) on a
// transient git-rest error (logged by caller).
func readPriorForSupersede(
	ctx context.Context,
	gitClient gitclient.GitClient,
	priorRelPath string,
	taskIdentifier lib.TaskIdentifier,
) ([]byte, error) {
	content, err := gitClient.ReadFile(ctx, priorRelPath)
	if err == nil {
		return content, nil
	}
	if isNotFoundReadError(err) {
		glog.V(3).
			Infof("auto-supersede: no prior instance at %s for %s (first-ever instance)", priorRelPath, taskIdentifier)
		return nil, nil
	}
	glog.Warningf(
		"auto-supersede: read prior %s failed for %s: %v",
		priorRelPath,
		taskIdentifier,
		err,
	)
	return nil, err
}

// priorIsInProgress parses the prior file's frontmatter and reports whether
// its status is in_progress. Returns false (no-op) on parse error or any
// non-in_progress status.
func priorIsInProgress(
	ctx context.Context,
	content []byte,
	priorRelPath string,
	taskIdentifier lib.TaskIdentifier,
) bool {
	frontmatterStr, err := result.ExtractFrontmatter(ctx, content)
	if err != nil {
		glog.Warningf(
			"auto-supersede: extract frontmatter from prior %s failed for %s: %v",
			priorRelPath,
			taskIdentifier,
			err,
		)
		return false
	}
	priorFm, err := parseTaskFrontmatter(frontmatterStr)
	if err != nil {
		glog.Warningf(
			"auto-supersede: parse prior frontmatter %s failed for %s: %v",
			priorRelPath,
			taskIdentifier,
			err,
		)
		return false
	}
	status, _ := priorFm.String("status")
	if status != "in_progress" {
		glog.V(3).
			Infof("auto-supersede: prior %s status=%q (not in_progress), no-op for %s", priorRelPath, status, taskIdentifier)
		return false
	}
	return true
}

// transitionPrior performs the read-modify-write that transitions the prior
// file to aborted. Best-effort: write errors are logged and swallowed.
func transitionPrior(
	ctx context.Context,
	gitClient gitclient.GitClient,
	currentDateTime libtime.CurrentDateTimeGetter,
	priorRelPath string,
	priorTitle string,
	newRelPath string,
	taskIdentifier lib.TaskIdentifier,
) {
	ts := currentDateTime.Now().UTC().Format(time.RFC3339)
	priorAbsPath := filepath.Join(gitClient.Path(), priorRelPath)
	modify := buildSupersedeModifyFn(ctx, newRelPath, ts)
	msg := "[agent-task-controller] auto-supersede prior recurring task " + priorTitle
	if err := gitClient.AtomicReadModifyWriteAndCommitPush(ctx, priorAbsPath, modify, msg); err != nil {
		glog.Warningf(
			"auto-supersede: write prior %s failed for %s: %v",
			priorRelPath,
			taskIdentifier,
			err,
		)
		return
	}
	glog.Infof(
		"auto-supersede: %s -> %s (prior superseded by new instance)",
		priorRelPath,
		newRelPath,
	)
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
