// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package command

import (
	"context"

	lib "github.com/bborbe/agent"
	task "github.com/bborbe/agent/command/task"
	delivery "github.com/bborbe/agent/delivery"
	"github.com/bborbe/cqrs/base"
	"github.com/bborbe/cqrs/cdb"
	"github.com/bborbe/errors"
	libkv "github.com/bborbe/kv"
	"github.com/golang/glog"

	"github.com/bborbe/agent-task-controller/pkg/metrics"
	"github.com/bborbe/agent-task-controller/pkg/result"
	"github.com/bborbe/agent-task-controller/pkg/routing"
	"github.com/bborbe/agent-task-controller/pkg/vaultapi"
)

// frontmatterCommandVaultMismatch applies the frontmatter-command routing guard:
// a command whose non-empty TargetVault differs from vaultName is cross-vault
// traffic (both controllers consume the shared topic) and is skipped before the
// task-file lookup. It logs a single glog.V(2) line naming the command's target
// vault and VAULT_NAME, then returns an error wrapping cdb.ErrCommandObjectSkipped
// — never nil, never a plain error: a nil return with SendResultEnabled publishes
// a spurious Success event on the shared result topic for every cross-vault
// command. An empty targetVault falls through (legacy unstamped commands are
// attempted by every controller; the owner heals, the non-owner drops). Returns
// nil when the command is owned.
func frontmatterCommandVaultMismatch(
	ctx context.Context,
	operation string,
	targetVault string,
	vaultName string,
	taskID lib.TaskIdentifier,
) error {
	if routing.ShouldProcessFrontmatterCommand(targetVault, vaultName) {
		return nil
	}
	glog.V(2).Infof(
		"%s: skipped vault mismatch target_vault=%q vault=%q task=%s",
		operation, targetVault, vaultName, taskID,
	)
	return errors.Wrapf(
		ctx,
		cdb.ErrCommandObjectSkipped,
		"cross-vault %s for task %s",
		operation,
		taskID,
	)
}

// UpdateFrontmatterCommandOperation is the CQRS command operation name for partial frontmatter update.
const UpdateFrontmatterCommandOperation base.CommandOperation = task.UpdateFrontmatterCommandOperation

// NewUpdateFrontmatterExecutor creates a cdb.CommandObjectExecutorTx that atomically
// reads the task file, merges only the specified key-value pairs, and commits.
// All other frontmatter keys are left unchanged. vaultName is the controller's
// VAULT_NAME; a command whose non-empty TargetVault differs is cross-vault
// traffic and is skipped before the task-file lookup (no write, no counter,
// no result event). resolver is the scanner's identifier→path index, consulted
// by the lookup before it walks the vault.
func NewUpdateFrontmatterExecutor(
	client vaultapi.Client,
	vaultName string,
	m metrics.Metrics,
) cdb.CommandObjectExecutorTx {
	return cdb.CommandObjectExecutorTxFunc(
		UpdateFrontmatterCommandOperation,
		true,
		func(ctx context.Context, tx libkv.Tx, commandObject cdb.CommandObject) (*base.EventID, base.Event, error) {
			var cmd task.UpdateFrontmatterCommand
			if err := commandObject.Command.Data.MarshalInto(ctx, &cmd); err != nil {
				return nil, nil, errors.Wrapf(
					ctx,
					cdb.ErrCommandObjectSkipped,
					"malformed UpdateFrontmatterCommand: %v",
					err,
				)
			}
			if err := frontmatterCommandVaultMismatch(
				ctx,
				"update-frontmatter",
				cmd.TargetVault,
				vaultName,
				cmd.TaskIdentifier,
			); err != nil {
				return nil, nil, err
			}
			// Empty updates with no body section is a no-op — nothing to write.
			if len(cmd.Updates) == 0 && cmd.Body == nil {
				return nil, nil, nil
			}
			// The identifier IS the address: no path lookup, no working copy.
			task, err := client.ReadTask(ctx, string(cmd.TaskIdentifier))
			if errors.Is(err, vaultapi.ErrNotFound) {
				glog.Warningf(
					"update-frontmatter: task not found for %s, skipping",
					cmd.TaskIdentifier,
				)
				m.FrontmatterCommandsTotal("update-frontmatter", "not_found").Inc()
				return nil, nil, nil
			}
			if err != nil {
				m.FrontmatterCommandsTotal("update-frontmatter", "error").Inc()
				return nil, nil, errors.Wrapf(ctx, err, "read task for update")
			}
			if err := applyUpdate(ctx, client, task, cmd, vaultName); err != nil {
				m.FrontmatterCommandsTotal("update-frontmatter", "error").Inc()
				return nil, nil, errors.Wrapf(
					ctx,
					err,
					"update task %s",
					cmd.TaskIdentifier,
				)
			}
			m.FrontmatterCommandsTotal("update-frontmatter", "success").Inc()
			return nil, nil, nil
		},
	)
}

// applyUpdate writes the command's two halves. ⚠️ **They are two calls, not one
// atomic write**, and the difference is real: the old shape read the file,
// merged frontmatter and body together, and committed once, so a failure left
// nothing behind. The typed surface has no such primitive — PATCH replaces named
// frontmatter fields, PUT replaces the body — so a failure between them leaves
// frontmatter updated and the body stale. The window is one request wide and the
// caller retries the command, which is idempotent for both halves; it is named
// here rather than hidden because the method this replaces was called "atomic".
func applyUpdate(
	ctx context.Context,
	client vaultapi.Client,
	task *vaultapi.Task,
	cmd task.UpdateFrontmatterCommand,
	vaultName string,
) error {
	if fields := buildUpdatePatch(cmd.Updates, task.Frontmatter, vaultName); len(fields) > 0 {
		if _, err := client.PatchTask(ctx, task.ID, fields); err != nil {
			return errors.Wrapf(ctx, err, "patch frontmatter")
		}
	}
	if cmd.Body == nil {
		return nil
	}
	body := delivery.ReplaceOrAppendSection(task.Body, cmd.Body.Heading, cmd.Body.Section)
	if err := client.PutBody(ctx, task.ID, body); err != nil {
		return errors.Wrapf(ctx, err, "put body")
	}
	return nil
}

// buildUpdatePatch returns the fields to PATCH. It carries the command's own
// updates plus the two doctrines that used to be applied to the merged file:
// spec 042's phase:human_review ⇒ assignee:"" and the target_vault heal.
//
// The doctrines are computed against a merged COPY so only the fields they
// actually change are sent — a PATCH that restated every key would rewrite
// frontmatter the command never touched.
func buildUpdatePatch(
	updates lib.TaskFrontmatter,
	frontmatter map[string]any,
	vaultName string,
) map[string]any {
	// `before` is the task as it stands, snapshotted BEFORE the command's own
	// updates are merged — otherwise the diff below would exclude them and the
	// command would patch nothing.
	before := map[string]any{}
	merged := lib.TaskFrontmatter{}
	for key, value := range frontmatter {
		before[key] = value
		merged[key] = value
	}
	for key, value := range updates {
		merged[key] = value
	}
	// spec 042: phase: human_review ⇒ assignee: "". No-op otherwise.
	result.ClearAssigneeIfHumanReview(merged)
	// Heal-on-write: stamp target_vault on legacy tasks lacking it.
	result.HealTargetVault(merged, vaultName)

	patch := map[string]any{}
	for key, value := range merged {
		if previous, existed := before[key]; !existed || previous != value {
			patch[key] = value
		}
	}
	return patch
}
