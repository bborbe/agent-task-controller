// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package command

import (
	"context"

	lib "github.com/bborbe/agent"
	task "github.com/bborbe/agent/command/task"
	"github.com/bborbe/cqrs/base"
	"github.com/bborbe/cqrs/cdb"
	"github.com/bborbe/errors"
	libkv "github.com/bborbe/kv"
	libtime "github.com/bborbe/time"
	"github.com/golang/glog"
	"gopkg.in/yaml.v3"

	"github.com/bborbe/agent-task-controller/pkg/metrics"
	"github.com/bborbe/agent-task-controller/pkg/result"
	"github.com/bborbe/agent-task-controller/pkg/vaultapi"
)

// IncrementFrontmatterCommandOperation is the CQRS command operation name for atomic field increment.
const IncrementFrontmatterCommandOperation base.CommandOperation = task.IncrementFrontmatterCommandOperation

// NewIncrementFrontmatterExecutor creates a cdb.CommandObjectExecutorTx that atomically
// reads the task file, increments the named frontmatter field by delta, and commits.
// When trigger_count reaches the trigger cap — an explicit max_triggers OR a
// ref-bearing task (the same predicate the executor's checkTriggerBudget refuses
// on) — the assignee is cleared (captured into previous_assignee) and a
// ## Trigger Cap Escalation body section is recorded, so an exhausted task lands
// where a human can find it instead of parking silently with a live assignee. A
// plain recurring task with neither max_triggers nor a ref is never capped: its
// routing assignee survives re-dispatch accumulation.
// vaultName is the controller's VAULT_NAME; a command whose non-empty TargetVault
// differs is cross-vault traffic and is skipped before the task read.
func NewIncrementFrontmatterExecutor(
	client vaultapi.Client,
	vaultName string,
	currentDateTime libtime.CurrentDateTimeGetter,
	m metrics.Metrics,
) cdb.CommandObjectExecutorTx {
	return cdb.CommandObjectExecutorTxFunc(
		IncrementFrontmatterCommandOperation,
		true,
		func(ctx context.Context, tx libkv.Tx, commandObject cdb.CommandObject) (*base.EventID, base.Event, error) {
			var cmd task.IncrementFrontmatterCommand
			if err := commandObject.Command.Data.MarshalInto(ctx, &cmd); err != nil {
				return nil, nil, errors.Wrapf(
					ctx,
					cdb.ErrCommandObjectSkipped,
					"malformed IncrementFrontmatterCommand: %v",
					err,
				)
			}
			if err := frontmatterCommandVaultMismatch(
				ctx,
				"increment-frontmatter",
				cmd.TargetVault,
				vaultName,
				cmd.TaskIdentifier,
			); err != nil {
				return nil, nil, err
			}
			// The identifier IS the address: no path lookup, no working copy.
			task, err := client.ReadTask(ctx, string(cmd.TaskIdentifier))
			if errors.Is(err, vaultapi.ErrNotFound) {
				glog.Warningf(
					"increment-frontmatter: task not found for %s, skipping",
					cmd.TaskIdentifier,
				)
				m.FrontmatterCommandsTotal("increment-frontmatter", "not_found").Inc()
				return nil, nil, nil
			}
			if err != nil {
				m.FrontmatterCommandsTotal("increment-frontmatter", "error").Inc()
				return nil, nil, errors.Wrapf(ctx, err, "read task for increment")
			}
			patch, escalation := buildIncrementPatch(cmd, task.Frontmatter, vaultName)
			// Escalate BEFORE the frontmatter PATCH: the PATCH carries a DELTA
			// (trigger_count + cmd.Delta), so a failed PATCH that is retried
			// re-increments exactly once, whereas a failed body PUT *after* a landed
			// PATCH would re-increment on redelivery. The body write is idempotent
			// (guarded by the section heading) and re-reads the task immediately
			// before the PUT so a concurrent body write — e.g. the executor's
			// ## Failure section on the deadline path — is not clobbered.
			if escalation != nil {
				if err := writeTriggerCapEscalation(
					ctx,
					client,
					task.ID,
					*escalation,
					currentDateTime.Now(),
				); err != nil {
					m.FrontmatterCommandsTotal("increment-frontmatter", "error").Inc()
					return nil, nil, errors.Wrapf(
						ctx,
						err,
						"escalate trigger cap for task %s",
						cmd.TaskIdentifier,
					)
				}
			}
			if _, err := client.PatchTask(ctx, task.ID, patch); err != nil {
				m.FrontmatterCommandsTotal("increment-frontmatter", "error").Inc()
				return nil, nil, errors.Wrapf(
					ctx,
					err,
					"increment %s for task %s",
					cmd.Field,
					cmd.TaskIdentifier,
				)
			}
			m.FrontmatterCommandsTotal("increment-frontmatter", "success").Inc()
			return nil, nil, nil
		},
	)
}

// triggerCapEscalation carries the values the ## Trigger Cap Escalation section
// needs, produced when an increment reaches the cap.
type triggerCapEscalation struct {
	triggerCount int
	maxTriggers  int
	agentName    string
}

// buildIncrementPatch computes the fields to PATCH from the task as it stands,
// returning a non-nil escalation when the increment reaches the trigger cap.
//
// It replaces a whole-file read-modify-write: the old shape parsed the YAML,
// mutated it and re-serialised the file, so every unrelated key round-tripped
// through marshal. The typed surface names the fields, so only the fields this
// command changes are sent — and the API applies them atomically per task.
//
// The trigger cap engages for an explicit max_triggers OR a ref-bearing task
// (result.TriggerCapEngaged) — the same predicate the executor's refusal uses.
// A plain recurring task with neither is never capped, so its routing assignee
// survives re-dispatch accumulation. Only trigger_count drives the cap.
func buildIncrementPatch(
	cmd task.IncrementFrontmatterCommand,
	frontmatter map[string]any,
	vaultName string,
) (map[string]any, *triggerCapEscalation) {
	fm := lib.TaskFrontmatter(frontmatter)
	newVal := intFromFrontmatter(fm, cmd.Field) + cmd.Delta
	patch := map[string]any{cmd.Field: newVal}
	var escalation *triggerCapEscalation
	if cmd.Field == "trigger_count" && result.TriggerCapEngaged(fm) && newVal >= fm.MaxTriggers() {
		agentName := result.ClearAssignee(fm)
		patch["assignee"] = ""
		if agentName != "" {
			patch["previous_assignee"] = agentName
		}
		escalation = &triggerCapEscalation{
			triggerCount: newVal,
			maxTriggers:  fm.MaxTriggers(),
			agentName:    agentName,
		}
	}
	// Heal-on-write: stamp target_vault on legacy tasks lacking it so the
	// non-owning controller permanently stops falling through.
	if _, ok := fm["target_vault"]; !ok {
		patch["target_vault"] = vaultName
	}
	return patch, escalation
}

// writeTriggerCapEscalation appends the ## Trigger Cap Escalation section to the
// task body, if it is not already present. It re-reads the task immediately before
// the PUT (the body is replaced wholesale) so a body edit made in the window — the
// executor's ## Failure section on the deadline path — is merged onto rather than
// dropped, and the duplicate guard is re-checked against that fresh body.
func writeTriggerCapEscalation(
	ctx context.Context,
	client vaultapi.Client,
	id string,
	escalation triggerCapEscalation,
	now libtime.DateTime,
) error {
	fresh, err := client.ReadTask(ctx, id)
	if err != nil {
		return errors.Wrapf(ctx, err, "re-read task for trigger cap escalation body")
	}
	if result.ContainsTriggerCapEscalation(fresh.Body) {
		return nil
	}
	body := fresh.Body + result.TriggerCapEscalationSection(
		escalation.triggerCount,
		escalation.maxTriggers,
		escalation.agentName,
		now,
	)
	if err := client.PutBody(ctx, fresh.ID, body); err != nil {
		return errors.Wrapf(ctx, err, "put trigger cap escalation body")
	}
	return nil
}

func intFromFrontmatter(fm lib.TaskFrontmatter, field string) int {
	v, ok := fm[field]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	default:
		return 0
	}
}

func parseTaskFrontmatter(frontmatterStr string) (lib.TaskFrontmatter, error) {
	var fm lib.TaskFrontmatter
	if err := yaml.Unmarshal([]byte(frontmatterStr), &fm); err != nil {
		return nil, err
	}
	if fm == nil {
		fm = make(lib.TaskFrontmatter)
	}
	return fm, nil
}

func marshalFileContent(ctx context.Context, fm lib.TaskFrontmatter, body string) ([]byte, error) {
	marshaled, err := yaml.Marshal(map[string]any(fm))
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "marshal frontmatter")
	}
	return []byte("---\n" + string(marshaled) + "---\n" + body), nil
}
