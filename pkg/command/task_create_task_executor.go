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
// A file that already exists at the resolved path causes ErrTaskAlreadyExists (a benign
// Failure on the result topic — no overwrite, no git write) UNLESS the existing file's
// status is terminal ("completed"/"aborted"), in which case a fresh non-terminal task is
// materialized at that path.
// Frontmatter must include "assignee" and "status"; missing fields return a wrapped validation error.
// Commands whose effective target vault (cmd.TargetVault or the legacy fallback) does not
// match vaultName are skipped without side effects (no git write, no error, no result event).
func NewCreateTaskExecutor(
	gitClient gitclient.GitClient,
	taskDir string,
	vaultName string,
	currentDateTime libtime.CurrentDateTimeGetter,
	k int,
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
				// ErrCommandObjectSkipped — not nil: a nil return with SendResultEnabled
				// publishes a spurious Success result on the shared result topic for every
				// cross-vault create (go-cqrs/skipped-not-nil-for-non-retryable).
				return nil, nil, errors.Wrapf(
					ctx,
					cdb.ErrCommandObjectSkipped,
					"cross-vault create for task %s",
					cmd.TaskIdentifier,
				)
			}
			if err := validateCreateTaskFrontmatter(ctx, cmd.Frontmatter); err != nil {
				return nil, nil, errors.Wrapf(ctx, err, "validate frontmatter")
			}
			relPath := resolveCreateTaskRelPath(ctx, taskDir, cmd)
			reopened, priorStatus, err := checkTitlePathFree(
				ctx,
				gitClient,
				relPath,
				cmd.TaskIdentifier,
			)
			if err != nil {
				return nil, nil, err
			}
			if err := writeTaskFile(ctx, gitClient, relPath, cmd, vaultName, reopened, priorStatus); err != nil {
				return nil, nil, err
			}
			supersedePriorRecurringTask(ctx, gitClient, taskDir, currentDateTime, k, cmd, relPath)
			return nil, nil, nil
		},
	)
}

// checkTitlePathFree reports whether the title path is free for a new task.
// The slot is free when the path is unoccupied (git-rest 404) OR when the
// existing file's frontmatter status is a terminal status ("completed" or
// "aborted", compared case-sensitively after whitespace trim) — a terminal
// task is no longer a live duplicate, so the slot is reusable. On a
// terminal-status free it returns reopened=true and the prior status so the
// caller can distinguish a reopen from a first-ever create. Every other
// occupied state — any non-terminal status, an absent/empty/unknown status,
// missing frontmatter delimiters, or unparseable YAML — returns
// ErrTaskAlreadyExists (a benign Failure on the result topic — no overwrite,
// no git write) wrapped with the "title path %s occupied" message shape. A
// transient git-rest read error is propagated. The decision consumes the
// bytes already read by this function; the caller must not issue a second
// ReadFile.
func checkTitlePathFree(
	ctx context.Context,
	gitClient gitclient.GitClient,
	relPath string,
	taskIdentifier lib.TaskIdentifier,
) (reopened bool, priorStatus string, err error) {
	existing, err := gitClient.ReadFile(ctx, relPath)
	if err != nil {
		if !isNotFoundReadError(err) {
			return false, "", errors.Wrapf(
				ctx, err, "check existing task file at %s for %s", relPath, taskIdentifier,
			)
		}
		// git-rest 404: the title path is free, first-ever create.
		return false, "", nil
	}
	frontmatterStr, parseErr := result.ExtractFrontmatter(ctx, existing)
	if parseErr != nil {
		glog.Warningf(
			"create-task: cannot read status from existing file at %s for %s; holding title path: %v",
			relPath,
			taskIdentifier,
			parseErr,
		)
		return false, "", errors.Wrapf(
			ctx,
			task.ErrTaskAlreadyExists,
			"title path %s occupied",
			relPath,
		)
	}
	existingFm, parseErr := parseTaskFrontmatter(frontmatterStr)
	if parseErr != nil {
		glog.Warningf(
			"create-task: cannot parse frontmatter of existing file at %s for %s; holding title path: %v",
			relPath,
			taskIdentifier,
			parseErr,
		)
		return false, "", errors.Wrapf(
			ctx,
			task.ErrTaskAlreadyExists,
			"title path %s occupied",
			relPath,
		)
	}
	status, _ := existingFm.String("status")
	status = strings.TrimSpace(status)
	if status == "completed" || status == "aborted" {
		return true, status, nil
	}
	glog.V(2).Infof(
		"create-task: title path %s already occupied (%d bytes), returning ErrTaskAlreadyExists for %s",
		relPath,
		len(existing),
		taskIdentifier,
	)
	return false, "", errors.Wrapf(
		ctx,
		task.ErrTaskAlreadyExists,
		"title path %s occupied",
		relPath,
	)
}

// writeTaskFile builds the task content and writes it atomically to the vault
// via git-rest, then logs the creation. vaultName is stamped as target_vault so
// the created task is self-describing for the result-path routing guard.
// reopened and priorStatus carry the reopen signal from the title-path collision
// check — reopened is true only when the slot was freed by a terminal-status
// file, and priorStatus is that file's status — for the write path to use.
// On reopened == true the write commits with a "[agent-task-controller] reopen
// terminal task <id>" message and emits the unconditional INFO log
// "create-task: reopening terminal task" (visible at default verbosity, naming
// the path and prior status); a first-ever create (reopened == false) carries
// neither — it keeps the "create task" commit message and the V(2) creation log.
func writeTaskFile(
	ctx context.Context,
	gitClient gitclient.GitClient,
	relPath string,
	cmd task.CreateCommand,
	vaultName string,
	reopened bool,
	priorStatus string,
) error {
	content, err := buildCreateTaskContent(ctx, cmd, vaultName)
	if err != nil {
		return errors.Wrapf(ctx, err, "build task file content for %s", cmd.TaskIdentifier)
	}
	absPath := filepath.Join(gitClient.Path(), relPath)
	msg := "[agent-task-controller] create task " + string(cmd.TaskIdentifier)
	if reopened {
		msg = "[agent-task-controller] reopen terminal task " + string(cmd.TaskIdentifier)
	}
	if err := gitClient.AtomicWriteAndCommitPush(
		ctx,
		absPath,
		content,
		msg,
	); err != nil {
		return errors.Wrapf(ctx, err, "atomic write and push for task %s", cmd.TaskIdentifier)
	}
	glog.V(2).Infof("create-task: created task file at %s for %s", relPath, cmd.TaskIdentifier)
	if reopened {
		glog.Infof(
			"create-task: reopening terminal task at %s (prior status %s)",
			relPath,
			priorStatus,
		)
	}
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

// validateCreateTaskFrontmatter rejects a create-command whose frontmatter is
// unusable downstream.
//
// `assignee` is deliberately NOT required. An empty assignee is the fleet's
// operator-inbox signal (see the "Make Parked Agent Tasks Visible to Operator"
// doctrine): escalation clears the assignee so no agent claims the task and a
// human picks it up. Requiring it here made a task that is *born* parked
// unrepresentable — github-pr-watcher's untrusted-author path stamps
// `assignee: "", phase: human_review` and every such command was rejected, so
// the PR silently never reached the operator queue (bborbe/git-sync#5,
// 2026-07-28). `vault_scanner` already treats an empty assignee as unclaimed,
// so accepting it at create matches how update and dispatch already behave.
func validateCreateTaskFrontmatter(ctx context.Context, fm lib.TaskFrontmatter) error {
	if s, _ := fm.String("status"); s == "" {
		return errors.Wrap(ctx, validation.Error, "frontmatter missing required field: status")
	}
	return nil
}

func buildCreateTaskContent(
	ctx context.Context,
	cmd task.CreateCommand,
	vaultName string,
) ([]byte, error) {
	fm := make(lib.TaskFrontmatter)
	maps.Copy(fm, cmd.Frontmatter)
	fm["task_identifier"] = string(cmd.TaskIdentifier)
	fm["target_vault"] = vaultName
	return marshalFileContent(ctx, fm, cmd.Body)
}

// supersedePriorRecurringTask collapses a recurring schedule to a single open
// instance: after a new instance is materialized it lists same-slug candidates,
// excludes the new instance, ranks them most-recent-first, and transitions every
// still-in_progress candidate whose period-token is strictly older than the new
// instance's token to aborted — capped at the k most-recent such candidates.
// Best-effort: list/read/parse/write errors on any single file are logged and
// swallowed; the already-written new instance is never rolled back. newRelPath is
// the repo-root-relative path of the new instance (the superseded_by back-pointer).
func supersedePriorRecurringTask(
	ctx context.Context,
	gitClient gitclient.GitClient,
	taskDir string,
	currentDateTime libtime.CurrentDateTimeGetter,
	k int,
	cmd task.CreateCommand,
	newRelPath string,
) {
	if !isEligibleForSupersede(cmd) {
		return
	}
	slug, newToken, ok := splitTitleToken(cmd.Title)
	if !ok {
		glog.V(3).Infof(
			"auto-supersede: new title %q has no period-token suffix, skipping for %s",
			cmd.Title, cmd.TaskIdentifier,
		)
		return
	}
	newOrdinal, err := parsePeriodTokenOrdinal(ctx, newToken)
	if err != nil {
		glog.Warningf(
			"auto-supersede: new token (len %d) unrecognized for %s: %v",
			len(newToken), cmd.TaskIdentifier, err,
		)
		return
	}
	titles, err := listSameSlugCandidateTitles(ctx, gitClient, taskDir, slug)
	if err != nil {
		glog.Warningf("auto-supersede: list candidates failed for slug %q: %v", slug, err)
		return
	}
	// Exclude the new instance and rank.
	var kept []string
	for _, t := range titles {
		if err := ctx.Err(); err != nil {
			return
		}
		if t == cmd.Title {
			continue
		}
		kept = append(kept, t)
	}
	ranked := rankSameSlugCandidatesDescending(ctx, kept)
	collapseCandidates(
		ctx,
		gitClient,
		taskDir,
		currentDateTime,
		k,
		ranked,
		newOrdinal,
		newRelPath,
		cmd,
	)
}

// listSameSlugCandidateTitles lists task files scoped to a schedule's slug and
// returns their TITLES (basename without ".md"), filtered to those whose title
// starts with "<slug> - ". It uses a slug-scoped glob when the slug contains no
// glob metacharacters; otherwise it lists all task files and filters in memory
// (glob-injection defense). List errors are returned; the caller logs them once.
func listSameSlugCandidateTitles(
	ctx context.Context,
	gitClient gitclient.GitClient,
	taskDir string,
	slug string,
) ([]string, error) {
	prefix := slug + " - "
	var glob string
	if strings.ContainsAny(slug, "*?[]\\") {
		glob = filepath.Join(taskDir, "*.md")
		glog.V(2).Infof(
			"auto-supersede: slug %q contains glob metacharacters; falling back to list-all + in-memory filter",
			slug,
		)
	} else {
		glob = filepath.Join(taskDir, slug+" - *.md")
		glog.V(3).Infof("auto-supersede: listing slug-scoped glob %q", glob)
	}
	relPaths, err := gitClient.ListFiles(ctx, glob)
	if err != nil {
		return nil, err
	}
	var titles []string
	for _, relPath := range relPaths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		title := strings.TrimSuffix(filepath.Base(relPath), ".md")
		if strings.HasPrefix(title, prefix) {
			titles = append(titles, title)
		}
	}
	return titles, nil
}

// collapseCandidates iterates ranked candidates most-recent-first, transitions
// each still-in_progress candidate older than newOrdinal to aborted, and stops
// after k candidates have been inspected (read attempted). Best-effort per file.
func collapseCandidates(
	ctx context.Context,
	gitClient gitclient.GitClient,
	taskDir string,
	currentDateTime libtime.CurrentDateTimeGetter,
	k int,
	ranked []rankedCandidate,
	newOrdinal int64,
	newRelPath string,
	cmd task.CreateCommand,
) {
	inspected := 0
	for _, rc := range ranked {
		if err := ctx.Err(); err != nil {
			return
		}
		if inspected >= k {
			break
		}
		if rc.Ordinal > newOrdinal {
			continue
		}
		inspected++
		candidateRelPath := filepath.Join(taskDir, rc.Title+".md")
		if strings.ContainsAny(rc.Title, "/\\") {
			glog.Warningf(
				"auto-supersede: candidate title %q contains path separator; skipping for %s",
				rc.Title, cmd.TaskIdentifier,
			)
			continue
		}
		content, err := readPriorForSupersede(ctx, gitClient, candidateRelPath, cmd.TaskIdentifier)
		if err != nil || content == nil {
			continue
		}
		if !priorIsInProgress(ctx, content, candidateRelPath, cmd.TaskIdentifier) {
			continue
		}
		transitionPrior(
			ctx,
			gitClient,
			currentDateTime,
			candidateRelPath,
			rc.Title,
			newRelPath,
			cmd.TaskIdentifier,
		)
	}
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
	glog.V(2).Infof(
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
