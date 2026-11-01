---
status: approved
spec: [020-open-task-census]
created: "2026-11-01T14:20:00Z"
queued: "2026-11-01T13:28:07Z"
branch: dark-factory/open-task-census
---

# Compute the open-task census and clear the pairs that vanished

<summary>

- The scanner gains a read-only pass that walks the task directory once and counts open records per `(phase, task_type)` pair.
- A record is open unless its status is exactly `completed` or `aborted`; a missing status, an unknown status, and a `human_review` or `done` status all count.
- A pair value that is missing or not a string is published as an empty label value — the record is still counted, never dropped and never normalised.
- A file that cannot be read, has no frontmatter, or will not deduplicate or unmarshal is skipped silently and counted in no pair.
- Duplicate frontmatter keys that deduplicate cleanly are counted once, with the last value winning.
- Counts are a level: each pair's series is set to its count, and nothing accumulates across passes.
- Counts are published only after the whole listing, so a pass abandoned mid-way writes nothing at all.
- Pairs that were published before and are absent now are removed from the exposition entirely, and the removal is scoped to this controller's own vault value — never a blanket reset.
- Unit specs prove every rule above, including that two vault names publish under their own label.

</summary>

<objective>

Add the census computation and its clearing rule to the scanner: an unexported recompute that lists the task directory, classifies every file, counts open records per `(phase, task_type)`, `Set`s each present pair through the metrics accessor after the whole loop, and `DeleteLabelValues`-removes the pairs the previous pass published and this one did not — scoped to the controller's own `vault` value — with a focused internal spec for each Acceptance Criterion from 4 to 14 (spec 020, Desired Behavior 2, 3 and 4).

</objective>

<context>

This repo has no root `CLAUDE.md`; the global YOLO container `CLAUDE.md` (already in your context) governs project conventions.

Read the spec: `specs/in-progress/020-open-task-census.md` — Desired Behavior 2, 3 and 4, Acceptance Criteria 4 through 14, Constraints ("Frozen open definition", "Frozen clearing mechanism", "Frozen label value source"), Failure Modes rows 2, 3 and 4, and the "Suggested Decomposition" table (this prompt is row 2).

DEPENDENCY — prompt 1 must have shipped before this prompt runs, because the computation writes through the metrics accessor it added. Verify first:

```bash
grep -n 'OpenTasks() \*prometheus.GaugeVec' pkg/metrics/metrics.go
grep -n 'func (m \*defaultMetrics) OpenTasks()' pkg/metrics/metrics.go
```

Expect both lines. If either returns nothing, STOP and report `status: failed` with message `"open-task metrics accessor not yet deployed (prompt 1)"` — do not write a computation that cannot publish.

Read these files before writing anything:

- `pkg/scanner/task_age.go` — the sibling pass this one must mirror in shape. Note exactly how it is built:
  - `recomputeTaskAgeMetrics(ctx context.Context)` lists `v.taskDir + "/*.md"` through `v.ops.listFiles` and returns immediately on a listing error, logging a warning and writing nothing;
  - `publishTaskAgeMetrics(ctx, paths, now, vaultName)` loops with a non-blocking `select { case <-ctx.Done(): return; default: }` at the top of every iteration, accumulates into a `map[...]int`, and `Set`s the values only after the loop;
  - `taskAgeFromFile(ctx, relPath, now)` reads the file through `v.ops.readFile`, calls `extractFrontmatter`, then `DeduplicateFrontmatter`, then `yaml.Unmarshal` into `map[string]interface{}`, then reads fields with `stringField`, and returns a `(value, state)` pair where the state tells the caller to skip or count.
- `pkg/scanner/frontmatter.go` — the exact signatures: `extractFrontmatter(ctx context.Context, content []byte) (string, error)` and `DeduplicateFrontmatter(ctx context.Context, fmYAML string) (string, bool, error)`.
- `pkg/scanner/stuck_planning.go` — `func stringField(fmMap map[string]interface{}, key string) string`, which returns `""` for a missing or non-string value. Use it for `status`, `phase` and `task_type`; do not write a second field reader.
- `pkg/scanner/vault_scanner.go` — the `vaultScanner` struct (fields `gitClient`, `taskDir`, `pollInterval`, `hashes`, `trigger`, `metrics`, `ops`, `autoInject`, `vaultName`, `metricsInterval`, `indexMutex`, `index`) and the `fileOps` struct (`listFiles`, `readFile`, `writeFile`). The census reads only `taskDir`, `ops`, `metrics` and `vaultName`.
- `pkg/scanner/task_age_internal_test.go` — the internal-spec style: a `taskAgeSummaryScanner(files map[string]string, unreadable map[string]struct{}) *vaultScanner` helper that builds a `vaultScanner` literal over a `fileOps` closure, plus small record builders and value readers. Mirror that shape.
- `pkg/scanner/scanner_suite_test.go` — the suite entry point. Note that internal spec files declare `package scanner` while the suite file declares `package scanner_test`; both compile into one test binary, which is how the existing internal specs run. Keep `package scanner`.
- `pkg/metrics/metrics.go` — the `OpenTasks() *prometheus.GaugeVec` accessor added by prompt 1.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo/Gomega suite files, coverage expectations, error-path testing.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-context-cancellation-in-loops.md` — the non-blocking `select` check at the top of every loop iteration.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — this pass logs with `glog.Warningf` and returns no error; it never uses `fmt.Errorf`.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-precommit.md` — `funlen` 80 lines / 50 statements, `nestif` 4, `golines --max-len=100`, license headers.

Environment facts that shape this prompt:

- The execution container's `.git` is masked (the daemon runs with `hideGit=true`; `git status` fails with `fatal: not a git repository`). Do NOT run any `git` command anywhere in this prompt — a failed `git` command would read as a pass.
- Do NOT run `kubectl*`, `docker`, `make build`, `make buca`, `gh`, or any deploy command.
- Do NOT commit. dark-factory owns git.

</context>

<requirements>

All production code is in a new file `pkg/scanner/open_task_census.go`, plus one struct-field addition in `pkg/scanner/vault_scanner.go`. All specs are in a new file `pkg/scanner/open_task_census_internal_test.go` with `package scanner`.

1. **Add the previous-pair set to the scanner struct.** In `pkg/scanner/vault_scanner.go`, add one field to `type vaultScanner struct`, immediately after `metricsInterval time.Duration`:

   ```go
   	// openTaskPairs is the set of (phase, task_type) pairs the previous census pass
   	// published, so the next pass can delete the ones that vanished. It is a nil map
   	// — a valid empty set — until the first successful pass, which is why neither
   	// constructor initialises it. Single-goroutine: the census runs inline in Run's
   	// goroutine and never overlaps itself.
   	openTaskPairs map[openTaskPair]struct{}
   ```

   Do NOT modify `NewVaultScanner` or `NewGitRestVaultScanner`: a nil map reads as an empty set, and requirement 4 allocates the new map after every completed pass.

2. **Create `pkg/scanner/open_task_census.go`** with the four-line BSD license header copied verbatim from `pkg/scanner/task_age.go`, `package scanner`, and the following content. Imports: `context`, `github.com/golang/glog`, `gopkg.in/yaml.v3`, and `github.com/bborbe/agent-task-controller/pkg/metrics` is NOT needed here (the accessor returns the vector directly) — import exactly what the code below uses: `context`, `github.com/golang/glog`, `gopkg.in/yaml.v3`.

   ```go
   // openTaskPair is the label triple, minus the vault, one census sample counts.
   // The field order matches the declaration order of the gauge's labels after
   // "vault", which is the argument order of WithLabelValues and DeleteLabelValues.
   type openTaskPair struct {
   	phase    string
   	taskType string
   }

   // recomputeOpenTaskCensus lists the task directory once and publishes the
   // open-task census from it. It never pulls, scans, writes, commits or sends on
   // the results channel: it is a read-only consumer of the files the scan cycle
   // already reads.
   //
   // A failed listing writes nothing at all. The values from the previous successful
   // pass stay in place and nothing is Set to 0, because 0 would claim a clean board;
   // on a pod's first failed pass nothing has been published yet, so the series are
   // absent.
   func (v *vaultScanner) recomputeOpenTaskCensus(ctx context.Context) {
   	glob := v.taskDir + "/*.md"
   	paths, err := v.ops.listFiles(ctx, glob)
   	if err != nil {
   		glog.Warningf("open task census: list %s failed: %v", glob, err)
   		return
   	}
   	v.publishOpenTaskCensus(ctx, paths)
   }

   // publishOpenTaskCensus counts the open records among paths and publishes the
   // census. Counts are accumulated across the whole listing and written only after
   // the loop, so a pass abandoned mid-way publishes nothing rather than a partial
   // board — and the previous pass's values stay untouched.
   func (v *vaultScanner) publishOpenTaskCensus(ctx context.Context, paths []string) {
   	counts := make(map[openTaskPair]int, len(v.openTaskPairs))
   	for _, relPath := range paths {
   		select {
   		case <-ctx.Done():
   			return
   		default:
   		}
   		pair, open := v.openTaskPairFromFile(ctx, relPath)
   		if !open {
   			continue
   		}
   		counts[pair]++
   	}
   	census := v.metrics.OpenTasks()
   	for pair, count := range counts {
   		census.WithLabelValues(v.vaultName, pair.phase, pair.taskType).Set(float64(count))
   	}
   	// Delete, never Reset: three controllers run from one image, so a blanket reset
   	// would clear another vault's series. DeleteLabelValues is scoped to this
   	// controller's own vault value, and the three values are passed in the gauge's
   	// declaration order (vault, phase, task_type).
   	for pair := range v.openTaskPairs {
   		if _, present := counts[pair]; present {
   			continue
   		}
   		census.DeleteLabelValues(v.vaultName, pair.phase, pair.taskType)
   	}
   	v.openTaskPairs = make(map[openTaskPair]struct{}, len(counts))
   	for pair := range counts {
   		v.openTaskPairs[pair] = struct{}{}
   	}
   }

   // openTaskPairFromFile classifies one task file. It returns the record's
   // (phase, task_type) pair and true when the record is open, and the zero pair and
   // false when the record is terminal or the file must be skipped.
   //
   // Only "completed" and "aborted" are terminal. Every other status — including a
   // missing key, an unknown value, human_review and done — is open, because the
   // resting-phase exclusion belongs to the alert expression, not the instrument.
   func (v *vaultScanner) openTaskPairFromFile(
   	ctx context.Context,
   	relPath string,
   ) (openTaskPair, bool) {
   	content, err := v.ops.readFile(ctx, relPath)
   	if err != nil {
   		return openTaskPair{}, false
   	}
   	fmYAML, err := extractFrontmatter(ctx, content)
   	if err != nil {
   		return openTaskPair{}, false
   	}
   	deduped, _, err := DeduplicateFrontmatter(ctx, fmYAML)
   	if err != nil {
   		return openTaskPair{}, false
   	}
   	var fmMap map[string]interface{}
   	if err := yaml.Unmarshal([]byte(deduped), &fmMap); err != nil {
   		return openTaskPair{}, false
   	}
   	status := stringField(fmMap, "status")
   	if status == "completed" || status == "aborted" {
   		return openTaskPair{}, false
   	}
   	// A missing or non-string phase/task_type contributes the empty string. No
   	// normalisation, no sentinel and no filtering: the record is still counted.
   	return openTaskPair{
   		phase:    stringField(fmMap, "phase"),
   		taskType: stringField(fmMap, "task_type"),
   	}, true
   }
   ```

   Do not add a ticker, a goroutine, a context timeout, an error return, a retry, or any configuration read to these functions.

3. **Do not wire the census into `Run`, `RunCycle`, `scanFiles` or `buildIndex` in this prompt.** Prompt 3 owns the wiring. The unexported method is kept reachable by the specs in requirement 4, which is why `unused` does not flag it (`run.tests: true` in `.golangci.yml`).

4. **Create `pkg/scanner/open_task_census_internal_test.go`** with `package scanner`, the BSD license header, and the following helpers plus one `It` per Acceptance Criterion. The file comment must state why it is an internal spec: the census is unexported and the specs read the package gauges through `prometheus.DefaultGatherer`.

   Helpers — use these exact names, because prompt 3 reuses them. The AC 4-14 specs pass `nil` as the third argument; only the requirement-5 broken-file spec passes an `unreadable` set. (Prompt 3 refers to these helpers by name only and calls none of them, so the third parameter is safe to add.)

   ```go
   // openTaskCensusScanner builds a scanner over an in-memory file set. listFiles
   // iterates the maps live, so a spec can delete a key between passes. A path in
   // unreadable is listed but fails on read, exactly like the sibling
   // taskAgeSummaryScanner; pass nil when every listed file is readable.
   func openTaskCensusScanner(
   	vaultName string,
   	files map[string]string,
   	unreadable map[string]struct{},
   ) *vaultScanner {
   	return &vaultScanner{
   		metrics:   metrics.New(),
   		vaultName: vaultName,
   		ops: fileOps{
   			listFiles: func(_ context.Context, _ string) ([]string, error) {
   				paths := make([]string, 0, len(files)+len(unreadable))
   				for relPath := range files {
   					paths = append(paths, relPath)
   				}
   				for relPath := range unreadable {
   					paths = append(paths, relPath)
   				}
   				sort.Strings(paths)
   				return paths, nil
   			},
   			readFile: func(_ context.Context, relPath string) ([]byte, error) {
   				if _, ok := unreadable[relPath]; ok {
   					return nil, os.ErrNotExist
   				}
   				content, ok := files[relPath]
   				if !ok {
   					return nil, os.ErrNotExist
   				}
   				return []byte(content), nil
   			},
   		},
   	}
   }

   // openTaskCensusRecord builds a task file from raw frontmatter lines, so a spec
   // can omit a key or repeat one.
   func openTaskCensusRecord(lines ...string) string {
   	content := "---\n"
   	for _, line := range lines {
   		content += line + "\n"
   	}
   	return content + "---\n# Task\n"
   }

   // openTaskCensusGathered returns every gathered census sample keyed by
   // vault\x00phase\x00task_type. It gathers rather than reading the accessor,
   // because WithLabelValues on a GaugeVec CREATES the series and would make every
   // absence assertion vacuous.
   func openTaskCensusGathered() map[string]float64 {
   	mfs, err := prometheus.DefaultGatherer.Gather()
   	Expect(err).NotTo(HaveOccurred())
   	result := make(map[string]float64)
   	for _, mf := range mfs {
   		if mf.GetName() != "agent_controller_open_tasks" {
   			continue
   		}
   		for _, sample := range mf.GetMetric() {
   			labels := make(map[string]string, len(sample.GetLabel()))
   			for _, lp := range sample.GetLabel() {
   				labels[lp.GetName()] = lp.GetValue()
   			}
   			key := labels["vault"] + "\x00" + labels["phase"] + "\x00" + labels["task_type"]
   			result[key] = sample.GetGauge().GetValue()
   		}
   	}
   	return result
   }

   func openTaskCensusValue(vault, phase, taskType string) (float64, bool) {
   	value, ok := openTaskCensusGathered()[vault+"\x00"+phase+"\x00"+taskType]
   	return value, ok
   }
   ```

   Every spec uses a vault name unique to itself (for example `census-idempotent`, `census-fall-by-one`, `census-absent-on-last`, `census-empty-board`, `census-other-vault`, `census-human-review`, `census-terminal`, `census-unknown-status`, `census-empty-labels`, `census-duplicate-keys`, `census-vault-openclaw`, `census-vault-personal`). The gauge is package-level and every spec reads the same registry, so a unique `vault` value keeps an absence assertion from depending on spec order. State that reason in a comment above the `Describe`.

   The specs, one per Acceptance Criterion:

   - **AC 4 — idempotency.** Two open records at `planning`/`github-update-go`. Recompute, read the value (expect `2`), recompute again, read again, and assert the second read equals the first and is `2`. A value that accumulated would read `2` then `4`.
   - **AC 5 — fall by exactly one.** Pair P holds 2 open records and pair Q holds 1. Recompute; P reads `2`. Delete one of P's files from the map; recompute; P reads `1` and Q still reads `1`.
   - **AC 6 — absence on the last record.** Pair P and pair Q each hold 1 open record. Recompute; both read `1`. Delete P's only file; recompute; `openTaskCensusValue(vault, P.phase, P.taskType)` returns `ok == false`, and Q still reads `1`. Assert absence through the gathered map, not through `testutil.ToFloat64` — a `GaugeVec` retains every child it ever created, so only absence distinguishes a cleared pair from a stale one.
   - **AC 7 — empty board.** Recompute over a board holding records at two pairs, then over an empty map. After the second pass, no gathered sample carries this spec's vault value: filter `openTaskCensusGathered()` for keys beginning with `vault+"\x00"` and assert the filtered map is empty.
   - **AC 8 — other-vault isolation.** Seed `metrics.New().OpenTasks().WithLabelValues("census-other-vault", "planning", "x").Set(7)`. Build a scanner with `vaultName = "census-isolated"` over an empty board and recompute. Assert the `census-other-vault` sample still reads `7`. A `Reset()` implementation clears every vault and fails this spec.
   - **AC 9 — `human_review` is counted.** One open record at `phase: human_review` with `task_type: pr-review`; recompute; the pair reads `1`. This is the positive control that the resting-phase filter is not in the instrument.
   - **AC 10 — terminal exclusion.** Three records: one `completed`, one `aborted`, one `in_progress`, each at its own pair. Recompute. Only the `in_progress` pair has a sample (reading `1`); the two terminal pairs return `ok == false`.
   - **AC 11 — missing and unknown status are open.** One record with no `status` key and one with `status: bogus`, each at its own pair. Recompute; both pairs read `1`.
   - **AC 12 — empty label values.** One record with no `phase` key (it carries `task_type: github-update-go`) and one with no `task_type` key (it carries `phase: planning`). Recompute. Assert a sample exists at `(vault, "", "github-update-go")` reading `1` and a sample at `(vault, "planning", "")` reading `1`. Neither record is dropped.
   - **AC 13 — duplicate keys, last wins.** One open record whose frontmatter repeats `phase` — `phase: planning` first, `phase: execution` last — with `task_type: github-update-go`. Recompute. Assert exactly one sample exists for that record's triple and it carries `phase="execution"`; assert `(vault, "planning", "github-update-go")` is absent. `DeduplicateFrontmatter` keeps the last value, and `yaml.Unmarshal` on the deduplicated text must therefore see `execution`.
   - **AC 14 — the vault label is the constructor's name verbatim.** Build two scanners over an identical board, one with `vaultName = "census-vault-openclaw"` and one with `vaultName = "census-vault-personal"`, and recompute each. Each publishes under its own `vault` value and both values are present after the two recomputes — spec AC 14's literal "the other value is absent from `Gather`" is unsatisfiable once both scanners publish into the one registry, so the assertion is one series per name and no third name. Two distinct names are required: a single name reads identically whether the label carries the constructor's value or a hardcoded literal. Do not assert against `private-agent` — the vault-api display name must never be applied.

5. **Cover the skip-on-broken-file rule inside the AC 4-14 block.** Add one spec that puts three broken files next to one good open record — one unreadable path (passed in the helper's `unreadable` set, so `listFiles` returns it while `readFile` returns `os.ErrNotExist`), one file with no frontmatter (`"# no frontmatter\n"`), and one whose frontmatter is a scalar rather than a mapping (`"---\n- a\n- b\n---\n# Task\n"`) — and assert the good record's pair reads `1` and no sample exists for any broken file's would-be pair. One broken file must not blank the instrument.

6. **Do not change any existing spec, helper or production behaviour.** The age pass, the stuck-planning gauge, the scan cycle and the identifier index are untouched by this prompt.

</requirements>

<constraints>

- **Frozen open definition:** `status` neither `completed` nor `aborted`. Missing, unknown, `human_review` and `done` are all open — the resting-phase exclusion lives in the alert expression, not here.
- **Frozen clearing mechanism:** `DeleteLabelValues(vaultName, phase, taskType)` scoped to this controller's `vault` value. `Reset()` is forbidden — it clears every vault's series, and three controllers share one image.
- **Frozen label value source:** the `vault` label is the scanner's configured vault name, passed through verbatim. The vault-api display name (`private-agent`) is a different naming scheme and must not be applied.
- **Empty label values are published, not dropped.** A missing or non-string `phase` or `task_type` contributes `""` and the record is still counted — no normalisation, no sentinel, no filtering.
- **Counts are a level, published after the whole listing.** Nothing is incremented, accumulated or carried between passes; a cancelled pass writes nothing.
- **A failed listing writes nothing** — never `Set` to 0, which would claim a clean board.
- **No configuration surface** — no env var, flag, interval knob or enable/disable switch. `main.go` is untouched by this prompt.
- **No new goroutine, ticker or timeout.** The pass runs inline and relies on the caller's context; it checks `ctx.Done()` at the top of each iteration and performs no retries.
- **Read-only** — the census never pulls, scans, writes, commits or sends on the results channel.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.

</constraints>

<verification>

Run from the repository root:

```bash
# the new pass exists and nothing else was wired
grep -n 'recomputeOpenTaskCensus\|DeleteLabelValues\|openTaskPair' pkg/scanner/open_task_census.go
grep -rn 'recomputeOpenTaskCensus(' pkg/scanner/vault_scanner.go pkg/scanner/task_age.go pkg/scanner/stuck_planning.go   # expect no output — prompt 3 owns the wiring
grep -n 'Reset()' pkg/scanner/*.go            # expect no output

# no new configuration surface
grep -rn -Ei 'env:"[^"]*census|arg:"[^"]*census|CENSUS_INTERVAL|OPEN_TASKS_INTERVAL' --include='*.go' .   # expect no output

# the specs and the package build
go test -count=1 ./pkg/scanner/...
go build ./...
```

Expected: the first grep prints the new symbols; the `Reset()` grep and the configuration grep print nothing; both `go` commands exit 0.

Then the full gate once, at the end:

```bash
make precommit
```

`make precommit` runs `ensure format generate test check addlicense`; `check` also runs `vulncheck osv-scanner trivy`, which query external vulnerability databases — a failure there is a database or dependency finding, not evidence against this change; report it verbatim and do not edit `.trivyignore` or `VULNCHECK_IGNORE`.

If `make precommit` exits non-zero, fix the failure, re-run only the failing target (`make lint`, `make test`, …), and report `"status":"failed"` unless the final `make precommit` exits 0.

</verification>
