---
status: completed
spec: [009-scanner-repair-convergence-guard]
summary: 'Implemented the vault scanner''s task_identifier repair convergence guard: injectAndStore now re-evaluates candidate bytes through the read-path pipeline (repairConverges) before persisting, refuses non-converging repairs (zero writes/commits, one frozen ERROR log, one repair_not_converging counter increment), stores the on-disk hash so halts ride the existing content-hash short-circuit and self-clear on content change, and halt bookkeeping never leaks an empty identifier into the deleted-tasks stream; parity spec raised to 10, new AC2-AC6 test file with real non-converging fixtures, and CHANGELOG ## Unreleased entry added.'
execution_id: agent-task-controller-identifier-convergence-exec-017-spec-009-scanner-convergence-guard
dark-factory-version: dev
created: "2026-09-05T18:05:00Z"
queued: "2026-09-05T18:30:53Z"
started: "2026-09-05T18:36:11Z"
completed: "2026-09-05T18:43:33Z"
---

# Convergence guard on the vault scanner's task_identifier repair write

<summary>

- The scanner now proves, before writing, that a `task_identifier` repair will actually fix the file — it re-reads the bytes it is about to save exactly the way the next scan cycle would read them.
- A repair that would not fix the file is refused outright: nothing is written to the vault, nothing is committed, and no partially-damaged file ever reaches the shared repository.
- A refused repair surfaces to the operator as one ERROR log line naming the file plus one increment of the existing skipped-files counter — no new metric, no new dashboard.
- Bytes that cannot be parsed at all count as "not fixed" and are refused too, rather than being written and hoped for.
- A refused file is remembered by its content, so it is not retried, re-logged, or re-counted while it stays the same — one log line per broken state, not one per scan cycle forever.
- The moment anyone edits the file, the scanner tries again from scratch; there is no retry counter, no timer, and no permanent giveup.
- A refused file that is later deleted from the vault no longer emits a nameless task into the deleted-tasks stream.
- The guard is unconditional and root-cause-agnostic: it bounds any repair loop, including causes nobody has enumerated yet, without needing to know what caused it.
- Two real malformed frontmatter shapes — not test doubles — drive the new tests, and the previously-fixed block-style shapes are proven to still repair silently in one write.
- The change is recorded in the changelog under a new Unreleased section.

</summary>

<objective>

Make the vault scanner refuse to persist a `task_identifier` repair write that would not clear its own trigger. Before `injectAndStore` writes, re-evaluate the candidate bytes through the exact parse pipeline `processFile` runs on the next cycle; on non-convergence (including a parse failure) write nothing, log one ERROR, increment the `repair_not_converging` skip reason, and remember the file by its on-disk content hash so the existing hash short-circuit suppresses further attempts until the file changes. Satisfies spec 009 AC2-AC6 and AC8.

</objective>

<context>

Prompt 1 of this spec must have landed first — it adds `metrics.ReasonRepairNotConverging`. If `grep -n 'ReasonRepairNotConverging' pkg/metrics/metrics.go` returns nothing, stop and report rather than adding the constant here.

Read these before changing anything:

- `specs/in-progress/009-scanner-repair-convergence-guard.md` — the spec. Read Desired Behavior 1-3, Design Decisions, Constraints (especially the fixture substitution table), Failure Modes, Security / Abuse Cases, and AC2-AC6, AC8.
- `pkg/scanner/vault_scanner.go` — `processFile` (the content-hash short-circuit, the three repair sites), `injectAndStore`, `writeCounterReset`, `collectDeleted`, the `fileEntry` struct, and the `fileOps` struct.
- `pkg/scanner/frontmatter.go` — `extractFrontmatter` and `DeduplicateFrontmatter` (last-wins semantics), the two halves of the read-path pipeline the guard must reuse.
- `pkg/scanner/task_identifier.go` — `taskIdentifierKeyLine`, `removeTaskIdentifier` (span-aware removal shipped by spec 008), `InjectTaskIdentifier`, `isValidUUID`, `isIdentifierUnique`.
- `pkg/scanner/vault_scanner_internal_test.go` — `captureGlogWarnings`, `countLinesMatching`, and the spec-008 `Describe("task_identifier backfill repair (spec 008)")` block whose `ac2Fixtures` rows 10-12 must NOT be touched.
- `pkg/scanner/vault_scanner_test.go` — the parity spec `It("maintains counter-call parity with skip-site log lines ...")` near the end of the file.
- `pkg/gitrestclient/git_rest_client.go` — the `GitClient` interface (10 methods) that the new test's stub must satisfy.
- `pkg/metrics/metrics.go` — `Metrics.SkippedFilesTotal(reason SkipReason) prometheus.Counter` and the `ReasonRepairNotConverging` constant added by prompt 1.
- `CHANGELOG.md` — note there is currently no `## Unreleased` section; the newest section is `## v0.7.4`.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-glog-guide.md` — glog severity conventions.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` and `/home/node/.claude/plugins/marketplaces/coding/docs/go-test-types-guide.md` — Ginkgo conventions in this codebase.
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — changelog bullet format.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-precommit.md` — what `make precommit` runs.

This repo has no root `CLAUDE.md`; `README.md` and `docs/controller-design.md` carry the project conventions.

</context>

<requirements>

## A. The guard itself — `pkg/scanner/vault_scanner.go`

1. Add a package-level function `repairConverges` in `pkg/scanner/vault_scanner.go`, placed immediately AFTER the closing brace of `injectAndStore`. Placement matters: the parity spec in `vault_scanner_test.go` runs `awk '/^func \(v \*vaultScanner\) (processFile|injectAndStore)\(/,/^}/'` over this file, and `repairConverges` must fall outside that range.

```go
// repairConverges reports whether candidate — the exact bytes injectAndStore is
// about to persist — clears the repair trigger. It re-evaluates the candidate
// through the same pipeline processFile runs on the next cycle: frontmatter
// extraction, duplicate-key deduplication (last-wins), and YAML unmarshal.
// Reusing that exact pipeline, rather than inspecting the injected line, is what
// catches the accumulator shape: post-injection the deduped view can resolve
// task_identifier back to the stale value instead of the minted UUID.
//
// It returns true only when the re-evaluated task_identifier is present, is a
// string, and equals id. Every failure inside the pipeline is non-convergence,
// never an error to swallow: bytes that cannot be parsed cannot be proven to have
// cleared the trigger, so they are refused (fail-closed). The V(3) diagnostics
// carry the parse error only — never file content — so a crafted value cannot be
// used to flood the log.
func repairConverges(ctx context.Context, candidate []byte, id string) bool {
	fmYAML, extractErr := extractFrontmatter(ctx, candidate)
	if extractErr != nil {
		glog.V(3).Infof("convergence guard: extract frontmatter failed: %v", extractErr)
		return false
	}
	dedupedYAML, _, dedupErr := DeduplicateFrontmatter(ctx, fmYAML)
	if dedupErr != nil {
		glog.V(3).Infof("convergence guard: deduplicate frontmatter failed: %v", dedupErr)
		return false
	}
	var fmMap map[string]interface{}
	if unmarshalErr := yaml.Unmarshal([]byte(dedupedYAML), &fmMap); unmarshalErr != nil {
		glog.V(3).Infof("convergence guard: unmarshal frontmatter failed: %v", unmarshalErr)
		return false
	}
	raw, present := fmMap["task_identifier"]
	if !present {
		return false
	}
	resolved, isString := raw.(string)
	if !isString {
		return false
	}
	return resolved == id
}
```

2. Change the `injectAndStore` signature to take the file's on-disk content hash as a fifth parameter, and install the guard between the successful `InjectTaskIdentifier` and the `v.ops.writeFile` call. Full replacement for the existing function, doc comment included:

```go
// injectAndStore generates a UUID, writes it into the file via ops.writeFile,
// and records a sentinel hash entry with the file's current assignee.
//
// Before persisting, the candidate bytes are re-evaluated through the exact parse
// pipeline processFile uses on the next cycle (repairConverges). A repair that
// would not clear its own trigger is refused entirely — nothing written, nothing
// committed, zero footprint in the vault — and the file is remembered by its
// on-disk hash so processFile's content-hash short-circuit suppresses every
// further attempt until the file's bytes change on disk.
//
// onDiskHash is the sha256 of the bytes processFile READ this cycle, not of the
// candidate bytes. Storing the real on-disk hash is exactly what makes a halt ride
// the existing short-circuit and re-arm on content change, and is what makes the
// halt path behave differently from the zero-hash sentinel a successful write
// stores.
//
// Returns (nil task, writtenRelPath, writeError).
func (v *vaultScanner) injectAndStore(
	ctx context.Context,
	content []byte,
	relPath string,
	currentAssignee lib.TaskAssignee,
	onDiskHash [32]byte,
) (*lib.Task, string, bool) {
	id := uuid.New().String()
	newContent, injectErr := InjectTaskIdentifier(ctx, content, id)
	if injectErr != nil {
		glog.Warningf("skipping %s: failed to inject task_identifier: %v", relPath, injectErr)
		v.metrics.SkippedFilesTotal(metrics.ReasonInjectTaskIdentifierFailed).Inc()
		return nil, "", false
	}
	if !repairConverges(ctx, newContent, id) {
		// The offending value is deliberately NOT interpolated: a large sequence or
		// mapping under task_identifier must not be usable to flood the log. The path
		// is the only thing an operator needs to find the file.
		glog.Errorf("task_identifier repair did not converge, halting repair for: %s", relPath)
		v.metrics.SkippedFilesTotal(metrics.ReasonRepairNotConverging).Inc()
		v.hashes[relPath] = fileEntry{
			hash:           onDiskHash,
			taskIdentifier: "",
			assignee:       currentAssignee,
		}
		return nil, "", false
	}
	if writeErr := v.ops.writeFile(ctx, relPath, newContent); writeErr != nil {
		glog.Warningf("failed to write %s: %v", relPath, writeErr)
		return nil, "", true
	}
	v.hashes[relPath] = fileEntry{
		hash:           [32]byte{},
		taskIdentifier: lib.TaskIdentifier(id),
		assignee:       currentAssignee,
	}
	return nil, relPath, false
}
```

   The halt returns the frozen skip shape `(nil, "", false)` — do not add a fourth return value, and do not change the `(nil, relPath, false)` success shape. `scanFiles` bookkeeping must need no changes.

   The ERROR log substring `task_identifier repair did not converge, halting repair for:` is frozen. Do not reword it, do not change its severity from `glog.Errorf`, and do not add a second log line on this path.

3. Update all three `injectAndStore` call sites in `processFile` to pass the `hash` variable computed at the top of `processFile` (`hash := sha256.Sum256(content)`). All three are in `processFile` and all three pass the same `hash`, including the key-absent site where `content` is unmodified:

   - key-absent site: `return v.injectAndStore(ctx, content, relPath, currentFMAssignee)` → `return v.injectAndStore(ctx, content, relPath, currentFMAssignee, hash)`
   - present-but-invalid site: `return v.injectAndStore(ctx, removeTaskIdentifier(content), relPath, currentFMAssignee)` → `return v.injectAndStore(ctx, removeTaskIdentifier(content), relPath, currentFMAssignee, hash)`
   - duplicate-identifier site: same edit as the present-but-invalid site.

   Do NOT add the guard to `writeCounterReset` — it is not a `task_identifier` repair and is out of scope for this spec.

   Do NOT remove, weaken, or bypass the content-hash short-circuit at the top of `processFile` (`if existing, ok := v.hashes[relPath]; ok && existing.hash == hash { return nil, "", false }`). It is frozen; the halt rides it rather than replacing it.

4. Append to the existing `//nolint:funlen,gocognit` directive comment above `processFile` so the parity bookkeeping note stays truthful. Add this clause to the end of the existing comment text, keeping everything already there:

```
; spec-009 adds no statements to processFile (the convergence guard lives in injectAndStore), but raises the parity-check awk count from 9 to 10 via the convergence-halt log+counter site.

   Additionally, correct the now-false trailing clause of the existing comment: change `parity-check awk count stays at 9.` to `parity-check awk count was 9 at spec-008.` so the comment does not assert 9 and 10 simultaneously. Do not touch the `//nolint:funlen,gocognit` directive itself.
```

5. In `collectDeleted`, drop a halted file's bookkeeping entry without emitting its empty identifier downstream. Replace the body of the `if _, ok := seen[relPath]; !ok {` block:

```go
		if _, ok := seen[relPath]; !ok {
			delete(v.hashes, relPath)
			// A halted repair has no identifier to record, so its entry carries an
			// empty lib.TaskIdentifier. Dropping the bookkeeping is correct; emitting
			// the empty identifier downstream is not — an empty identifier is not a
			// task, and it must never reach the deleted-tasks stream.
			if entry.taskIdentifier == "" {
				continue
			}
			deleted = append(deleted, entry.taskIdentifier)
		}
```

   Deleting from a map while ranging over it is defined behaviour in Go; keep the `delete` before the `continue` so the halt entry is removed either way.

## B. Parity spec — `pkg/scanner/vault_scanner_test.go`

6. Update the parity spec `It("maintains counter-call parity with skip-site log lines ...")` so it recognises the new skip site and pins both counts at `10`. This is a parity-preserving raise, not a loosening: the guard adds exactly one skip-site log line AND exactly one counter call, so the invariant "every skip-site log line has a matching counter increment" is unchanged.

   Add a regex for the new log line next to `autoInjectGateRe`. It must be a regex, not `strings.Count`, for the same reason `autoInjectGateRe` is: `make format` runs `golines --max-len=100 -w`, which can wrap the call so a newline lands between `glog.Errorf(` and the string literal.

```go
				autoInjectGateRe := regexp.MustCompile(
					`glog\.Warningf\(\s*"AUTO_INJECT_TASK_IDENTIFIER=false; skipping`,
				)
				convergenceHaltRe := regexp.MustCompile(
					`glog\.Errorf\(\s*"task_identifier repair did not converge`,
				)
				skipCount := strings.Count(body, `glog.Warningf("skipping`) +
					strings.Count(body, `glog.Errorf("skipping`) +
					strings.Count(body, `glog.Warningf("failed to read`) +
					len(autoInjectGateRe.FindAllStringIndex(body, -1)) +
					len(convergenceHaltRe.FindAllStringIndex(body, -1))
				counterCount := strings.Count(body, `SkippedFilesTotal(`)
				Expect(
					skipCount,
				).To(Equal(10), "expected 10 skip-site log lines (6 existing + 3 auto-inject gate sites + 1 spec-009 convergence halt), got %d", skipCount)
				Expect(
					counterCount,
				).To(Equal(10), "expected 10 counter increment calls (6 existing + 3 auto-inject gate sites + 1 spec-009 convergence halt), got %d", counterCount)
```

   Also update the spec's `It(...)` description string so it names the new count, e.g. `"maintains counter-call parity with skip-site log lines (AC#6 invariant, raised to 10 by the spec-009 convergence halt)"`.

## C. Compile fix in the existing internal test — `pkg/scanner/vault_scanner_internal_test.go`

7. The single existing direct call to `injectAndStore` (inside `Describe("injectAndStore", ...)`, the spec titled `"increments inject_task_identifier_failed counter when InjectTaskIdentifier returns error"`) needs the new fifth argument. Change:

```go
			task, written, werr := v.injectAndStore(
				context.Background(),
				[]byte("no frontmatter at all"),
				"rel.md",
				"",
			)
```

   to:

```go
			task, written, werr := v.injectAndStore(
				context.Background(),
				[]byte("no frontmatter at all"),
				"rel.md",
				"",
				[32]byte{},
			)
```

   In the same spec, add `hashes: make(map[string]fileEntry),` to the `&vaultScanner{...}` literal. That scanner currently leaves `hashes` nil; the InjectTaskIdentifier-failure path never writes to it, but the halt path does, and a nil map assignment panics.

   These two edits are the ONLY changes permitted in `vault_scanner_internal_test.go`. Do NOT touch the `ac2Fixtures` table, its rows 10-12, or any `Expect(writeCount).To(Equal(1))` assertion in the spec-008 `Describe` block.

## D. New test file — `pkg/scanner/vault_scanner_convergence_internal_test.go`

8. Create `pkg/scanner/vault_scanner_convergence_internal_test.go` in package `scanner` (internal, so it can reuse `captureGlogWarnings`, `countLinesMatching`, `vaultScanner`, `fileOps`, `fileEntry`, and `extractFrontmatter`). Start it with the same three-line BSD copyright header every other file in this repo carries. The git-client interface is imported as `gitclient "github.com/bborbe/agent-task-controller/pkg/gitrestclient"` — the same alias `pkg/scanner/vault_scanner.go` uses. A hand-written double is required rather than the counterfeiter mock: `mocks/` imports `scanner` for `scanner.ScanResult`, so `scanner` cannot import `mocks` (documented exception, see the note at the top of `pkg/scanner/vault_scanner_test.go`).

   Add the shared scaffolding at package level:

```go
// convergenceGitClient is a no-op gitclient.GitClient so RunCycle-driven specs can
// observe ScanResult. CommitAndPush is counted so a spec can prove a halted repair
// committed nothing as well as writing nothing.
type convergenceGitClient struct {
	path        string
	commitCount int
}

func (t *convergenceGitClient) EnsureCloned(_ context.Context) error { return nil }

func (t *convergenceGitClient) Pull(_ context.Context) error { return nil }

func (t *convergenceGitClient) Path() string { return t.path }

func (t *convergenceGitClient) CommitAndPush(_ context.Context, _ string) error {
	t.commitCount++
	return nil
}

func (t *convergenceGitClient) AtomicWriteAndCommitPush(
	_ context.Context,
	_ string,
	_ []byte,
	_ string,
) error {
	return nil
}

func (t *convergenceGitClient) AtomicWriteIfAbsentAndCommitPush(
	_ context.Context,
	_ string,
	_ []byte,
	_ string,
) error {
	return nil
}

func (t *convergenceGitClient) AtomicReadModifyWriteAndCommitPush(
	_ context.Context,
	_ string,
	_ func([]byte) ([]byte, error),
	_ string,
) error {
	return nil
}

func (t *convergenceGitClient) ListFiles(_ context.Context, _ string) ([]string, error) {
	return nil, nil
}

func (t *convergenceGitClient) ReadFile(_ context.Context, _ string) ([]byte, error) {
	return nil, nil
}

func (t *convergenceGitClient) WriteFile(_ context.Context, _ string, _ []byte) error {
	return nil
}

// convergenceHarness wires a vaultScanner over a real tmpdir with write-counting
// file ops, driven through RunCycle so ScanResult is directly observable.
type convergenceHarness struct {
	dir        string
	scanner    *vaultScanner
	git        *convergenceGitClient
	writeCount int
	results    chan ScanResult
}

func newConvergenceHarness(dir string, autoInject bool) *convergenceHarness {
	h := &convergenceHarness{
		dir:     dir,
		git:     &convergenceGitClient{path: dir},
		results: make(chan ScanResult, 1),
	}
	h.scanner = &vaultScanner{
		gitClient: h.git,
		taskDir:   ".",
		hashes:    make(map[string]fileEntry),
		metrics:   metrics.New(),
		ops: fileOps{
			listFiles: func(_ context.Context, glob string) ([]string, error) {
				matches, err := filepath.Glob(filepath.Join(dir, glob))
				if err != nil {
					return nil, err
				}
				rel := make([]string, 0, len(matches))
				for _, m := range matches {
					r, relErr := filepath.Rel(dir, m)
					if relErr != nil {
						continue
					}
					rel = append(rel, r)
				}
				return rel, nil
			},
			readFile: func(_ context.Context, p string) ([]byte, error) {
				return os.ReadFile(filepath.Join(dir, p)) // #nosec G304 -- test-only path
			},
			writeFile: func(_ context.Context, p string, content []byte) error {
				h.writeCount++
				return os.WriteFile(filepath.Join(dir, p), content, 0600)
			},
		},
		autoInject: autoInject,
	}
	return h
}

// runCycles runs n RunCycle calls, draining the buffered results channel after each
// so the scanner's non-blocking send never drops a result, and returns every
// ScanResult in order.
func (h *convergenceHarness) runCycles(ctx context.Context, n int) []ScanResult {
	out := make([]ScanResult, 0, n)
	for i := 0; i < n; i++ {
		h.scanner.RunCycle(ctx, h.results)
		var r ScanResult
		Expect(h.results).To(Receive(&r))
		out = append(out, r)
	}
	return out
}

// skipCounterValue reads the current value of the skipped-files counter for reason
// from the default registry.
func skipCounterValue(reason metrics.SkipReason) float64 {
	mfs, err := prometheus.DefaultGatherer.Gather()
	Expect(err).NotTo(HaveOccurred())
	for _, mf := range mfs {
		if mf.GetName() != "agent_controller_vault_scanner_skipped_files_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, lp := range m.GetLabel() {
				if lp.GetName() == "reason" && lp.GetValue() == reason.String() {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}

// convergenceKeyLineRe counts surviving task_identifier key lines in a repaired
// file, including the quoted and spaced key spellings. Declared here rather than
// reused from production so the assertion does not move if the production regex does.
var convergenceKeyLineRe = regexp.MustCompile(
	`^[[:space:]]*['"]?task_identifier['"]?[[:space:]]*:`,
)

// haltLogAnywhereRe is the greppable halt substring, used for the negative-evidence
// specs where the expected count is zero.
var haltLogAnywhereRe = regexp.MustCompile(`task_identifier repair did not converge`)

// haltLogFor builds the ERROR-level, path-specific halt log matcher. glog prefixes
// error lines with E, so anchoring on ^E also proves the severity.
func haltLogFor(relPath string) *regexp.Regexp {
	return regexp.MustCompile(
		`^E.*task_identifier repair did not converge, halting repair for: ` +
			regexp.QuoteMeta(relPath) + `$`,
	)
}
```

9. Declare the two non-converging fixtures at package level in the same file. Both were verified by execution against the shipped `v0.7.4` pipeline (`removeTaskIdentifier` -> `InjectTaskIdentifier` -> `extractFrontmatter` -> `DeduplicateFrontmatter` -> `yaml.Unmarshal`); neither uses a test double or a fault-injection seam.

```go
// convergenceFixtureA — escaped-underscore key spelling. yaml.v3 resolves the
// double-quoted escape to the key task_identifier, so processFile routes the file
// into the present-but-invalid repair branch, but taskIdentifierKeyLine matches only
// the literal spellings, so removeTaskIdentifier is a no-op. Injection then prepends
// a fresh UUID and DeduplicateFrontmatter's last-wins resolves task_identifier back
// to the integer 501 — the original accumulator loop, at flat file size, reachable in
// v0.7.4. Widening the removal regex to chase this spelling is explicitly NOT this
// spec's job (spec Constraints); the guard bounds it instead.
const convergenceFixtureA = "---\n\"task\\u005fidentifier\": 501\nstatus: in_progress\n---\nbody\n"

// convergenceFixtureB — flow-mapping frontmatter. removeTaskIdentifier does not match
// a line beginning "{", and the injected line makes the candidate frontmatter
// unparseable: DeduplicateFrontmatter fails with "could not find expected ':'".
// Non-convergence via the fail-closed parse path.
const convergenceFixtureB = "---\n{task_identifier: 501, status: in_progress}\n---\nbody\n"
```

   The Go escaping is load-bearing: `"task\\u005fidentifier"` in Go source produces the on-disk text `"task_identifier"`. Do not "simplify" it to `_`, which Go would resolve at compile time into a literal underscore and turn the fixture into a converging file.

10. Add a Ginkgo `Describe("task_identifier repair convergence guard (spec 009)", ...)` with the specs below. Isolation is per FIXTURE, not per spec: for every fixture — including each row of a multi-fixture loop — create a fresh `os.MkdirTemp` directory AND a fresh `newConvergenceHarness` INSIDE the loop body, so `writeCount`, `commitCount` and hash bookkeeping never accumulate across rows and each `RunCycle` sees exactly one file. This mirrors the spec-008 `ac2Fixtures` loop in `pkg/scanner/vault_scanner_internal_test.go` (~line 626), which rebuilds `writeCount` and the scanner per row for the same reason. Read the `repair_not_converging` counter immediately before that fixture's cycles and assert on the delta — never on an absolute value, because the whole package shares one default registry. NEVER loosen a per-fixture `Equal(1)` / `Equal(0)` to `BeNumerically(">=", ...)` to make a shared-harness leak pass; fix the isolation instead.

    **AC2 — the guard refuses a non-converging repair.** One spec iterating both fixtures (`ac2-a-escaped-key.md` with `convergenceFixtureA`, `ac2-b-flow-map.md` with `convergenceFixtureB`), `autoInject=true`, five `RunCycle` calls each, with the cycles wrapped in `captureGlogWarnings`. Assert per fixture:
    - `h.writeCount` equals `0`
    - `h.git.commitCount` equals `0`
    - the on-disk file read back equals the fixture string exactly (`Equal(fx.content)`, not a substring or a parse)
    - `countLinesMatching([]byte(captured), haltLogFor(fx.relPath))` equals `1` across all five cycles
    - the `repair_not_converging` counter delta equals `1` across all five cycles
    - every returned `ScanResult.Changed` is empty

    Put no Gomega assertion inside the `captureGlogWarnings` closure — it redirects `os.Stderr`, and a failure raised inside it produces unreadable output. Run the cycles inside, assert outside.

    **AC3 — the halt self-clears when the file content changes.** Continue from AC2's halted state for fixture A: run five cycles, confirm `h.writeCount` is `0`, then read the counter, then overwrite the file on disk with the repairable shape `"---\ntask_identifier: 501\nstatus: in_progress\n---\nbody\n"`, then run exactly one more cycle inside a fresh `captureGlogWarnings`. Assert:
    - `h.writeCount` equals `1` (delta of exactly one write)
    - the resulting file has exactly one surviving key line (`countLinesMatching(final, convergenceKeyLineRe)` equals `1`) and its `task_identifier`, parsed via `extractFrontmatter` + `yaml.Unmarshal`, is a `string` that `uuid.Parse` accepts Use the comma-ok form for every type assertion in the new test file — `idStr, isString := fmMap["task_identifier"].(string)`, never a bare `.(string)`. The repo enables `forcetypeassert` with `run.tests: true` and no `_test.go` exclusion; a single-value assertion fails `make check`.
    - the `repair_not_converging` counter delta over this last cycle is exactly `0`
    - `countLinesMatching([]byte(captured), haltLogAnywhereRe)` for this last cycle equals `0`

    Add a comment in this spec stating that the re-arm is content-keyed: no time-based, cycle-count-based, or process-global flag participates in the decision.

    **AC4 — a halted file never emits an empty identifier on delete.** Continue from AC2's halted state for fixture A, `os.Remove` the file, run exactly one more cycle. Assert `results[0].Deleted` satisfies `NotTo(ContainElement(lib.TaskIdentifier("")))` and that `h.writeCount` is still `0`.

    **AC5 — negative evidence, three specs, five cycles each.**
    - (i) healthy fixture `"---\ntask_identifier: 3f2b1c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d\nstatus: in_progress\nassignee: claude\n---\nbody\n"`, `autoInject=true`: `h.writeCount` equals `0`, `repair_not_converging` delta equals `0`, halt-log count equals `0`.
    - (ii) `convergenceFixtureA` with `autoInject=false`: `h.writeCount` equals `0`, `repair_not_converging` delta equals `0`, halt-log count equals `0`, and the `auto_inject_disabled` counter delta equals `5`. Comment why it is `5` and not `1`: the auto-inject gate returns before any hash entry is stored, so `processFile`'s content-hash short-circuit never engages and every cycle re-skips.
    - (iii) key-absent fixture `"---\nstatus: in_progress\nassignee: claude\n---\nbody\n"`, `autoInject=true`: `h.writeCount` equals `1`, `repair_not_converging` delta equals `0`, halt-log count equals `0`, and the final file has exactly one key line whose value parses as a UUID.

    **AC6 — the guard stays silent on the spec-008 block-style shapes.** One spec iterating three fixtures whose frontmatter strings are re-declared literally here (they are NOT imported from, and must not be moved out of, the spec-008 `ac2Fixtures` table):
    - `"task_identifier:\n  - a\n  - b"` as `ac6-10-block-seq.md`
    - `"task_identifier:\n  a: b"` as `ac6-11-block-map.md`
    - `"task_identifier: |\n  abc"` as `ac6-12-block-scalar.md`

    Wrap each into `"---\n" + frontmatter + "\nstatus: in_progress\nassignee: claude\n---\nbody\n"`, run five cycles with `autoInject=true`, and assert per fixture: `h.writeCount` equals `1` (span-aware removal still converges them in exactly one write), `repair_not_converging` delta equals `0`, halt-log count equals `0`, and the final file has exactly one key line whose value parses as a UUID.

11. Do not use `PIt`, `XIt`, `Pending`, `Skip(`, or `Focus`/`FIt` anywhere in the new or edited test files — spec AC6 requires the scanner suite to run with zero skipped and zero pending specs.

12. Keep each `It` closure under 80 lines so `funlen` stays green. If a spec grows past that, lift its per-fixture body into a package-level helper function in the same file rather than adding a `//nolint` directive.

## E. Changelog — `CHANGELOG.md`

13. Insert a new `## Unreleased` section directly above the existing `## v0.7.4` heading, containing exactly one bullet. Do not create a second `## Unreleased` section and do not modify any existing version section or bullet. The bullet must contain the word `convergence` (spec AC8 greps for it):

```
## Unreleased

- feat: refuse any `task_identifier` repair write that would not clear its own trigger — before persisting, the vault scanner re-evaluates the candidate bytes through the exact read-path pipeline the next cycle uses (frontmatter extraction, last-wins deduplication, YAML unmarshal) and requires the resolved `task_identifier` to be a string equal to the freshly minted UUID. A repair failing this convergence check writes nothing, commits nothing, logs one ERROR line `task_identifier repair did not converge, halting repair for: <path>`, and increments `agent_controller_vault_scanner_skipped_files_total{reason="repair_not_converging"}`; candidate bytes that cannot be parsed are treated as non-converging (fail-closed). The halt is keyed on the file's on-disk content hash, so it re-arms exactly once per distinct file state and clears itself as soon as any writer changes the file, and halt bookkeeping never leaks an empty task identifier into the deleted-tasks stream. The guard is unconditional and root-cause-agnostic: it bounds any non-converging repair — including causes nobody has enumerated — to one log line and one counter increment instead of an unbounded rewrite loop against the shared vault repository (spec 009)
```

</requirements>

<constraints>

- Do NOT commit — dark-factory handles git.
- Do NOT make the guard configurable, opt-outable, or threshold-tunable. No env var, no config field, no flag, no `if enabled` branch, no "max N attempts" tuning knob. An escape hatch on a termination guard is the regression the guard exists to prevent. (Spec Non-goals.)
- Do NOT add a per-file write-rate circuit breaker, a retry counter, a time-based backoff, or a process-global "already halted" flag. The halt is keyed on the file's on-disk content hash and nothing else. (Spec Non-goals and Design Decisions.)
- Do NOT introduce a new Prometheus counter, metric, or label set. The `repair_not_converging` reason added by prompt 1 is the whole observability surface. (Spec Non-goals.)
- Do NOT widen `taskIdentifierKeyLine`, rewrite `removeTaskIdentifier` to resolve keys by parsing, or otherwise "fix" fixture A's escaped-underscore spelling. Closing that door at the source is explicitly a separate spec; this spec's entire argument is that spelling-chasing does not terminate. Fixture A must stay a live non-converging input.
- Do NOT change `DeduplicateFrontmatter`'s last-wins semantics — other callers depend on it, and the guard's correctness depends on modelling it faithfully rather than changing it.
- Do NOT change `AUTO_INJECT_TASK_IDENTIFIER` semantics. When `autoInject` is `false` no repair is attempted, so the guard must not fire, and the frozen substring `AUTO_INJECT_TASK_IDENTIFIER=false; skipping task without valid task_identifier:` is unchanged.
- Do NOT change the frozen `processFile` return shapes: `(nil, "", false)` for a skip (a halt returns this) and `(nil, relPath, false)` for a write. `scanFiles` must need no changes.
- Do NOT change or remove the content-hash short-circuit at the top of `processFile`, and do NOT change the zero-hash sentinel (`hash: [32]byte{}`) that a successful write stores. The halt stores the file's real on-disk hash instead; that difference is the whole mechanism.
- Do NOT publish a task whose repair was halted — the halt path returns no `*lib.Task`, same as every other skip.
- Do NOT interpolate the offending `task_identifier` value into any log line. The path is the only thing logged; a large sequence or mapping must not be usable to flood the log. (Spec Security / Abuse Cases.)
- Do NOT modify the spec-008 `ac2Fixtures` table in `pkg/scanner/vault_scanner_internal_test.go`, its rows 10-12, or any of their `Expect(writeCount).To(Equal(1))` assertions. Under spec 008's span-aware removal those three shapes converge in one write and the guard must stay silent on them; this prompt only proves that with a NEW independent spec. (Spec AC6.)
- The only permitted edits to `pkg/scanner/vault_scanner_internal_test.go` are the two in requirement 7 (fifth argument, and initialising `hashes`).
- Do NOT add commit-rate anomaly alerting on the vault repo and do NOT reinstate the removed `AgentControllerWritebackFailing` alert. (Spec Non-goals.)
- Do NOT try to identify what originally wrote `task_identifier: 501`, and do NOT change `pkg/command/task_increment_frontmatter_executor.go`. That lead is explicitly UNVERIFIED and out of scope; the guard must hold regardless of origin. (Spec Non-goals.)
- **Scenario coverage: NO new scenario.** Do NOT create or edit anything under `scenarios/`. The spec states the guard has no cluster-observable behavior distinct from the log and counter that the in-repo harness already captures.
- The landing mechanism is a manually opened PR to `master` (`.dark-factory.yaml` is `workflow: direct`, `pr: false`, `autoMerge: false`) — do not attempt any git, `gh`, or PR steps in this container.
- Do NOT run bare `git` commands — the container has no working `.git`, and a failed git command would produce a false-positive verification pass.
- Do NOT run `kubectl*`, `docker`, `make buca`, `make build`, or any operator/deploy command. Spec 009 has no operator-executable verification rung.
- Existing tests must still pass. `make precommit` runs `make format` (goimports-reviser plus `golines --max-len=100 -w`) over every non-vendor `.go` file on every run, green or not — files being reformatted in place is expected, not a failure.

</constraints>

<verification>

Fast loop while implementing:

```
go build ./...
go test -mod=mod ./pkg/scanner/...
```

AC2 evidence — the frozen ERROR substring is present at the guard site:

```
grep -n 'task_identifier repair did not converge, halting repair for:' pkg/scanner/vault_scanner.go
```

Expect exactly one line, inside `injectAndStore`, on a `glog.Errorf` call.

Parity invariant — the guard added one log site and one counter site, so both counts are `10`:

```
awk '/^func \(v \*vaultScanner\) (processFile|injectAndStore)\(/,/^}/' pkg/scanner/vault_scanner.go | grep -c 'SkippedFilesTotal('
```

Expect `10`. NEVER make the parity spec green by deleting an assertion, lowering a count, or moving the counter call out of `injectAndStore` — if this number is not `10`, the guard site is wrong, not the test.

AC2-AC6 evidence — the new specs run and pass:

```
go test -mod=mod -count=1 -v ./pkg/scanner/... ./pkg/metrics/... 2>&1 | grep -E 'SUCCESS!|Skipped|Pending|FAIL'
```

Expect the Ginkgo summary to report `SUCCESS`, `0 Skipped`, and `0 Pending`.

AC6 evidence — no skipped or pending specs were introduced:

```
grep -c 'PIt(\|XIt(\|FIt(\|Skip(\|Pending' pkg/scanner/vault_scanner_convergence_internal_test.go || true
```

Expect `0`.

AC8 evidence — the changelog entry exists and sits above every released section:

```
grep -n 'convergence' CHANGELOG.md
awk '/^## v/{exit} /convergence/{f=1} END{exit !f}' CHANGELOG.md
```

Expect the `grep` to print a line inside the `## Unreleased` block, and the `awk` to exit `0` (it proves the `convergence` line appears before the first `## v` heading; it exits `1` today). This is the non-git equivalent of the spec's `git diff origin/master -- CHANGELOG.md` check — the container has no working `.git`.

Run ONCE at the end:

```
make precommit
```

Expect exit 0 with the full Ginkgo suite green, including the spec-008 specs whose rows 10-12 must still pass unmodified at write count `1`. If it fails, iterate on the specific failing target (`make test`, `make check`) rather than re-running the whole chain, then re-run `make precommit` once the individual targets pass.

</verification>

<!--
NOTES FOR THE HUMAN REVIEWER — open questions resolved from the spec alone, non-blocking:

1. Spec AC6(a) cites `git diff pkg/scanner/vault_scanner_test.go` for "rows 10-12". Those rows actually
   live in the `ac2Fixtures` table in `pkg/scanner/vault_scanner_internal_test.go` (lines 545-547), not in
   `vault_scanner_test.go`. Resolved in favour of the source: the no-change requirement is applied to the
   internal test file, where the rows really are.

2. Spec AC5 row (ii) says "the AC2 block-style fixture with autoInject=false" — stale wording predating the
   fixture substitution documented in the same spec's Constraints. Resolved: row (ii) uses AC2 Fixture A
   (escaped-underscore key), the fixture that actually reaches the repair path.

3. Spec Constraints name `result.ExtractFrontmatter` in the verification transcript. The scanner's read path
   actually uses the package-local `extractFrontmatter` in `pkg/scanner/frontmatter.go`. Resolved: the guard
   reuses the scanner's own pipeline, which is the one the next cycle genuinely runs. Verified by execution
   on 2026-09-05 — fixture A resolves to int 501 (isString=false), fixture B fails dedup with
   "could not find expected ':'", and rows 10-12 plus the `task_identifier: 501` baseline all converge.

4. The spec did not anticipate the parity spec in `vault_scanner_test.go` that pins skip-site log lines and
   counter calls at 9 each. The guard adds exactly one of each, so both must be raised to 10 (requirement 6).
   Prompt 015 of spec 008 froze "never weaken the Equal(9) assertions" — this is a parity-preserving raise,
   not a weakening, and the invariant (one counter per skip log) is unchanged.

5. Requirement A.4 (updating the `//nolint` bookkeeping comment) and B.6 are drift maintenance the spec did
   not enumerate but which follow directly from AC6(c) requiring `go test ./pkg/scanner/...` to exit 0.
-->
