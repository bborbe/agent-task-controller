// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package command

import (
	"context"
	stderrors "errors"
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

// errTitlePathOccupiedByOtherIdentifier signals that the title path is occupied
// by a DIFFERENT task_identifier — a filename collision between two distinct
// tasks, not a re-publish of the same task. checkTitlePathFree returns it
// instead of task.ErrTaskAlreadyExists so the caller can disambiguate the path
// (short-identifier suffix) and give the losing identifier its own file; before
// this fix the losing task was never materialized and every result for it was
// dropped silently forever (two-identifiers-one-title-path, fixed here).
var errTitlePathOccupiedByOtherIdentifier = stderrors.New(
	"title path occupied by a different task identifier",
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
				cmd,
			)
			if errors.Is(err, errTitlePathOccupiedByOtherIdentifier) {
				// Two distinct task_identifiers targeted one title path. The
				// occupant belongs to a different task, so this is a filename
				// collision, not a re-publish of this task. Disambiguate the
				// path with a short-identifier suffix so this task still gets
				// its own file — the previous behavior dropped the losing
				// identifier's results forever.
				relPath, reopened, priorStatus, err = disambiguateCreateTaskRelPath(
					ctx,
					gitClient,
					taskDir,
					cmd,
				)
			}
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
// "aborted", compared case-sensitively after whitespace trim) and the command
// is not a recurring instance — a terminal task is no longer a live duplicate,
// so the slot is reusable. On a terminal-status free it returns reopened=true
// and the prior status so the caller can distinguish a reopen from a first-ever
// create. A live (non-terminal) occupant that is a DIFFERENT task_identifier
// returns errTitlePathOccupiedByOtherIdentifier so the caller disambiguates the
// path instead of orphaning the incoming identifier (two-identifiers-one-title-
// path; previously the loser's results were dropped forever). Every other
// occupied state — any non-terminal status with the same (or unreadable)
// identifier, an absent/empty/unknown status, missing frontmatter delimiters,
// or unparseable YAML — returns ErrTaskAlreadyExists (a benign Failure on the
// result topic — no overwrite, no git write) wrapped with the "title path %s
// occupied" message shape. A transient git-rest read error is propagated. The
// decision consumes the bytes already read by this function; the caller must
// not issue a second ReadFile.
//
// Recurring-task instances are carved out of the reopen path: a command from
// the recurring-task-creator publisher (created_by: recurring-task-creator)
// NEVER reopens a terminal-status file. The recurring pipeline republishes the
// same title path every tick (always-fire cadences), and the title-path file IS
// the dedupe state — so a completed instance must hold the slot until the
// period rolls, exactly as it did before the per-alert reopen feature. Without
// this carve-out, the next hourly tick reopens and overwrites every completed
// recurring task with a fresh blank instance (observed 2026-08-27: v0.6.0 wiped
// 27 completed monthly/quarterly/yearly/weekly tasks).
func checkTitlePathFree(
	ctx context.Context,
	gitClient gitclient.GitClient,
	relPath string,
	taskIdentifier lib.TaskIdentifier,
	cmd task.CreateCommand,
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
		if !isRecurringTaskCommand(cmd) {
			return true, status, nil
		}
		// Recurring carve-out (unchanged): a terminal recurring instance holds
		// the slot until the period rolls — the file IS the dedupe state, so no
		// reopen and no disambiguation for terminal files.
		return false, "", occupiedErr(ctx, relPath, existing, taskIdentifier)
	}
	// Live (non-terminal) occupant with a DIFFERENT identifier = a filename
	// collision between two tasks, not a re-publish of this one — signal the
	// caller to disambiguate instead of orphaning this identifier. Same-id (or
	// unreadable-id) occupancy keeps ErrTaskAlreadyExists: idempotent re-publish,
	// the task already has a file, nothing is lost.
	existingID, _ := existingFm.String("task_identifier")
	if existingID != "" && existingID != string(taskIdentifier) {
		return false, "", errors.Wrapf(
			ctx,
			errTitlePathOccupiedByOtherIdentifier,
			"title path %s occupied by task %s",
			relPath,
			existingID,
		)
	}
	return false, "", occupiedErr(ctx, relPath, existing, taskIdentifier)
}

// occupiedErr reports the benign already-exists outcome for an occupied title
// path (V(2) log naming the occupant bytes + wrapped ErrTaskAlreadyExists),
// shared by the recurring carve-out and the same-identifier occupancy path.
func occupiedErr(
	ctx context.Context,
	relPath string,
	existing []byte,
	taskIdentifier lib.TaskIdentifier,
) error {
	glog.V(2).Infof(
		"create-task: title path %s already occupied (%d bytes), returning ErrTaskAlreadyExists for %s",
		relPath,
		len(existing),
		taskIdentifier,
	)
	return errors.Wrapf(
		ctx,
		task.ErrTaskAlreadyExists,
		"title path %s occupied",
		relPath,
	)
}

// writeTaskFile builds the task content and creates it in the vault via
// git-rest, then logs the creation. vaultName is stamped as target_vault so the
// created task is self-describing for the result-path routing guard.
// reopened and priorStatus carry the reopen signal from the title-path collision
// check — reopened is true only when the slot was freed by a terminal-status
// file, and priorStatus is that file's status — for the write path to use.
// On reopened == true the write commits with a "[agent-task-controller] reopen
// terminal task <id>" message and emits the unconditional INFO log
// "create-task: reopening terminal task" (visible at default verbosity, naming
// the path and prior status); a first-ever create (reopened == false) carries
// neither — it keeps the "create task" commit message and the V(2) creation log.
//
// The write is create-only (?create_only=1) whenever reopened == false — which
// covers every first-ever create AND every recurring-task instance (those never
// reopen, see checkTitlePathFree). git-rest refuses to overwrite an
// already-occupied path with 409 Conflict, surfaced here as ErrAlreadyExists and
// mapped to task.ErrTaskAlreadyExists (a benign Failure, no git write). This is
// the authoritative no-overwrite guarantee: even if checkTitlePathFree's read
// falsely reports the path free, the create-only write can never clobber an
// existing task file. The upsert path is used ONLY for reopened == true, the
// deliberate per-alert reopen of a terminal task.
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
		if err := gitClient.AtomicWriteAndCommitPush(
			ctx,
			absPath,
			content,
			msg,
		); err != nil {
			return errors.Wrapf(ctx, err, "atomic write and push for task %s", cmd.TaskIdentifier)
		}
	} else {
		if err := createTaskFileIfAbsent(
			ctx,
			gitClient,
			absPath,
			content,
			msg,
			relPath,
			cmd,
		); err != nil {
			return err
		}
	}
	glog.V(2).Infof("create-task: created task file at %s for %s", relPath, cmd.TaskIdentifier)
	// Unconditional Infof, not V(n)-gated, by design: a reopen overwrites a
	// task an operator (or another agent) already closed, so it must be
	// visible in prod logs at default verbosity. Gating it behind -v=2 would
	// make the one event worth auditing the one event nobody sees. The
	// first-ever-create line above stays V(2) — that one is routine.
	if reopened {
		glog.Infof(
			"create-task: reopening terminal task at %s (prior status %s)",
			relPath,
			priorStatus,
		)
	}
	return nil
}

// createTaskFileIfAbsent performs the create-only write for a non-reopen create
// (a first-ever create or a recurring-task instance). git-rest refuses an
// occupied path with 409 Conflict, surfaced here as ErrAlreadyExists and mapped
// to task.ErrTaskAlreadyExists (a benign Failure, no git write). Extracted from
// writeTaskFile to keep its nesting within the linter budget.
func createTaskFileIfAbsent(
	ctx context.Context,
	gitClient gitclient.GitClient,
	absPath string,
	content []byte,
	msg string,
	relPath string,
	cmd task.CreateCommand,
) error {
	if err := gitClient.AtomicWriteIfAbsentAndCommitPush(ctx, absPath, content, msg); err != nil {
		if errors.Is(err, gitclient.ErrAlreadyExists) {
			glog.V(2).Infof(
				"create-task: title path %s occupied (create-only write refused), returning ErrTaskAlreadyExists for %s",
				relPath,
				cmd.TaskIdentifier,
			)
			return errors.Wrapf(
				ctx,
				task.ErrTaskAlreadyExists,
				"title path %s occupied",
				relPath,
			)
		}
		return errors.Wrapf(
			ctx,
			err,
			"atomic create-only write and push for task %s",
			cmd.TaskIdentifier,
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

// disambiguateCreateTaskRelPath resolves an alternate title path for a create
// whose original title path is occupied by a DIFFERENT task_identifier. Two
// distinct tasks collided on one filename; this gives the losing task its own
// file by appending a short-identifier suffix ("{Title} - {id[:8]}.md",
// matching the sha[:8]/retry-token short-segment convention used across task
// filenames), so its results can never be lost to the occupant's path. The
// candidate path is re-checked via checkTitlePathFree so the same
// same-identifier-republish and terminal-reopen semantics apply; if the
// suffixed path is itself occupied, the propagated error keeps the existing
// benign ErrTaskAlreadyExists-style outcome (a rare race — skipped create, no
// data loss). The caller (NewCreateTaskExecutor) is the only consumer, and it
// reaches this only when the identifier-derived UUID fallback path (already
// unique per identifier) is not in play.
func disambiguateCreateTaskRelPath(
	ctx context.Context,
	gitClient gitclient.GitClient,
	taskDir string,
	cmd task.CreateCommand,
) (relPath string, reopened bool, priorStatus string, err error) {
	// Defense-in-depth: a title that fails validation here means the original
	// path was the identifier-derived fallback, which is already unique per
	// identifier — a collision there cannot be a different-identifier clash.
	if err := cmd.Validate(ctx); err != nil {
		return "", false, "", errors.Wrapf(
			ctx,
			task.ErrTaskAlreadyExists,
			"title path %s occupied",
			cmd.Title,
		)
	}
	short := string(cmd.TaskIdentifier)
	if len(short) > 8 {
		short = short[:8]
	}
	candidate := filepath.Join(taskDir, cmd.Title+" - "+short+".md")
	reopened, priorStatus, err = checkTitlePathFree(
		ctx,
		gitClient,
		candidate,
		cmd.TaskIdentifier,
		cmd,
	)
	if err != nil {
		return "", false, "", err
	}
	glog.V(2).Infof(
		"create-task: title path occupied by a different identifier, disambiguating for %s to %s",
		cmd.TaskIdentifier,
		candidate,
	)
	return candidate, reopened, priorStatus, nil
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

// isRecurringTaskCommand reports whether cmd was published by the recurring-task
// creator (created_by: recurring-task-creator). Such commands must never reopen a
// terminal-status task file — the recurring pipeline republishes the same title
// path every tick and the file IS the dedupe state, so a completed instance must
// hold the slot until its period rolls. The predicate is created_by-only (unlike
// isEligibleForSupersede, which additionally requires auto_abort_prior): monthly
// schedules carry auto_abort_prior=false and must still be held.
func isRecurringTaskCommand(cmd task.CreateCommand) bool {
	createdBy, _ := cmd.Frontmatter.String("created_by")
	return createdBy == "recurring-task-creator"
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
