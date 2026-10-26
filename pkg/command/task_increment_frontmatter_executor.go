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
	"github.com/golang/glog"
	"gopkg.in/yaml.v3"

	"github.com/bborbe/agent-task-controller/pkg/metrics"
	"github.com/bborbe/agent-task-controller/pkg/vaultapi"
)

// IncrementFrontmatterCommandOperation is the CQRS command operation name for atomic field increment.
const IncrementFrontmatterCommandOperation base.CommandOperation = task.IncrementFrontmatterCommandOperation

// NewIncrementFrontmatterExecutor creates a cdb.CommandObjectExecutorTx that atomically
// reads the task file, increments the named frontmatter field by delta, and commits.
// If trigger_count reaches an explicitly-set max_triggers the assignee is cleared and
// phase is preserved in the same write. An absent max_triggers means no cap: the routing
// assignee is never stripped on recurring tasks that accumulate trigger_count.
// vaultName is the controller's VAULT_NAME; a command whose non-empty TargetVault
// differs is cross-vault traffic and is skipped before the task-file lookup.
// resolver is the scanner's identifier→path index, consulted by the lookup before
// it walks the vault.
func NewIncrementFrontmatterExecutor(
	client vaultapi.Client,
	vaultName string,
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
			if _, err := client.PatchTask(
				ctx,
				task.ID,
				buildIncrementPatch(cmd, task.Frontmatter, vaultName),
			); err != nil {
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

// buildIncrementPatch computes the fields to PATCH from the task as it stands.
//
// It replaces a whole-file read-modify-write: the old shape parsed the YAML,
// mutated it and re-serialised the file, so every unrelated key round-tripped
// through marshal. The typed surface names the fields, so only the fields this
// command changes are sent — and the API applies them atomically per task.
func buildIncrementPatch(
	cmd task.IncrementFrontmatterCommand,
	frontmatter map[string]any,
	vaultName string,
) map[string]any {
	fm := lib.TaskFrontmatter(frontmatter)
	currentVal := intFromFrontmatter(fm, cmd.Field)
	newVal := currentVal + cmd.Delta
	patch := map[string]any{cmd.Field: newVal}
	// The trigger cap is opt-in: an absent max_triggers means no cap, so a
	// recurring task that accumulates trigger_count across re-dispatches
	// never has its routing assignee stripped (the lib default-3 fallback
	// would silently kill the re-dispatch loop). Only the trigger_count
	// field drives the cap — never compare another incremented field
	// against max_triggers.
	if cmd.Field == "trigger_count" {
		if _, ok := fm["max_triggers"]; ok && newVal >= fm.MaxTriggers() {
			patch["assignee"] = ""
		}
	}
	// Heal-on-write: stamp target_vault on legacy tasks lacking it so the
	// non-owning controller permanently stops falling through.
	if _, ok := fm["target_vault"]; !ok {
		patch["target_vault"] = vaultName
	}
	return patch
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
