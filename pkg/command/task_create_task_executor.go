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
	"github.com/bborbe/agent-task-controller/pkg/vaultapi"
)

// NewCreateTaskExecutor creates a cdb.CommandObjectExecutorTx that materializes
// a new vault task file for the given task_identifier. If cmd.Title passes validation
// the file is written at tasks/{title}.md; otherwise it falls back to tasks/{task_identifier}.md.
// The identifier index is consulted before the create: when it already holds a
// file for the identifier, THAT file's title is the one sent, so a re-emit of one
// key under a different title cannot mint a second file carrying the same
// identifier.
// A file that already exists at the resolved path causes ErrTaskAlreadyExists (a benign
// Failure on the result topic — no overwrite, no git write) UNLESS the existing file's
// status is terminal ("completed"/"aborted") AND it carries this task's identifier, in
// which case a fresh non-terminal task is materialized at that path. A terminal file
// belonging to a DIFFERENT identifier is never replaced.
// Frontmatter must include "assignee" and "status"; missing fields return a wrapped validation error.
// Commands whose effective target vault (cmd.TargetVault or the legacy fallback) does not
// match vaultName are skipped without side effects (no git write, no error, no result event).
// resolver is the identifier→path index; a nil resolver looks nothing up and records
// nothing, and is a test-only value.
func NewCreateTaskExecutor(
	gitClient gitclient.GitClient,
	client vaultapi.Client,
	taskDir string,
	vaultName string,
	currentDateTime libtime.CurrentDateTimeGetter,
	k int,
	resolver result.TaskPathResolver,
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
			title := createTaskTitle(ctx, cmd)
			if indexedTitle, found, err := indexedTaskTitle(
				ctx, resolver, taskDir, cmd.TaskIdentifier,
			); err != nil {
				return nil, nil, err
			} else if found {
				// The index already knows a file for this identifier, so the create
				// must land on THAT file rather than on a path derived from this
				// command's title. Sending the occupant's own stem is what makes a
				// second emit of one key under a different title a no-op instead of a
				// second file carrying the same identifier (2026-10-27: 16 identifiers
				// shared across 35 files, and the re-drive streak store is keyed on the
				// identifier, so two records shared one counter).
				glog.V(2).Infof(
					"create-task: identifier %s is indexed at %s; creating there instead of at title %q",
					cmd.TaskIdentifier,
					filepath.Join(taskDir, indexedTitle+".md"),
					title,
				)
				title = indexedTitle
			}
			pathTitle, reopened, err := createThroughAPI(ctx, client, cmd, title, vaultName)
			if err != nil {
				return nil, nil, err
			}
			// No path check here on purpose. Every check that can fail has to run
			// BEFORE the create, because the file exists server-side the moment it
			// returns: failing afterwards would leave a materialized task that
			// recordCreatedTaskPath and the supersede hooks never see, so its
			// results would be dropped for the pod's life. createTaskTitle already
			// rejects separators (falling back to the identifier), the
			// disambiguated title is that title plus a fixed suffix, and ".md" is
			// appended — so no ".." path segment can form, and a title like
			// "Wait... what?" stays legal.
			relPath := filepath.Join(taskDir, pathTitle+".md")
			if reopened {
				// Unconditional Infof, not V(n)-gated, by design: a reopen replaces a
				// task an operator or another agent already closed, so it must be
				// visible in prod logs at default verbosity. Gating it behind -v=2
				// would hide the one event worth auditing. The prior status is not
				// in the typed response — the server replaces the file and reports
				// only that it did — so this line names the task and the path.
				glog.Infof(
					"create-task: reopening terminal task %s at %s",
					cmd.TaskIdentifier,
					relPath,
				)
			}
			recordCreatedTaskPath(ctx, resolver, cmd.TaskIdentifier, relPath)
			retireSupersededBuildFixTasks(ctx, gitClient, taskDir, currentDateTime, cmd, relPath)
			supersedePriorRecurringTask(ctx, gitClient, taskDir, currentDateTime, k, cmd, relPath)
			supersedePriorUpdateGoTasks(ctx, gitClient, taskDir, currentDateTime, k, cmd, relPath)
			return nil, nil, nil
		},
	)
}

// createTaskTitle returns the filename stem the typed create sends: the command's
// own title when it passes validation and carries no path separator, the
// identifier otherwise. The server owns the filename, so this is the only thing
// the controller still decides about the path.
func createTaskTitle(ctx context.Context, cmd task.CreateCommand) string {
	if err := cmd.Validate(ctx); err != nil {
		glog.Warningf(
			"create-task: Title validation failed for task %s (%v); falling back to the identifier",
			cmd.TaskIdentifier, err,
		)
		return string(cmd.TaskIdentifier)
	}
	if strings.ContainsAny(cmd.Title, "/\\") {
		glog.Warningf(
			"create-task: Title %q contains path separator; falling back to the identifier",
			cmd.Title,
		)
		return string(cmd.TaskIdentifier)
	}
	return cmd.Title
}

// indexedTaskTitle returns the filename stem of the file the identifier index
// already holds for id, so a create addresses that file instead of trusting
// cmd.Title alone.
//
// The file path is keyed on the title while the identifier lives in the
// frontmatter, so nothing about the title path tells a re-emit apart from a new
// task: a second emit of one key under a different title used to land on a free
// path and write a second file carrying the same identifier. The re-drive streak
// store is keyed on the identifier, so the two records then shared one counter
// and a record that failed three times capped the key and refused its siblings
// (2026-10-27: 16 identifiers across 35 files).
//
// A nil resolver (test-only) and an unknown identifier both report a miss, so
// the caller keeps the title-derived path. A miss is not evidence of absence —
// it is the ordinary answer for a genuinely new task.
//
// A duplicate identifier is returned as an error, never as a miss. Two files
// already carry it, so there is no single path to reuse, and picking either
// would write this create onto a record that may belong to a different task. The
// create then fails loudly and the vault is left untouched; adding a third file
// is the one outcome that must not happen. The known duplicate groups are a
// separate migration, not this lookup's business.
//
// A stale hit needs no guard here: the title it returns is re-resolved by the
// API, whose conflict names the real occupant, so a path since taken by another
// task reaches the same disambiguation as any other collision.
func indexedTaskTitle(
	ctx context.Context,
	resolver result.TaskPathResolver,
	taskDir string,
	id lib.TaskIdentifier,
) (string, bool, error) {
	if resolver == nil {
		return "", false, nil
	}
	relPath, found, err := resolver.Resolve(ctx, id)
	if err != nil {
		return "", false, errors.Wrapf(ctx, err, "resolve task_identifier %s", id)
	}
	if !found || relPath == "" {
		return "", false, nil
	}
	// Only a file directly inside the task directory names a task this executor
	// may address; anything else is treated as a miss rather than sent as a title.
	if filepath.Dir(relPath) != filepath.Clean(taskDir) {
		glog.Warningf(
			"create-task: identifier %s is indexed at %s, outside %s; keeping the title path",
			id,
			relPath,
			taskDir,
		)
		return "", false, nil
	}
	stem := strings.TrimSuffix(filepath.Base(relPath), ".md")
	if stem == "" {
		return "", false, nil
	}
	return stem, true, nil
}

// createThroughAPI materialises the task and settles the three answers the old
// title-path pre-check computed: created, reopened, or a collision.
//
// The pre-check is gone rather than ported. The API resolves the occupant by
// PATH and reports it in the 409, so the controller never touches the index's
// shared identifier/name/slug key space — the lookup that let a page elsewhere
// shadow a live task and destroy it in vault-api#28.
// It returns the title the file actually landed under — the request title, or
// the disambiguated one — because that, not the server's echo, is what the
// result-path index and the supersede back-pointers key off.
func createThroughAPI(
	ctx context.Context,
	client vaultapi.Client,
	cmd task.CreateCommand,
	title string,
	vaultName string,
) (pathTitle string, reopened bool, err error) {
	reopened, err = createWithTerminalReopen(ctx, client, cmd, title, vaultName)
	if err == nil {
		return title, reopened, nil
	}
	var conflict *vaultapi.AlreadyExistsError
	if !errors.As(err, &conflict) {
		return "", false, err
	}
	if conflict.TaskIdentifier == "" || conflict.TaskIdentifier == string(cmd.TaskIdentifier) {
		// Idempotent re-publish: this task already has a file, so nothing is lost.
		return "", false, errors.Wrapf(
			ctx,
			task.ErrTaskAlreadyExists,
			"title %q occupied for task %s",
			title,
			cmd.TaskIdentifier,
		)
	}
	if isRecurringTaskCommand(cmd) && vaultapi.IsTerminalStatus(conflict.Status) {
		// Recurring carve-out: a terminal recurring instance holds the slot until
		// the period rolls, because the title-path file IS the dedupe state. This
		// must be checked before disambiguation — otherwise the next hourly tick
		// would create a second file beside every completed instance and the
		// dedupe would never fire again (observed 2026-08-27: v0.6.0 wiped 27
		// completed monthly/quarterly/yearly/weekly tasks by reopening them).
		return "", false, errors.Wrapf(
			ctx,
			task.ErrTaskAlreadyExists,
			"title %q held by terminal recurring task %s",
			title,
			conflict.TaskIdentifier,
		)
	}
	// A DIFFERENT identifier holds the title: two tasks collided on one filename.
	// Give the loser its own file, or its results are dropped forever.
	alt := title + " - " + shortIdentifier(cmd.TaskIdentifier)
	reopened, err = createWithTerminalReopen(ctx, client, cmd, alt, vaultName)
	if err != nil {
		var altConflict *vaultapi.AlreadyExistsError
		if errors.As(err, &altConflict) {
			// The suffixed path is occupied too — on a replay of a disambiguated
			// task it holds this task's own file. That is the same benign
			// already-exists as any other re-publish, so it must map to the same
			// sentinel: downstream result-topic consumers special-case
			// task.ErrTaskAlreadyExists and treat anything else as a real failure.
			return "", false, errors.Wrapf(
				ctx,
				task.ErrTaskAlreadyExists,
				"title %q and %q both occupied for task %s",
				title,
				alt,
				cmd.TaskIdentifier,
			)
		}
		return "", false, errors.Wrapf(ctx, err, "disambiguated create at %q", alt)
	}
	return alt, reopened, nil
}

// createWithTerminalReopen creates at title, replacing a terminal occupant ONLY
// when that occupant is this task (or carries no identifier at all).
//
// The reopen is proved from the occupant, never assumed from the command.
// ReopenTerminal used to be sent up front for every non-recurring create, so a
// create whose title collided with a CLOSED task of a different identifier
// replaced that file and stamped its own identifier over the closed record's —
// the same identity confusion as the title-keyed path itself, one layer down: a
// closed task's identifier disappears from the vault and its re-drive history is
// destroyed (2026-10-27). The conflict names the occupant, so the first attempt
// asks for no reopen at all and a second is issued only when the answer proves
// the occupant is terminal AND this task's own.
//
// A live occupant is never replaced: two attempts must not share an identity. An
// occupant with no identifier cannot be proved to be another task, so it keeps
// the reopen it has always had. A recurring command never reopens — the
// title-path file IS its dedupe state.
func createWithTerminalReopen(
	ctx context.Context,
	client vaultapi.Client,
	cmd task.CreateCommand,
	title string,
	vaultName string,
) (bool, error) {
	reopened, err := postCreate(ctx, client, cmd, title, vaultName, false)
	if err == nil {
		return reopened, nil
	}
	var conflict *vaultapi.AlreadyExistsError
	if !errors.As(err, &conflict) {
		return false, err
	}
	if isRecurringTaskCommand(cmd) || !vaultapi.IsTerminalStatus(conflict.Status) {
		return false, err
	}
	if conflict.TaskIdentifier != "" && conflict.TaskIdentifier != string(cmd.TaskIdentifier) {
		return false, err
	}
	return postCreate(ctx, client, cmd, title, vaultName, true)
}

// postCreate sends one create and reports whether it replaced a terminal
// occupant. It returns no task: the only field the caller could use is the
// server's echoed title, and the caller deliberately keys off the title it
// asked for instead. reopen is the caller's proof that the occupant is terminal
// and this task's own — createWithTerminalReopen is the only caller that decides
// it, from the conflict rather than from the command.
func postCreate(
	ctx context.Context,
	client vaultapi.Client,
	cmd task.CreateCommand,
	title string,
	vaultName string,
	reopen bool,
) (bool, error) {
	fm := make(lib.TaskFrontmatter)
	maps.Copy(fm, cmd.Frontmatter)
	fm["task_identifier"] = string(cmd.TaskIdentifier)
	fm["target_vault"] = vaultName
	created, err := client.CreateTask(
		ctx,
		vaultapi.Task{Title: title, Frontmatter: fm, Body: cmd.Body},
		vaultapi.CreateOptions{ReopenTerminal: reopen},
	)
	if err != nil {
		return false, err
	}
	return created.Reopened, nil
}

// shortIdentifier is the filename-suffix form of an identifier, matching the
// sha[:8] short-segment convention used across task filenames.
func shortIdentifier(id lib.TaskIdentifier) string {
	s := string(id)
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

// recordCreatedTaskPath adds the file just written to the identifier→path index, so
// the results and frontmatter updates that follow a create resolve with one read
// instead of a full-vault walk. A trigger-only controller builds its index once at
// pod start, so without this every task created afterwards missed for the pod's
// whole life (2026-10-25: 155 of 159 walked identifiers, consumer 60 min behind,
// commands dropped as expired). It records only when the resolver reports a clean
// miss: a hit means the index already knows a file for this identifier, and a
// resolver error means a known duplicate — neither may be overwritten.
func recordCreatedTaskPath(
	ctx context.Context,
	resolver result.TaskPathResolver,
	id lib.TaskIdentifier,
	relPath string,
) {
	if resolver == nil {
		return
	}
	if _, found, err := resolver.Resolve(ctx, id); err != nil || found {
		return
	}
	resolver.Record(ctx, id, relPath)
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
			"[agent-task-controller] auto-supersede prior recurring task ",
			true,
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
//
// msgPrefix and setCreatedBy are supplied by the caller because the two hooks
// that share this transition must not share its attribution. The recurring
// publisher stamps created_by: recurring-task-creator, which is truthful there;
// a github-update-go prior was emitted by the update-go watcher, so stamping it
// would overwrite its real created_by with a false one. The build-fix retire
// path (buildBuildFixRetireModifyFn) already omits the field for the same
// reason. An empty msgPrefix means the caller builds the whole message.
func transitionPrior(
	ctx context.Context,
	gitClient gitclient.GitClient,
	currentDateTime libtime.CurrentDateTimeGetter,
	priorRelPath string,
	priorTitle string,
	newRelPath string,
	taskIdentifier lib.TaskIdentifier,
	msgPrefix string,
	setCreatedBy bool,
) {
	ts := currentDateTime.Now().UTC().Format(time.RFC3339)
	priorAbsPath := filepath.Join(gitClient.Path(), priorRelPath)
	modify := buildSupersedeModifyFn(ctx, newRelPath, ts, setCreatedBy)
	msg := msgPrefix + priorTitle
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
// that transitions a prior instance to aborted. setCreatedBy stamps
// created_by: recurring-task-creator, which is only truthful for a prior the
// recurring publisher emitted — the update-go hook passes false so it leaves the
// prior's real attribution alone.
func buildSupersedeModifyFn(
	ctx context.Context,
	newRelPath string,
	ts string,
	setCreatedBy bool,
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
		if setCreatedBy {
			fm["created_by"] = "recurring-task-creator"
		}
		return marshalFileContent(ctx, fm, body)
	}
}

// retireSupersededBuildFixTasks retires the tasks a re-emitted build-fix task
// supersedes: every other still-live task carrying the same build_id, and the
// task whose task_identifier matches supersedes_task_id. Both markers are read
// from the create command's frontmatter; an absent or empty marker is treated
// as absent and never matches anything. Best-effort: a list, read, parse or
// write failure on any single file is logged and swallowed, and the
// already-created task is never rolled back. newRelPath is the repo-root-
// relative path of the task that was just written (the superseded_by
// back-pointer) and must not be re-derived — the path may have been
// disambiguated with a short-identifier suffix.
func retireSupersededBuildFixTasks(
	ctx context.Context,
	gitClient gitclient.GitClient,
	taskDir string,
	currentDateTime libtime.CurrentDateTimeGetter,
	cmd task.CreateCommand,
	newRelPath string,
) {
	supersedesTaskID, _ := cmd.Frontmatter.String("supersedes_task_id")
	supersedesBuildID, _ := cmd.Frontmatter.String("supersedes_build_id")
	if supersedesTaskID == "" && supersedesBuildID == "" {
		return
	}
	relPaths, err := gitClient.ListFiles(ctx, filepath.Join(taskDir, "*.md"))
	if err != nil {
		glog.Warningf(
			"auto-supersede build-fix: list candidates failed for %s: %v",
			cmd.TaskIdentifier,
			err,
		)
		return
	}
	for _, relPath := range relPaths {
		if err := ctx.Err(); err != nil {
			return
		}
		if relPath == newRelPath {
			continue
		}
		retireSupersededBuildFixCandidate(
			ctx,
			gitClient,
			currentDateTime,
			relPath,
			supersedesTaskID,
			supersedesBuildID,
			newRelPath,
			cmd.TaskIdentifier,
		)
	}
}

// retireSupersededBuildFixCandidate retires one candidate task file when a
// marker names it and it is still live. Every failure is logged and swallowed.
func retireSupersededBuildFixCandidate(
	ctx context.Context,
	gitClient gitclient.GitClient,
	currentDateTime libtime.CurrentDateTimeGetter,
	relPath string,
	supersedesTaskID string,
	supersedesBuildID string,
	newRelPath string,
	taskIdentifier lib.TaskIdentifier,
) {
	content, err := gitClient.ReadFile(ctx, relPath)
	if err != nil {
		glog.Warningf(
			"auto-supersede build-fix: read %s failed for %s: %v",
			relPath,
			taskIdentifier,
			err,
		)
		return
	}
	fm, _, err := parseTaskFrontmatterAndBody(ctx, content)
	if err != nil {
		glog.Warningf(
			"auto-supersede build-fix: parse %s failed for %s: %v",
			relPath,
			taskIdentifier,
			err,
		)
		return
	}
	if !buildFixCandidateMatches(fm, supersedesTaskID, supersedesBuildID) {
		return
	}
	if !buildFixCandidateIsLive(fm) {
		return
	}
	buildID, _ := fm.String("build_id")
	ts := currentDateTime.Now().UTC().Format(time.RFC3339)
	absPath := filepath.Join(gitClient.Path(), relPath)
	msg := "[agent-task-controller] auto-supersede build-fix: " + relPath
	if err := gitClient.AtomicReadModifyWriteAndCommitPush(
		ctx,
		absPath,
		buildBuildFixRetireModifyFn(ctx, newRelPath, ts),
		msg,
	); err != nil {
		glog.Warningf(
			"auto-supersede build-fix: write %s failed for %s: %v",
			relPath,
			taskIdentifier,
			err,
		)
		return
	}
	glog.V(2).Infof(
		"auto-supersede build-fix: %s -> %s (build %s retired)",
		relPath,
		newRelPath,
		buildID,
	)
}

// buildFixCandidateMatches reports whether a candidate task file is named by
// one of the two supersede markers. Matching is always by value on the parsed
// frontmatter — the marker is never joined into a path. An empty marker
// matches nothing.
func buildFixCandidateMatches(
	fm lib.TaskFrontmatter,
	supersedesTaskID string,
	supersedesBuildID string,
) bool {
	if supersedesTaskID != "" {
		if id, _ := fm.String("task_identifier"); id == supersedesTaskID {
			return true
		}
	}
	if supersedesBuildID != "" {
		if id, _ := fm.String("build_id"); id == supersedesBuildID {
			return true
		}
	}
	return false
}

// buildFixCandidateIsLive reports whether a candidate is still live: its
// status is readable and is neither of the two terminal statuses. An absent,
// empty, non-string or unparseable status is not live, so it is never retired.
func buildFixCandidateIsLive(fm lib.TaskFrontmatter) bool {
	status, _ := fm.String("status")
	status = strings.TrimSpace(status)
	if status == "" {
		return false
	}
	return status != "completed" && status != "aborted"
}

// buildBuildFixRetireModifyFn builds the modify closure for
// AtomicReadModifyWriteAndCommitPush that transitions a superseded build-fix
// task to aborted. It writes the same frozen transition set the recurring
// supersede writes and deliberately does NOT write created_by: that value names
// the recurring publisher and would be false on a build-fix task, whose
// created_by is whatever the watcher's create command carried.
func buildBuildFixRetireModifyFn(
	ctx context.Context,
	newRelPath string,
	ts string,
) func([]byte) ([]byte, error) {
	return func(current []byte) ([]byte, error) {
		fm, body, err := parseTaskFrontmatterAndBody(ctx, current)
		if err != nil {
			return nil, err
		}
		fm["status"] = "aborted"
		fm["phase"] = "done"
		fm["completed_date"] = ts
		fm["superseded_by"] = newRelPath
		return marshalFileContent(ctx, fm, body)
	}
}

// parseTaskFrontmatterAndBody extracts a task file's frontmatter and body and
// parses the frontmatter into a map. Extracted so the retire closure stays a
// few lines and its token overlap with the sibling modify closure stays far
// below the dupl linter's 150-token run.
func parseTaskFrontmatterAndBody(
	ctx context.Context,
	current []byte,
) (lib.TaskFrontmatter, string, error) {
	fmStr, err := result.ExtractFrontmatter(ctx, current)
	if err != nil {
		return nil, "", errors.Wrapf(ctx, err, "extract frontmatter")
	}
	body, err := result.ExtractBody(ctx, current)
	if err != nil {
		return nil, "", errors.Wrapf(ctx, err, "extract body")
	}
	fm, err := parseTaskFrontmatter(fmStr)
	if err != nil {
		return nil, "", errors.Wrapf(ctx, err, "parse frontmatter")
	}
	return fm, body, nil
}
