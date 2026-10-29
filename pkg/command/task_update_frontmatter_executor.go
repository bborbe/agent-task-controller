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
	libtime "github.com/bborbe/time"
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
// the command names (plus the doctrines), and PUTs the body when the command
// carries a body section or the trigger cap escalates. All other frontmatter keys
// are left untouched — the field level PATCH is what guarantees that, so no
// whole-file merge happens and none of the task's frontmatter is rewritten.
//
// It is explicitly NOT atomic: PATCH and PUT are two requests and the typed surface
// has no combined primitive, so a failure between them leaves the body updated and
// the frontmatter stale. See applyUpdate for the window and why the retry is safe.
//
// ⚠️ This executor is the ONLY write a capped task receives. A task at the trigger
// cap is refused by the executor's checkTriggerBudget, so it never spawns, never
// produces a WriteResult, and never gets an IncrementFrontmatterCommand — the two
// sites the trigger-cap fix originally guarded. What it does get is this executor's
// once-a-minute restore-assignee write from the reconcile loop. Without the cap
// check here the escalation is unreachable for exactly the population it exists for
// (observed live on dev 2026-10-28, task 3a6501ec: the spawn charge reached
// trigger_count 3 through an update-frontmatter command and escalated nothing).
//
// vaultName is the controller's VAULT_NAME; a command whose non-empty TargetVault
// differs is cross-vault traffic and is skipped before the task lookup (no write, no
// counter, no result event).
func NewUpdateFrontmatterExecutor(
	client vaultapi.Client,
	vaultName string,
	currentDateTime libtime.CurrentDateTimeGetter,
	m metrics.Metrics,
	humans result.HumanAssignees,
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
			if err := applyUpdate(ctx, client, task, cmd, vaultName, currentDateTime, humans); err != nil {
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
// one half applied. The window is one request wide and the caller retries the
// command, which is idempotent for both halves; it is named here rather than
// hidden because the method this replaces was called "atomic".
//
// ⚠️ **The body is written BEFORE the frontmatter PATCH, and the order is
// load-bearing.** The escalation section records the assignee the cap cleared. If
// the PATCH landed first and the body PUT then failed, the retry would recompute
// the escalation from a task whose assignee is already empty and render the section
// with a blank Assignee — a permanently wrong record. Body-first inverts that: a
// failed PATCH retries against unchanged frontmatter, so the captured name is still
// there, and the section's own heading guard makes the second PUT a no-op.
func applyUpdate(
	ctx context.Context,
	client vaultapi.Client,
	task *vaultapi.Task,
	cmd task.UpdateFrontmatterCommand,
	vaultName string,
	currentDateTime libtime.CurrentDateTimeGetter,
	humans result.HumanAssignees,
) error {
	fields, escalation := buildUpdatePatch(cmd.Updates, task.Frontmatter, vaultName, humans)
	if cmd.Body == nil && escalation == nil {
		if len(fields) > 0 {
			if _, err := client.PatchTask(ctx, task.ID, fields); err != nil {
				return errors.Wrapf(ctx, err, "patch frontmatter")
			}
		}
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
	body := fresh.Body
	if cmd.Body != nil {
		body = delivery.ReplaceOrAppendSection(body, cmd.Body.Heading, cmd.Body.Section)
	}
	// Duplicate guard checked against the FRESH body, not the one captured above:
	// a concurrent write may have already appended the section.
	if escalation != nil && !result.ContainsTriggerCapEscalation(body) {
		body += result.TriggerCapEscalationSection(
			escalation.triggerCount,
			escalation.maxTriggers,
			escalation.agentName,
			currentDateTime.Now(),
		)
	}
	// Addressed by fresh.ID, not task.ID: the body being written is the fresh
	// read's, so a task deleted and recreated under the same identifier between
	// the two reads must not have this body written onto the stale record.
	//
	// Skipped when nothing changed: a capped task re-enters this branch on every
	// once-a-minute reconcile tick, and the heading guard only suppresses the
	// append — an unconditional PUT would rewrite the whole body forever.
	if body != fresh.Body {
		if err := client.PutBody(ctx, fresh.ID, body); err != nil {
			return errors.Wrapf(ctx, err, "put body")
		}
	}
	if len(fields) > 0 {
		if _, err := client.PatchTask(ctx, task.ID, fields); err != nil {
			return errors.Wrapf(ctx, err, "patch frontmatter")
		}
	}
	return nil
}

// buildUpdatePatch returns the fields to PATCH and, when this write takes the task
// to its trigger cap, the escalation to record in the body.
//
// It carries the command's own updates, the controller-owned budget guard, the
// trigger-cap escalation, and the two doctrines that used to be applied to the
// merged file: spec 042's phase:human_review ⇒ assignee:"" and the target_vault
// heal.
//
// The doctrines are computed against a merged COPY so only the fields they
// actually change are sent — a PATCH that restated every key would rewrite
// frontmatter the command never touched.
func buildUpdatePatch(
	updates lib.TaskFrontmatter,
	frontmatter map[string]any,
	vaultName string,
	humans result.HumanAssignees,
) (map[string]any, *triggerCapEscalation) {
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
	applyControllerOwnedGuard(updates, frontmatter, merged)
	// Evaluated BEFORE the human-review doctrine, and that order is load-bearing:
	// the cap captures the current assignee into previous_assignee, so running it
	// after ClearAssigneeIfHumanReview would capture "" and lose the name.
	escalation := applyUpdateTriggerCap(merged, humans)
	// spec 042: phase: human_review ⇒ assignee: "", except a configured human
	// owner, which is retained. No-op otherwise.
	result.ClearAssigneeIfHumanReview(merged, humans)
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
	return patch, escalation
}

// capDefinitionFields are the operator's definition of the cap itself. An agent
// snapshot may neither raise one nor introduce one where the task has none:
// `applyUpdateTriggerCap` reads the merged map, so a snapshot that raises
// max_triggers in the same write would suppress the very escalation this path
// exists to produce. Only a task's own create sets it.
var capDefinitionFields = []string{"max_triggers"}

// applyControllerOwnedGuard keeps the controller's frontmatter keys over a stale
// value the command carries.
//
// ⚠️ **The budget counters themselves are deliberately NOT guarded here.** A
// monotonic "a lower count loses" rule reads well and is wrong: `triggerBudgetWrite`
// has a `scopeChanged` arm that writes `trigger_count: 1` on purpose, to grant a
// fresh budget when the task's scope moves (planning → execution), and that write
// arrives through this executor. Guarding the count would cap a task that had
// legitimately earned a new budget. The guard therefore covers only what an agent
// has no business writing at all — the cap's definition, and a scope without the
// count that always travels with it.
//
// The consequence is stated rather than hidden: an agent echo that names a lower
// trigger_count still lands, so this path does not defend the counter against a
// stale snapshot. Doing that needs a discriminator the controller does not have —
// the legitimate reset and the stale echo are the same shape on the wire.
func applyControllerOwnedGuard(
	updates lib.TaskFrontmatter,
	frontmatter map[string]any,
	merged lib.TaskFrontmatter,
) {
	for _, field := range capDefinitionFields {
		if _, named := updates[field]; !named {
			continue
		}
		if onDisk, ok := frontmatter[field]; ok {
			merged[field] = onDisk
			continue
		}
		delete(merged, field)
	}

	// trigger_scope travels only with a trigger_count: `triggerBudgetWrite` writes
	// the pair together on both of its arms, and nothing else writes the scope. A
	// scope arriving alone is stale, whether or not the task has one on disk.
	if _, named := updates["trigger_scope"]; !named {
		return
	}
	if _, namedCount := updates["trigger_count"]; namedCount {
		return
	}
	if onDiskScope, ok := frontmatter["trigger_scope"]; ok {
		merged["trigger_scope"] = onDiskScope
	} else {
		delete(merged, "trigger_scope")
	}
}

// applyUpdateTriggerCap enforces the trigger-count cap on this write path, using
// the same predicate as the result writer and the increment executor
// (result.TriggerCapEngaged: an explicit max_triggers OR a ref-bearing task).
//
// The `triggerCount == 0` guard is the result writer's, kept for the same reason:
// a brand-new task with no count must not be escalated degenerately.
//
// Returns nil when the cap does not engage, so the caller writes no body section.
func applyUpdateTriggerCap(
	merged lib.TaskFrontmatter,
	humans result.HumanAssignees,
) *triggerCapEscalation {
	if !result.TriggerCapEngaged(merged) {
		return nil
	}
	triggerCount := merged.TriggerCount()
	if triggerCount == 0 || triggerCount < merged.MaxTriggers() {
		return nil
	}
	// Human-aware: this branch runs BEFORE ClearAssigneeIfHumanReview in
	// buildUpdatePatch, so an unconditional clear here would move a human owner
	// into previous_assignee and defeat the chokepoint on this write path — the
	// same ordering hazard the result writer closes for applyTriggerCap and
	// applyRetryCap.
	agentName := result.ClearAssigneeUnlessHuman(merged, humans)
	return &triggerCapEscalation{
		triggerCount: triggerCount,
		maxTriggers:  merged.MaxTriggers(),
		agentName:    agentName,
	}
}
