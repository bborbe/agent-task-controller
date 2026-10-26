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

// NewUpdateFrontmatterExecutor creates a cdb.CommandObjectExecutorTx that reads the
// task through the typed vault API by identifier, PATCHes only the frontmatter fields
// the command names (plus the two doctrines), and PUTs the body when the command
// carries a body section. All other frontmatter keys are left untouched — the field
// level PATCH is what guarantees that, so no whole-file merge happens and none of the
// task's frontmatter is rewritten.
//
// It is explicitly NOT atomic: PATCH and PUT are two requests and the typed surface
// has no combined primitive, so a failure between them leaves frontmatter updated and
// the body stale. See applyUpdate for the window and why the retry is safe.
//
// vaultName is the controller's VAULT_NAME; a command whose non-empty TargetVault
// differs is cross-vault traffic and is skipped before the task lookup (no write, no
// counter, no result event).
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
	// Re-read before the PUT: PUT replaces the WHOLE body, so merging the section
	// onto the body captured at the top of the command would silently drop any
	// edit that landed in between. The old write path re-read the file inside its
	// modify closure, which is why it could not lose a concurrent body edit; this
	// re-read restores that property at the cost of one extra request per
	// body-carrying command. A failure here is returned rather than falling back
	// to the stale snapshot — the retry re-runs the whole command.
	fresh, err := client.ReadTask(ctx, string(cmd.TaskIdentifier))
	if err != nil {
		return errors.Wrapf(ctx, err, "re-read task for body merge")
	}
	body := delivery.ReplaceOrAppendSection(fresh.Body, cmd.Body.Heading, cmd.Body.Section)
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
		// NEVER compare the two `any` values with `!=`: frontmatter is
		// JSON-decoded, so a task carrying `tags:` or any nested field yields
		// map[string]any / []any on both sides and Go panics with "comparing
		// uncomparable type []interface {}". This loop runs over EVERY key in the
		// task's frontmatter, not only the ones the command names, so one list
		// field would crash the executor for every update command against that
		// task. result.FrontmatterValueEqual is reflect.DeepEqual-based (plus
		// int/float64 equivalence) and never panics.
		if previous, existed := before[key]; !existed ||
			!result.FrontmatterValueEqual(previous, value) {
			patch[key] = value
		}
	}
	return patch
}
