---
status: completed
spec: [008-bug-task-identifier-non-uuid-loop]
summary: Fixed the vault scanner task_identifier parse site so a present-but-invalid value is stripped and re-injected in exactly one write (span-aware, frontmatter-scoped, key-aware removal); verified the production code against the spec, added the spec-008 capture helper plus removeTaskIdentifier unit test and AC1-AC7 write-count/byte-exact/log/parity acceptance tests, and passed the full precommit gate.
execution_id: agent-task-controller-identifier-convergence-exec-014-spec-008-type-and-validity-fix
dark-factory-version: dev
created: "2026-09-05T10:48:59Z"
queued: "2026-09-05T11:10:07Z"
started: "2026-09-05T11:13:16Z"
completed: "2026-09-05T11:22:28Z"
branch: dark-factory/bug-task-identifier-non-uuid-loop
---

# Task_identifier type-and-validity fix at the vault scanner parse site

<summary>

- A task file whose `task_identifier` is present but is not a valid UUID string is now repaired in exactly one write, instead of being rewritten every scan cycle forever (the 2026-09-05 production incident: an unquoted `task_identifier: 501` grew one file to 3007 keys / 163 KB over 50 hours).
- The parse site now distinguishes key-absent from key-present-but-not-a-valid-UUID-string, using the map presence check and the comma-ok type assertion result separately — neither signal is discarded anymore.
- Key-absent files take the exact same raw-content path as today (byte-identical output); only files whose identifier is present but invalid are routed through the strip-and-reinject path.
- Stripping is now driven by a key-aware regex (so quoted keys, spaced colons, and the bare spelling all resolve), is span-aware (block sequences, block mappings, and block scalars are fully removed, not orphaned), and is confined to the frontmatter region (body lines beginning `task_identifier:` survive byte-identically).
- A single greppable warn log `replacing invalid task_identifier` now covers the whole present-but-invalid class and names the observed value's Go type and the file path — so an operator can tell from `kubectl logs` alone which malformation was repaired.
- The existing key-absent and duplicate-UUID auto-inject gates are preserved as two separate gates, so the source-structure parity test keeps its `Equal(9)` assertions unchanged.
- Sixteen malformed shapes (unquoted int/bool/null/float, empty and whitespace strings, flow and block sequences/mappings, block scalar, quoted non-UUID, and quoted/spaced key spellings) are each proven to converge in exactly one write across five scan cycles, with the surviving value parsing as a valid UUID.
- `AUTO_INJECT_TASK_IDENTIFIER=false` behavior, `DeduplicateFrontmatter` last-wins semantics, the zero-hash sentinel, and all healthy-file behavior are unchanged.

</summary>

<objective>

Fix the vault scanner's `task_identifier` backfill so a present-but-not-a-valid-UUID-string value (for any reason: non-string scalar, empty or whitespace string, sequence, mapping, block scalar, or non-UUID string) is stripped from the frontmatter and replaced with a fresh UUID in exactly one write, while the key-absent path stays byte-identical to today and every malformed shape leaves parseable frontmatter. Proven by the AC1-AC7 five-cycle write-count harness in the scanner's internal test package.

</objective>

<context>

There is no CLAUDE.md in this repo; the global YOLO container CLAUDE.md (already in your context) governs project conventions. Read the repo's own code as the source of truth for style.

Read the coding-plugin docs that apply to this change:
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-glog-guide.md` (unconditional `glog.Warningf`, frozen greppable substrings)
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` (Ginkgo/Gomega, internal vs external test packages, coverage ≥80% for changed code)
- `/home/node/.claude/plugins/marketplaces/coding/docs/definition-of-done.md`

Read `pkg/scanner/vault_scanner.go` IN FULL. The parse site you change is `func (v *vaultScanner) processFile(ctx context.Context, relPath string) (*lib.Task, string, bool)` (currently lines 228-341). Inside it, the current decision block is:

```go
	frontmatter := lib.TaskFrontmatter(fmMap)
	taskID, _ := fmMap["task_identifier"].(string)
	currentFMAssignee := frontmatter.Assignee()
	if taskID == "" {
		if !v.autoInject {
			glog.Warningf(
				"AUTO_INJECT_TASK_IDENTIFIER=false; skipping task without valid task_identifier: %s",
				relPath,
			)
			v.metrics.SkippedFilesTotal(metrics.ReasonAutoInjectDisabled).Inc()
			return nil, "", false
		}
		return v.injectAndStore(ctx, content, relPath, currentFMAssignee)
	}
	if !isValidUUID(taskID) {
		if !v.autoInject {
			glog.Warningf(
				"AUTO_INJECT_TASK_IDENTIFIER=false; skipping task without valid task_identifier: %s",
				relPath,
			)
			v.metrics.SkippedFilesTotal(metrics.ReasonAutoInjectDisabled).Inc()
			return nil, "", false
		}
		glog.Warningf("replacing non-UUID task_identifier %q in %s", taskID, relPath)
		return v.injectAndStore(ctx, removeTaskIdentifier(content), relPath, currentFMAssignee)
	}
```

The `//nolint:funlen,gocognit` comment sits at line 227 directly above `processFile`. The duplicate-UUID branch immediately after the block above (the `if !v.isIdentifierUnique(taskID, relPath)` branch, with `glog.Warningf("replacing duplicate task_identifier %q in %s", ...)`) is NOT changed by this spec except that it automatically benefits from the rewritten `removeTaskIdentifier`. `injectAndStore` (lines 346-369), `writeCounterReset` (lines 374-402), `isIdentifierUnique` (`pkg/scanner/task_identifier.go:22-29`), and `isValidUUID` (`pkg/scanner/task_identifier.go:16-19`, `uuid.Parse` based) are unchanged.

Read `pkg/scanner/task_identifier.go` IN FULL. The function you rewrite is `func removeTaskIdentifier(content []byte) []byte` (lines 33-45), currently a raw whole-file `strings.HasPrefix(trimmed, "task_identifier:")` line filter that right-trims `\r` only. Its only callers are the present-but-invalid and duplicate-UUID branches in `processFile`. `InjectTaskIdentifier` (lines 48-60) always prepends `task_identifier: <id>` immediately after the opening `---` (LF or CRLF) — it is unchanged and must remain so.

Read `pkg/scanner/frontmatter.go` IN FULL. `extractFrontmatter(ctx, content []byte) (string, error)` (line 54) and `extractBody(content []byte) string` (line 73) define the frontmatter region exactly: opening delimiter `---` at byte 0 (optionally `---\r\n`), closing delimiter the first following `\n---`. `DeduplicateFrontmatter` (line 16) keys on `keyNode.Value` and keeps the LAST value per key — it is frozen, do not touch it.

Read `pkg/scanner/vault_scanner_internal_test.go` IN FULL (currently 277 lines, `package scanner`). This is the ONLY internal test file and the only place `&vaultScanner{...}` literals exist. The counting harness pattern you extend lives in the `Describe("auto-inject flag gate (spec 001)", ...)` block (lines 88-277): a `fileOps` closure whose `writeFile` increments `writeCount` and calls `v.processFile(ctx, relPath)` directly (NOT `RunCycle` — `RunCycle` requires a `gitClient`, which these literals do not set; `processFile` is where the repair write happens and is the faithful realization of the spec's five-cycle harness). The local `counterValue(reason metrics.SkipReason)` closure (lines 89-105) reads `agent_controller_vault_scanner_skipped_files_total` from `prometheus.DefaultGatherer`. The file currently imports: `context`, `lib "github.com/bborbe/agent"`, ginkgo/gomega dot-imports, `prometheus`, and `metrics`.

Read the source-structure parity test `pkg/scanner/vault_scanner_test.go` lines 1063-1095 (`It("maintains counter-call parity with skip-site log lines (AC#6 invariant ...)")`). It shells out to `awk` over `/^func \(v \*vaultScanner\) (processFile|injectAndStore)\(/,/^}/` and asserts `skipCount == 9` and `counterCount == 9`. This spec KEEPS the count at 9 (the key-absent and present-but-invalid auto-inject gates are kept as two separate inlined gates, replacing the current two gates 1:1), so this test needs NO change — you verify it still passes. `pkg/scanner/vault_scanner_test.go` is `package scanner_test` (external); do not add tests there for the unexported fix, and do NOT add a counter to `fileOpsTestGitClient` (other exported tests depend on its plain pass-through behavior).

Read `CHANGELOG.md` — the changelog entry for this spec is added by prompt 2, not here.

Spec cross-references to keep straight:
- DB1 (two signals, neither discarded): presence check + comma-ok, separate branches.
- DB2 (key-absent byte-identical): raw `content` to `injectAndStore`, exact original bytes, no repair log.
- DB3 (present-but-invalid): strip the key then mint a fresh UUID, exactly one write.
- DB4 (region-scoped): body lines beginning `task_identifier:` survive (AC5).
- DB5 (one frozen log substring for the class): `replacing invalid task_identifier`, naming file path + Go type via `%T`, never the raw value.
- DB6 (healthy files invisible): valid unique UUID files are never written (AC4).
- AC1 production shape: write count exactly 1 across five cycles, one `task_identifier` key, value parses as UUID.
- AC2: the 16-row table; rows 10-12 additionally assert valid post-write frontmatter; rows 14-16 are the quoted/spaced key spellings the old prefix strip silently missed.
- AC3: key-absent byte-exact + negative log evidence + unmodified-content assertion.
- AC4: healthy file, `status: in_progress` AND `assignee: claude` both mandatory (otherwise `processFile` returns `(nil, "", false)` at the empty-status/empty-assignee sites and the publish assertion passes vacuously).
- AC5/AC5b: byte-preservation, exact string comparison — never a frontmatter-map comparison.
- AC6: captured glog contains `replacing invalid task_identifier` once per repaired file, with relPath and Go type (`int` for the unquoted int, `string` for the empty and quoted strings).
- AC7: `autoInject=false` — write count 0 per row, `reason="auto_inject_disabled"` counter increments, glog contains spec 001's frozen `AUTO_INJECT_TASK_IDENTIFIER=false; skipping task without valid task_identifier:` and NOT `replacing invalid task_identifier`.

The spec deliberately EXCLUDES the uniformly-indented frontmatter block (a `  task_identifier: 501` key with all sibling keys also indented) from AC2 — it is bounded-but-corrupt and handled by a separate spec. Do not add a test for it and do not claim convergence for it.

</context>

<requirements>

All edits are in `pkg/scanner/` unless stated otherwise. Anchor by function/branch names, not line numbers.

## 1. Rewrite `removeTaskIdentifier` in `pkg/scanner/task_identifier.go`

Replace the current `removeTaskIdentifier` (lines 33-45) with a frontmatter-region-scoped, key-aware, span-aware removal. Add the regex var and the indentation helper alongside it. The signature is unchanged: `func removeTaskIdentifier(content []byte) []byte`. The function must:

- Identify the frontmatter region: line 0 is the opening delimiter (`strings.TrimRight(lines[0], "\r") == "---"`); the closing delimiter is the first following line whose `strings.TrimRight(line, "\r") == "---"`. If there is no opening delimiter or no closing delimiter, return `content` unchanged (safety no-op; `processFile` only reaches removal after `extractFrontmatter` succeeded, so this is defensive).
- Within the frontmatter region only (strictly between opening and closing delimiters), remove EVERY line matching the key regex `^\s*['"]?task_identifier['"]?\s*:` — this regex is the spec's explicitly-sanctioned key-aware match and is exactly the regex form AC2's evidence uses, so `"task_identifier": 501`, `'task_identifier': 501`, `task_identifier : 501`, and `task_identifier: 501` all resolve. It is NOT a raw `strings.HasPrefix` match.
- For each matched key line, also remove every following frontmatter line whose leading whitespace is strictly greater than the key line's leading whitespace (the key's full indentation span — what a block sequence, block mapping, and block scalar occupy). The span terminates at the first following line at-or-less indented than the key, or at the closing delimiter.
- Preserve every kept line byte-for-byte, including its original `\r` on CRLF files, and preserve the body (lines at/after the closing delimiter) untouched.
- Remove ALL matched key lines, not just the first — a file that already accumulated N `task_identifier` keys (the incident's 3007) must have every one stripped, because `DeduplicateFrontmatter`'s last-wins keeps the trailing bad value on the next cycle.

Reference implementation (match the file's existing TAB indentation and `errors`/`strings` imports; add `regexp` to the import block):

```go
// taskIdentifierKeyLine matches any frontmatter line whose key resolves to
// task_identifier under the spellings YAML accepts: bare, double-quoted,
// single-quoted, or with whitespace before the colon.
var taskIdentifierKeyLine = regexp.MustCompile(`^\s*['"]?task_identifier['"]?\s*:`)

// removeTaskIdentifier removes every task_identifier key line from the
// frontmatter region of content, together with the full indentation span of
// each key's value (block sequences, block mappings, and block scalars), so
// injectAndStore can safely prepend a fresh value. Lines outside the frontmatter
// region — including a body line beginning task_identifier: — are preserved
// byte-for-byte.
func removeTaskIdentifier(content []byte) []byte {
	s := string(content)
	lines := strings.Split(s, "\n")
	if len(lines) == 0 || strings.TrimRight(lines[0], "\r") != "---" {
		return content
	}
	closing := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], "\r") == "---" {
			closing = i
			break
		}
	}
	if closing == -1 {
		return content
	}
	remove := make([]bool, len(lines))
	for i := 1; i < closing; i++ {
		line := strings.TrimRight(lines[i], "\r")
		if !taskIdentifierKeyLine.MatchString(line) {
			continue
		}
		remove[i] = true
		indent := leadingWhitespaceLen(line)
		for j := i + 1; j < closing && leadingWhitespaceLen(strings.TrimRight(lines[j], "\r")) > indent; j++ {
			remove[j] = true
		}
	}
	out := make([]string, 0, len(lines))
	for i, line := range lines {
		if remove[i] {
			continue
		}
		out = append(out, line)
	}
	return []byte(strings.Join(out, "\n"))
}

// leadingWhitespaceLen returns the number of leading spaces/tabs in line.
func leadingWhitespaceLen(line string) int {
	n := 0
	for n < len(line) && (line[n] == ' ' || line[n] == '\t') {
		n++
	}
	return n
}
```

## 2. Update the parse site in `processFile` (`pkg/scanner/vault_scanner.go`)

Replace the current decision block (reproduced in `<context>`) with the presence-aware version. Keep the key-absent and present-but-invalid auto-inject gates as TWO separate inlined `if !v.autoInject` blocks — do NOT fold them into one gate (folding drops the parity count to 8 and forces a change to the `Equal(9)` parity test; keeping two gates preserves the existing test unchanged, which is the intended low-risk path). New code:

```go
	frontmatter := lib.TaskFrontmatter(fmMap)
	rawTaskID, present := fmMap["task_identifier"]
	taskID, isString := rawTaskID.(string)
	currentFMAssignee := frontmatter.Assignee()
	if !present {
		if !v.autoInject {
			glog.Warningf(
				"AUTO_INJECT_TASK_IDENTIFIER=false; skipping task without valid task_identifier: %s",
				relPath,
			)
			v.metrics.SkippedFilesTotal(metrics.ReasonAutoInjectDisabled).Inc()
			return nil, "", false
		}
		return v.injectAndStore(ctx, content, relPath, currentFMAssignee)
	}
	if !isString || !isValidUUID(taskID) {
		if !v.autoInject {
			glog.Warningf(
				"AUTO_INJECT_TASK_IDENTIFIER=false; skipping task without valid task_identifier: %s",
				relPath,
			)
			v.metrics.SkippedFilesTotal(metrics.ReasonAutoInjectDisabled).Inc()
			return nil, "", false
		}
		glog.Warningf("replacing invalid task_identifier of type %T in %s", rawTaskID, relPath)
		return v.injectAndStore(ctx, removeTaskIdentifier(content), relPath, currentFMAssignee)
	}
```

Notes on the log line: the frozen greppable substring `replacing invalid task_identifier` must appear verbatim, the line must contain the file path (`%s` = `relPath`) and the observed value's Go type (`%T` = `int`, `string`, `[]interface {}`, etc.), and it must NEVER interpolate the raw value (spec Security: a large sequence/mapping must not blow up a log line). This line supersedes the old `replacing non-UUID task_identifier %q in %s` line (verified: nothing in the repo greps the old substring). The duplicate-UUID branch below (`if !v.isIdentifierUnique(taskID, relPath)`) and its `replacing duplicate task_identifier %q in %s` log are unchanged.

## 3. Update the `//nolint:funlen,gocognit` rationale above `processFile` (line 227)

The branch change grows `processFile` slightly and the gate structure is now key-absent + present-but-invalid + duplicate. Append a spec-008 note to the existing comment so the rationale stays honest, e.g. append `; +4 lines from spec-008 present-but-invalid task_identifier branch (key-absent and present-invalid auto-inject gates kept separate; parity-check awk count stays at 9).` Keep the ENTIRE `//nolint` comment on ONE line, exactly as it is today — do NOT split it across lines and do NOT try to fit it under 100 columns. Verified: line 227 is already 243 columns on a single line, and golines does not shorten comments (`--shorten-comments` is not passed by `make format`), so a long directive line is not a lint failure. Splitting a `//nolint` directive across lines detaches it from the `processFile` declaration and reintroduces the funlen/gocognit failures it suppresses.

## 4. Do NOT touch

- `DeduplicateFrontmatter` (`pkg/scanner/frontmatter.go`) — last-wins semantics are frozen.
- `injectAndStore`, `writeCounterReset`, `isIdentifierUnique`, `isValidUUID`, `InjectTaskIdentifier` — unchanged.
- Spec 001's `AUTO_INJECT_TASK_IDENTIFIER` gate semantics and the frozen substring `AUTO_INJECT_TASK_IDENTIFIER=false; skipping task without valid task_identifier:` — unchanged (AC7 greps for it).
- The `(nil, "", false)` skip shape and the `(nil, relPath, false)` write shape from `processFile`.
- The zero-hash sentinel stored by `injectAndStore` (`hash: [32]byte{}`).
- The parity test `pkg/scanner/vault_scanner_test.go` lines 1063-1095 — it stays at `Equal(9)`/`Equal(9)`; verify it passes without modification.
- `fileOpsTestGitClient` — do NOT add a write counter to it.
- `pkg/metrics/metrics.go` — no new metrics or skip reasons.

## 5. Add the glog-capture helper and new imports to `pkg/scanner/vault_scanner_internal_test.go`

Add to the import block: `"flag"`, `"io"`, `"os"`, `"path/filepath"`, `"regexp"`, `"strings"`, `"github.com/golang/glog"`, `"github.com/google/uuid"`, `"gopkg.in/yaml.v3"` (alphabetical per goimports; `gopkg.in/yaml.v3` groups with the third-party block). The file already imports `context`, `lib "github.com/bborbe/agent"`, ginkgo/gomega dot-imports, `prometheus`, and `metrics`.

Add a package-level capture helper. This is REQUIRED for the AC3/AC6/AC7 captured-glog evidence, and it must work around glog's defaults: `github.com/golang/glog@v1.2.5` defaults `-logtostderr=false` and `-stderrthreshold=ERROR`, so WARNING logs go to files, NOT stderr, unless `logtostderr` is set. The helper sets the flag, redirects `os.Stderr` to a pipe (glog's stderr sink reads `os.Stderr` at emit time and writes synchronously), runs the function, then restores:

```go
// captureGlogWarnings runs fn with glog WARNING output captured and returns the
// captured bytes. glog's -logtostderr defaults to false, so warnings otherwise
// go to files; setting the flag routes them to stderr, which we redirect to a
// pipe. A drain goroutine reads the pipe concurrently, so fn can never block on
// a full 64 KB kernel pipe buffer. Ginkgo runs specs serially, so the global
// os.Stderr redirect is safe.
func captureGlogWarnings(fn func()) string {
	Expect(flag.Set("logtostderr", "true")).To(Succeed())
	oldStderr := os.Stderr
	r, w, err := os.Pipe()
	Expect(err).NotTo(HaveOccurred())
	os.Stderr = w

	type drainResult struct {
		out []byte
		err error
	}
	// Drain concurrently and BEFORE fn runs: an unread pipe blocks the writer
	// once the 64 KB kernel buffer fills. AC7 emits roughly 12 KB today, so a
	// read-after-run helper only works by luck.
	done := make(chan drainResult, 1)
	go func() {
		b, rerr := io.ReadAll(r)
		done <- drainResult{out: b, err: rerr}
	}()

	fn()

	// Flush glog's buffers into the pipe FIRST (a flush after the read is dead
	// code), then restore os.Stderr before closing the writer so glog's
	// flushDaemon goroutine can never write into a closed pipe mid-read, then
	// close the writer so the drain goroutine sees EOF.
	glog.Flush()
	os.Stderr = oldStderr
	_ = w.Close()

	res := <-done
	Expect(res.err).NotTo(HaveOccurred())
	_ = r.Close()
	return string(res.out)
}
```

`flag.Set` is safe to call repeatedly (idempotent) and only affects the scanner test binary. Do not run any of these specs with ginkgo's parallel executor — the suite runs serially.

## 6. Direct unit test for `removeTaskIdentifier`

Add a new `Describe("removeTaskIdentifier (spec 008)", ...)` block with one `It` asserting byte-exact outputs for the removal's edge cases (the internal test package can call the unexported function directly). Assert at least these cases:

- quoted key: `"---\n\"task_identifier\": 501\nstatus: in_progress\n---\nbody\n"` -> `"---\nstatus: in_progress\n---\nbody\n"`
- space before colon: `"---\ntask_identifier : 501\nstatus: in_progress\n---\nbody\n"` -> `"---\nstatus: in_progress\n---\nbody\n"`
- block-style sequence (span): `"---\ntask_identifier:\n  - a\n  - b\nstatus: in_progress\n---\nbody\n"` -> `"---\nstatus: in_progress\n---\nbody\n"`
- block-style mapping (span): `"---\ntask_identifier:\n  a: b\nstatus: in_progress\n---\nbody\n"` -> `"---\nstatus: in_progress\n---\nbody\n"`
- block scalar (span): `"---\ntask_identifier: |\n  abc\nstatus: in_progress\n---\nbody\n"` -> `"---\nstatus: in_progress\n---\nbody\n"`
- multiple key lines all removed: `"---\ntask_identifier: 501\nstatus: in_progress\ntask_identifier: 502\n---\nbody\n"` -> `"---\nstatus: in_progress\n---\nbody\n"`
- body line at column 0 inside a fenced block preserved: for a file whose frontmatter carries a `task_identifier` key AND whose body contains a triple-backtick fenced block with `task_identifier: 501` at column 0, only the frontmatter key line is removed; the entire body — fence markers and the body's own `task_identifier: 501` line — must be returned byte-identically. In the Go fixture, write the fence markers as the standard three backticks inside a double-quoted string literal (with `\n` escapes), not a raw literal.
- CRLF: `"---\r\ntask_identifier: 501\r\nstatus: in_progress\r\n---\r\nbody\r\n"` -> `"---\r\nstatus: in_progress\r\n---\r\nbody\r\n"`
- unterminated frontmatter returns content unchanged: `"---\ntask_identifier: 501\nstatus: in_progress\n"` unchanged

Use raw string literals where the fixture contains no backtick, and double-quoted strings with `\n`/`\r` escapes for the fence and CRLF cases.

## 7. AC1 — production-shape regression, asserted by write count

Add the fixtures slice and helper (used by AC2 and AC7) at the top of the new spec-008 Describe block:

```go
var ac2Fixtures = []struct {
	label       string
	relPath     string
	frontmatter string // frontmatter line(s) between the opening and closing --- delimiters
}{
	{"row 1 - unquoted int (production incident)", "ac2-01-int.md", "task_identifier: 501"},
	{"row 2 - bool", "ac2-02-bool.md", "task_identifier: true"},
	{"row 3 - null", "ac2-03-null.md", "task_identifier: null"},
	{"row 4 - float", "ac2-04-float.md", "task_identifier: 1.5"},
	{"row 5 - empty double-quoted string", "ac2-05-empty-dq.md", `task_identifier: ""`},
	{"row 6 - empty single-quoted string", "ac2-06-empty-sq.md", `task_identifier: ''`},
	{"row 7 - whitespace-only string", "ac2-07-ws.md", `task_identifier: "   "`},
	{"row 8 - flow sequence", "ac2-08-flow-seq.md", "task_identifier: [a, b]"},
	{"row 9 - flow mapping", "ac2-09-flow-map.md", "task_identifier: {a: b}"},
	{"row 10 - block-style sequence", "ac2-10-block-seq.md", "task_identifier:\n  - a\n  - b"},
	{"row 11 - block-style mapping", "ac2-11-block-map.md", "task_identifier:\n  a: b"},
	{"row 12 - block scalar", "ac2-12-block-scalar.md", "task_identifier: |\n  abc"},
	{"row 13 - quoted non-UUID string (parity row)", "ac2-13-quoted.md", `task_identifier: "501"`},
	{"row 14 - double-quoted key", "ac2-14-dq-key.md", `"task_identifier": 501`},
	{"row 15 - space before colon", "ac2-15-spaced-colon.md", "task_identifier : 501"},
	{"row 16 - single-quoted key", "ac2-16-sq-key.md", `'task_identifier': 501`},
}

func ac2Content(frontmatter string) string {
	return "---\n" + frontmatter + "\nstatus: in_progress\nassignee: claude\n---\nbody\n"
}
```

Write AC1 as a dedicated `It` (the load-bearing assertion is the write count): fixture `ac2Content("task_identifier: 501")` under relPath `ac1.md` in a `t.TempDir`-equivalent (`os.MkdirTemp` + `defer os.RemoveAll`, matching `vault_scanner_test.go` line 195). Construct one `&vaultScanner{metrics: metrics.New(), hashes: make(map[string]fileEntry), ops: fileOps{...}, autoInject: true}` whose `readFile` closure reads `os.ReadFile(filepath.Join(tmpDir, p))` and whose `writeFile` closure increments `writeCount` and writes through to the temp dir. Call `v.processFile(ctx, "ac1.md")` FIVE times, asserting `werr` is false each time. Then assert:

- (a) `writeCount == 1` — the load-bearing assertion. Do NOT accept `>= 1` or final-state-only checks.
- (b) reading the final file back, the number of lines matching `regexp.MustCompile(`^[[:space:]]*['"]?task_identifier['"]?[[:space:]]*:`)` is exactly `1`.
- (c) the surviving value is a string that parses via `uuid.Parse`: `extractFrontmatter(ctx, finalBytes)` (unexported, callable from `package scanner`) -> `yaml.Unmarshal` -> comma-ok assertion -> `uuid.Parse`. Write it exactly in the comma-ok form:

```go
idStr, isString := fmMap["task_identifier"].(string)
Expect(isString).To(BeTrue())
_, parseErr := uuid.Parse(idStr)
Expect(parseErr).NotTo(HaveOccurred())
```

The comma-ok form with an explicit `Expect(isString).To(BeTrue())` is MANDATORY, not stylistic: `.golangci.yml` enables `forcetypeassert` under `run.tests: true` and the `_test.go` exclusion block does NOT exclude it (it excludes only revive/dupl/errcheck/unparam/gosec), so the single-value form `fmMap["task_identifier"].(string)` fails `make lint`.

## 8. AC2 — the 16-row present-but-not-a-valid-UUID-string table

One `It` iterating `ac2Fixtures` (table-driven per the spec). One `os.MkdirTemp` dir for the whole spec, one unique relPath per row (from the slice). For each row: write `ac2Content(fx.frontmatter)` to `filepath.Join(tmpDir, fx.relPath)`, declare the `writeCount` variable INSIDE the per-row loop body (never outside it, and never reuse one counter across rows) so each row's `== 1` assertion is independent of every preceding row, build a FRESH `&vaultScanner{...}` with the write-through counting harness from AC1 closing over that per-row counter, call `processFile` five times, then assert, with the row label in every failure message:

- write count exactly `1` across the five cycles
- exactly one `task_identifier` key line in the final bytes (the spec's regex form, NOT `^task_identifier:` — the plain form cannot see rows 14-16 and would let a surviving bad key pass)
- `extractFrontmatter(ctx, finalBytes)` succeeds, `yaml.Unmarshal` on it succeeds (this is the rows 10-12 "valid post-write frontmatter" evidence, asserted for every row), and the surviving value parses via `uuid.Parse` (this UUID-validity check is part of the evidence — a fix that strips the key and writes a non-UUID placeholder must fail). Use the comma-ok form per row:

```go
idStr, isString := fmMap["task_identifier"].(string)
Expect(isString).To(BeTrue(), fx.label)
_, parseErr := uuid.Parse(idStr)
Expect(parseErr).NotTo(HaveOccurred(), fx.label)
```

The comma-ok form with an explicit `Expect(isString).To(BeTrue(), ...)` is MANDATORY: `.golangci.yml` enables `forcetypeassert` under `run.tests: true` and does NOT exclude it for `_test.go` files, so the single-value form `fmMap["task_identifier"].(string)` fails `make lint`.

Because each row uses a fresh scanner and its own relPath, cycles 2-5 re-read the repaired file and short-circuit: cycle 1 writes (zero-hash sentinel stored), cycle 2 re-reads the valid-UUID file and hits the normal path, cycles 3-5 hit the sha256 short-circuit.

## 9. AC3 — key-absent path is byte-for-byte unchanged

One `It`: fixture with NO `task_identifier` key, `"---\nstatus: in_progress\nassignee: claude\n---\nbody\n"`, under relPath `ac3.md`, write-through harness whose `writeFile` closure ALSO captures the written bytes (`lastWritten = append([]byte(nil), content...)`). Run five `processFile` cycles inside `captureGlogWarnings`. Assert:

- `writeCount == 1`
- exact string comparison (never a frontmatter-map comparison): parse the injected UUID from `lastWritten` (extract frontmatter -> unmarshal -> comma-ok assertion `id, isString := fmMap["task_identifier"].(string)` with `Expect(isString).To(BeTrue())`), then `Expect(string(lastWritten)).To(Equal("---\ntask_identifier: " + id + "\n" + strings.TrimPrefix(fixture, "---\n")))`. This byte-identity of the WRITE argument against the deterministic injection of the UNMODIFIED original read bytes is the direct "injection received the unmodified content" assertion (the writeFile closure is the harness-captured argument; a strip or re-marshal path would change these exact bytes).
- negative log evidence: the captured output does NOT contain `replacing invalid task_identifier`.

## 10. AC4 — healthy files are never written

One `It`: fixture with a valid unique string UUID plus `status: in_progress` AND `assignee: claude` (both mandatory — without them `processFile` returns `(nil, "", false)` before the publish site and the assertion passes vacuously): `"---\ntask_identifier: 55555555-5555-4555-8555-555555555555\nstatus: in_progress\nassignee: claude\n---\nbody\n"`. Run the five cycles INSIDE `captureGlogWarnings` (the healthy path needs the same negative log evidence the key-absent path gets). Assert: `writeCount == 0`; the file on disk equals the original bytes exactly; exactly ONE non-nil task returned across the five cycles (the first-cycle publish, matching the existing short-circuit behavior); and `strings.Count(captured, "replacing invalid task_identifier") == 0` — this is spec AC6's negative evidence over the healthy-file run, and it forbids a spurious repair WARNING on every healthy task file in prod.

## 11. AC5 — content outside the frontmatter region is preserved byte-for-byte

One `It`: fixture with frontmatter `task_identifier: 501` + `status: in_progress`, and a body containing a fenced code block whose line at column 0 is `task_identifier: 501`. Build the fixture like this (the triple-backtick fence lives inside a double-quoted Go string literal):

```go
body := "# Runbook\n\n```\ntask_identifier: 501\n```\n"
fixture := "---\ntask_identifier: 501\nstatus: in_progress\n---\n" + body
```

Run ONE cycle. Assert: (a) `extractBody(finalBytes) == extractBody(fixture)` (exact string comparison, fence markers and the `task_identifier: 501` line intact); (b) the number of lines matching `^task_identifier:` in the whole written file is exactly `2` (one frontmatter, one body); (c) the frontmatter occurrence parses as a valid UUID (`extractFrontmatter` -> unmarshal -> `uuid.Parse`) and the body occurrence is still the literal `task_identifier: 501`.

## 12. AC5b — frontmatter outside the removed key is preserved byte-for-byte

One `It`: fixture whose frontmatter contains, in order, the comment `# managed by vault-cli`, `task_identifier: 501`, `status: in_progress`, `assignee: claude`, unquoted `due: 2026-09-05`, quoted `page_count: "07"`, plus a one-line body. Run ONE cycle. Assert (exact string comparison): the written file equals `"---\ntask_identifier: <uuid>\n"` + (original bytes minus the leading `"---\n"` minus the single `task_identifier: 501\n` line) — compute the expected by extracting the injected UUID from the written bytes and `strings.Replace(strings.TrimPrefix(fixture, "---\n"), "task_identifier: 501\n", "", 1)`. Then assert line counts: `^# managed by vault-cli` == 1, `^due: 2026-09-05$` == 1 (unconverted, still unquoted), `^page_count: "07"$` == 1 (quotes intact). Assertion (a) is load-bearing; (b)-(d) name the three regressions it catches (comment dropped, date coerced, quoted scalar unquoted by a re-marshal implementation).

## 13. AC6 — repair is greppable from logs alone

One `It` running rows 1, 5, and 13 (unquoted int, empty string, quoted non-UUID) under distinct relPaths (`ac6-int.md`, `ac6-empty.md`, `ac6-quoted.md`) with a single write-through harness scanner. Write the three fixtures, run one `processFile` per row inside `captureGlogWarnings`. Assert: `strings.Count(captured, "replacing invalid task_identifier")` equals `3` (once per repaired file), and for each row the captured output contains the exact line `replacing invalid task_identifier of type <type> in <relPath>` where `<type>` is `int` for the unquoted int and `string` for the other two.

## 14. AC7 — `AUTO_INJECT_TASK_IDENTIFIER=false` parity

One `It`: a `&vaultScanner{...}` with `autoInject: false` (fresh `hashes` map) over all 16 `ac2Fixtures`, five cycles each, inside `captureGlogWarnings`. Capture `before := counterValue(metrics.ReasonAutoInjectDisabled)` (copy the local `counterValue` closure from the existing `Describe("auto-inject flag gate (spec 001)", ...)` block — do not redeclare a conflicting one). Assert: `writeCount == 0` for the whole run; `counterValue(metrics.ReasonAutoInjectDisabled) == before + 16*5`; the captured output contains `AUTO_INJECT_TASK_IDENTIFIER=false; skipping task without valid task_identifier:` and does NOT contain `replacing invalid task_identifier`.

## 15. Self-check before finishing

Before you finish, re-run `<verification>` and confirm it passes; walk each acceptance criterion (AC1-AC7) against the change. In particular confirm: the parity test at `pkg/scanner/vault_scanner_test.go:1063` still passes with its `Equal(9)`/`Equal(9)` assertions unmodified (it must — you kept two auto-inject gates); every pre-existing test in `pkg/scanner/vault_scanner_test.go` and `pkg/scanner/vault_scanner_internal_test.go` still passes; and the frozen substrings `replacing invalid task_identifier`, `AUTO_INJECT_TASK_IDENTIFIER=false; skipping task without valid task_identifier:` appear in `pkg/scanner/vault_scanner.go` exactly as specified.

</requirements>

<constraints>

- Do NOT commit — dark-factory handles git.
- `DeduplicateFrontmatter`'s last-wins semantics are frozen — other callers depend on it, and it is not the defect.
- Spec 001's `AUTO_INJECT_TASK_IDENTIFIER` gate is frozen: when `false`, every malformed shape still skips without writing, and the frozen substring `AUTO_INJECT_TASK_IDENTIFIER=false; skipping task without valid task_identifier:` is unchanged (AC7 greps for it).
- The existing `(nil, "", false)` skip return shape and `(nil, relPath, false)` write return shape from `processFile` are frozen.
- The zero-hash sentinel stored by `injectAndStore` (`hash: [32]byte{}`) is frozen.
- **Hard constraint — key removal is key-based, is span-aware, and is confined to the frontmatter region. All three hold; none may be traded for another.** (1) Key-based, not prefix-based: a raw `strings.HasPrefix` match is a verified no-op for `"task_identifier": 501`, `'task_identifier': 501`, and `task_identifier : 501` (AC2 rows 14-16 are the executable form). (2) Span-aware, not line-only: deleting only the key line leaves rows 10 and 12 looping (the orphaned continuation lines fold into the injected UUID as a multi-line plain scalar — measured 5 writes over 5 cycles under line-only removal). (3) Frontmatter-region only: a body line beginning `task_identifier:` at column 0 inside a fenced block must survive (AC5 is the executable form).
- **Hard constraint — the source-structure parity test.** The two hardcoded `Equal(9)` assertions at `pkg/scanner/vault_scanner_test.go` lines 1090 and 1093 must remain at 9. This is satisfied by keeping the key-absent and present-but-invalid auto-inject gates as two separate inlined gates. Do NOT fold them, and do NOT delete or loosen the parity assertion.
- The predicate is "present but not a valid UUID string", not "non-string" — a fix keyed on the type assertion alone would leave `""`, `''`, and `"   "` looping (AC2 rows 5-7).
- The uniformly-indented frontmatter block is deliberately OUT OF SCOPE (bounded-but-corrupt, handled by a separate spec). Do not test it, do not claim convergence for it.
- Do NOT implement the convergence guard (separate spec), do NOT add a per-file write-rate circuit breaker, do NOT change `DeduplicateFrontmatter` last-wins, do NOT add any config flag/env var/opt-out.
- The log line must name the file path and the Go type only — NEVER the raw value's full contents (a large sequence or mapping must not blow up a log line).
- Per `go-error-wrapping-guide.md`: `errors.Wrapf(ctx, ...)` in `pkg/`, never `fmt.Errorf`.
- Per `go-glog-guide.md`: the repair log is unconditional `glog.Warningf`.
- Per `go-precommit.md`: keep `processFile` under the existing `//nolint:funlen,gocognit` (update the rationale per Requirement 3); golines 100 applies; goimports ordering applies.
- Existing tests in `pkg/scanner/vault_scanner_test.go` and `pkg/scanner/vault_scanner_internal_test.go` must keep passing. No existing test asserts the buggy raw-content path for a present-but-invalid value, so behavioral assertions are unaffected; the source-structure parity test is the one to watch, and it stays green by design (Requirement 4).
- Do NOT add a write counter to `fileOpsTestGitClient` — other exported tests depend on its plain pass-through behavior.
- This prompt does NOT run `make precommit` — spec AC8 runs in prompt 2 (CHANGELOG + build gate). The code you write must still be lint-clean (`make test`, goimports, golines) so prompt 2's `make precommit` is green on the first try.
- **Every type assertion in test code must use the comma-ok form, with an explicit expectation that `ok` is true.** Per `.golangci.yml`: `forcetypeassert` is enabled, `run.tests: true`, and the `_test.go` exclusion block excludes only revive/dupl/errcheck/unparam/gosec — NOT forcetypeassert. There are currently zero single-value type assertions anywhere in `pkg/**/*_test.go`; do not introduce the first one. A single-value `x.(T)` in any test you add fails `make lint`.

</constraints>

<verification>

Run iteratively while implementing (fast loop, repo root):

```
cd /workspace && make test
```

Frozen-substring and structure checks (every grep wrapped in `|| true` because `grep -c`/`grep -n` exit 1 on a zero count):

```
cd /workspace && grep -n 'replacing invalid task_identifier' pkg/scanner/vault_scanner.go || true
cd /workspace && grep -c 'AUTO_INJECT_TASK_IDENTIFIER=false; skipping task without valid task_identifier:' pkg/scanner/vault_scanner.go || true
cd /workspace && grep -n 'removeTaskIdentifier\|taskIdentifierKeyLine\|leadingWhitespaceLen' pkg/scanner/task_identifier.go || true
```
Expect: the first grep returns the one `glog.Warningf("replacing invalid task_identifier of type %T in %s", ...)` line; the second returns `3` (three inlined auto-inject gates — two in `processFile` after this spec + the duplicate branch); the third returns all three identifiers.

Run the scanner suite with the new spec names visible:

```
cd /workspace && go test -v ./pkg/scanner/...
```

Expected new spec names (all inside `Describe` blocks you added):
- `removeTaskIdentifier (spec 008) > ...`
- AC1 production-shape spec
- AC2 16-row table spec
- AC3 key-absent byte-for-byte spec
- AC4 healthy-file spec
- AC5 frontmatter-region boundary spec
- AC5b frontmatter byte-preservation spec
- AC6 repair-log greppable spec
- AC7 auto-inject-disabled parity spec

The pre-existing parity test continues to pass unmodified:
- `maintains counter-call parity with skip-site log lines (AC#6 invariant ...)` — still `Equal(9)`/`Equal(9)`.

Coverage for the changed package (new code must be ≥80% statement-covered; the AC tests exercise every branch of `processFile`'s new decision block and every `removeTaskIdentifier` path):

```
cd /workspace && go test -coverprofile=/tmp/cover.out ./pkg/scanner/... && go tool cover -func=/tmp/cover.out
```

Review `vault_scanner.go` `processFile` and `task_identifier.go` `removeTaskIdentifier` in the coverage output — both should be well above 80%. `make precommit` runs in prompt 2 (spec AC8); do not run it here.

</verification>
