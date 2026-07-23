// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package command

import (
	"context"
	"fmt"
	"path/filepath"

	lib "github.com/bborbe/agent"
	task "github.com/bborbe/agent/command/task"
	"github.com/bborbe/cqrs/base"
	"github.com/bborbe/cqrs/cdb"
	"github.com/bborbe/errors"
	libkv "github.com/bborbe/kv"
	"github.com/golang/glog"
	"gopkg.in/yaml.v3"

	gitclient "github.com/bborbe/agent-task-controller/pkg/gitrestclient"
	"github.com/bborbe/agent-task-controller/pkg/metrics"
	"github.com/bborbe/agent-task-controller/pkg/result"
)

// IncrementFrontmatterCommandOperation is the CQRS command operation name for atomic field increment.
const IncrementFrontmatterCommandOperation base.CommandOperation = task.IncrementFrontmatterCommandOperation

// NewIncrementFrontmatterExecutor creates a cdb.CommandObjectExecutorTx that atomically
// reads the task file, increments the named frontmatter field by delta, and commits.
// If trigger_count reaches max_triggers the assignee is cleared and phase is preserved in the same write.
func NewIncrementFrontmatterExecutor(
	gitClient gitclient.GitClient,
	taskDir string,
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
			matchedRelPath, _, err := result.FindTaskFilePath(
				ctx,
				gitClient,
				taskDir,
				cmd.TaskIdentifier,
			)
			if err != nil {
				m.FrontmatterCommandsTotal("increment-frontmatter", "error").Inc()
				return nil, nil, errors.Wrapf(ctx, err, "find task file for increment")
			}
			if matchedRelPath == "" {
				glog.Warningf(
					"increment-frontmatter: task file not found for %s, skipping",
					cmd.TaskIdentifier,
				)
				m.FrontmatterCommandsTotal("increment-frontmatter", "not_found").Inc()
				return nil, nil, nil
			}
			fullAbsPath := filepath.Join(gitClient.Path(), matchedRelPath)
			if err := gitClient.AtomicReadModifyWriteAndCommitPush(
				ctx,
				fullAbsPath,
				buildIncrementModifyFn(ctx, cmd),
				fmt.Sprintf("[agent-task-controller] increment %s for task %s", cmd.Field, cmd.TaskIdentifier),
			); err != nil {
				m.FrontmatterCommandsTotal("increment-frontmatter", "error").Inc()
				return nil, nil, errors.Wrapf(
					ctx,
					err,
					"atomic increment for task %s",
					cmd.TaskIdentifier,
				)
			}
			m.FrontmatterCommandsTotal("increment-frontmatter", "success").Inc()
			return nil, nil, nil
		},
	)
}

func buildIncrementModifyFn(
	ctx context.Context,
	cmd task.IncrementFrontmatterCommand,
) func([]byte) ([]byte, error) {
	return func(current []byte) ([]byte, error) {
		frontmatterStr, err := result.ExtractFrontmatter(ctx, current)
		if err != nil {
			return nil, errors.Wrapf(ctx, err, "extract frontmatter")
		}
		body, err := result.ExtractBody(ctx, current)
		if err != nil {
			return nil, errors.Wrapf(ctx, err, "extract body")
		}
		fm, err := parseTaskFrontmatter(frontmatterStr)
		if err != nil {
			return nil, errors.Wrapf(ctx, err, "parse frontmatter")
		}
		currentVal := intFromFrontmatter(fm, cmd.Field)
		newVal := currentVal + cmd.Delta
		fm[cmd.Field] = newVal
		if cmd.Field == "trigger_count" && newVal >= fm.MaxTriggers() {
			fm["assignee"] = ""
		}
		return marshalFileContent(ctx, fm, body)
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

func marshalFileContent(ctx context.Context, fm lib.TaskFrontmatter, body string) ([]byte, error) {
	marshaled, err := yaml.Marshal(map[string]any(fm))
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "marshal frontmatter")
	}
	return []byte("---\n" + string(marshaled) + "---\n" + body), nil
}
