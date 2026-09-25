---
status: completed
spec: [016-bug-task-identifier-path-index]
summary: Added the result.TaskPathResolver seam and published a concurrency-safe identifier-to-path index from the scanner once per completed scan cycle, preserving the duplicate-identifier error and never indexing an empty identifier
execution_id: agent-task-controller-taskpath-index-exec-027-resolver-seam-and-index
dark-factory-version: v0.196.0
created: "2026-09-25T18:45:00Z"
queued: "2026-09-25T17:04:40Z"
started: "2026-09-25T17:04:42Z"
completed: "2026-09-25T17:10:45Z"
branch: dark-factory/bug-task-identifier-path-index
---

# Publish the scanner's identifier→path index behind a resolver seam

<summary>

- The scanner already knows which task file carries which identifier — it parses every file's identifier to decide whether the file changed — but it throws that knowledge away at the end of each cycle.
- It now publishes that mapping once per completed scan cycle, so another part of the same process can ask "which file carries this identifier?" and get an answer without reading the vault.
- A reader always sees a complete mapping: either the previous cycle's or the new one. The mapping is built in full and then swapped as a whole, so no reader can observe a half-built one or one being changed underneath it.
- The scanner's own internal bookkeeping is untouched and stays single-goroutine; only the published snapshot is shared between goroutines.
- A file whose identifier is empty — which happens when the scanner refuses to repair a file it cannot fix — is never published, so an empty identifier can never be resolved to a file.
- Two files sharing one identifier keep both paths, and asking for that identifier reports the ambiguity as an error naming both files instead of silently picking one.
- An identifier that is not in the mapping is reported as "unknown, no error", so a caller can fall back to its own lookup — an unknown identifier and an ambiguous one are deliberately different answers.
- The mapping is rebuilt from scratch every cycle, so a deleted or renamed file stops being reported on the next cycle and a stale entry cannot accumulate.
- Nothing about how results are written to task files changes yet: this change only builds, publishes and proves the mapping.
- No new setting, flag, metric or second scanner is introduced — the mapping is derived from bookkeeping the scanner already keeps.

</summary>

<objective>

Give the process a concurrency-safe, in-process answer to "which vault file carries this task identifier?", built by the scanner from the bookkeeping it already holds and published once per completed scan cycle, so a later change can resolve an identifier by reading one file instead of walking the whole vault — while preserving the fail-loud behaviour on a duplicate identifier and never making an empty identifier resolvable. Satisfies spec 016 Desired Behavior 1-4 and Acceptance Criteria 4-7 (plus AC 1).

</objective>

<context>

This repo has no root `CLAUDE.md`; the global YOLO container `CLAUDE.md` already in your context governs project conventions. The spec for this work is `specs/in-progress/016-bug-task-identifier-path-index.md`.

Read the spec first, in full. Pay particular attention to: Problem, Goal, Non-goals (all of them — several are load-bearing vetoes), Desired Behavior 1-4, Constraints, Assumptions, Failure Modes (every row), Security / Abuse Cases, Acceptance Criteria 4-7, and the "Suggested Decomposition" table (this prompt is its row 1). Acceptance Criteria 8-10, 11-12 and 13-14 belong to the later prompts in this spec, not to this one.

Read these files IN FULL before writing anything:

- `pkg/scanner/vault_scanner.go` (519 lines) — the whole file. The edit sites are: the `VaultScanner` interface (with its counterfeiter annotation directly above it), the `vaultScanner` struct, both constructors `NewVaultScanner` and `NewGitRestVaultScanner`, `scanFiles` (its early `return nil, nil, nil, false` on a list failure is why the index is NOT rebuilt on a failed cycle), `processFile` (note that `v.hashes[relPath]` is written at the normal-path site BEFORE the empty-status and empty-assignee early returns, so a parked task IS indexed), `injectAndStore` (the halted-repair site stores `taskIdentifier: ""`), and `collectDeleted` (which deletes bookkeeping for unseen paths before returning, and refuses to emit an empty identifier downstream).
- `pkg/scanner/task_identifier.go` (181 lines) — `isIdentifierUnique`, and note that it reads `v.hashes` with no lock. That is deliberate and must stay that way.
- `pkg/result/result_writer.go` — read the `escalationCoalescingMutex` block (`resultWriter` struct fields, ~lines 72-81) as the in-repo precedent for "a mutex guarding an in-process map with a doc comment naming what it protects". Also read `FindTaskFilePath` (~lines 309-375) so the frozen duplicate-error message shape and the resolver contract you are declaring are exactly what the later prompt will consume. **Do not change anything in `pkg/result/result_writer.go` in this prompt.**
- `pkg/scanner/scanner_suite_test.go` — the Ginkgo bootstrap and the `//go:generate ... counterfeiter ... -generate` directive. New spec files in this package add no `TestXxx` function.
- `pkg/scanner/vault_scanner_convergence_internal_test.go` (~lines 24-190) — `convergenceGitClient` (a no-op `gitclient.GitClient` with an unexported `path` field), `convergenceHarness`, `newConvergenceHarness`, `runCycles`, and the `haltLogAnywhereRe` helper. Your new spec file lives in the same `package scanner` and reuses `convergenceGitClient`.
- `pkg/scanner/vault_scanner_test.go` (~lines 1-215) — note that this file is `package scanner_test` and that its doubles (`testGitClient`, `fileOpsTestGitClient`) are therefore NOT visible from `package scanner`. Also note the comment at the top explaining why the scanner's external test package cannot import `mocks` (import cycle), which is why your specs use hand-written doubles.

Read the coding-plugin docs (in-container paths):

- `/home/node/.claude/plugins/marketplaces/coding/docs/go-patterns.md` — public interface + private struct + `New*` constructor; counterfeiter annotations on interfaces; `errors.Wrapf` / `errors.Errorf` from `github.com/bborbe/errors`, never `fmt.Errorf`.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-concurrency-patterns.md` — caller-owned channels, and why a mutex-guarded snapshot is preferred over adding a goroutine to "sync" a map.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo/Gomega `Describe` / `Context` / `It` style, `DescribeTable` / `Entry`, and the `package foo_test` external-test-package convention (note this prompt's spec file is deliberately `package scanner`, see requirement 4).
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-precommit.md` — the linter budgets: `funlen` 80 lines / 50 statements, `nestif` 4, `gocognit` 20, and `golines --max-len=100` which `make format` runs over every `.go` file including tests.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-doc-best-practices.md` — GoDoc comment style for the new interface and methods.
- `/home/node/.claude/plugins/marketplaces/coding/docs/definition-of-done.md` — coverage rules for new code (≥80% statement coverage on new code).

Environment facts that shape this prompt:

- The execution container's `.git` is masked (the daemon launches with `hideGit=true`; this repo's `.dark-factory.yaml` sets `workflow: direct` and no `hideGit`, so the masking comes from the launch flags). Do NOT run any `git` command anywhere in this prompt — it dies with `fatal: not a git repository`, and the daemon does not check `<verification>` exit codes, so a failed git command reads as a pass. There is no `git`-dependent step in this prompt.
- Do NOT run `kubectl*`, `docker`, `make build`, `make buca`, `gh`, or any operator/deploy command.
- `make precommit` runs `make format` (go-modtool, goimports-reviser, golines, gofmt), which rewrites `.go` files in place, and `make generate`, which wipes and regenerates `mocks/`. Both are expected, not failures.
- `make precommit` and `make test` run from the **repo root** — this is a single Go module and `pkg/scanner/` carries no Makefile, so there is no per-directory target to run. The focused equivalent is `go test -count=1 ./pkg/scanner/... ./pkg/result/...`.
- The race detector is opted into with `ENABLE_RACE=true` (`Makefile.precommit:29-34`). It is scoped to `./pkg/result/... ./pkg/scanner/...` on purpose: the whole-suite `-race` run is documented-flaky in the `cmd/*`-style binary smoke tests.

</context>

<requirements>

## 1. Declare the resolver seam in the consumer package — new file `pkg/result/task_path_resolver.go`

Create that file with the BSD license header the sibling files carry (`// Copyright (c) 2026 Benjamin Borbe All rights reserved.` / `// Use of this source code is governed by a BSD-style` / `// license that can be found in the LICENSE file.`), `package result`, and exactly this content below the imports:

```go
//counterfeiter:generate -o ../../mocks/task_path_resolver.go --fake-name TaskPathResolver . TaskPathResolver

// TaskPathResolver resolves a task_identifier to the vault-relative path of the
// task file that carries it.
//
// Three outcomes, and a caller must not conflate them:
//
//   - (path, true, nil) — the identifier is known. path is a path the resolver
//     itself observed; it is never built from the identifier.
//   - ("", false, nil) — the identifier is unknown, so the caller runs its own
//     lookup. A miss is NOT an error. The empty identifier is never indexed, so
//     it is unknown by construction.
//   - ("", false, err) — more than one file carries the identifier, and the error
//     names both paths. A resolver error is never a miss: treating it as one
//     re-derives the ambiguity and can silently resolve to one of the two files.
type TaskPathResolver interface {
	Resolve(ctx context.Context, id lib.TaskIdentifier) (string, bool, error)
}
```

- Imports: `"context"` and `lib "github.com/bborbe/agent"`.
- The interface name, the method name, the parameter order and the return triple are **frozen** by the spec's Constraints. Do not add a second method, do not rename, do not change the signature.
- The annotation must sit directly above the type's doc comment, exactly as in `pkg/result/result_writer.go:31` (annotation line, blank line, doc comment, type declaration). It is what produces `mocks/task_path_resolver.go` (requirement 3), and the later prompt's executor spec asserts `ResolveCallCount()` on that fake.
- Do **not** declare a struct, a constructor, a default implementation, or any helper in this file. It declares the seam only; `pkg/scanner` satisfies it structurally.
- Do **not** touch `pkg/result/result_writer.go` in this prompt.

## 2. Build and publish the index in `pkg/scanner/vault_scanner.go`

### 2a. Extend the `VaultScanner` interface

Add a third method to the `VaultScanner` interface (below `RunCycle`, above the closing brace), keeping the existing counterfeiter annotation line untouched:

```go
	// Resolve returns the vault-relative path of the task file carrying id, from the
	// identifier→path index the last completed scan cycle published.
	// It returns ("", false, nil) when the index does not hold id — an unknown
	// identifier is not an error — and ("", false, err) naming both paths when two
	// files carry id. The index is empty until the first cycle completes.
	Resolve(ctx context.Context, id lib.TaskIdentifier) (string, bool, error)
```

This is what makes the scanner satisfy `result.TaskPathResolver` structurally, without `pkg/scanner` importing `pkg/result`. The import direction is frozen: `pkg/scanner` must NOT import `pkg/result`. Add no import of the consumer package anywhere.

### 2b. Add the published snapshot to `vaultScanner`

Append two fields to the `vaultScanner` struct, after `autoInject`, with these doc comments (this mirrors the `escalationCoalescingMutex` precedent in `pkg/result/result_writer.go` — a mutex guarding an in-process map, with a comment naming exactly what it protects):

```go
	// indexMutex guards index. publishIndex builds the whole map and swaps it under
	// the write lock; Resolve reads the published snapshot under the read lock. A
	// reader therefore observes either the previous cycle's map or the new one,
	// never a partially built map and never one being mutated. The scanner's own
	// bookkeeping (hashes) stays single-goroutine and unguarded — only this
	// published snapshot is shared. In-process only, never serialized.
	indexMutex sync.RWMutex
	// index maps a task identifier to every vault-relative path carrying it. The
	// slice is a multimap entry on purpose: collapsing a duplicate to one path is
	// the 2026-08-31 incident (a result landing on the wrong task file). Rebuilt
	// wholesale once per completed scan cycle; never mutated in place.
	index map[lib.TaskIdentifier][]string
```

Add `"sync"` to the file's import block (it is not imported today). Keep `"time"`, `"context"`, `"crypto/sha256"`, `"os"`, `"path/filepath"` and the existing third-party imports.

### 2c. Initialise the snapshot in both constructors

In `NewVaultScanner` and in `NewGitRestVaultScanner`, add `index: make(map[lib.TaskIdentifier][]string),` to the returned `&vaultScanner{...}` literal, so `Resolve` is well-defined before the first cycle. Do not otherwise change either constructor's signature.

### 2d. Add `publishIndex`

Add this method to `pkg/scanner/vault_scanner.go`, placed directly after `scanFiles`:

```go
// publishIndex rebuilds the identifier→path index from the scanner's bookkeeping and
// publishes it for readers, replacing the previous snapshot wholesale under the write
// lock.
//
// It is called once per completed scan cycle, after the deleted-file collection has
// returned, so a path that cycle deleted is already gone from the bookkeeping and the
// published map never holds a deleted file. The map is built completely before the
// swap, so a reader can never observe a partially built index.
//
// An entry whose identifier is empty is skipped: a halted repair stores an empty
// identifier (injectAndStore), and an empty identifier is not a task — it must never
// be resolvable, exactly as collectDeleted refuses to emit it downstream.
//
// A duplicate identifier keeps BOTH paths in the slice; Resolve reports the ambiguity
// as an error rather than picking one.
func (v *vaultScanner) publishIndex() {
	index := make(map[lib.TaskIdentifier][]string, len(v.hashes))
	for relPath, entry := range v.hashes {
		if entry.taskIdentifier == "" {
			continue
		}
		index[entry.taskIdentifier] = append(index[entry.taskIdentifier], relPath)
	}
	v.indexMutex.Lock()
	defer v.indexMutex.Unlock()
	v.index = index
}
```

The source of truth is `v.hashes` — the same bookkeeping `processFile` uses to decide whether a file changed. Do not build the index by listing the vault, and do not read `v.hashes` under `indexMutex` (its single-goroutine access is deliberate and stays as it is).

### 2e. Call it at the end of `scanFiles`

In `scanFiles`, after the `deleted, err := v.collectDeleted(ctx, seen)` block and immediately before `return changed, deleted, written, writeError`, add `v.publishIndex()`.

- Exactly one call site. Do not call it from `RunCycle`, and do not call it on the list-failure early-return path (`return nil, nil, nil, false`) — a cycle whose list failed must leave the previous snapshot standing, which is the spec's Failure Modes row for an unavailable git-rest.
- Do not move, restructure or reorder anything else in `scanFiles`, and do not change its signature or return values.

### 2f. Add `Resolve`

Add this method to `pkg/scanner/vault_scanner.go`, placed directly after `publishIndex`:

```go
// Resolve returns the vault-relative path of the task file carrying id, from the
// index the last completed scan cycle published.
//
// The empty identifier is never indexed, so it is always reported absent.
// Two paths for one identifier is unresolvable — picking either one writes a result
// onto a file that may belong to a different task (2026-08-31) — so it is reported as
// an error naming both paths, never as a miss.
func (v *vaultScanner) Resolve(
	ctx context.Context,
	id lib.TaskIdentifier,
) (string, bool, error) {
	if id == "" {
		return "", false, nil
	}
	v.indexMutex.RLock()
	paths := v.index[id]
	v.indexMutex.RUnlock()
	switch len(paths) {
	case 0:
		return "", false, nil
	case 1:
		return paths[0], true, nil
	default:
		return "", false, errors.Errorf(
			ctx,
			"duplicate task_identifier %s in %s and %s",
			id,
			paths[0],
			paths[1],
		)
	}
}
```

- The duplicate message shape is frozen by the spec: `duplicate task_identifier %s in %s and %s`. It is the same string `pkg/result/result_writer.go` already uses for the walk's duplicate error, so a caller cannot tell which mechanism reported the ambiguity. Reproduce it byte-for-byte.
- `errors.Errorf` is `github.com/bborbe/errors` (already imported in this file). Never `fmt.Errorf`.
- The lock is held only long enough to read the map reference, never across the error construction.
- `Resolve` performs **no I/O**: no `ReadFile`, no `ListFiles`, no `Pull`, no context check, no retry. The caller reads the file.
- Do not add a second goroutine, an `init()`, a `sync.Once`, a package-level index variable, or a package-level lookup function. The spec's Constraints list each of those as a forbidden shape.
- `isIdentifierUnique` and `collectDeleted` keep reading `v.hashes` unguarded. Do not add the mutex to them.

### 2g. Do not touch anything else

No change to `pkg/scanner/task_identifier.go`, `pkg/scanner/frontmatter.go`, `pkg/result/`, `pkg/command/`, `pkg/factory/`, `main.go`, the metrics, or the existing scanner specs' `Expect(` lines. `processFile`, `injectAndStore`, `repairConverges`, `writeCounterReset` and `collectDeleted` keep their current behaviour exactly.

## 3. Regenerate the counterfeiter fakes

From the repo root run `go generate -mod=mod ./...` (equivalent to the `generate` target `make precommit` runs; it wipes and regenerates `mocks/`).

Expected outcomes:

- `mocks/task_path_resolver.go` is created, declaring `type TaskPathResolver struct` with `ResolveCallCount()`, `ResolveReturns(...)` and `var _ result.TaskPathResolver = new(TaskPathResolver)`.
- `mocks/vault_scanner.go` is regenerated and now also carries `Resolve`, `ResolveCallCount()`, `ResolveReturns(...)`, `ResolveArgsForCall(i)`.
- No other generated file changes semantically.

The `mocks` package must not import `pkg/scanner` from the resolver fake in a way that creates a cycle: `mocks/task_path_resolver.go` imports `pkg/result`, which does not import `mocks`. If generation produces an import cycle, stop and report it rather than editing the generated file.

## 4. Add `pkg/scanner/vault_scanner_index_internal_test.go`

Create that exact file. It is `package scanner` — **not** `scanner_test` — with the BSD license header, and no `TestXxx` function (`scanner_suite_test.go` owns the bootstrap).

**Why internal, and say so in a file-level comment:** the multimap spec has to seed the scanner's unexported bookkeeping, and the duplicate case it covers is unreachable through a plain `RunCycle` because `processFile` repairs a duplicate it processes. Write that reason as a comment at the top of the file, naming `hashes` and `publishIndex`.

**4a. The doubles.** The external test package's doubles (`testGitClient`, `fileOpsTestGitClient` in `vault_scanner_test.go`) are `package scanner_test` and are NOT visible here. Reuse `convergenceGitClient` (already declared in `vault_scanner_convergence_internal_test.go`, same package) and add one local double that gives real file I/O plus a read counter:

```go
// indexGitClient is a gitclient.GitClient with real file I/O over a tmpdir and a read
// counter, so a spec can prove a resolver lookup issues no read. It embeds
// convergenceGitClient for the methods the index specs never exercise.
type indexGitClient struct {
	*convergenceGitClient
	readFileCalls int
}

var _ gitclient.GitClient = (*indexGitClient)(nil)

func (c *indexGitClient) ListFiles(_ context.Context, glob string) ([]string, error) {
	matches, err := filepath.Glob(filepath.Join(c.path, glob))
	if err != nil {
		return nil, err
	}
	rel := make([]string, 0, len(matches))
	for _, m := range matches {
		r, relErr := filepath.Rel(c.path, m)
		if relErr != nil {
			continue
		}
		rel = append(rel, r)
	}
	return rel, nil
}

func (c *indexGitClient) ReadFile(_ context.Context, relPath string) ([]byte, error) {
	c.readFileCalls++
	return os.ReadFile(filepath.Join(c.path, relPath)) // #nosec G304 -- test-only path
}

func (c *indexGitClient) WriteFile(_ context.Context, relPath string, content []byte) error {
	return os.WriteFile(filepath.Join(c.path, relPath), content, 0600) // #nosec G306 -- test-only file
}
```

**4b. The harness.** One top-level `var _ = Describe("vaultScanner identifier index (spec 016)", func() { ... })` with:

- `BeforeEach`: `ctx = context.Background()`; `dir, err = os.MkdirTemp("", "scanner-index-test-*")`; `client = &indexGitClient{convergenceGitClient: &convergenceGitClient{path: dir}}`; `results = make(chan ScanResult, 1)`; and `s, ok = NewGitRestVaultScanner(client, ".", time.Hour, nil, metrics.New(), true).(*vaultScanner)` followed by `Expect(ok).To(BeTrue())` — the **two-value** form is required, not `.(*vaultScanner)` alone: `forcetypeassert` is enabled repo-wide (`.golangci.yml:32`) with `run.tests: true` and is **not** in the `_test.go` exclusion list, so a forced assertion fails `make lint` and with it AC 1. Do not replace the constructor with a bare `&vaultScanner{...}` literal — that would drop the constructor's `index: make(...)` initialization from the exercised path. `taskDir` is `"."` so the scanner's glob is `./*.md`, which `filepath.Join` cleans to `<dir>/*.md`, and the relPaths it stores are bare filenames such as `alpha.md`.
- `AfterEach`: `Expect(os.RemoveAll(dir)).To(Succeed())`.
- Local helpers, declared after the variables they capture: `write(name, content string)` writing `0600` into `dir`; `cycle()` calling `s.RunCycle(ctx, results)` then `Expect(results).To(Receive(&r))`; and `taskFile(id string) string` returning a minimal valid task file carrying `id` (`---\ntask_identifier: <id>\nstatus: in_progress\nassignee: claude\n---\nbody\n`).
- Constants for the identifiers, declared before the helpers that use them: `alphaID = "11111111-1111-4111-8111-111111111111"`, `betaID = "22222222-2222-4222-8222-222222222222"`, `dupID = "33333333-3333-4333-8333-333333333333"`.

Imports the file needs: `"context"`, `"os"`, `"path/filepath"`, `"time"`, `lib "github.com/bborbe/agent"`, `gitclient "github.com/bborbe/agent-task-controller/pkg/gitrestclient"`, `"github.com/bborbe/agent-task-controller/pkg/metrics"`, `. "github.com/onsi/ginkgo/v2"`, `. "github.com/onsi/gomega"`.

**4c. The six specs.** Each `It` must stay under the `funlen` budget of 80 lines; lift any repeated fixture into the helpers rather than adding a `//nolint`.

1. **The mapping is built.** Write `alpha.md` carrying `alphaID` and `beta.md` carrying `betaID`; run one `cycle()`. Then `path, found, err := s.Resolve(ctx, lib.TaskIdentifier(alphaID))` → no error, `found` true, `path` equal to `"alpha.md"`; the same for `betaID` → `"beta.md"`.

2. **An unknown identifier is absent, not an error.** With only `alpha.md` written and one `cycle()`, `s.Resolve(ctx, lib.TaskIdentifier(betaID))` returns no error, `found` false, path `""`.

3. **The empty identifier is never indexed.** Write `halted.md` with the flow-style frontmatter `"---\n{task_identifier: 501, status: in_progress}\n---\nbody\n"` — the shape spec 009/011 prove never converges, so `injectAndStore` halts and stores an entry carrying an empty identifier — plus `alpha.md` carrying `alphaID`; run one `cycle()`. Assert the fixture actually produced the state: `Expect(s.hashes["halted.md"].taskIdentifier).To(Equal(lib.TaskIdentifier("")))` and `Expect(s.hashes["alpha.md"].taskIdentifier).To(Equal(lib.TaskIdentifier(alphaID)))`. Then assert the **published index itself** refuses the empty key: `Expect(s.index).NotTo(HaveKey(lib.TaskIdentifier("")))` and `Expect(s.index).To(HaveLen(1))`. The file is `package scanner`, so the unexported field is reachable, and these two assertions are what make the spec falsifiable: `Resolve` short-circuits `id == ""` before it reads the map, so `Resolve(ctx, "")` returns `("", false, nil)` even on an implementation that indexed the empty identifier, and `s.hashes["halted.md"]` reads the zero value on a missing key, so it too passes when the file was skipped outright. Record `before := client.readFileCalls`, then `s.Resolve(ctx, lib.TaskIdentifier(""))` → no error, `found` false, path `""`, and `Expect(client.readFileCalls).To(Equal(before))` (the resolver issues no read). Then assert the index is not simply empty: `s.Resolve(ctx, lib.TaskIdentifier(alphaID))` → `found` true, `"alpha.md"`, and the read count is still `before`.

4. **The index is rebuilt per cycle, not accumulated.** Write `mover.md` carrying `alphaID`; `cycle()`; assert `s.Resolve(ctx, alphaID)` → `"mover.md"`, no error. Then `Expect(os.Remove(filepath.Join(dir, "mover.md"))).To(Succeed())` and run `cycle()` **before** the identifier reappears — the scanner only drops a path from its bookkeeping in `collectDeleted`, which runs after the whole file loop (`pkg/scanner/vault_scanner.go:217`), so a replacement file written in the same cycle is seen as a duplicate by `isIdentifierUnique` and repaired with a fresh UUID instead of keeping `alphaID`; with `autoInject=false` it is skipped instead, and the identifier is absent either way. Assert `s.Resolve(ctx, alphaID)` now reports absent (no error, `found` false, path `""`) — that is the negative assertion for the stale entry, and it fails if the rebuild is skipped or if the index is merged rather than replaced. Then write `newhome.md` carrying the same `alphaID` and run `cycle()` again; assert `s.Resolve(ctx, alphaID)` returns no error, `found` true, and path `"newhome.md"`.

5. **Duplicate identifiers are preserved and reported.** Do not try to produce this through `RunCycle` — the scanner repairs a duplicate it processes. Instead seed the bookkeeping and publish through the real `publishIndex`:

```go
		s.hashes = map[string]fileEntry{
			"first.md":  {taskIdentifier: lib.TaskIdentifier(dupID)},
			"second.md": {taskIdentifier: lib.TaskIdentifier(dupID)},
		}
		s.publishIndex()

		path, found, err := s.Resolve(ctx, lib.TaskIdentifier(dupID))
		Expect(found).To(BeFalse())
		Expect(path).To(Equal(""))
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("duplicate task_identifier " + dupID))
		Expect(err.Error()).To(ContainSubstring("first.md"))
		Expect(err.Error()).To(ContainSubstring("second.md"))
```

   Assert on the two paths with `ContainSubstring`, **not** on their order: `publishIndex` iterates a map, so the order of `paths[0]` and `paths[1]` is not deterministic.

6. **Concurrent scan and lookup.** Write `alpha.md` carrying `alphaID` and run one `cycle()` first so lookups can hit. Then start one goroutine running 50 `s.RunCycle(ctx, results)` calls, draining `<-results` after each (the buffer is size 1 and is empty before each send, so this is deterministic); the goroutine defers `GinkgoRecover()` and `close(done)`. In the spec goroutine, call `s.Resolve(ctx, lib.TaskIdentifier(alphaID))` 50 times, discarding the results. Wait with `Eventually(done).Should(BeClosed())`, then assert the final `s.Resolve(ctx, alphaID)` returns no error, `found` true, `"alpha.md"`. Put no Gomega assertion inside the goroutine — the race detector, not the assertions, is the check here.

   Do not use `PIt`, `XIt`, `FIt`, `Skip(`, or `Pending` anywhere.

## 5. Self-check before finishing

Re-run every command in `<verification>` and confirm each one passes. Then walk the spec's Acceptance Criteria against the change:

- **AC 4** — the multimap: the duplicate spec asserts both paths are named and the outcome is an error, not a miss.
- **AC 5** — the empty-identifier rule: the halted-repair fixture's entry is present in the bookkeeping with an empty identifier, the empty key is absent from the published index, `Resolve(ctx, "")` returns `("", false, nil)`, and the read count is unchanged across the lookup.
- **AC 6** — the per-cycle rebuild: the mover spec's cycle-2 absence assertion and its cycle-3 new-path assertion together fail if a stale entry survived.
- **AC 7** — the race detector: `ENABLE_RACE=true go test -race -count=1 ./pkg/scanner/... ./pkg/result/...` exits 0 with no `DATA RACE` line.
- **AC 1** — `make precommit` exits 0 at the repo root.

Also confirm you did NOT: import `pkg/result` from `pkg/scanner`; add a config field, env var, CLI flag or metric; add a second goroutine or a package-level index; call `publishIndex` from `RunCycle`; or change any existing `Expect(` line in `pkg/scanner/` or `pkg/result/`.

</requirements>

<constraints>

- **Import direction is frozen:** `pkg/scanner` must not import `pkg/result`. The seam is satisfied structurally.
- **The seam's name and shape are frozen:** `result.TaskPathResolver`, declaring the single method `Resolve(ctx context.Context, id lib.TaskIdentifier) (string, bool, error)`. `scanner.VaultScanner` gains `Resolve` with the same signature so the scanner satisfies the seam structurally.
- **The scanner's bookkeeping stays unguarded.** `vaultScanner.hashes` (`pkg/scanner/vault_scanner.go:58`) keeps its single-goroutine access, written in `processFile` / `injectAndStore` / the empty-to-named reset path and read in `isIdentifierUnique` (`pkg/scanner/task_identifier.go:23`) and `collectDeleted` (`pkg/scanner/vault_scanner.go:495-519`). Only the published snapshot is locked.
- **A resolver miss is not an error.** The resolver reports absence with a nil error; only an ambiguous identifier is an error.
- **The duplicate error's message shape is frozen:** `duplicate task_identifier %s in %s and %s`. The two paths are the two carrying the identifier.
- **Error wrapping uses `errors.Errorf` / `errors.Wrapf` from `github.com/bborbe/errors`** — never `fmt.Errorf`.
- **Forbidden shapes**, each of which would satisfy a naive reading of the goal while breaking an invariant: exposing the live bookkeeping map by pointer to a reader; a package-level index variable; `init()` or `sync.Once` lazy wiring; a second goroutine added to "sync" the index; a package-level lookup function on the scanner called from the consumer's methods.
- **No new config field, env var, CLI flag, or metric.** The index is invariant: a switch that disables it re-opens this bug at the moment the vault is largest.
- **No second resolution mechanism:** no TTL, no expiry, no incremental update, no write-through invalidation. The index is rebuilt wholesale once per scan cycle and read between cycles.
- **The full walk is not removed.** It stays as the fallback for every identifier the index does not hold; removing or bypassing it is out of scope here and is the next prompt's work.
- **Frozen:** `commandExpireDuration`, the routing predicates, the frontmatter merge and its ownership table, the escalation path, the heal-on-write stamp. Do not touch any of them.
- **`make precommit` and `make test` run from the repo root** (single Go module; `pkg/scanner/` carries no Makefile). The race detector is opted into with `ENABLE_RACE=true`.
- The existing specs in `pkg/result/`, `pkg/scanner/` and `pkg/command/` must pass with unmodified `Expect(` lines. This prompt forces **no** argument additions anywhere — no existing signature changes.
- Per `go-precommit.md`: keep `funlen` under 80 lines / 50 statements, `nestif` under 4, `gocognit` under 20, and every line under 100 characters. Write the new spec file already within that budget.
- Do NOT commit — dark-factory handles git.
- Do NOT run any `git` command — the container's `.git` is masked and a failed git command reads as a pass.
- Do NOT run `kubectl*`, `docker`, `make build`, `make buca`, `gh`, or any operator/deploy command.

</constraints>

<verification>

Run from the repo root. Fast loop while iterating:

```
go test -count=1 ./pkg/scanner/... ./pkg/result/...
```

Expect the Ginkgo suites to report `SUCCESS` with `0 Skipped` and `0 Pending`, including the six new index specs. This command is the primary evidence for AC 4, AC 5 and AC 6.

The race detector (AC 7) — scoped to the two packages under test:

```
ENABLE_RACE=true go test -race -count=1 ./pkg/scanner/... ./pkg/result/...
```

Expect exit 0 and **no** `DATA RACE` line in the output. Confirm the absence explicitly (the count form would exit 1 on the outcome being measured, so the negative is written as a `! grep -q`):

```
ENABLE_RACE=true go test -race -count=1 ./pkg/scanner/... ./pkg/result/... >/tmp/race.log 2>&1 && ! grep -q 'DATA RACE' /tmp/race.log
```

Expect exit 0 and no output. The log file is what makes the absence meaningful: a `go test ... | (! grep -q 'DATA RACE')` pipeline reports only the last stage's status, so a red or non-compiling race run — which prints no `DATA RACE` — would exit 0. The `&&` form requires the test run itself to succeed before the absence is checked.

The seam is declared where it is consumed, with its annotation:

```
grep -n 'type TaskPathResolver interface' pkg/result/task_path_resolver.go
grep -c 'counterfeiter:generate' pkg/result/task_path_resolver.go || true
grep -n 'func (v \*vaultScanner) Resolve' pkg/scanner/vault_scanner.go
grep -n 'func (v \*vaultScanner) publishIndex' pkg/scanner/vault_scanner.go
awk '/^func \(v \*vaultScanner\) scanFiles\(/,/^}/' pkg/scanner/vault_scanner.go | grep -c 'v.publishIndex()' || true
awk '/^func \(v \*vaultScanner\) RunCycle\(/,/^}/' pkg/scanner/vault_scanner.go | (! grep -q 'publishIndex')
```

Expect: one line from each of the four `grep -n` calls (the interface declaration, the annotation, the two new methods), a count of `1` from the `scanFiles` awk (exactly one call site, and it is inside `scanFiles`), and exit 0 with no output from the `RunCycle` awk. Placement matters, not just the count: a single call from `RunCycle` would satisfy a bare count while violating DB 1, which requires the index to be published even when the subsequent commit/push step fails.

The import direction is frozen (must print nothing):

```
! grep -rq 'agent-task-controller/pkg/result' pkg/scanner/
! go list -deps -test ./pkg/scanner/... | grep -q 'agent-task-controller/pkg/result'
```

Expect exit 0 and no output from both. The `grep -r` covers every source file in the package, not just `vault_scanner.go`, and the `go list -deps -test` covers the compiled dependency graph including the new internal test file — a directory-scoped grep alone would miss an import added in a sibling file.

The generated fakes exist:

```
test -f mocks/task_path_resolver.go && grep -c 'ResolveCallCount' mocks/task_path_resolver.go
grep -c 'ResolveCallCount' mocks/vault_scanner.go
```

Expect the first to print a count of at least 1 and the second to print at least 1.

The new spec file is internal and seeds the bookkeeping:

```
grep -n '^package scanner$' pkg/scanner/vault_scanner_index_internal_test.go
grep -c 'publishIndex()' pkg/scanner/vault_scanner_index_internal_test.go || true
grep -c 'PIt(\|XIt(\|FIt(\|Skip(\|Pending(' pkg/scanner/vault_scanner_index_internal_test.go || true
```

Expect the first to print `package scanner`, the second at least 1, and the third `0`.

The full gate, run ONCE at the end (AC 1):

```
make precommit
```

Expect exit 0 with the full suite green. `make precommit` runs `ensure format generate test check addlicense`; `format` rewrites `.go` files in place and `generate` wipes and regenerates `mocks/`, both expected. If it fails, fix the failing target and re-run only that target (`make lint`, `make vet`, `make test`, `make gosec`) until it passes, then re-run the full `make precommit` once.

NOT run here, and deliberately so — the spec's Post-Deploy rungs (AC 13/14) read a running controller's pod log on dev and prod, and the image build, pin bump and deploy steps are operator-side. Also NOT run here: any `git` command, because the container's `.git` is masked.

</verification>

<!--
NOTES FOR THE HUMAN REVIEWER — decisions taken and open questions, non-blocking:

1. The spec's Suggested Decomposition is followed exactly: this is its row 1. AC 1 (`make precommit`
   exits 0) applies to every prompt, so it appears here as well as in prompts 2 and 3.

2. AMBIGUITY RESOLVED — "the index holds two paths for one identifier" (spec AC 7 / Failure Modes row
   4) is NOT reachable through `RunCycle`. `processFile` calls `isIdentifierUnique` before storing any
   file-derived identifier, and on a duplicate it repairs the file (minting a fresh UUID) or — when
   `AUTO_INJECT_TASK_IDENTIFIER=false` — skips it entirely without storing an entry; a refused repair
   stores an EMPTY identifier, which the index skips. So two paths sharing one identifier can never
   accumulate in `hashes` by scanning. The multimap is therefore the defensive guarantee the spec asks
   for, and requirement 4c.5 tests it where it is actually observable: it seeds `hashes` and drives the
   real `publishIndex` + `Resolve`. If the reviewer wants a *reachable* duplicate instead, the spec
   would need to say what produces one — that is a spec-level question, not a prompt-level one.

3. AMBIGUITY RESOLVED — the spec's test-style note says the index specs drive "the scanner's exported
   `RunCycle` over a fixture vault", but its AC 4 evidence names `ReadFileCallCount()`, a `mocks.GitClient`
   method. `pkg/scanner`'s external test package cannot import `mocks` (documented import cycle:
   `mocks` imports `pkg/scanner`), and the doubles it does have live in `scanner_test`, invisible from
   `package scanner`. Resolution: the new spec file is `package scanner` (internal), reuses the existing
   `convergenceGitClient`, and adds one local real-file-I/O double with a read counter so the
   "no read on a lookup" clause is still asserted. `RunCycle` is exported and is still what the specs
   drive for the build / rebuild / empty-identifier / concurrency cases.

4. The index deliberately includes a parked task (empty `assignee`) because `processFile` writes
   `v.hashes[relPath]` before the empty-assignee early return. That is required for the real use case —
   a result write for a parked task must resolve — and it is recorded in `<context>` so the executor
   does not "fix" it.

5. `docs/controller-design.md` and `CHANGELOG.md` are prompt 3's scope. This prompt changes no
   documentation, so the doc greps in the spec's container-executable block will still fail after this
   prompt and pass after prompt 3.

6. KNOWN RISK, recorded not fixed — the duplicate error message is now hand-duplicated. `pkg/scanner`
   cannot import `pkg/result`, so `Resolve` carries its own copy of the frozen format string
   `duplicate task_identifier %s in %s and %s`. The two copies must stay in sync by hand and nothing
   asserts their equality: the scanner spec matches only a substring of its own literal, and prompt 2's
   resolver double is constructed from the same frozen string rather than from the walk's real output.
   A `pkg/result` spec that drives both mechanisms over one duplicate fixture and compares the two error
   strings would close it — that package is prompt 2's, so it is recorded rather than done here.
-->

