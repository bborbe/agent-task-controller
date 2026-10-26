// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package command

import (
	"context"
	"strings"
	"time"

	task "github.com/bborbe/agent/command/task"
	"github.com/bborbe/cqrs/base"
	"github.com/bborbe/cqrs/cdb"
	"github.com/bborbe/errors"
	libkv "github.com/bborbe/kv"
	libtime "github.com/bborbe/time"
	"github.com/golang/glog"

	"github.com/bborbe/agent-task-controller/pkg/metrics"
	"github.com/bborbe/agent-task-controller/pkg/vaultapi"
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
// vaultName is the controller's VAULT_NAME; a command whose non-empty TargetVault
// differs is cross-vault traffic and is skipped before the task-file lookup.
// resolver is the scanner's identifier→path index, consulted by the lookup before
// it walks the vault.
func NewCompleteTaskExecutor(
	client vaultapi.Client,
	vaultName string,
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
			if err := frontmatterCommandVaultMismatch(
				ctx,
				"complete-task",
				cmd.TargetVault,
				vaultName,
				cmd.TaskIdentifier,
			); err != nil {
				return nil, nil, err
			}
			if err := cmd.TaskIdentifier.Validate(ctx); err != nil {
				return nil, nil, errors.Wrapf(ctx, err, "validate task_identifier")
			}
			// The identifier IS the address: no path lookup, no working copy.
			found, err := client.ReadTask(ctx, string(cmd.TaskIdentifier))
			if errors.Is(err, vaultapi.ErrNotFound) {
				glog.Warningf(
					"complete-task: task not found for %s, skipping",
					cmd.TaskIdentifier,
				)
				m.FrontmatterCommandsTotal("complete-task", "not_found").Inc()
				return nil, nil, nil
			}
			if err != nil {
				m.FrontmatterCommandsTotal("complete-task", "error").Inc()
				return nil, nil, errors.Wrapf(ctx, err, "read task for complete")
			}
			ts := currentDateTime.Now().UTC().Format(time.RFC3339)
			if err := closeTask(ctx, client, found, cmd, ts, vaultName); err != nil {
				m.FrontmatterCommandsTotal("complete-task", "error").Inc()
				return nil, nil, errors.Wrapf(
					ctx,
					err,
					"complete task %s",
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

// closeTask transitions the task to completed. It is a no-op when the body
// already carries a Resolution block, which is the idempotency guard: a task
// closed twice must not gain a second Resolution section.
//
// ⚠️ Two writes, not one: PATCH sets the frontmatter fields, PUT appends the
// Resolution body. The old shape merged both and committed once. A failure
// between the two leaves the task marked completed with no Resolution section,
// and a retry re-runs both halves — the guard reads the BODY, not the status,
// so the second attempt still writes and still appends exactly one section.
func closeTask(
	ctx context.Context,
	client vaultapi.Client,
	found *vaultapi.Task,
	cmd task.CompleteCommand,
	ts string,
	vaultName string,
) error {
	if hasResolutionSection(found.Body) {
		glog.V(2).Infof(
			"complete-task: task %s already resolved, skipping (idempotent)",
			cmd.TaskIdentifier,
		)
		return nil
	}
	if _, err := client.PatchTask(
		ctx,
		found.ID,
		buildCompletePatch(cmd, found.Frontmatter, ts, vaultName),
	); err != nil {
		return errors.Wrapf(ctx, err, "patch frontmatter")
	}
	if err := client.PutBody(ctx, found.ID, resolutionBody(found.Body, cmd, ts)); err != nil {
		return errors.Wrapf(ctx, err, "put resolution body")
	}
	return nil
}

// buildCompletePatch returns the fields completion sets.
func buildCompletePatch(
	cmd task.CompleteCommand,
	frontmatter map[string]any,
	ts string,
	vaultName string,
) map[string]any {
	patch := map[string]any{
		"status":         "completed",
		"phase":          "done",
		"completed_date": ts,
		"processed_at":   ts,
	}
	if cmd.RecoverySHA != "" {
		patch["recovery_sha"] = cmd.RecoverySHA
	}
	// Heal-on-write: stamp target_vault on legacy tasks lacking it so the
	// non-owning controller permanently stops falling through.
	if _, ok := frontmatter["target_vault"]; !ok {
		patch["target_vault"] = vaultName
	}
	return patch
}

// resolutionBody appends the Resolution section to the task body.
func resolutionBody(body string, cmd task.CompleteCommand, ts string) string {
	closed := body
	if strings.TrimSpace(closed) != "" && !strings.HasSuffix(closed, "\n") {
		closed += "\n"
	}
	closed += "\n## Resolution\n\n"
	closed += "- **Verdict:** green — build recovered; original failure no longer reproduces\n"
	if cmd.RecoverySHA != "" {
		closed += "- **Recovery SHA:** `" + cmd.RecoverySHA + "` (default-branch HEAD at close time)\n"
	}
	closed += "- **Closed at:** " + ts + " (by `complete-task`)\n"
	return closed
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
