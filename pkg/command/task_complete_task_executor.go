// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package command

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	task "github.com/bborbe/agent/command/task"
	"github.com/bborbe/cqrs/base"
	"github.com/bborbe/cqrs/cdb"
	"github.com/bborbe/errors"
	libkv "github.com/bborbe/kv"
	libtime "github.com/bborbe/time"
	"github.com/golang/glog"

	gitclient "github.com/bborbe/agent-task-controller/pkg/gitrestclient"
	"github.com/bborbe/agent-task-controller/pkg/metrics"
	"github.com/bborbe/agent-task-controller/pkg/result"
)

// CompleteTaskCommandOperation is the CQRS command operation name for task completion.
const CompleteTaskCommandOperation base.CommandOperation = task.CompleteCommandOperation

// NewCompleteTaskExecutor creates a cdb.CommandObjectExecutorTx that transitions a vault
// task to status: completed, phase: done with a # Resolution body section carrying the
// recovery SHA. The watcher publishes this on a build red→green transition (spec 076).
//
// Resolution is by task_identifier (matching the CreateCommand's deterministic UUID5).
// Idempotent: a task that already carries a # Resolution block is a no-op (second
// completion does not append a duplicate block, matching the build-fix-close-obsolete-tasks
// skill's existing guard).
func NewCompleteTaskExecutor(
	gitClient gitclient.GitClient,
	taskDir string,
	currentDateTime libtime.CurrentDateTimeGetter,
	m metrics.Metrics,
) cdb.CommandObjectExecutorTx {
	return cdb.CommandObjectExecutorTxFunc(
		CompleteTaskCommandOperation,
		true,
		func(ctx context.Context, tx libkv.Tx, commandObject cdb.CommandObject) (*base.EventID, base.Event, error) {
			var cmd task.CompleteCommand
			if err := commandObject.Command.Data.MarshalInto(ctx, &cmd); err != nil {
				return nil, nil, errors.Wrapf(
					ctx,
					cdb.ErrCommandObjectSkipped,
					"malformed CompleteTaskCommand: %v",
					err,
				)
			}
			if err := cmd.TaskIdentifier.Validate(ctx); err != nil {
				return nil, nil, errors.Wrapf(ctx, err, "validate task_identifier")
			}
			matchedRelPath, _, err := result.FindTaskFilePath(
				ctx,
				gitClient,
				taskDir,
				cmd.TaskIdentifier,
			)
			if err != nil {
				m.FrontmatterCommandsTotal("complete-task", "error").Inc()
				return nil, nil, errors.Wrapf(ctx, err, "find task file for complete")
			}
			if matchedRelPath == "" {
				glog.Warningf(
					"complete-task: task file not found for %s, skipping",
					cmd.TaskIdentifier,
				)
				m.FrontmatterCommandsTotal("complete-task", "not_found").Inc()
				return nil, nil, nil
			}
			fullAbsPath := filepath.Join(gitClient.Path(), matchedRelPath)
			ts := currentDateTime.Now().UTC().Format(time.RFC3339)
			if err := gitClient.AtomicReadModifyWriteAndCommitPush(
				ctx,
				fullAbsPath,
				buildCompleteModifyFn(ctx, cmd, ts),
				fmt.Sprintf("[agent-task-controller] complete task %s", cmd.TaskIdentifier),
			); err != nil {
				m.FrontmatterCommandsTotal("complete-task", "error").Inc()
				return nil, nil, errors.Wrapf(
					ctx,
					err,
					"atomic complete for task %s",
					cmd.TaskIdentifier,
				)
			}
			m.FrontmatterCommandsTotal("complete-task", "success").Inc()
			glog.V(2).Infof(
				"complete-task: closed %s (recovery %s)",
				cmd.TaskIdentifier,
				shortSHA(cmd.RecoverySHA),
			)
			return nil, nil, nil
		},
	)
}

// buildCompleteModifyFn builds the modify closure for AtomicReadModifyWriteAndCommitPush
// that transitions the task file to completed. It returns (nil, nil) — signalling the
// caller to skip the write — when the task is already resolved (idempotency guard).
func buildCompleteModifyFn(
	ctx context.Context,
	cmd task.CompleteCommand,
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
		if hasResolutionSection(body) {
			glog.V(2).Infof(
				"complete-task: task %s already resolved, skipping (idempotent)",
				cmd.TaskIdentifier,
			)
			return nil, nil
		}
		fm, err := parseTaskFrontmatter(fmStr)
		if err != nil {
			return nil, errors.Wrapf(ctx, err, "parse frontmatter")
		}
		fm["status"] = "completed"
		fm["phase"] = "done"
		fm["completed_date"] = ts
		fm["processed_at"] = ts
		if cmd.RecoverySHA != "" {
			fm["recovery_sha"] = cmd.RecoverySHA
		}
		closedBody := body
		if strings.TrimSpace(closedBody) != "" && !strings.HasSuffix(closedBody, "\n") {
			closedBody += "\n"
		}
		closedBody += "\n## Resolution\n\n"
		closedBody += "- **Verdict:** green — build recovered; original failure no longer reproduces\n"
		if cmd.RecoverySHA != "" {
			closedBody += "- **Recovery SHA:** `" + cmd.RecoverySHA + "` (default-branch HEAD at close time)\n"
		}
		closedBody += "- **Closed at:** " + ts + " (by `complete-task`)\n"
		return marshalFileContent(ctx, fm, closedBody)
	}
}

// hasResolutionSection reports whether the task body already carries a # Resolution
// block (idempotency guard — never append a second one).
func hasResolutionSection(body string) bool {
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "# Resolution" || trimmed == "## Resolution" {
			return true
		}
	}
	return false
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
