---
status: completed
summary: Added a bytes.Equal skip for unchanged read-modify-write POSTs, moved three index-fallback logs and added index-miss/timing logs to V(2), plus tests, changelog, and docs.
execution_id: agent-task-controller-skip-noop-push-exec-033-skip-unchanged-gitrest-write-and-intake-timing
dark-factory-version: v0.198.7
created: "2026-10-22T00:00:00Z"
queued: "2026-10-22T06:43:42Z"
started: "2026-10-22T06:45:06Z"
completed: "2026-10-22T06:50:21Z"
---

<summary>
- A result write whose merged content matches the file already on disk no longer triggers a git-rest write, commit and push
- Unchanged writes are skipped quietly, with a log line at the verbosity prod runs, so operators can count how often the shortcut fires
- Writes that do change the file behave exactly as before
- Every successful result write now logs how long the write took, in milliseconds, at the deployed verbosity
- A task the path index does not know now logs that it fell back to the full directory walk, at the deployed verbosity
- The three reasons an index hit gets rejected (file unreadable, frontmatter invalid, identifier no longer present) become visible at the deployed verbosity
- A new test proves an unchanged write makes no git-rest write call and returns no error
- The changelog records the fix and the logging change
</summary>

<objective>
Stop `AtomicReadModifyWriteAndCommitPush` from POSTing to git-rest when the modify closure returns bytes identical to what it read, and make result-intake timing and index-miss reasons visible at the deployed `-v=2`. Measured 2026-10-21 on openclaw-0: each git-rest write+commit+push took p50 2.71 s / p99 7.18 s over 1294 writes, it dominated per-message intake time under a 1172-deep backlog, and 20 commands expired before they were consumed. Many of those writes change nothing. Prod runs `-v=2`, so the existing V(3) diagnostics cannot be seen there.
</objective>

<context>
Read CLAUDE.md for project conventions, if it exists. This repo has no root CLAUDE.md today, so do not treat its absence as an error.

Read these files before changing anything:
- `pkg/gitrestclient/git_client_adapter.go`: the method `func (a *gitRestGitClientAdapter) AtomicReadModifyWriteAndCommitPush(ctx context.Context, absPath string, modify func(current []byte) ([]byte, error), message string) error`. It computes `rel` with `filepath.Rel`, reads `current, err := a.client.Get(ctx, rel)`, calls `updated, err := modify(current)`, then logs `glog.V(3).Infof("gitrest: POST (read-modify-write) %s (%s)", rel, message)` and returns `errors.Wrapf(ctx, a.client.Post(ctx, rel, updated), "write after modify %s", rel)`. The current imports are `context`, `path/filepath`, `strings`, `github.com/bborbe/errors`, `github.com/golang/glog`. There is no `bytes` import yet.
- `pkg/gitrestclient/git_client_adapter_test.go`: `Describe("AtomicReadModifyWriteAndCommitPush", ...)`. The fixtures are `fakeClient *mocks.GitRestClient`, built in `BeforeEach` and wrapped by `adapter = gitrestclient.NewGitClient(fakeClient, basePath)`. `basePath` is `/data/vault`. The existing tests use `fakeClient.GetReturns(...)`, `fakeClient.GetCallCount()`, `fakeClient.PostCallCount()` and `fakeClient.PostArgsForCall(0)`. Follow the first `It("calls Get then Post; content passed to Post is modify's return value", ...)` for style.
- `pkg/result/result_writer.go`:
  - `func resolveFromIndex(ctx context.Context, gitClient gitclient.GitClient, id lib.TaskIdentifier, resolver TaskPathResolver) (string, lib.TaskFrontmatter, bool, error)`. It has a bare `if !found { return "", nil, false, nil }` branch. It also has three `glog.V(3).` + newline + `Infof("FindTaskFilePath: index hit %s ...falling back to walk", ...)` calls: one for `unreadable (%v)`, one for `has invalid frontmatter (%v)`, and one for `no longer carries %s`. A fourth V(3) line, `could not unmarshal existing frontmatter for %s: %v`, is NOT a fallback and must stay at V(3).
  - `func (r *resultWriter) WriteResult(ctx context.Context, req lib.Task) error`. Near its end it logs `glog.V(2).Infof("WriteResult: writing and pushing for task %s", req.TaskIdentifier)`, calls `r.writeAndPublish(ctx, absPath, req, matchedRelPath, commitMessage)` inside an `if err := ...; err != nil` block, then logs `glog.V(2).Infof("WriteResult: completed successfully for task %s", req.TaskIdentifier)`. The `time` package is already imported in this file.
- `.golangci.yml`: `funlen` is set to `lines: 80`, `statements: 50`. `WriteResult` is already close to that limit.
- `CHANGELOG.md`: the top section is `## v0.15.1` and there is no `## Unreleased` section yet. Copy the bullet style (`- fix: **bold lead.** ...`) from that section.
</context>

<requirements>
1. In `pkg/gitrestclient/git_client_adapter.go`:
   - Add `"bytes"` to the standard-library import group, keeping alphabetical order (before `"context"`).
   - In `AtomicReadModifyWriteAndCommitPush`, add a guard right after the `modify` error check and before the existing `glog.V(3).Infof("gitrest: POST (read-modify-write) ...")` line:
     ```go
     if bytes.Equal(updated, current) {
     	// An unchanged file produces no commit, so the POST would only spend a
     	// git-rest write+push round trip (~2.7 s p50 on openclaw, 2026-10-21) for nothing.
     	glog.V(2).Infof("gitrest: skip unchanged read-modify-write %s (%s)", rel, message)
     	return nil
     }
     ```
   - Leave the rest unchanged: the `Get` error path, the `modify` error path, the V(3) POST log line, and the `Post` call with its `errors.Wrapf`. Do not change the method signature or the `GitClient` interface, so no mocks need regenerating.

2. In `pkg/result/result_writer.go`, function `resolveFromIndex`:
   - In the `if !found {` branch, add `glog.V(2).Infof("FindTaskFilePath: index miss for task %s, falling back to walk", id)` before the existing `return "", nil, false, nil`.
   - Change `glog.V(3).` to `glog.V(2).` for exactly three calls: the `index hit %s unreadable (%v), falling back to walk` line, the `index hit %s has invalid frontmatter (%v), falling back to walk` line, and the `index hit %s no longer carries %s, falling back to walk` line. Keep their message text and their two-line `glog.V(2).` / `Infof(...)` layout.
   - Keep the `could not unmarshal existing frontmatter` line at `glog.V(3)`.

3. In `pkg/result/result_writer.go`, function `WriteResult`:
   - Add `writeStart := time.Now()` on the line directly after `glog.V(2).Infof("WriteResult: writing and pushing for task %s", req.TaskIdentifier)` and directly before the `if err := r.writeAndPublish(...)` block.
   - Replace the success log with this exact two-line call:
     ```go
     glog.V(2).Infof("WriteResult: completed successfully for task %s in %dms",
     	req.TaskIdentifier, time.Since(writeStart).Milliseconds())
     ```
   - Change nothing else in `WriteResult`. The change adds two lines net, which keeps the function within `funlen` (80 lines). Do not reflow the call onto more lines. Do not extract a helper function; this exact shape has been verified to pass `make precommit` including `funlen`.

4. In `pkg/gitrestclient/git_client_adapter_test.go`, inside `Describe("AtomicReadModifyWriteAndCommitPush", ...)`, add this `It` right after the existing `It("calls Get then Post; content passed to Post is modify's return value", ...)`:
   ```go
   It("does not call Post when modify returns the content unchanged", func() {
   	existing := []byte("same")
   	fakeClient.GetReturns(existing, nil)
   	err := adapter.AtomicReadModifyWriteAndCommitPush(
   		ctx,
   		"/data/vault/tasks/bar.md",
   		func(current []byte) ([]byte, error) { return []byte("same"), nil },
   		"msg",
   	)
   	Expect(err).To(Succeed())
   	Expect(fakeClient.GetCallCount()).To(Equal(1))
   	Expect(fakeClient.PostCallCount()).To(Equal(0))
   })
   ```
   The closure returns a new slice (`[]byte("same")`), not `current`. That way the test exercises content equality rather than slice identity. Without the `bytes.Equal` guard, the fake's `Post` gets called once and the `PostCallCount()` assertion fails. That is the point of the test.

5. In `CHANGELOG.md`, insert a `## Unreleased` section directly above `## v0.15.1`, with one blank line before and after the heading, containing exactly these two bullets:
   ```
   - fix: **an unchanged result write no longer posts to git-rest.** `AtomicReadModifyWriteAndCommitPush` POSTed the merged content even when the modify closure returned the bytes it read — e.g. a re-delivered result, or one where the ownership guard keeps every on-disk field. Each POST is a git-rest write+commit+push round trip, measured 2026-10-21 on openclaw-0 at p50 2.71 s / p99 7.18 s over 1294 writes, and it dominated per-message intake time (p50 2.40 s/msg under a 1172-deep backlog, 20 `command expired` drops). An unchanged result now returns without the POST and logs `gitrest: skip unchanged read-modify-write` at V(2).
   - feat: intake timing is visible at the deployed `-v=2`. `WriteResult: completed successfully` now carries the write duration in ms, a plain resolver miss logs `FindTaskFilePath: index miss`, and the three index-hit fallback reasons (unreadable, invalid frontmatter, identifier no longer carried) moved from V(3) to V(2).
   ```
   Leave the `## v0.15.1` section and everything below it unchanged.

5b. In `docs/controller-design.md`, replace the sentence ``gitRestGitClientAdapter.AtomicReadModifyWriteAndCommitPush` uses it only for a `glog.V(3)` line and `gitRestClient.Post` sends path and content only` with ``gitRestGitClientAdapter.AtomicReadModifyWriteAndCommitPush` uses it only for log lines (the `glog.V(3)` POST line, and the `glog.V(2)` `gitrest: skip unchanged read-modify-write` line when the modify closure returns the bytes it read, in which case no POST is made) and `gitRestClient.Post` sends path and content only`. After the sentence containing `index hit for task`, add: `A miss logs `index miss for task` and each of the three index-hit fallbacks logs its reason, all at `glog.V(2)`.` The index-miss line fires once per lookup attempt from every `FindTaskFilePath` caller, including non-owning-vault fan-out; that volume is accepted.

6. Self-check: re-run every `<verification>` command and confirm each one passes. Then walk the change against each requirement:
   - the guard sits after the modify error check and returns `nil`;
   - exactly three fallback lines moved to V(2), and the unmarshal line is still V(3);
   - the index-miss line is present;
   - the duration is timed around `writeAndPublish` only;
   - the new test is present;
   - both CHANGELOG bullets sit under `## Unreleased`.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
- Do not change any exported signature, interface, or mock. `GitClient` and `GitRestClient` stay as they are.
- A write whose content differs must still POST exactly as before (same rel path, same bytes, same error wrapping)
- The skip compares bytes only (`bytes.Equal`). Do not add YAML or frontmatter-aware comparison, and do not normalize whitespace.
- No new metrics, flags, env vars, or config fields. Observability for this change is log lines only.
- Use the `github.com/bborbe/errors` wrapping idioms already in these files. Do not add `fmt.Errorf`.
- `WriteResult` must stay within `funlen` (80 lines, 50 statements)
- Do not run `go mod vendor` and never use `-mod=vendor`. This change adds no dependencies (`bytes` is stdlib).
</constraints>

<verification>
Run `ROOTDIR=/workspace make precommit` — must pass.

Run `grep -q 'bytes.Equal(updated, current)' pkg/gitrestclient/git_client_adapter.go` — must exit 0.

Run `grep -q 'skip unchanged read-modify-write' pkg/gitrestclient/git_client_adapter.go` — must exit 0.

Run `grep -q 'does not call Post when modify returns the content unchanged' pkg/gitrestclient/git_client_adapter_test.go` — must exit 0.

Run `grep -q 'index miss for task %s, falling back to walk' pkg/result/result_writer.go` — must exit 0.

Run `grep -q 'completed successfully for task %s in %dms' pkg/result/result_writer.go` — must exit 0.

Run `grep -c 'glog.V(3)' pkg/result/result_writer.go` — must print `7` (it is `10` before the change; exactly three fallback lines move to V(2)).

Run `awk '/^## /{sec=$0} /an unchanged result write no longer posts to git-rest/{print sec}' CHANGELOG.md` — must print `## Unreleased`.

Run `awk '/^## /{sec=$0} /intake timing is visible at the deployed/{print sec}' CHANGELOG.md` — must print `## Unreleased`.
</verification>
