---
status: completed
spec: [014-bug-retire-superseded-buildfix-tasks]
summary: Added retireSupersededBuildFixTasks to the create path so a re-emitted build-fix task retires the other live task for its build_id and the task named by supersedes_task_id, writing the frozen four-field transition (never created_by), best-effort and never rolling back the new task, plus 12 new specs and the three doc artifacts (spec 014).
execution_id: agent-task-controller-supersede-retire-exec-025-spec-014-retire-superseded-buildfix-tasks
dark-factory-version: dev
created: "2026-09-18T05:25:00Z"
queued: "2026-09-18T05:55:42Z"
started: "2026-09-18T05:55:44Z"
completed: "2026-09-18T06:00:49Z"
branch: dark-factory/bug-retire-superseded-buildfix-tasks
---

# Retire the superseded build-fix task on create

<summary>

- A build re-emitted with a supersede marker now leaves exactly one live task: the earlier task for that build is retired instead of sitting alongside the replacement.
- The retirement reads the two markers the build watcher stamps on a re-emission — the one naming the replaced task and the one naming the build.
- A command naming a build retires every other still-live task for that build; a command naming a task identifier retires the task carrying that identifier.
- A command carrying neither marker, or carrying empty markers, behaves exactly as it does today — nothing is listed, nothing is written.
- A retired task is written to the same terminal state the recurring supersede already writes, so the vault reads the same way under both mechanisms; the provenance field the recurring mechanism owns is deliberately not written.
- The retirement is best-effort: a failure while listing, reading, parsing or writing any single file is logged and swallowed, and the task that was just created is never rolled back.
- The retirement never retires the task it has just created, which matters because the build marker always names the creating task's own build.
- Its log lines and its commit message carry one frozen prefix; the operator-facing record is the log lines, because the commit message never reaches git and is not visible at the deployed verbosity.
- Three documents record the contract so the knowledge outlives this change: the period-token semantics doc, the controller design doc, and the changelog.
- The recurring-schedule supersede is untouched — this is a sibling mechanism on the same create path, not a generalisation of it.

</summary>

<objective>

Make the create path consume the two supersede markers a re-emitted build-fix task carries. After a build-fix task file is written, the controller retires any other live task for the same `build_id` and the task whose `task_identifier` matches `supersedes_task_id`, writing the same frozen transition set the recurring supersede writes, best-effort and never rolling back the new task — so a re-classified build leaves exactly one dispatchable task instead of two concurrent build-fixer runs.

</objective>

<context>

This repo has no root `CLAUDE.md`; the global YOLO container CLAUDE.md already in your context governs project conventions.

Read the spec first: `specs/in-progress/014-bug-retire-superseded-buildfix-tasks.md` — Summary, Problem, Goal, Desired Behavior 1-7, Constraints, Failure Modes (every row), Security / Abuse Cases, Acceptance Criteria 1-9, and the "Suggested Decomposition" table (this prompt is its single row; AC10 is the operator-side Post-Deploy rung and is **not** in this prompt's scope).

Read these files IN FULL before writing anything:

- `pkg/command/task_create_task_executor.go` (818 lines) — the whole file. The create callback is in `NewCreateTaskExecutor`; the sibling mechanism you are mirroring is `supersedePriorRecurringTask` / `collapseCandidates` / `transitionPrior` / `buildSupersedeModifyFn`; the in-package helpers you will reuse are `parseTaskFrontmatter` (declared in `pkg/command/task_increment_frontmatter_executor.go`) and `marshalFileContent` (same file). Note `isNotFoundReadError` and the existing terminal-status idiom in `checkTitlePathFree` (`status, _ := existingFm.String("status"); status = strings.TrimSpace(status); if status == "completed" || status == "aborted"`).
- `pkg/command/task_create_task_executor_test.go` (1360 lines) — the whole file. Note the harness in the top-level `BeforeEach` (`fakeGit` is `*mocks.GitClient`, `fakeGit.PathReturns(tmpDir)`, `taskDir = "tasks"`, the two write stubs write to disk, `fakeGit.ReadFileReturns(nil, errors.New("GET file returned 404: not found"))`), the `buildCmdObj` closure, and the `Context("scan-and-collapse supersede", ...)` block at the end — your new `Context` is a sibling of it at the same indentation (two tabs), inserted after it and before the `})` that closes `Describe("HandleCommand")`.
- `pkg/gitrestclient/git_rest_client.go` lines 340-394 — the `GitClient` interface. The only methods this change uses are `ListFiles(ctx, glob) ([]string, error)`, `ReadFile(ctx, relPath) ([]byte, error)`, `Path() string`, and `AtomicReadModifyWriteAndCommitPush(ctx, absPath, modify func(current []byte) ([]byte, error), message string) error`.
- `pkg/result/result_writer.go` — `ExtractFrontmatter(ctx, content) (string, error)` (line 1076) and `ExtractBody(ctx, content) (string, error)` (line 1095). `ExtractFrontmatter` only slices the text between the `---` delimiters; a YAML syntax error therefore surfaces in `parseTaskFrontmatter`, not here.
- `docs/period-token-semantics.md` — the frozen transition block under `### Collapse (Auto-Supersede)`; its last line is `created_by: recurring-task-creator`.
- `docs/controller-design.md` — the `**Heal-on-write.**` paragraph is the last paragraph of `### 2. Command Processing (Kafka → git)`; it ends "…create commands already route through `ShouldProcess` into the owning vault." and is followed by the `## Frontmatter Merge` heading.
- `CHANGELOG.md` — the frozen preamble is the `# Changelog` title, the "All notable changes…" line and the two SemVer lines; there is currently NO `## Unreleased` section and the newest section is `## v0.10.1`.
- `mocks/git_client.go` — the counterfeiter fake regenerated by `make generate`. Confirm the accessors you use exist: `ListFilesReturns`, `ListFilesCallCount`, `ReadFileStub`, `AtomicWriteIfAbsentAndCommitPushStub`, `AtomicWriteIfAbsentAndCommitPushArgsForCall(i) (context.Context, string, []byte, string)`, `AtomicWriteAndCommitPushCallCount`, `AtomicReadModifyWriteAndCommitPushStub`, `AtomicReadModifyWriteAndCommitPushCallCount`, `AtomicReadModifyWriteAndCommitPushReturns(error)`, `AtomicReadModifyWriteAndCommitPushArgsForCall(i) (context.Context, string, func([]byte) ([]byte, error), string)`, and `ListFilesArgsForCall(i) (context.Context, string)`.

Read the coding-plugin docs (in-container paths):

- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo/Gomega suite + spec style, counterfeiter mocks, external test package.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — `github.com/bborbe/errors` `Wrapf`, never `fmt.Errorf`.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-precommit.md` — the linter budgets this file is held to: `funlen` 80 lines / 50 statements, `nestif` complexity 4, `gocognit` 20, `golines --max-len=100`. The `dupl` linter is **not** in that doc: its threshold is golangci-lint's default of 150 duplicated tokens, and `.golangci.yml` sets no override and excludes `dupl` for `_test\.go$`.
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — entry format, required prefix, one bullet per logical change, `## Unreleased` placement.
- `/home/node/.claude/plugins/marketplaces/coding/docs/documentation-guide.md` — prose style for the two doc edits.

Environment facts that shape this prompt:

- The execution container's `.git` is masked when the daemon is launched with `hideGit=true` (`git status` then fails with `fatal: not a git repository`; this repo's `.dark-factory.yaml` sets `workflow: direct` and no `hideGit`, so the masking comes from the launch flags, not the repo config). Do NOT run any `git` command anywhere in this prompt — a failed git command reads as a pass. The spec's `git diff` evidence (AC 4 and AC 9) and the Post-Deploy rung (AC 10) are operator-side; the container-side equivalents are stated in `<verification>`.
- Do NOT run `kubectl*`, `docker`, `make build`, `make buca`, `gh`, or any deploy command.
- `make precommit` runs `make format`, which rewrites `.go` files in place (go-modtool, goimports-reviser, golines, gofmt) and `make generate`, which wipes and regenerates `mocks/`. Both are expected, not failures.
- Both deployed controllers run `-v=2` (see the comments at `pkg/result/result_writer.go:136` and `:177`), so a `glog.V(2)` line is visible in the pod logs.

</context>

<requirements>

## 1. Add the build-fix retirement to `pkg/command/task_create_task_executor.go`

Add the following five declarations to `pkg/command/task_create_task_executor.go` — **in that file, not a new one**: the spec's container-executable verification greps that exact path for the four frozen failure strings. Put them after `buildSupersedeModifyFn` at the end of the file. Every import they need (`context`, `path/filepath`, `strings`, `time`, `lib`, `task`, `libtime`, `errors`, `glog`, `gitclient`, `result`) is already imported by that file.

**1a. The hook.** This is the entry point called from the create callback.

```go
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
```

Requirements for this function:

- The guard reads the marker **values** through `TaskFrontmatter.String`, which reports a non-string value as empty. Never read key presence — `supersedes_task_id: ""` must behave exactly like an absent key, and must never be read as "match every task" (spec Security / Abuse Cases).
- The candidate set is the result of listing `tasks/*.md`. The marker value is **never** interpolated into `filepath.Join` — a `supersedes_task_id` of `../../etc/passwd` must be inert because matching is by comparing parsed values.
- One `ListFiles` call, then at most one `ReadFile` per listed path (the created task's own path is skipped before it is read), then at most one write per matching live candidate. No retry loop: the handler's context deadline is the bound, and the loop checks `ctx.Err()` each iteration exactly as `collapseCandidates` does.
- The four failure log lines are frozen strings. Keep each format string a single string literal — never build it by concatenation and never split it across a `+`. `golines` may re-wrap the call, but it never splits a string literal, so the substring survives.
- The success line is `glog.V(2)`, mirroring `transitionPrior`. Its third argument is the retired candidate's own `build_id` frontmatter value (empty when the file carries none), and the literal `(build %s retired)` is part of the frozen contract.

**1b. The per-candidate step.** This is the only place the four failure discriminators are emitted.

```go
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
```

- A not-found read on a listed path is logged with the `read` prefix like any other read error — the recovery column for "target missing or unreadable" is the same string. Do not special-case 404 here.
- The write goes through `AtomicReadModifyWriteAndCommitPush` — the same call the recurring supersede uses. No new write path, no `WriteFile`, no local-disk write.
- The commit message contains the literal `auto-supersede build-fix:` (AC 7 asserts exactly this substring on this call's message argument).
- Do **not** heal `target_vault` inside the modify closure: the prior file is written to terminal `aborted` and create commands already route through `ShouldProcess` into the owning vault (spec Constraints, and `docs/controller-design.md` line 54).

**1c. The two predicates.**

```go
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
```

- The terminal set is exactly `completed` and `aborted`, trimmed, matching the idiom already in `checkTitlePathFree`. A target that is already terminal is a no-op (spec Failure Modes).
- Both markers present and naming different tasks retires both — the two predicates are evaluated independently per candidate.

**1d. The modify closure and its extraction helper.**

```go
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
```

- **Exactly four fields.** Do not copy `fm["created_by"] = "recurring-task-creator"` from `buildSupersedeModifyFn`. `docs/period-token-semantics.md` lists five fields and the fifth is written only by the recurring mechanism; stamping it here is the named failure of spec Desired Behavior 3.
- Do not refactor `buildSupersedeModifyFn` to share this helper. The spec forbids generalising the sibling mechanism, and its output must stay byte-identical.
- `parseTaskFrontmatterAndBody` is called by the new code only. Do not change any existing caller of `parseTaskFrontmatter` or `marshalFileContent`.

## 2. Call the retirement from the create callback

In `NewCreateTaskExecutor`'s closure in the same file, the tail is currently:

```go
			if err := writeTaskFile(ctx, gitClient, relPath, cmd, vaultName, reopened, priorStatus); err != nil {
				return nil, nil, err
			}
			supersedePriorRecurringTask(ctx, gitClient, taskDir, currentDateTime, k, cmd, relPath)
			return nil, nil, nil
```

Change it to:

```go
			if err := writeTaskFile(ctx, gitClient, relPath, cmd, vaultName, reopened, priorStatus); err != nil {
				return nil, nil, err
			}
			retireSupersededBuildFixTasks(ctx, gitClient, taskDir, currentDateTime, cmd, relPath)
			supersedePriorRecurringTask(ctx, gitClient, taskDir, currentDateTime, k, cmd, relPath)
			return nil, nil, nil
```

- The call sits immediately after `writeTaskFile` (spec Constraints), and `relPath` — the already-resolved, possibly disambiguated path — is passed as `newRelPath`. Do not re-derive the path from the title or the task identifier.
- `supersedePriorRecurringTask` keeps its exact arguments and position relative to `writeTaskFile`; only the new line is inserted above it.
- Nothing else in `NewCreateTaskExecutor` changes. In particular, do **not** add a retirement call on the `ErrTaskAlreadyExists` path (spec Assumptions: that branch is the deterministic-ID dedup for a byte-identical re-emission and carries no new task to point `superseded_by` at), and do **not** add a periodic sweep or reconciler (spec Non-goals).

## 3. Add the specs to `pkg/command/task_create_task_executor_test.go`

Add one `Context("build-fix supersede retirement", func() { ... })` as a sibling of `Context("scan-and-collapse supersede", ...)` — two-tab indentation, inserted after that block closes and before the `})` that closes `Describe("HandleCommand")`. Reuse the existing harness (`fakeGit`, `tmpDir`, `taskDir`, `clock`, `executor`, `buildCmdObj`); do not add a new `BeforeEach`. Define one local helper at the top of the Context:

```go
			buildFixCmd := func(id lib.TaskIdentifier, fm lib.TaskFrontmatter) cdb.CommandObject {
				return buildCmdObj(task.CreateCommand{
					TaskIdentifier: id,
					Title:          "Fix Build - dashboard",
					Frontmatter:    fm,
				})
			}
```

The eight specs in 3a-3g below are the container-executable evidence for AC 2-6 and for the Failure Modes rows; 3h adds four more for the remaining failure branches and the two Security/Failure-Modes properties, twelve in total. Write them with these exact setups and assertions.

**3a. AC 2 — the build marker retires the other live task for that build.**

```go
			It("retires the other live task for the same build_id", func() {
				priorPath := "tasks/Fix Build - dashboard - b1file.md"
				newPath := "tasks/Fix Build - dashboard.md"
				priorContent := []byte(
					"---\ntask_identifier: fixbuild-b1\nbuild_id: B\nassignee: claude\nstatus: in_progress\nphase: execution\n---\nbody\n",
				)
				fakeGit.ListFilesReturns([]string{priorPath, newPath}, nil)
				fakeGit.ReadFileStub = func(_ context.Context, relPath string) ([]byte, error) {
					if relPath == priorPath {
						return priorContent, nil
					}
					return nil, errors.New("GET " + relPath + " returned 404: not found")
				}

				cmdObj := buildFixCmd(lib.TaskIdentifier("fixbuild-b2"), lib.TaskFrontmatter{
					"assignee":            "claude",
					"status":              "in_progress",
					"phase":               "execution",
					"build_id":            "B",
					"supersedes_build_id": "B",
				})

				_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
				Expect(err).NotTo(HaveOccurred())
				Expect(fakeGit.AtomicWriteIfAbsentAndCommitPushCallCount()).To(Equal(1))
				Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(1))

				_, absPath, modify, msg := fakeGit.AtomicReadModifyWriteAndCommitPushArgsForCall(0)
				Expect(absPath).To(HaveSuffix(priorPath))
				Expect(msg).To(ContainSubstring("auto-supersede build-fix:"))

				retired, modifyErr := modify(priorContent)
				Expect(modifyErr).NotTo(HaveOccurred())
				retiredStr := string(retired)
				Expect(retiredStr).To(ContainSubstring("status: aborted"))
				Expect(retiredStr).To(ContainSubstring("phase: done"))
				Expect(retiredStr).To(ContainSubstring("completed_date:"))
				Expect(retiredStr).To(ContainSubstring("superseded_by:"))
				Expect(retiredStr).To(ContainSubstring(newPath))
				Expect(retiredStr).NotTo(ContainSubstring("created_by: recurring-task-creator"))

				_, _, newContent, _ := fakeGit.AtomicWriteIfAbsentAndCommitPushArgsForCall(0)
				Expect(string(newContent)).To(ContainSubstring("status: in_progress"))
			})
```

The four-field assertion is the point: a bare `status: aborted` write, or a copy of the five-field recurring block, fails this spec. `newPath` is exactly what `resolveCreateTaskRelPath` returns for the title `Fix Build - dashboard`, so the retirement must skip it without reading it.

**3b. AC 3 — the task marker retires the named task; an unrelated task is untouched.**

```go
			It("retires the task whose task_identifier matches supersedes_task_id", func() {
				priorPath := "tasks/Fix Build - dashboard - b1file.md"
				unrelatedPath := "tasks/Unrelated Task.md"
				priorContent := []byte(
					"---\ntask_identifier: fixbuild-b1\nbuild_id: B1\nassignee: claude\nstatus: in_progress\n---\nbody\n",
				)
				fakeGit.ListFilesReturns([]string{priorPath, unrelatedPath}, nil)
				fakeGit.ReadFileStub = func(_ context.Context, relPath string) ([]byte, error) {
					switch relPath {
					case priorPath:
						return priorContent, nil
					case unrelatedPath:
						return []byte(
							"---\ntask_identifier: unrelated-1\nassignee: claude\nstatus: in_progress\n---\nbody\n",
						), nil
					}
					return nil, errors.New("GET " + relPath + " returned 404: not found")
				}

				cmdObj := buildFixCmd(lib.TaskIdentifier("fixbuild-b2"), lib.TaskFrontmatter{
					"assignee":           "claude",
					"status":             "in_progress",
					"build_id":           "B2",
					"supersedes_task_id": "fixbuild-b1",
				})

				_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
				Expect(err).NotTo(HaveOccurred())
				Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(1))

				_, absPath, modify, msg := fakeGit.AtomicReadModifyWriteAndCommitPushArgsForCall(0)
				Expect(absPath).To(HaveSuffix(priorPath))
				Expect(msg).To(ContainSubstring("auto-supersede build-fix:"))

				retired, modifyErr := modify(priorContent)
				Expect(modifyErr).NotTo(HaveOccurred())
				retiredStr := string(retired)
				Expect(retiredStr).To(ContainSubstring("status: aborted"))
				Expect(retiredStr).To(ContainSubstring("phase: done"))
				Expect(retiredStr).To(ContainSubstring("completed_date:"))
				Expect(retiredStr).To(ContainSubstring("superseded_by:"))
				Expect(retiredStr).NotTo(ContainSubstring("created_by: recurring-task-creator"))
			})
```

The unrelated file is read but never written — the call count of one plus the `absPath` suffix is the evidence that it is left byte-identical.

**3c. AC 4 + Security — an absent or empty marker is a no-op.**

```go
			DescribeTable(
				"writes nothing when the markers are absent or empty",
				func(fm lib.TaskFrontmatter) {
					priorPath := "tasks/Prior Build Fix.md"
					priorContent := []byte(
						"---\ntask_identifier: prior-b1\nbuild_id: B\nassignee: claude\nstatus: in_progress\n---\nbody\n",
					)
					Expect(os.WriteFile(filepath.Join(tmpDir, priorPath), priorContent, 0600)).To(Succeed())
					fakeGit.AtomicReadModifyWriteAndCommitPushStub = func(
						_ context.Context,
						absPath string,
						modify func([]byte) ([]byte, error),
						_ string,
					) error {
						current, readErr := os.ReadFile(absPath)
						if readErr != nil {
							return readErr
						}
						updated, modifyErr := modify(current)
						if modifyErr != nil {
							return modifyErr
						}
						return os.WriteFile(absPath, updated, 0600)
					}

					_, _, err := executor.HandleCommand(
						ctx,
						nil,
						buildFixCmd(lib.TaskIdentifier("fixbuild-b2"), fm),
					)
					Expect(err).NotTo(HaveOccurred())
					Expect(fakeGit.ListFilesCallCount()).To(Equal(0))
					Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(0))

					after, readErr := os.ReadFile(filepath.Join(tmpDir, priorPath))
					Expect(readErr).NotTo(HaveOccurred())
					Expect(after).To(Equal(priorContent))
				},
				Entry("no marker keys", lib.TaskFrontmatter{
					"assignee": "claude",
					"status":   "in_progress",
					"build_id": "B",
				}),
				Entry("markers present but empty", lib.TaskFrontmatter{
					"assignee":            "claude",
					"status":              "in_progress",
					"build_id":            "B",
					"supersedes_task_id":  "",
					"supersedes_build_id": "",
				}),
			)
```

The stub on `AtomicReadModifyWriteAndCommitPush` writes to disk, so a retirement that fired would change the prior file — the byte comparison is what makes "nothing changed" load-bearing rather than a statement about a mock that never writes. `ListFilesCallCount() == 0` proves the guard returns before listing.

**3d. AC 5 — a same-state re-emission creates no second file and retires nothing.**

```go
			It("creates no second file and retires nothing when the same build is re-emitted unchanged", func() {
				titlePath := "tasks/Fix Build - dashboard.md"
				existing := []byte(
					"---\ntask_identifier: fixbuild-b2\nbuild_id: B\nassignee: claude\nstatus: in_progress\n---\nbody\n",
				)
				fakeGit.ReadFileStub = func(_ context.Context, relPath string) ([]byte, error) {
					if relPath == titlePath {
						return existing, nil
					}
					return nil, errors.New("GET " + relPath + " returned 404: not found")
				}

				cmdObj := buildFixCmd(lib.TaskIdentifier("fixbuild-b2"), lib.TaskFrontmatter{
					"assignee":            "claude",
					"status":              "in_progress",
					"build_id":            "B",
					"supersedes_build_id": "B",
				})

				_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
				Expect(errors.Is(err, task.ErrTaskAlreadyExists)).To(BeTrue())
				Expect(fakeGit.AtomicWriteIfAbsentAndCommitPushCallCount()).To(Equal(0))
				Expect(fakeGit.AtomicWriteAndCommitPushCallCount()).To(Equal(0))
				Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(0))
			})
```

The pre-existing file carries the *same* `task_identifier` as the command, so `checkTitlePathFree` collapses the re-emission at `ErrTaskAlreadyExists` and the retirement is never reached.

**3e. Desired Behavior 6 — the retirement never retires the task it just created.**

```go
			It("does not retire the task it has just created", func() {
				titlePath := "tasks/Fix Build - dashboard.md"
				selfContent := []byte(
					"---\ntask_identifier: fixbuild-b2\nbuild_id: B\nassignee: claude\nstatus: in_progress\n---\nbody\n",
				)
				var written bool
				fakeGit.AtomicWriteIfAbsentAndCommitPushStub = func(
					_ context.Context,
					absPath string,
					content []byte,
					_ string,
				) error {
					written = true
					return os.WriteFile(absPath, content, 0600)
				}
				fakeGit.ReadFileStub = func(_ context.Context, relPath string) ([]byte, error) {
					if written && relPath == titlePath {
						return selfContent, nil
					}
					return nil, errors.New("GET " + relPath + " returned 404: not found")
				}
				fakeGit.ListFilesReturns([]string{titlePath}, nil)

				cmdObj := buildFixCmd(lib.TaskIdentifier("fixbuild-b2"), lib.TaskFrontmatter{
					"assignee":            "claude",
					"status":              "in_progress",
					"build_id":            "B",
					"supersedes_build_id": "B",
				})

				_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
				Expect(err).NotTo(HaveOccurred())
				Expect(fakeGit.AtomicWriteIfAbsentAndCommitPushCallCount()).To(Equal(1))
				Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(0))
			})
```

This is the producer's own first filing: `supersedes_build_id` always equals the creating task's own `build_id`, so a naive "retire every task whose `build_id` matches" retires the file it has just written. The `written` flag makes the read of the self path succeed only after the create, so an implementation that forgets the exclusion retires the new task and fails this spec.

**3f. AC 6 — best-effort: an unreadable and an unparseable target both still create the new task.**

```go
			It("still creates the new task when the supersede target is unreadable", func() {
				targetPath := "tasks/Fix Build - dashboard - b1file.md"
				fakeGit.ListFilesReturns([]string{targetPath}, nil)
				fakeGit.ReadFileStub = func(_ context.Context, relPath string) ([]byte, error) {
					if relPath == targetPath {
						return nil, errors.New("git-rest 503")
					}
					return nil, errors.New("GET " + relPath + " returned 404: not found")
				}

				cmdObj := buildFixCmd(lib.TaskIdentifier("fixbuild-b2"), lib.TaskFrontmatter{
					"assignee":            "claude",
					"status":              "in_progress",
					"build_id":            "B",
					"supersedes_build_id": "B",
				})

				_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
				Expect(err).NotTo(HaveOccurred())
				Expect(fakeGit.AtomicWriteIfAbsentAndCommitPushCallCount()).To(Equal(1))
				Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(0))
			})

			It("still creates the new task when the supersede target is unparseable", func() {
				targetPath := "tasks/Fix Build - dashboard - b1file.md"
				fakeGit.ListFilesReturns([]string{targetPath}, nil)
				fakeGit.ReadFileStub = func(_ context.Context, relPath string) ([]byte, error) {
					if relPath == targetPath {
						return []byte("---\nstatus: [unclosed\n---\nbody\n"), nil
					}
					return nil, errors.New("GET " + relPath + " returned 404: not found")
				}

				cmdObj := buildFixCmd(lib.TaskIdentifier("fixbuild-b2"), lib.TaskFrontmatter{
					"assignee":            "claude",
					"status":              "in_progress",
					"build_id":            "B",
					"supersedes_build_id": "B",
				})

				_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
				Expect(err).NotTo(HaveOccurred())
				Expect(fakeGit.AtomicWriteIfAbsentAndCommitPushCallCount()).To(Equal(1))
				Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(0))
			})
```

In the unreadable spec the stub returns 503 only for the target path, so the pre-write title-path read still sees a 404 and the create proceeds. In the unparseable spec the delimiters are well formed (`ExtractFrontmatter` succeeds) and the YAML flow sequence is unterminated, so the failure lands in `parseTaskFrontmatter`.

**3g. Failure Modes — a marker whose target is already terminal is a no-op.**

```go
			It("does not retire a target that is already terminal", func() {
				targetPath := "tasks/Fix Build - dashboard - b1file.md"
				terminalContent := []byte(
					"---\ntask_identifier: fixbuild-b1\nbuild_id: B\nassignee: claude\nstatus: aborted\nphase: done\n---\nbody\n",
				)
				fakeGit.ListFilesReturns([]string{targetPath}, nil)
				fakeGit.ReadFileStub = func(_ context.Context, relPath string) ([]byte, error) {
					if relPath == targetPath {
						return terminalContent, nil
					}
					return nil, errors.New("GET " + relPath + " returned 404: not found")
				}

				cmdObj := buildFixCmd(lib.TaskIdentifier("fixbuild-b2"), lib.TaskFrontmatter{
					"assignee":            "claude",
					"status":              "in_progress",
					"build_id":            "B",
					"supersedes_build_id": "B",
				})

				_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
				Expect(err).NotTo(HaveOccurred())
				Expect(fakeGit.AtomicWriteIfAbsentAndCommitPushCallCount()).To(Equal(1))
				Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(0))
			})
```

**3h. The remaining failure branches and the two properties the security and Failure Modes sections name.**

Four more specs. The first two close the branch-coverage gap in `retireSupersededBuildFixTasks` (its list-error path) and `retireSupersededBuildFixCandidate` (its write-error path) — two added branches with no spec otherwise. The last two pin the two properties the spec names but no spec exercises: the path-traversal guard, and both markers naming different tasks.

```go
			It("still creates the new task when listing candidates fails", func() {
				fakeGit.ListFilesReturns(nil, errors.New("git-rest 503"))

				cmdObj := buildFixCmd(lib.TaskIdentifier("fixbuild-b2"), lib.TaskFrontmatter{
					"assignee":            "claude",
					"status":              "in_progress",
					"build_id":            "B",
					"supersedes_build_id": "B",
				})

				_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
				Expect(err).NotTo(HaveOccurred())
				Expect(fakeGit.AtomicWriteIfAbsentAndCommitPushCallCount()).To(Equal(1))
				Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(0))
			})

			It("still creates the new task when the retire write is rejected", func() {
				targetPath := "tasks/Fix Build - dashboard - b1file.md"
				fakeGit.ListFilesReturns([]string{targetPath}, nil)
				fakeGit.ReadFileStub = func(_ context.Context, relPath string) ([]byte, error) {
					if relPath == targetPath {
						return []byte(
							"---\ntask_identifier: fixbuild-b1\nbuild_id: B\nassignee: claude\nstatus: in_progress\n---\nbody\n",
						), nil
					}
					return nil, errors.New("GET " + relPath + " returned 404: not found")
				}
				fakeGit.AtomicReadModifyWriteAndCommitPushReturns(errors.New("git-rest 409"))

				cmdObj := buildFixCmd(lib.TaskIdentifier("fixbuild-b2"), lib.TaskFrontmatter{
					"assignee":            "claude",
					"status":              "in_progress",
					"build_id":            "B",
					"supersedes_build_id": "B",
				})

				_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
				Expect(err).NotTo(HaveOccurred())
				Expect(fakeGit.AtomicWriteIfAbsentAndCommitPushCallCount()).To(Equal(1))
				Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(1))
			})

			It("treats a path-traversal marker as an inert value, never a path", func() {
				targetPath := "tasks/Fix Build - dashboard - b1file.md"
				fakeGit.ListFilesReturns([]string{targetPath}, nil)
				fakeGit.ReadFileStub = func(_ context.Context, relPath string) ([]byte, error) {
					if relPath == targetPath {
						return []byte(
							"---\ntask_identifier: fixbuild-b1\nbuild_id: B\nassignee: claude\nstatus: in_progress\n---\nbody\n",
						), nil
					}
					return nil, errors.New("GET " + relPath + " returned 404: not found")
				}

				cmdObj := buildFixCmd(lib.TaskIdentifier("fixbuild-b2"), lib.TaskFrontmatter{
					"assignee":           "claude",
					"status":             "in_progress",
					"build_id":           "B",
					"supersedes_task_id": "../../etc/passwd",
				})

				_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
				Expect(err).NotTo(HaveOccurred())
				// The glob is the only path the marker could have influenced, and it is
				// constant: the marker is compared as a value, never joined into a path.
				Expect(fakeGit.ListFilesCallCount()).To(Equal(1))
				_, glob := fakeGit.ListFilesArgsForCall(0)
				Expect(glob).To(Equal("tasks/*.md"))
				// The candidate is live and carries build_id B, but the command's
				// supersedes_build_id is absent and its supersedes_task_id does not
				// match fixbuild-b1 — so nothing is retired.
				Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(0))
			})

			It("retires a task-marker match and a build-marker match in one pass", func() {
				byTaskID := "tasks/Fix Build - dashboard - byid.md"
				byBuildID := "tasks/Fix Build - dashboard - bybuild.md"
				fakeGit.ListFilesReturns([]string{byTaskID, byBuildID}, nil)
				fakeGit.ReadFileStub = func(_ context.Context, relPath string) ([]byte, error) {
					switch relPath {
					case byTaskID:
						return []byte(
							"---\ntask_identifier: fixbuild-b1\nbuild_id: OTHER\nassignee: claude\nstatus: in_progress\n---\nbody\n",
						), nil
					case byBuildID:
						return []byte(
							"---\ntask_identifier: unrelated-1\nbuild_id: B\nassignee: claude\nstatus: in_progress\n---\nbody\n",
						), nil
					}
					return nil, errors.New("GET " + relPath + " returned 404: not found")
				}

				cmdObj := buildFixCmd(lib.TaskIdentifier("fixbuild-b2"), lib.TaskFrontmatter{
					"assignee":            "claude",
					"status":              "in_progress",
					"build_id":            "B",
					"supersedes_task_id":  "fixbuild-b1",
					"supersedes_build_id": "B",
				})

				_, _, err := executor.HandleCommand(ctx, nil, cmdObj)
				Expect(err).NotTo(HaveOccurred())
				// Neither candidate matches both markers: the two predicates are
				// evaluated independently, so each retires exactly one file.
				Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(2))
			})
```

**3i. Do not weaken the existing specs.** The `Context("scan-and-collapse supersede", ...)` block must survive verbatim: the `AtomicReadModifyWriteAndCommitPushCallCount` assertions and the `Expect(msg).To(ContainSubstring("auto-supersede prior recurring task"))` assertion stay exactly as they are. Do not edit, reorder or reformat any pre-existing spec, and do not change any pre-existing assertion to accommodate the new behaviour.

## 4. Qualify the frozen transition block in `docs/period-token-semantics.md`

The `created_by: recurring-task-creator` line at the end of the fenced block under `### Collapse (Auto-Supersede)` is written only by the recurring mechanism. Change that one line to:

```
created_by: recurring-task-creator  # written only by the recurring mechanism
```

Then add one sentence immediately after the closing fence of that block (before the `### Best-Effort Per File` heading), naming the four-field house reading:

> The build-fix retirement writes the four fields above and never `created_by`, which names the recurring publisher and would be false on a build-fix task.

The frozen literal `written only by the recurring mechanism` must sit within two lines either side of the `created_by: recurring-task-creator` field. A sentence placed immediately after the closing fence *would* be in range — but the margin is a single line, so one blank line or any later edit pushes it out. Putting the qualifier **on that line** cannot drift, which is why it goes there. Do not reorder the fields and do not remove any field.

## 5. Document the mechanism in `docs/controller-design.md`

Insert a new paragraph into `### 2. Command Processing (Kafka → git)`, immediately after the paragraph that ends "…create commands already route through `ShouldProcess` into the owning vault." and immediately before the `## Frontmatter Merge` heading. Write it as **one single unwrapped line** (the surrounding paragraphs are unwrapped long lines, and the spec's evidence greps matching *lines*), using the doc's bold-lead-in style:

```
**Build-fix supersede (spec 014).** A failed-build task is re-emitted whenever a build is re-classified or its log fetch recovers, and the re-emission carries two markers: `supersedes_task_id` names the `task_identifier` of the task it replaces, and `supersedes_build_id` names the build. After `writeTaskFile`, the create callback calls `retireSupersededBuildFixTasks`, which lists `tasks/*.md`, excludes the task just created, and retires every still-live candidate whose parsed `task_identifier` equals `supersedes_task_id` or whose parsed `build_id` equals `supersedes_build_id`; matching is always on the parsed value, never by joining a marker into a path, and an absent or empty marker matches nothing. A retired task carries the same frozen four-field transition the recurring supersede writes (`status: aborted`, `phase: done`, `completed_date`, and `superseded_by` naming the created task's relPath) and deliberately does not carry `created_by`, which names the recurring publisher and would be false here. The retirement is best-effort: a list, read, parse or write failure on any single file is logged and swallowed, the already-created task is never rolled back, and no retry loop is added — the handler's context deadline is the bound. Every line it emits, and the commit message it passes to `AtomicReadModifyWriteAndCommitPush`, carries the frozen prefix `auto-supersede build-fix:`, so an operator can separate this mechanism's record from the recurring mechanism's `auto-supersede:` in the pod log. The message is deliberately not described as readable after the fact: `gitRestGitClientAdapter.AtomicReadModifyWriteAndCommitPush` uses it only for a `glog.V(3)` line and `gitRestClient.Post` sends path and content only, so it never reaches git; the operator-facing record is the log lines — the four failures at `glog.Warningf` and the success line at `glog.V(2)`, both emitted at the deployed `-v=2`. The prefix is on the message as well because that is the mechanically assertable half.
```

The literal `auto-supersede build-fix:` MUST appear. Do not modify any other paragraph, and do not document a config knob, an opt-out flag, a periodic sweep or a retry (the spec's Non-goals forbid all four).

## 6. Add the changelog entry

Insert a new `## Unreleased` section into `CHANGELOG.md` immediately after the frozen preamble (after the "and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html)." line) and immediately above the `## v0.10.1` heading, containing exactly one bullet:

```markdown
## Unreleased

- fix: retire the superseded build-fix task when a failed build is re-emitted, so a re-classified build — or one whose log fetch recovers — leaves exactly one live task instead of two concurrent build-fixer runs against the same repo and commit. The build watcher stamps `supersedes_task_id` and `supersedes_build_id` on every re-emission and documents the consumer as existing, but nothing in this repo read either marker, so both the original and the re-emission stayed non-terminal and dispatchable. The create callback now calls `retireSupersededBuildFixTasks` after `writeTaskFile`: it lists the task directory, excludes the task just created, and retires every still-live task whose parsed `task_identifier` matches `supersedes_task_id` or whose parsed `build_id` matches `supersedes_build_id`, writing the same frozen transition the recurring supersede writes (`status: aborted`, `phase: done`, `completed_date`, `superseded_by`) and never `created_by`. The retirement is best-effort — a list, read, parse or write failure is logged and swallowed and the new task is never rolled back — and every line it emits, plus its commit message, carries the frozen prefix `auto-supersede build-fix:` (spec 014)
```

The `fix:` prefix is required and is the only prefix used here: the spec frames this as a defect — a documented cross-repo contract with one end unimplemented — so it ships a patch bump, not a new capability. The word `retire` MUST appear in the text above the first `## vX.Y.Z` heading. One bullet, one logical change. Do not create a second `## Unreleased` section, do not touch any existing version section or bullet, and leave the `# Changelog` title and the SemVer preamble byte-for-byte unchanged.

</requirements>

<constraints>

- The existing `supersedePriorRecurringTask` behaviour and its specs must not change. This is a sibling mechanism on the same code path, not a generalisation of it — do NOT replace or widen `supersedePriorRecurringTask`, and do NOT refactor `buildSupersedeModifyFn` to share the new helper.
- The retirement is invoked from the same create callback as `supersedePriorRecurringTask`, immediately after `writeTaskFile`, and receives the already-resolved `relPath` as its `newRelPath`. It must not re-derive the path: the path may have been disambiguated with a short-identifier suffix, and `superseded_by` has to name the file that was actually written.
- Four fields, not five. Write `status: aborted`, `phase: done`, `completed_date`, and `superseded_by` naming the new task's relPath. Do NOT write `created_by` — `docs/period-token-semantics.md` lists a fifth field, `created_by: recurring-task-creator`, which this mechanism does not write. That value names the recurring publisher and would be false on a build-fix task, whose `created_by` is whatever the watcher's create command carried.
- The prior-file write does not heal `target_vault`, matching `docs/controller-design.md` line 54. Prior files go to terminal `aborted` and create commands already route through `ShouldProcess` into the owning vault; stamping `target_vault` onto a file nobody will re-create is a write with no reader.
- No new write path is introduced: the retirement writes through the same `gitClient.AtomicReadModifyWriteAndCommitPush` the recurring supersede uses, so the vault's write serialization is unchanged.
- No extra idempotency guard, lock or dedup pass is added. Kafka redelivery of the same command is absorbed by the deterministic task ID at the `ErrTaskAlreadyExists` branch before the retirement is reached, and when one controller serves the vault the same `AtomicReadModifyWriteAndCommitPush` serialization that orders the recurring supersede orders this one too — so two creates for one build race and the last write wins, leaving at most one live task. (On a stage running two controllers for one vault the loser returns `ErrTaskAlreadyExists` *before* the retirement and no retirement runs; that is the spec's Post-Deploy precondition and is operator-side, not something this prompt compensates for.)
- No exported signature changes anywhere. `NewCreateTaskExecutor` keeps its exact parameter list, its only production call site is `pkg/factory/factory.go:62`, and there is no `cmd/` variant of `main.go` in this repo — so no wiring change is required in this prompt. Do not add one.
- `task_identifier` is the identity used for matching `supersedes_task_id`; the build identity for `supersedes_build_id` is the `build_id` frontmatter field. Neither is re-derived from the other.
- An empty or malformed marker means absent. `supersedes_task_id: ""` must be treated exactly like an absent key, never as "match every task" — the existing `TaskFrontmatter.String` accessor reports a non-string value as empty, and a guard that reads key presence rather than value turns a malformed producer payload into a vault-wide retirement.
- The marker value must never be joined into a path. A `supersedes_task_id` of `../../etc/passwd` must be harmless: candidate paths come from listing the task directory and from each matched file's own listed path, and matching is by comparing the parsed `task_identifier` as a value.
- No new configuration knob, opt-out flag or tunable bound — the retirement is unconditional when a marker is present.
- Do NOT handle a re-emission that carries no marker: the controller has nothing to read. The `unknown/log_fetch_failed=true → unknown/log_fetch_failed=false` flip on the producer's released code is exactly that case and is explicitly outside this spec's acceptance criteria.
- Do NOT add a periodic sweep or reconciler for tasks missed by the crash window (process death between `writeTaskFile` and the retirement). The retirement is a create-time hook, not a reconciler; that window has no automatic recovery and an operator closes the stale file by hand.
- Do NOT change the producer's marker semantics, the watcher's emission, or `Seibert-Data/google-cloud-build-watcher#8`.
- Do NOT retro-close the historical `Fix Build` tasks already sitting in the vault — that is operator cleanup, tracked by this spec's own vault entry.
- No retry loop: one list, at most one read and one write per candidate, bounded by the handler's context deadline. Failures are swallowed, never propagated — the retirement must never block or roll back the create.
- Per `go-error-wrapping-guide.md`: wrap with `github.com/bborbe/errors` (`errors.Wrapf(ctx, err, "...")`), never `fmt.Errorf`, and never `context.Background()` inside `pkg/`.
- Per `go-precommit.md`: keep `funlen` under 80 lines / 50 statements, `nestif` under complexity 4, `gocognit` under 20, and every line under 100 characters. The `dupl` linter fails on a duplicated run of 150 or more tokens, which is why the frontmatter/body extraction lives in `parseTaskFrontmatterAndBody` and is called by the new closure instead of being repeated.
- Do NOT commit — dark-factory handles git.
- Do NOT run any `git` command — the container's `.git` is masked and a failed git command reads as a pass. The spec's `git diff` evidence (AC 4, AC 9) and the Post-Deploy rung (AC 10) are operator-side.
- Do NOT run `kubectl*`, `docker`, `make build`, `make buca`, `gh`, or any operator/deploy command.

</constraints>

<verification>

Run from the repo root. Iterate with the fast loop while implementing:

```
make test
```

Fast, focused run while iterating on the new specs:

```
go test ./pkg/command/... -v
```

Spec AC 1 and AC 9 (the recurring supersede is unaffected) — the full gate, run ONCE at the end:

```
make precommit
make test
```

Expect exit 0 for both. If `make precommit` fails, fix the failing target and re-run only that target (`make lint`, `make vet`, `make gosec`, `make test`) until it passes, then re-run the full `make precommit` once.

Spec AC 9, the container-side half — the existing supersede assertions survive verbatim (the `git diff origin/master -- pkg/command/task_create_task_executor_test.go` half is operator-side):

```
grep -q 'ContainSubstring("auto-supersede prior recurring task")' pkg/command/task_create_task_executor_test.go
grep -c 'AtomicReadModifyWriteAndCommitPushCallCount' pkg/command/task_create_task_executor_test.go
```

Expect exit 0 for the first (it exits 1 if the assertion was deleted or weakened) and a count of at least 7 for the second.

The markers are read somewhere in `pkg/` — this grep returned zero hits and exit 1 before the change:

```
test "$(grep -rn 'supersedes_build_id\|supersedes_task_id' pkg/ | wc -l | tr -d ' ')" -gt 0
```

Expect exit 0. A count assertion rather than a bare grep, so an empty result fails instead of passing on a command that printed nothing. This is a presence check — a string literal or a comment satisfies it — not evidence that the retirement runs.

Each of the four frozen failure discriminators is present in the source (one assertion per row, so a missing discriminator fails rather than passing on the success line alone):

```
for op in list read parse write; do
  grep -q "auto-supersede build-fix: $op" pkg/command/task_create_task_executor.go || exit 1
done
```

Expect exit 0. This proves the strings exist in the source, not that they are ever emitted; only the success path has an emission assertion (the commit-message check inside the new specs), and the four failure paths stay verified by reading the source until a real failure emits one.

Spec AC 8, the three doc artifacts:

```
grep -C2 'created_by: recurring-task-creator' docs/period-token-semantics.md | grep -q 'written only by the recurring mechanism'
grep -n 'auto-supersede build-fix:' docs/controller-design.md
awk '/^## v/{exit} /retire/{f=1} END{exit !f}' CHANGELOG.md
grep -c '^## Unreleased' CHANGELOG.md
```

Expect exit 0 for the first, the third and the fourth (the fourth prints `1`), and at least one matching line from the second.

NOT run here, and deliberately so — these are the operator-side rungs of the spec's Verification ladder and the container cannot execute them: `git diff origin/master -- pkg/command/task_create_task_executor_test.go`, the dev-cluster rollout digest comparison, the two-controller precondition, and the Post-Deploy task-file grep.

</verification>
