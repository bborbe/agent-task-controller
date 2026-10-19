---
status: completed
spec: [017-bug-rename-remints-task-identifier]
summary: Reordered scanFiles to prune vanished paths before the file loop, creating rename-preservation test (spec 017)
execution_id: 325eaff1-af34-538f-ab6a-f48572a04185-exec-031-spec-017-rename-preserves-task-identifier
dark-factory-version: dev
created: "2026-10-19T17:03:00Z"
queued: "2026-10-19T18:26:44Z"
started: "2026-10-19T18:26:45Z"
completed: "2026-10-19T18:42:21Z"
branch: fix/rename-remints-identifier
---

# Prune vanished paths before the scan loop so a rename keeps its task_identifier

<summary>

- Renaming a task file currently makes the controller replace its identifier with a brand-new UUID, silently detaching the task from every reference keyed on that identifier.
- The scanner already knows how to forget a vanished path, but it runs that cleanup after it has processed every file in the cycle.
- Because the cleanup runs too late, the renamed file is compared against the bookkeeping that still remembers its own former path, and is judged a duplicate of itself.
- The cleanup now runs first: the cycle's path set is built from the listing, vanished paths are pruned, and only then are the files processed.
- After the change a rename is reconciled as a move: the identifier stays byte-identical, the file is not rewritten, and the published identifier-to-path mapping holds only the new path.
- Two genuinely existing files that share one identifier are still repaired exactly as today: one keeps the identifier and the other is re-minted in exactly one write.
- A missing identifier is still injected and a non-UUID identifier is still replaced — both existing behaviours are untouched.
- The set of identifiers reported as deleted per cycle is unchanged; only the moment it is computed moves earlier.
- No new setting, flag, metric, or identifier-generation change is introduced, and past orphans are not repaired.
- The changelog records the fix.

</summary>

<objective>

Stop a task file rename from re-minting its `task_identifier`: build the cycle's path set from the listing and prune vanished paths from the scanner's bookkeeping before the per-file loop, so the identifier-uniqueness check sees only paths that still exist and a renamed file no longer collides with its own former path — while a genuine duplicate (two existing files sharing one identifier) is still repaired by re-minting exactly one of them. Satisfies spec 017 Desired Behavior 1-6 and Acceptance Criteria 1-5.

</objective>

<context>

This repo has no root `CLAUDE.md`; the global YOLO container `CLAUDE.md` already in your context governs project conventions. The spec for this work is `specs/in-progress/017-bug-rename-remints-task-identifier.md`.

Read the spec first, in full. Pay particular attention to: Problem, Goal, Non-goals (all four are load-bearing vetoes), Desired Behavior 1-6, Constraints, Assumptions, Failure Modes (every row), Security / Abuse Cases, and the "Suggested Decomposition" table (this prompt is its single row). AC6 is the Post-Deploy rung and is operator-side — it is NOT in this prompt's scope.

Read these files IN FULL before writing anything:

- `pkg/scanner/vault_scanner.go` (607 lines) — the whole file. The single production edit site is `scanFiles`. Read `collectDeleted` (it deletes every `v.hashes` entry whose path is absent from `seen`, and refuses to emit an empty identifier downstream), `processFile` (the duplicate branch is the `if !v.isIdentifierUnique(taskID, relPath)` block that calls `injectAndStore` with `removeTaskIdentifier(content)`), `injectAndStore` (mints `uuid.New().String()` and writes), and `publishIndex` (rebuilds from `v.hashes`; its doc comment states it is called once per completed cycle after the deleted-file collection has returned — that remains true after this change).
- `pkg/scanner/task_identifier.go` (181 lines) — `isIdentifierUnique(id string, relPath string) bool` scans `v.hashes` for any **different path** carrying `id`. Its signature and body must NOT change. `removeTaskIdentifier` must NOT change.
- `pkg/scanner/scanner_suite_test.go` — the Ginkgo bootstrap (`TestSuite`) and the counterfeiter `//go:generate` directive. A new spec file adds no `TestXxx` function.
- `pkg/scanner/vault_scanner_internal_test.go` — the setup patterns for `package scanner` specs: hand-built `&vaultScanner{metrics: metrics.New(), hashes: ..., ops: fileOps{...}, autoInject: true}` literals (see the `injectAndStore` / auto-inject Describe blocks around lines 83-344), and the existing `empty-id.md` / `non-uuid.md` fixtures that must stay green.
- `pkg/scanner/vault_scanner_index_internal_test.go` — `indexGitClient`, `convergenceGitClient` usage, and the `RunCycle` harness. Note the spec "rebuilds the index per cycle instead of accumulating stale entries" (around lines 165-186): it removes `mover.md` and runs a cycle with no replacement, so it keeps passing when pruning moves earlier. Do not modify it.
- `pkg/scanner/vault_scanner_test.go` — the `Describe("runCycle")` block (around lines 598-805), including the existing duplicate spec ("duplicate task_identifier across files is replaced with fresh UUID") and the deleted-file spec ("deleted file appears in Deleted on next cycle"). These are the over-correction guards; do not change their `Expect(` lines.
- `pkg/scanner/vault_scanner_convergence_internal_test.go` — `convergenceGitClient`, `convergenceHarness`, `newConvergenceHarness`; a convenient precedent for building a `vaultScanner` over fixed file ops. You do not need to change it.

The current shape of the function you are changing is (verified by reading the source):

```go
	seen := make(map[string]struct{})
	var changed []lib.Task
	var written []string
	writeError := false
	for _, relPath := range paths {
		select {
		case <-ctx.Done():
			return nil, nil, nil, false
		default:
		}
		seen[relPath] = struct{}{}
		task, wrote, werr := v.processFile(ctx, relPath)
		if werr {
			writeError = true
		}
		if wrote != "" {
			written = append(written, wrote)
		}
		if task != nil {
			changed = append(changed, *task)
		}
	}
	deleted, err := v.collectDeleted(ctx, seen)
	if err != nil {
		glog.V(4).Infof("collectDeleted: %v", err)
	}
	v.publishIndex()
	return changed, deleted, written, writeError
```

Read the coding-plugin docs (in-container paths):

- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo/Gomega `Describe` / `It` style, and the external-test-package convention (this prompt's new file is deliberately `package scanner`, see requirement 2).
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-precommit.md` — linter budgets: `funlen` 80 lines / 50 statements, `nestif` 4, `gocognit` 20, and `golines --max-len=100`, which `make format` runs over every `.go` file including tests.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-patterns.md` — the in-repo pattern of a private struct with injected collaborators; no new exported API is introduced here.
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — `## Unreleased` placement, required prefix, one bullet per logical change.

Environment facts that shape this prompt:

- The execution container's `.git` is masked (the daemon launches with `hideGit=true`; this repo's `.dark-factory.yaml` sets `workflow: direct` and no `hideGit`, so the masking comes from the launch flags). Do NOT run any `git` command anywhere in this prompt — it dies with `fatal: not a git repository`, and the daemon does not check `<verification>` exit codes, so a failed git command reads as a pass. There is no `git`-dependent step in this prompt.
- Do NOT run `kubectl*`, `docker`, `make build`, `make buca`, `gh`, or any operator/deploy command.
- `make precommit` runs `make format` (go-modtool, goimports-reviser, golines, gofmt), which rewrites `.go` files in place, and `make generate`, which wipes and regenerates `mocks/`. Both are expected, not failures.
- `make precommit` and `make test` run from the **repo root** — this is a single Go module and `pkg/scanner/` carries no Makefile. The focused equivalent is `go test -count=1 ./pkg/scanner/...`.
- The spec's AC evidence uses `go test ./pkg/scanner/... -run TestSuite -ginkgo.focus='<string>'`. Ginkgo's focus is a regex matched against a spec's full description text, so the `It` descriptions must contain those exact substrings.

</context>

<requirements>

## 1. Reorder `scanFiles` in `pkg/scanner/vault_scanner.go`

Replace the body of `scanFiles` (everything after the `paths, err := v.ops.listFiles(ctx, glob)` error block) with the following. Do not change the function signature, the `glob` computation, or the list-failure early return (`return nil, nil, nil, false`) — a failed listing must still leave the previous bookkeeping and published index untouched.

```go
	// Build the cycle's path set and prune vanished paths BEFORE processing any
	// file. isIdentifierUnique answers against v.hashes, so a rename's old path
	// must be gone from the bookkeeping before the new path is checked; otherwise
	// the renamed file collides with its own former path and is re-minted.
	seen := make(map[string]struct{}, len(paths))
	for _, relPath := range paths {
		seen[relPath] = struct{}{}
	}
	deleted, err := v.collectDeleted(ctx, seen)
	if err != nil {
		glog.V(4).Infof("collectDeleted: %v", err)
	}
	var changed []lib.Task
	var written []string
	writeError := false
	for _, relPath := range paths {
		select {
		case <-ctx.Done():
			return nil, nil, nil, false
		default:
		}
		task, wrote, werr := v.processFile(ctx, relPath)
		if werr {
			writeError = true
		}
		if wrote != "" {
			written = append(written, wrote)
		}
		if task != nil {
			changed = append(changed, *task)
		}
	}
	v.publishIndex()
	return changed, deleted, written, writeError
```

Requirements for this edit:

- The `seen[relPath] = struct{}{}` assignment moves out of the per-file loop into the pre-loop build. There must be exactly one occurrence of `seen[relPath] = struct{}{}` in `scanFiles`, and it must precede the `v.collectDeleted(ctx, seen)` call.
- `v.collectDeleted(ctx, seen)` is called exactly once, before the loop. Its return value is the same `deleted` slice returned to the caller. The `glog.V(4)` error handling is unchanged.
- `v.publishIndex()` is still called exactly once, after the loop, immediately before the `return`. Do not call it before the loop and do not call it from `RunCycle`.
- The per-file `select` context check stays at the top of the loop.
- Do NOT change `collectDeleted`, `processFile`, `injectAndStore`, `repairConverges`, `writeCounterReset`, `publishIndex`, `Resolve`, `isIdentifierUnique`, `removeTaskIdentifier`, or `InjectTaskIdentifier`. In particular `isIdentifierUnique` keeps its signature `func (v *vaultScanner) isIdentifierUnique(id string, relPath string) bool` and its body.
- Do NOT change `ScanResult`, the `VaultScanner` interface, either constructor, or any struct field.
- Do NOT add a config field, env var, CLI flag, metric, log line, or retry. The change is a reorder over the same data.
- The comment above `seen` is load-bearing: it records why the ordering matters, so a later reader does not "simplify" it back into the loop. Keep it.

## 2. Add `pkg/scanner/vault_scanner_rename_internal_test.go`

Create that exact file, `package scanner` — **not** `scanner_test` — with the BSD license header the sibling files carry (`// Copyright (c) 2026 Benjamin Borbe All rights reserved.` / `// Use of this source code is governed by a BSD-style` / `// license that can be found in the LICENSE file.`). Add no `TestXxx` function; `scanner_suite_test.go` owns the bootstrap.

**Why internal and why `scanFiles`:** the specs must seed the unexported `hashes` bookkeeping and must assert the `deleted` and `written` values `scanFiles` returns. Driving `scanFiles` directly presents the post-rename directory state (old path absent from the listing) without git plumbing, and it is the function whose ordering is the defect. Add a short file-level comment saying exactly that.

**2a. Imports and helpers.** The file needs `"context"`, `lib "github.com/bborbe/agent"`, `. "github.com/onsi/ginkgo/v2"`, `. "github.com/onsi/gomega"`, and `"github.com/bborbe/agent-task-controller/pkg/metrics"`. No other import is required.

Declare one top-level `var _ = Describe("rename preserves task_identifier (spec 017)", func() { ... })` containing:

```go
	const (
		renameID = "5d83b642-1111-4111-8111-111111111111"
	)

	taskFile := func(id string) string {
		return "---\ntask_identifier: " + id +
			"\nstatus: in_progress\nassignee: claude\n---\nbody\n"
	}
```

**2b. Spec `RenameKeepsIdentifier`.** The `It` description must contain the exact substring `RenameKeepsIdentifier` (the acceptance command focuses on it). Use a description such as `"RenameKeepsIdentifier: a rename keeps the identifier and writes nothing"`.

The fixture is exactly: bookkeeping seeded with `tasks/old.md` carrying `renameID`, and the listing containing only `tasks/new.md`, whose content carries `renameID`.

```go
	It("RenameKeepsIdentifier: a rename keeps the identifier and writes nothing", func() {
		ctx := context.Background()
		oldPath := "tasks/old.md"
		newPath := "tasks/new.md"
		newContent := taskFile(renameID)

		var writes []string
		v := &vaultScanner{
			metrics: metrics.New(),
			taskDir: "tasks",
			hashes: map[string]fileEntry{
				oldPath: {
					hash:           [32]byte{1},
					taskIdentifier: lib.TaskIdentifier(renameID),
					assignee:       lib.TaskAssignee("claude"),
				},
			},
			ops: fileOps{
				listFiles: func(_ context.Context, _ string) ([]string, error) {
					return []string{newPath}, nil
				},
				readFile: func(_ context.Context, _ string) ([]byte, error) {
					return []byte(newContent), nil
				},
				writeFile: func(_ context.Context, relPath string, _ []byte) error {
					writes = append(writes, relPath)
					return nil
				},
			},
			autoInject: true,
			index:      make(map[lib.TaskIdentifier][]string),
		}

		changed, deleted, writtenPaths, writeError := v.scanFiles(ctx)

		Expect(writeError).To(BeFalse())
		Expect(writes).To(BeEmpty(), "a rename must not write the file")
		Expect(writtenPaths).To(BeEmpty())
		Expect(deleted).To(ConsistOf(lib.TaskIdentifier(renameID)))
		Expect(v.hashes).NotTo(HaveKey(oldPath))
		Expect(v.hashes).To(HaveLen(1))
		Expect(v.hashes[newPath].taskIdentifier).To(Equal(lib.TaskIdentifier(renameID)))
		Expect(changed).To(HaveLen(1))
		Expect(changed[0].TaskIdentifier).To(Equal(lib.TaskIdentifier(renameID)))
		path, found, err := v.Resolve(ctx, lib.TaskIdentifier(renameID))
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(path).To(Equal(newPath))
		Expect(v.index[lib.TaskIdentifier(renameID)]).To(ConsistOf(newPath))
	})
```

This spec must fail on the pre-fix code: the old path is still in `hashes` when `processFile("tasks/new.md")` runs, so `isIdentifierUnique` reports a duplicate, `injectAndStore` writes a fresh UUID, and the write assertions plus the identifier assertion fail. Do not weaken the assertions to make the pre-fix code pass.

**2c. Spec `GenuineDuplicateStillReMints`.** The `It` description must contain the exact substring `GenuineDuplicateStillReMints`. Use a description such as `"GenuineDuplicateStillReMints: two existing files carrying one identifier re-mint one"`.

The fixture has **no** seeded bookkeeping: both paths are listed and both file contents carry `renameID`. The listing order is fixed so the first-listed file deterministically keeps the identifier.

```go
	It("GenuineDuplicateStillReMints: two existing files carrying one identifier re-mint one", func() {
		ctx := context.Background()
		firstPath := "tasks/first.md"
		secondPath := "tasks/second.md"
		duplicateContent := taskFile(renameID)

		var writes []string
		var lastWritten []byte
		v := &vaultScanner{
			metrics: metrics.New(),
			taskDir: "tasks",
			hashes:  make(map[string]fileEntry),
			ops: fileOps{
				listFiles: func(_ context.Context, _ string) ([]string, error) {
					return []string{firstPath, secondPath}, nil
				},
				readFile: func(_ context.Context, _ string) ([]byte, error) {
					return []byte(duplicateContent), nil
				},
				writeFile: func(_ context.Context, relPath string, content []byte) error {
					writes = append(writes, relPath)
					lastWritten = append([]byte(nil), content...)
					return nil
				},
			},
			autoInject: true,
			index:      make(map[lib.TaskIdentifier][]string),
		}

		changed, deleted, writtenPaths, writeError := v.scanFiles(ctx)

		Expect(writeError).To(BeFalse())
		Expect(writes).To(Equal([]string{secondPath}), "exactly one file is re-minted")
		Expect(writtenPaths).To(Equal([]string{secondPath}))
		Expect(deleted).To(BeEmpty())
		Expect(v.hashes[firstPath].taskIdentifier).To(Equal(lib.TaskIdentifier(renameID)))
		Expect(v.hashes[secondPath].taskIdentifier).NotTo(Equal(lib.TaskIdentifier(renameID)))
		Expect(isValidUUID(string(v.hashes[secondPath].taskIdentifier))).To(BeTrue())
		Expect(changed).To(HaveLen(1))
		Expect(changed[0].TaskIdentifier).To(Equal(lib.TaskIdentifier(renameID)))
		Expect(string(lastWritten)).NotTo(ContainSubstring(renameID))
		Expect(string(lastWritten)).To(ContainSubstring("task_identifier: "))
		Expect(v.index[lib.TaskIdentifier(renameID)]).To(ConsistOf(firstPath))
		Expect(v.index).To(HaveLen(2))
	})
```

`isValidUUID` is the package-private helper in `pkg/scanner/task_identifier.go`; the file is `package scanner`, so call it directly — do not import `uuid` and do not reimplement it.

**2d. Test constraints.** Do not use `PIt`, `XIt`, `FIt`, `Skip(`, or `Pending` anywhere. Keep each `It` closure within the `funlen` budget (80 lines) and every line within 100 characters. Do not add a third spec: the two focus strings plus the existing `empty-id.md` / `non-uuid.md` cases in `vault_scanner_internal_test.go` are the required evidence, and the existing duplicate spec in `vault_scanner_test.go` is the over-correction guard.

## 3. Record the fix in `CHANGELOG.md`

The newest section today is `## v0.11.7`; there is no `## Unreleased`. Create `## Unreleased` immediately after the frozen preamble (after the "and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html)." line) and immediately above `## v0.11.7`, containing exactly one bullet:

```markdown
## Unreleased

- fix: prune vanished task paths from the scanner's per-path bookkeeping before the cycle's file loop, so renaming a task file no longer collides with its own former path in the identifier-uniqueness check and no longer re-mints its `task_identifier`. The scan already pruned vanished paths, but only after processing every file, so for the whole loop the bookkeeping still held the renamed file's old path; the new path matched that stale entry, was judged a duplicate, and `injectAndStore` stripped the identifier and wrote a fresh UUID — silently detaching the task from the escalation card, vault-cli payload and result write-back that key on it. The pruning now runs before the loop over the same listing-derived path set, so a rename is reconciled as a move: the identifier is byte-identical, no write occurs, and the spec-016 index publishes the new path only. A genuine duplicate — two existing files carrying one identifier — still re-mints exactly one file, the deleted-identifier list is unchanged, and the uniqueness check's signature is untouched (spec 017)
```

- One bullet, one logical change, `fix:` prefix (this is a defect, so it ships a patch bump).
- Do not create a second `## Unreleased`, do not touch any released section, and leave the `# Changelog` title and the SemVer preamble byte-for-byte unchanged.

## 4. Self-check before finishing

Re-run every command in `<verification>` and confirm each passes. Then walk the spec against the change:

- **Desired Behavior 1 / AC1** — the rename spec proves the identifier is unchanged with 0 writes.
- **Desired Behavior 2 / AC2** — the duplicate spec proves exactly one of two existing files is re-minted with 1 write.
- **Desired Behavior 3 / AC3** — the existing `empty-id.md` auto-inject cases still pass unchanged.
- **Desired Behavior 4 / AC4** — the existing `non-uuid.md` replacement cases still pass unchanged.
- **Desired Behavior 5** — the rename spec asserts `deleted` contains `renameID`, and the existing deleted-file spec still passes: the same identifiers are reported, only computed earlier.
- **Desired Behavior 6** — the rename spec asserts `Resolve` returns the new path and `index[renameID]` holds only the new path.
- **Failure Modes row 1** (partial listing) — no mitigation added; pruning still uses exactly the listing-derived set, so the pre-existing risk is neither widened nor narrowed. State this in the completion report; it is named in the spec.
- **Failure Modes row 2** (genuine duplicate with one path briefly absent) — with one path absent it is pruned, the surviving file keeps `X`, and the other is re-minted on a later cycle when both are present. This follows from requirements 1 and 2; no extra code.
- **Failure Modes row 3** (index lag) — bounded by `pollInterval`; spec 016's fallback walk still resolves. No change.

Also confirm you did NOT: change `isIdentifierUnique`, `collectDeleted`, `injectAndStore`, `removeTaskIdentifier`, the identifier generation, or the spec-016 index behaviour beyond rename correctness; add a config knob, flag, metric or log line; repair past orphans; or edit any existing `Expect(` line.

</requirements>

<constraints>

- **The uniqueness check's signature must not change:** `func (v *vaultScanner) isIdentifierUnique(id string, relPath string) bool` has one production caller and no direct test callers; the per-file processor is called directly at 13 sites in the internal tests. The fix is at the scan level precisely so no signature widens.
- **The deletion list reported downstream must be unchanged:** the same identifiers are reported deleted for the same cycles, only computed earlier. `collectDeleted` keeps its body and signature.
- **Index publication must still be called exactly once per cycle, after the walk.** `publishIndex` stays immediately before the `return` in `scanFiles`; it is not called from `RunCycle` and not called before the loop.
- **`pkg/scanner` must not import `pkg/result`.** Nothing in this change touches that import direction.
- **No new config field, env var, CLI flag, metric, retry or log line.** The spec's Goal and Non-goals are load-bearing: no opt-out, no tunable, no new observability.
- **No change to identifier generation** (`uuid.New().String()` in `injectAndStore`) and **no repair of identifiers already orphaned by past re-mints**.
- **The identifier stripper is frozen.** `removeTaskIdentifier`'s prefix/parse behaviour — including its known separate defect of removing a body line beginning `task_identifier:` — must not be touched.
- **The spec-016 index is frozen beyond rename correctness.** Do not change `publishIndex`, `Resolve`, the multimap, the empty-identifier skip, or the per-cycle rebuild.
- **A missing identifier is still injected and a non-UUID identifier is still replaced.** Both existing behaviours and their `AUTO_INJECT_TASK_IDENTIFIER=false` gates stay exactly as they are.
- **Existing specs must pass with unmodified `Expect(` lines**, including the duplicate spec and the deleted-file spec in `vault_scanner_test.go` and the index rebuild spec in `vault_scanner_index_internal_test.go`.
- **Linter budgets** per `go-precommit.md`: `funlen` under 80 lines / 50 statements, `nestif` under 4, `gocognit` under 20, every line under 100 characters. Write the new test file already within budget.
- **`make precommit` and `make test` run from the repo root** (single Go module; `pkg/scanner/` carries no Makefile).
- Do NOT commit — dark-factory handles git.
- Do NOT run any `git` command — the container's `.git` is masked and a failed git command reads as a pass.
- Do NOT run `kubectl*`, `docker`, `make build`, `make buca`, `gh`, or any operator/deploy command.

</constraints>

<verification>

Run from the repo root.

The two acceptance focus commands (spec AC1 and AC2). Each must exit 0; the focused run reports the rest of the suite as skipped, which is expected:

```
go test ./pkg/scanner/... -run TestSuite -ginkgo.focus='RenameKeepsIdentifier' -ginkgo.fail-on-empty
go test ./pkg/scanner/... -run TestSuite -ginkgo.focus='GenuineDuplicateStillReMints' -ginkgo.fail-on-empty
```

The full scanner package suite, including the existing `empty-id.md` and `non-uuid.md` guards (AC3-AC5):

```
go test -count=1 ./pkg/scanner/...
```

Expect Ginkgo `SUCCESS` with `0 Skipped` and `0 Pending`, and no failing spec.

The ordering is what this change is about, so assert it structurally rather than by reading the diff. This awk exits 0 only when, inside `scanFiles`, the `seen` assignment precedes `collectDeleted`, which precedes `processFile`, which precedes `publishIndex`:

```
awk '/^func \(v \*vaultScanner\) scanFiles\(/,/^}/ { if ($0 ~ /seen\[relPath\] = struct\{\}\{\}/) s=NR; if ($0 ~ /v.collectDeleted\(/) c=NR; if ($0 ~ /v.processFile\(/) p=NR; if ($0 ~ /v.publishIndex\(/) i=NR } END { exit !(s && c && p && i && s < c && c < p && p < i) }' pkg/scanner/vault_scanner.go
```

Expect exit 0 with no output. A fix that moved only `collectDeleted` but left `seen` being built inside the loop would leave `s > c` and fail here.

Exactly one `seen` assignment, one `collectDeleted` call and one `publishIndex` call inside `scanFiles`:

```
test "$(awk '/^func \(v \*vaultScanner\) scanFiles\(/,/^}/' pkg/scanner/vault_scanner.go | grep -c 'seen\[relPath\] = struct{}{}')" -eq 1
test "$(awk '/^func \(v \*vaultScanner\) scanFiles\(/,/^}/' pkg/scanner/vault_scanner.go | grep -c 'v.collectDeleted(ctx, seen)')" -eq 1
test "$(awk '/^func \(v \*vaultScanner\) scanFiles\(/,/^}/' pkg/scanner/vault_scanner.go | grep -c 'v.publishIndex()')" -eq 1
```

Expect all three to exit 0. The positive `test "$(…)" -eq N` form is deliberate: a bare `grep -c … || true` exits 0 even on a zero count and would read as a pass.

`publishIndex` is not called from `RunCycle`, and the uniqueness check and deletion collector are untouched:

```
awk '/^func \(v \*vaultScanner\) RunCycle\(/,/^}/' pkg/scanner/vault_scanner.go | (! grep -q 'publishIndex')
grep -n 'func (v \*vaultScanner) isIdentifierUnique(id string, relPath string) bool' pkg/scanner/task_identifier.go
```

Expect exit 0 with no output from the first, and exactly one matching line from the second.

The new spec file is internal, carries both focus strings, and uses no focused/pending specs:

```
grep -n '^package scanner$' pkg/scanner/vault_scanner_rename_internal_test.go
test "$(grep -c 'RenameKeepsIdentifier' pkg/scanner/vault_scanner_rename_internal_test.go)" -ge 1
test "$(grep -c 'GenuineDuplicateStillReMints' pkg/scanner/vault_scanner_rename_internal_test.go)" -ge 1
test "$(grep -c 'PIt(\|XIt(\|FIt(\|Skip(\|Pending(' pkg/scanner/vault_scanner_rename_internal_test.go)" -eq 0
```

Expect `package scanner`, then three exit-0 checks.

The changelog section exists exactly once, sits above the newest released section, and holds exactly one `fix:` bullet naming the identifier:

```
test "$(grep -c '^## Unreleased' CHANGELOG.md)" -eq 1
awk '/^## Unreleased/{u=NR} /^## v0\.11\.7/{v=NR} END{exit !(u && v && u < v)}' CHANGELOG.md
awk '/^## Unreleased/{f=1;next} /^## /{f=0} f && /^- fix:/{n++} END{exit !(n==1)}' CHANGELOG.md
awk '/^## Unreleased/{f=1;next} /^## /{f=0} f && /task_identifier/{f2=1} END{exit !f2}' CHANGELOG.md
```

Expect all four to exit 0.

The full gate, run ONCE at the end:

```
make precommit
```

Expect exit 0 with the full suite green. If it fails, fix the failing target and re-run only that target (`make lint`, `make vet`, `make test`, `make gosec`) until it passes, then re-run the full `make precommit` once. `make format` rewriting `.go` files and `make generate` regenerating `mocks/` are expected.

NOT run here, and deliberately so — spec AC6 is the Post-Deploy rung: it renames a file in a watched vault, waits a poll interval, and reads the deployed controller's git history. That is operator-side. Also NOT run here: any `git` command, because the container's `.git` is masked.

</verification>

<!--
NOTES FOR THE HUMAN REVIEWER — decisions taken and open questions, non-blocking:

1. The spec's Suggested Decomposition is followed exactly: one prompt, its row 1. AC6 is the Post-Deploy
   rung and is excluded from <verification>; AC1-AC5 are covered by the two new specs plus the existing
   guards. The changelog bullet is added per the global CLAUDE.md rule (the repo has CHANGELOG.md), not
   because the spec asks for it.

2. AMBIGUITY RESOLVED — the rename spec drives `scanFiles` directly rather than `RunCycle`. The spec's
   AC1 names only the focus string and the fixture (seed `tasks/old.md → X`, list only `tasks/new.md`,
   assert X preserved with 0 writes); it does not mandate the entry point. `scanFiles` returns the
   `deleted` and `written` values the spec asserts, so driving it directly avoids git plumbing while
   testing exactly the reordered function. The duplicate spec also drives `scanFiles`, with a fixed
   listing order so "which file keeps X" is deterministic.

3. The rename fixture lists ONLY `tasks/new.md`, exactly as the spec's AC1 states. It therefore does not
   separately assert that a live path present in the listing survives pruning. That property is covered
   by the existing index spec "rebuilds the index per cycle instead of accumulating stale entries" and
   the existing runCycle specs, which all pass with pruning moved earlier.

4. Failure Modes row 1 (partial listing) is pre-existing and deliberately not mitigated: the change uses
   the same listing-derived path set the deletion collector already used, so the risk is neither widened
   nor narrowed. Row 2 (genuine duplicate with one path briefly absent) needs no code: with one path
   absent it is pruned and the survivor keeps X; the other is re-minted on a later cycle when both are
   present. Row 3 (index lag) stays bounded by pollInterval.

5. BEHAVIOUR NOTE, deliberate and not observable downstream — when the context is cancelled partway
   through the loop, `scanFiles` still returns `nil, nil, nil, false` (unchanged), but pruning has now
   already run, whereas before it had not. The returned `Deleted` list is discarded on that path both
   before and after, so no downstream behaviour changes; only the internal bookkeeping of a cancelled
   cycle differs. This follows directly from the required ordering and needs no special handling.

6. No docs/controller-design.md change is included. The spec does not ask for one, and the spec-016
   paragraph at docs/controller-design.md:31 remains accurate: it already says the index is published
   "once a cycle's file walk and deleted-file collection have completed", which is still true. If the
   reviewer wants the pruning order recorded in the design doc, that is a separate documentation
   change, not a behaviour gap.
-->