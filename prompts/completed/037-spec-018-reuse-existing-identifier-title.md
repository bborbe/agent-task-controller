---
status: completed
spec: [018-bug-create-second-file-for-existing-identifier]
summary: Added reuseExistingIdentifierTitle so a create resolves the identifier against the identifier→path index and posts under the existing file's title stem, with tests, design doc and changelog updated.
execution_id: agent-task-controller-idlookup-exec-037-spec-018-reuse-existing-identifier-title
dark-factory-version: v0.198.7
created: "2026-10-27T22:58:00Z"
queued: "2026-10-27T22:03:09Z"
started: "2026-10-27T22:03:10Z"
completed: "2026-10-27T22:10:48Z"
branch: dark-factory/bug-create-second-file-for-existing-identifier
---

# Redirect a create onto the file that already carries its task_identifier

<summary>

- A task re-emitted under a new title no longer creates a second file for the same identity.
- Before a create is posted, the controller asks its identifier-to-file index whether this identity already has a file.
- When the index knows a file, the create is posted under that file's existing title instead of the title the command carries.
- The server therefore sees an occupied path and answers with its own conflict or reopen logic, exactly as it does for a same-title re-emit.
- When the index has no answer, the create keeps the command's own title and behaves precisely as today.
- When the index reports an ambiguity (more than one file for one identity), the create also keeps the command's own title and the ambiguity is logged.
- A controller built without an index behaves byte-identically to today.
- The redirect only ever chooses a filename stem, never a path, so a create can never be steered outside the task directory.
- The existing disambiguation for two different identities colliding on one title still works.
- The behaviour change is recorded in the changelog and the design doc.

</summary>

<objective>

Make a `task_identifier` resolve to at most one record file on the create path: before posting a create, resolve the command's identifier against the identifier→path index and, on a clean hit, post the create under the title stem of the file that already carries it. A re-emit of the same deterministic key under a different title (a retitled PR, a diverged title SHA) then lands on the occupied path and receives the server's existing conflict/reopen answer instead of silently materializing a second file that shares one identity — which today makes the re-drive streak store conflate two records and lets one record's failures cap its sibling. Satisfies spec 018 Desired Behavior 1-6 and Acceptance Criteria 1-7. AC8 is the Post-Deploy rung and is operator-side; it is NOT in this prompt's scope.

</objective>

<context>

This repo has no root `CLAUDE.md`; the global YOLO container `CLAUDE.md` already in your context governs project conventions. The spec for this work is `specs/in-progress/018-bug-create-second-file-for-existing-identifier.md` — read it in full before changing anything. Pay particular attention to: Problem, Goal, Non-goals (all five are load-bearing vetoes), Desired Behavior 1-6, Constraints, Assumptions, Failure Modes (every row), Security / Abuse Cases, and the Suggested Decomposition table (this prompt is its single row).

Read these files before changing anything:

- `pkg/command/task_create_task_executor.go` — the whole file. The change touches `NewCreateTaskExecutor` and adds one helper next to `createTaskTitle`. Note:
  - `NewCreateTaskExecutor(gitClient gitclient.GitClient, client vaultapi.Client, taskDir string, vaultName string, currentDateTime libtime.CurrentDateTimeGetter, k int, resolver result.TaskPathResolver) cdb.CommandObjectExecutorTx`. Its closure validates the command, routes it with `routing.ShouldProcess`, validates the frontmatter, then calls `title := createTaskTitle(ctx, cmd)`, then `pathTitle, reopened, err := createThroughAPI(ctx, client, cmd, title, vaultName)`, and only afterwards `recordCreatedTaskPath(ctx, resolver, cmd.TaskIdentifier, relPath)`.
  - `func createTaskTitle(ctx context.Context, cmd task.CreateCommand) string` returns the command's `Title`, or `string(cmd.TaskIdentifier)` when `cmd.Validate` fails or the title contains `/` or `\`.
  - `func createThroughAPI(ctx context.Context, client vaultapi.Client, cmd task.CreateCommand, title string, vaultName string) (pathTitle string, reopened bool, err error)` owns the conflict logic and is NOT to be changed: a 409 naming this same identifier (or naming none) becomes `task.ErrTaskAlreadyExists`; a 409 naming a DIFFERENT identifier for a non-terminal occupant posts a second create at `title + " - " + shortIdentifier(cmd.TaskIdentifier)`.
  - `func recordCreatedTaskPath(ctx context.Context, resolver result.TaskPathResolver, id lib.TaskIdentifier, relPath string)` is nil-safe and records only on a clean miss (`Resolve` returns `found == false, err == nil`). It runs after the create and is unchanged.
  - Existing imports already include `context`, `maps`, `path/filepath`, `strings`, `time`, `lib "github.com/bborbe/agent"`, `task "github.com/bborbe/agent/command/task"`, `result "github.com/bborbe/agent-task-controller/pkg/result"`, and `"github.com/golang/glog"`. This change needs NO new import.
- `pkg/result/task_path_resolver.go` — the interface, verbatim:
  ```go
  type TaskPathResolver interface {
  	Resolve(ctx context.Context, id lib.TaskIdentifier) (string, bool, error)
  	Record(ctx context.Context, id lib.TaskIdentifier, relPath string)
  }
  ```
  `Resolve` has three outcomes that must not be conflated: `(path, true, nil)` known; `("", false, nil)` unknown (a miss is not an error); `("", false, err)` more than one file carries the identifier (a resolver error is never a miss).
- `pkg/command/task_create_task_executor_test.go` — the whole file. Tests are Ginkgo/Gomega in `package command_test`, using `fakeGit *mocks.GitClient`, `fakeVault *mocks.VaultAPIClient` built by `newFSTaskClient(tmpDir, taskDir)` (defined in `pkg/command/vaultapi_fake_test.go`), and `clock *libtimemocks.CurrentDateTimeGetter`. The helpers you need already exist in the suite's `BeforeEach`: `buildCmdObj(task.CreateCommand)`, `seedRaw(title, content)`, `writtenTask(title)`, `taskExists(title)`. The fake vault's create mirrors the API: an occupant with the same `task_identifier` and a non-terminal status answers `&vaultapi.AlreadyExistsError{TaskIdentifier: identifier, Status: status}`.
  - The pattern for building an executor with a resolver is in `Context("index recording", ...)`: `fakeResolver := &mocks.TaskPathResolver{}`, `fakeResolver.ResolveReturns(...)`, then `command.NewCreateTaskExecutor(fakeGit, fakeVault, taskDir, "openclaw", clock, testK, fakeResolver)`.
- `docs/controller-design.md`, section `## Terminal Task Status (create-task dedup)` — the bulleted list that states the current dedup rule ("the title path is occupied by a live task"). The new rule belongs there.
- `CHANGELOG.md` — the top section is `## v0.22.0` and there is no `## Unreleased` section yet. Copy the bullet style (`- fix: **bold lead.** ...`) from `## v0.22.0`.

Relevant coding guidance (in-container paths): `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`, `/home/node/.claude/plugins/marketplaces/coding/docs/go-glog-guide.md`, `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`, `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md`.

Background the change relies on (do not re-derive it in code):
- The identifier→path index holds only files under this controller's own `taskDir` and keeps both paths when two files share an identifier (which is exactly how the resolver's error case arises).
- A previous identifier pre-check was removed for vault-api#28 because it keyed on the index's shared identifier/name/slug space and could shadow a live task. This change stays narrow: it reads the identifier→path index only, and returns a title stem rather than a path.
- Recurring-task commands are not exempted from the redirect. Each recurring instance carries its own period-scoped identifier, so a new period is a miss; the redirect only fires when the identity is genuinely the same record.

</context>

<requirements>

1. In `pkg/command/task_create_task_executor.go`, add a helper immediately after `createTaskTitle` (same file, unexported, `package command`), with this exact signature and behaviour:

   ```go
   // reuseExistingIdentifierTitle returns the title stem of the file that already
   // carries id, or fallbackTitle when the identifier has no single known file.
   // It returns a STEM, never a path: the create still lands in TASK_DIR and the
   // server still owns the filename. It reads the identifier->path index only, so
   // it can never be steered by the index's shared identifier/name/slug space
   // (the vault-api#28 shadowing class). A nil resolver, a miss, a resolver error
   // and an unusable stem all fall through to fallbackTitle.
   func reuseExistingIdentifierTitle(
   	ctx context.Context,
   	resolver result.TaskPathResolver,
   	id lib.TaskIdentifier,
   	fallbackTitle string,
   ) string
   ```
   Body, in this order:
   - `if resolver == nil { return fallbackTitle }`.
   - `relPath, found, err := resolver.Resolve(ctx, id)`.
   - `if err != nil` → log the ambiguity and return `fallbackTitle`. Use exactly this call (two lines of arguments so it stays inside the 100-column `golines` limit):
     ```go
     glog.Warningf(
     	"create-task: identifier %s resolves ambiguously (%v); using the command title %q",
     	id, err, fallbackTitle,
     )
     ```
     The comment above it must say why: a known-duplicate index entry must never choose a path, so the create keeps the title the command asked for.
   - `if !found { return fallbackTitle }`.
   - `stem := strings.TrimSuffix(filepath.Base(relPath), ".md")`.
   - `if stem == "" || stem == "." || stem == ".." { return fallbackTitle }`.
   - Otherwise log the redirect at `V(2)` (prod runs `-v=2`) and return `stem`:
     ```go
     glog.V(2).Infof(
     	"create-task: identifier %s resolves to %s; using title %q instead of %q",
     	id, relPath, stem, fallbackTitle,
     )
     ```

2. In `NewCreateTaskExecutor`'s closure, apply the redirect between the title computation and the create. The three lines must read exactly:

   ```go
   title := createTaskTitle(ctx, cmd)
   title = reuseExistingIdentifierTitle(ctx, resolver, cmd.TaskIdentifier, title)
   pathTitle, reopened, err := createThroughAPI(ctx, client, cmd, title, vaultName)
   ```
   Nothing else in the closure changes. In particular: the resolve happens after `routing.ShouldProcess` and after the frontmatter validation, so a skipped or invalid command performs no lookup; `createThroughAPI`, `postCreate`, `createTaskTitle` and `recordCreatedTaskPath` keep their current signatures and bodies.

3. Do NOT change `createThroughAPI` or `postCreate`. The redirect composes with the existing conflict logic rather than replacing any branch of it:
   - a redirect onto a path held by the SAME identifier maps to `task.ErrTaskAlreadyExists` through the existing `conflict.TaskIdentifier == string(cmd.TaskIdentifier)` branch;
   - a redirect onto a path whose occupant is terminal is reopened by the existing reopen path, and `pathTitle` then equals the redirected stem, so `recordCreatedTaskPath` records the correct path;
   - the ` - <id[:8]>` disambiguation still fires only when a DIFFERENT identifier holds the path, so it is not suppressed.

4. In `pkg/command/task_create_task_executor_test.go`, add a new `Context("identifier already has a file under a different title (regression)", ...)` directly after the existing `Context("collision with a different task_identifier", ...)` block, inside `Describe("HandleCommand", ...)`. It contains exactly two `It` blocks.

   4a. First case, named so the spec's name appears literally in the test name:

   ```go
   It("ExistingIdentifierTitleIsReused: posts the create under the existing file's title stem", func() {
   	const existingID = "09f1f349-bf09-5610-9cf2-bb79cf0383be"
   	seedRaw("Existing Title",
   		"---\ntask_identifier: "+existingID+"\nassignee: alice\nstatus: in_progress\n---\nbody\n")
   	fakeResolver := &mocks.TaskPathResolver{}
   	fakeResolver.ResolveReturns("tasks/Existing Title.md", true, nil)
   	redirectingExecutor := command.NewCreateTaskExecutor(
   		fakeGit, fakeVault, taskDir, "openclaw", clock, testK, fakeResolver,
   	)
   	cmdObj := buildCmdObj(task.CreateCommand{
   		TaskIdentifier: lib.TaskIdentifier(existingID),
   		Title:          "A Different Title",
   		Frontmatter:    lib.TaskFrontmatter{"assignee": "claude", "status": "next"},
   	})

   	_, _, err := redirectingExecutor.HandleCommand(ctx, nil, cmdObj)
   	Expect(errors.Is(err, task.ErrTaskAlreadyExists)).To(BeTrue())
   	Expect(fakeVault.CreateTaskCallCount()).To(Equal(1))
   	_, posted, _ := fakeVault.CreateTaskArgsForCall(0)
   	Expect(posted.Title).To(Equal("Existing Title"))
   	Expect(taskExists("A Different Title")).To(BeFalse())
   	Expect(writtenTask("Existing Title")).To(ContainSubstring("status: in_progress"))
   })
   ```
   The seeded occupant is what makes the redirected create conflict: the fake vault answers the create at `Existing Title` with a 409 naming `existingID`, which the executor maps to `task.ErrTaskAlreadyExists`. Pre-fix, the create goes to `A Different Title`, succeeds, and both the `posted.Title` assertion and `taskExists("A Different Title")` fail — that is the reproduction the spec records.

   4b. Second case — a resolver error must never redirect:

   ```go
   It("does not redirect when the resolver reports an ambiguous identifier", func() {
   	fakeResolver := &mocks.TaskPathResolver{}
   	fakeResolver.ResolveReturns("", false, errors.New("duplicate: tasks/a.md, tasks/b.md"))
   	redirectingExecutor := command.NewCreateTaskExecutor(
   		fakeGit, fakeVault, taskDir, "openclaw", clock, testK, fakeResolver,
   	)
   	cmdObj := buildCmdObj(task.CreateCommand{
   		TaskIdentifier: lib.TaskIdentifier("ambiguous-id"),
   		Title:          "Own Title",
   		Frontmatter:    lib.TaskFrontmatter{"assignee": "claude", "status": "next"},
   	})

   	_, _, err := redirectingExecutor.HandleCommand(ctx, nil, cmdObj)
   	Expect(err).NotTo(HaveOccurred())
   	Expect(fakeVault.CreateTaskCallCount()).To(Equal(1))
   	_, posted, _ := fakeVault.CreateTaskArgsForCall(0)
   	Expect(posted.Title).To(Equal("Own Title"))
   	Expect(taskExists("Own Title")).To(BeTrue())
   })
   ```

5. Update the one existing assertion the new pre-create resolve invalidates. In `Context("index recording", ...)`, the case `It("does not resolve or record when the create fails as already existing", ...)` currently asserts `Expect(fakeResolver.ResolveCallCount()).To(Equal(0))`. The executor now resolves once before the create to decide the redirect, so change that assertion to `Expect(fakeResolver.ResolveCallCount()).To(Equal(1))` and update the comment to say: one resolve chooses the create title, and none is made for recording because the create failed as already-existing. Leave `Expect(fakeResolver.RecordCallCount()).To(Equal(0))` unchanged. No other existing assertion changes.

6. Do not add tests for the miss path, the nil-resolver path, or the different-identifier disambiguation: they are already covered and the spec requires them to pass unchanged (AC2, AC4, AC5). Do not add a test for the empty/`.`/`..` stem guard.

7. In `docs/controller-design.md`, in section `## Terminal Task Status (create-task dedup)`, add one paragraph directly after the two-item bulleted list that ends with "...holds the path and the create is dropped with `ErrTaskAlreadyExists`.":

   > **Identifier redirect on create.** Before a create is posted, the executor resolves the command's `task_identifier` against the identifier→path index and, on a clean hit, posts the create under the title stem of the file that already carries it. A re-emit of the same deterministic key under a different title — a retitled PR, or an `Update Go` record whose title SHA diverged from its rewritten `ref` — therefore lands on the occupied path and is answered by the server's existing conflict or reopen logic instead of materializing a second file that shares one identity. A miss, a nil resolver and a resolver error all keep the command's own title; the error is logged as an ambiguity and never redirects, because a known-duplicate index entry must not choose a path. The redirect returns a title stem, never a path, and reads the identifier→path index only — never the index's shared identifier/name/slug space — so the vault-api#28 shadowing class stays closed, and the ` - <id[:8]>` disambiguation for two different identifiers colliding on one title is unaffected.

   Change nothing else in that document.

8. In `CHANGELOG.md`, insert a `## Unreleased` section directly above `## v0.22.0`, with one blank line before and after the heading, containing exactly this bullet:

   ```
   - fix: **a re-emit under a new title no longer creates a second file for the same task_identifier.** Before posting a create, the executor resolves the command's identifier against the identifier→path index and, on a clean hit, posts the create under the title stem of the file that already carries it — so a retitled PR or a diverged title SHA lands on the occupied path and receives the server's existing conflict/reopen answer instead of materializing a second file that shares one identity. A miss, a nil resolver and a resolver error keep the command's own title; the ambiguity is logged and never redirects. Measured 2026-10-27 in the prod task store: 16 identifiers across 35 files, and all five stuck `github-update-go` records were members of such a group.
   ```

   Leave `## v0.22.0` and everything below it unchanged.

9. Self-check before finishing: re-run every `<verification>` command and confirm each passes, then walk the change against each requirement — the helper sits after `createTaskTitle`, the redirect is applied between the title computation and `createThroughAPI`, no signature changed, the two new cases are present and named, the `ResolveCallCount` assertion is `1`, and the changelog bullet sits under `## Unreleased`.

</requirements>

<constraints>

- Do NOT commit — dark-factory handles git.
- Existing tests must still pass, including the clean-miss case, the nil-resolver cases, and the different-identifier disambiguation case.
- Do not change any exported signature, the `result.TaskPathResolver` interface, or any mock. `NewCreateTaskExecutor`, `createThroughAPI`, `postCreate`, `createTaskTitle` and `recordCreatedTaskPath` keep their current signatures and bodies.
- The resolver is already a constructor parameter and may be nil; the change must not make it mandatory and must not add a nil check at construction time.
- The server remains the authority on the filename: the executor still keys its recorded path off the title it asked for, and `recordCreatedTaskPath` is still recorded from that answer.
- The redirect must return a title stem, never a path. Do not join `taskDir` or construct any path from the resolver's answer, and do not read the vault file the resolver names.
- The redirect must not suppress the existing ` - <id[:8]>` disambiguation for two different identifiers colliding on one title.
- Do not reintroduce the pre-check removed for vault-api#28: read the identifier→path index only, never the index's shared identifier/name/slug space.
- Do not add a recurring-task carve-out: each recurring instance carries its own period-scoped identifier, so a new period is a miss.
- No new config field, env var, CLI flag, metric or dependency. Observability for this change is the two log lines named in the requirements.
- Use `glog` for logging and the `github.com/bborbe/errors` idioms already in the file; do not add `fmt.Errorf` and do not use `context.Background()`.
- `golines --max-len=100` applies. Keep the added lines within it.
- Do not run `go mod vendor` and never use `-mod=vendor`. This change adds no dependencies.

</constraints>

<verification>

Run `ROOTDIR=/workspace go test -mod=mod -count=1 ./pkg/command/...` — must exit 0.

Run `ROOTDIR=/workspace make precommit` — must pass.

Run `grep -q 'func reuseExistingIdentifierTitle(' pkg/command/task_create_task_executor.go` — must exit 0.

Run `grep -q 'title = reuseExistingIdentifierTitle(ctx, resolver, cmd.TaskIdentifier, title)' pkg/command/task_create_task_executor.go` — must exit 0.

Run `grep -q 'resolves ambiguously' pkg/command/task_create_task_executor.go` — must exit 0.

Run `grep -q 'using title %q instead of %q' pkg/command/task_create_task_executor.go` — must exit 0.

Run `grep -q 'ExistingIdentifierTitleIsReused' pkg/command/task_create_task_executor_test.go` — must exit 0.

Run `grep -q 'does not redirect when the resolver reports an ambiguous identifier' pkg/command/task_create_task_executor_test.go` — must exit 0.

Run `grep -q 'Identifier redirect on create' docs/controller-design.md` — must exit 0.

Run `awk '/^## /{sec=$0} /no longer creates a second file for the same task_identifier/{print sec}' CHANGELOG.md` — must print `## Unreleased`.

</verification>
