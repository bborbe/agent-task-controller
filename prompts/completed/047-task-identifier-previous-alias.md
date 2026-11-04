---
status: completed
spec: [021-bug-task-identifier-remint-orphans-result]
summary: The scanner now records a replaced non-UUID task_identifier as task_identifier_previous in the same repair write, FindTaskFilePath resolves either key with duplicate detection, and the design doc and changelog are updated.
execution_id: fa26fb7c-2ba7-50be-8878-f9bbbaaffb01-exec-047-task-identifier-previous-alias
dark-factory-version: dev
created: "2026-11-04T01:29:43Z"
queued: "2026-11-04T01:49:04Z"
started: "2026-11-04T01:49:05Z"
completed: "2026-11-04T02:12:13Z"
branch: dark-factory/bug-task-identifier-remint-orphans-result
---

<summary>
- A task whose identifier the scanner has replaced can still receive the agent result that was already in flight under the old value.
- When the scanner replaces a non-UUID identifier, it records the replaced value beside the new UUID in the same file and the same single write.
- A second repair replaces that recorded predecessor instead of adding a second record, so a file always carries at most one.
- Only values that could have been a live identifier are recorded: non-blank, single-line strings. Empty, whitespace-only, multi-line, non-string, missing-key and duplicate-UUID repairs record nothing.
- A recorded predecessor is written as a YAML scalar that round-trips to the exact original string, including numeric-looking values such as `501` and values containing `#` or `: `.
- The result lookup matches a task when the requested identifier equals either the current identifier or the recorded predecessor.
- A match made through the recorded predecessor is never added to the in-memory identifier-to-path index.
- If two files claim the same identifier, one by the current key and one by the recorded predecessor, the lookup fails and nothing is written.
- The design doc and the changelog record the amended address rule.
- Every existing spec-008 assertion still passes unchanged, including the five-cycle write counts, the byte-exact key-absent assertion and the source-structure parity test.
</summary>

<objective>
Make an agent result land on the task that produced it even when the scanner has since replaced that task's `task_identifier`. The scanner records the replaced value as `task_identifier_previous` in the same single write that mints the new UUID, and `FindTaskFilePath`'s walk resolves a requested identifier that appears as either `task_identifier` or a non-empty `task_identifier_previous`. The spec-008 repair itself is unchanged: one write, exactly one `task_identifier` key, a valid UUID, and no repeat on the next cycle.
</objective>

<context>
Read these files before changing anything:

- `pkg/scanner/vault_scanner.go` — `processFile` (the `!isString || !isValidUUID(taskID)` branch, currently around lines 611-637), `injectAndStore` (currently around lines 743-780), and `repairConverges`. `injectAndStore` is the only place the repaired bytes are written, so the alias must be added there to stay in one write.
- `pkg/scanner/task_identifier.go` — `isValidUUID`, `taskIdentifierKeyLines`, `removeTaskIdentifier`, `markValueSpan`, and `InjectTaskIdentifier`. The alias helper belongs in this file beside `InjectTaskIdentifier`.
- `pkg/result/result_writer.go` — `FindTaskFilePath` (currently around lines 415-496), `recordWalkMatch`, `resolveFromIndex`, and `WriteResult`. The walk parses each candidate into a small local struct with only `task_identifier`; the alias match is added there.
- `pkg/scanner/vault_scanner_internal_test.go` — the spec-008 `Describe` block: `ac2Fixtures` (16 rows), the five-cycle write-count assertions, `AC3` key-absent byte-exact assertion, `AC5b` frontmatter byte-exact assertion, `AC6` log assertions, and the helper `captureGlogWarnings`. The direct `v.injectAndStore(...)` call near line 149 is the only existing caller that must gain the new final argument.
- `pkg/scanner/vault_scanner_test.go` — the source-structure parity test near line 1280 (awk over `processFile|injectAndStore`, expects 10 skip-log sites and 10 counter calls). Do not add a skip log line or a `SkippedFilesTotal(` call to either function.
- `pkg/result/find_task_file_path_test.go`, `pkg/result/result_writer_index_test.go`, `pkg/result/result_writer_unowned_test.go` — the existing fake Git client, resolver double and metrics-double patterns to copy.
- `docs/controller-design.md` — section `### 1. Change Detection (git → Kafka)`, the `**Identifier→path index (spec 016).**` paragraph. Add the amended address rule there.
- `CHANGELOG.md` — it currently opens with `## Unreleased` and one bullet naming `task_identifier_previous`. Verify it; add the section or bullet only if absent.

Relevant coding guides (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-doc-best-practices.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-precommit.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md`
</context>

<requirements>
## 1. Scanner: write the replaced value in the same write

1. In `pkg/scanner/task_identifier.go`, generalize the frontmatter key remover so it can remove `task_identifier_previous` as well as `task_identifier`. Rename `taskIdentifierKeyLines` to `frontmatterKeyLines` and add a `wanted string` parameter; compare the parsed node's `Value` against `wanted`. Rename the loop variable from `key` to `keyNode` to avoid shadowing. Keep every existing behavior: unparseable frontmatter, non-mapping root and flow-style root still return `ok == false`; CRLF stripping is unchanged; duplicate keys are all reported.

```go
func frontmatterKeyLines(lines []string, closing int, wanted string) ([]int, bool) {
	body := make([]string, 0, closing-1)
	for i := 1; i < closing; i++ {
		body = append(body, strings.TrimRight(lines[i], "\r"))
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(strings.Join(body, "\n")), &doc); err != nil {
		return nil, false
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil, false
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode || root.Style&yaml.FlowStyle != 0 {
		return nil, false
	}
	var out []int
	for i := 0; i+1 < len(root.Content); i += 2 {
		keyNode := root.Content[i]
		if keyNode.Value != wanted {
			continue
		}
		out = append(out, keyNode.Line)
	}
	return out, true
}
```

2. Replace the body of `removeTaskIdentifier` with a delegation to a new generic `removeFrontmatterKey`, and keep `removeTaskIdentifier`'s existing signature and behavior byte-for-byte:

```go
// removeTaskIdentifier removes every task_identifier key line from the
// frontmatter region of content, together with the full indentation span of
// each key's value, so injectAndStore can safely prepend a fresh value.
func removeTaskIdentifier(content []byte) []byte {
	return removeFrontmatterKey(content, "task_identifier")
}

// removeFrontmatterKey removes every top-level key line named wanted from the
// frontmatter region of content, together with the full indentation span of
// each key's value (block sequences, block mappings and block scalars). Lines
// outside the frontmatter region are preserved byte-for-byte.
func removeFrontmatterKey(content []byte, wanted string) []byte {
	lines := strings.Split(string(content), "\n")
	closing := frontmatterClosingIndex(lines)
	if closing == -1 {
		return content
	}
	keyLines, ok := frontmatterKeyLines(lines, closing, wanted)
	if !ok {
		return content
	}
	remove := make([]bool, len(lines))
	for _, i := range keyLines {
		remove[i] = true
		markValueSpan(lines, i+1, closing, leadingWhitespaceLen(lines[i]), remove)
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
```

3. Add the alias predicate beside `isValidUUID`. It answers only the "was this a plausible live identifier" question; UUID validity is already excluded by the caller's branch.

```go
// shouldRecordPrevious reports whether a replaced task_identifier value is a
// candidate for task_identifier_previous: a non-blank, single-line string.
// A blank value was never a live identifier, and a multi-line value (a block
// scalar) cannot be rendered as one frontmatter line. Non-string shapes never
// reach this predicate: the caller's branch excludes them.
func shouldRecordPrevious(id string) bool {
	return strings.TrimSpace(id) != "" && !strings.ContainsAny(id, "\r\n")
}
```

4. Add the alias writer to `pkg/scanner/task_identifier.go`, beside `InjectTaskIdentifier`. It must remove any existing `task_identifier_previous` key before prepending the new one, so a second repair replaces rather than appends. Use `yaml.Marshal` for the scalar so numeric-looking values are quoted and the value round-trips exactly.

```go
// injectTaskIdentifierWithPrevious injects task_identifier id into content's
// frontmatter and, when previous is non-empty, also writes
// task_identifier_previous: previous in the same frontmatter. Any existing
// task_identifier_previous key is removed first, so a file carries at most one
// such key and a repeated repair replaces its value instead of appending a
// second. previous is rendered with yaml.Marshal so a value such as "501" is
// quoted and round-trips as a string rather than as an integer. When previous
// is empty this is exactly InjectTaskIdentifier.
func injectTaskIdentifierWithPrevious(
	ctx context.Context,
	content []byte,
	id string,
	previous string,
) ([]byte, error) {
	if previous == "" {
		return InjectTaskIdentifier(ctx, content, id)
	}
	s := string(content)
	var lineEnd string
	switch {
	case strings.HasPrefix(s, "---\r\n"):
		lineEnd = "\r\n"
	case strings.HasPrefix(s, "---\n"):
		lineEnd = "\n"
	default:
		return nil, errors.Errorf(ctx, "content does not start with frontmatter delimiter")
	}
	scalar, marshalErr := yaml.Marshal(previous)
	if marshalErr != nil {
		return nil, errors.Wrapf(ctx, marshalErr, "marshal task_identifier_previous")
	}
	stripped := removeFrontmatterKey(content, "task_identifier_previous")
	rest := string(stripped)[len("---"+lineEnd):]
	withAlias := "---" + lineEnd +
		"task_identifier_previous: " + strings.TrimSuffix(string(scalar), "\n") + lineEnd +
		rest
	return InjectTaskIdentifier(ctx, []byte(withAlias), id)
}
```

`InjectTaskIdentifier` stays exported and unchanged.

5. In `pkg/scanner/vault_scanner.go`, add a final `previous string` parameter to `injectAndStore` and pass it to `injectTaskIdentifierWithPrevious`:

```go
func (v *vaultScanner) injectAndStore(
	ctx context.Context,
	content []byte,
	relPath string,
	currentAssignee lib.TaskAssignee,
	onDiskHash [32]byte,
	previous string,
) (*lib.Task, string, bool) {
	id := uuid.New().String()
	newContent, injectErr := injectTaskIdentifierWithPrevious(ctx, content, id, previous)
	if injectErr != nil {
		glog.Warningf("skipping %s: failed to inject task_identifier: %v", relPath, injectErr)
		v.metrics.SkippedFilesTotal(metrics.ReasonInjectTaskIdentifierFailed).Inc()
		return nil, "", false
	}
	// ... the rest of the function is unchanged ...
}
```

Update the doc comment to state that `previous` is recorded as `task_identifier_previous` when non-empty and that it is written in the same write. Do not add a new skip log line or a new `SkippedFilesTotal(` call anywhere in `processFile` or `injectAndStore`.

6. In `processFile`, pass the replaced value only on the string-but-not-a-UUID branch. The key-absent and duplicate branches pass `""`:

```go
	if !isString || !isValidUUID(taskID) {
		if !v.autoInject {
			// ... unchanged auto-inject gate ...
		}
		if isString {
			glog.Warningf("replacing non-UUID task_identifier %q in %s", taskID, relPath)
		} else {
			glog.Warningf("replacing invalid task_identifier of type %T in %s", rawTaskID, relPath)
		}
		previous := ""
		if isString && shouldRecordPrevious(taskID) {
			previous = taskID
		}
		return v.injectAndStore(
			ctx,
			removeTaskIdentifier(content),
			relPath,
			currentFMAssignee,
			hash,
			previous,
		)
	}
```

The key-absent call (currently `return v.injectAndStore(ctx, content, relPath, currentFMAssignee, hash)`) and the duplicate-branch call both gain a trailing `""`. The duplicate branch must write no alias: a duplicated valid UUID is already indexed, and an alias there would let the index resolve it to the wrong file before the walk's duplicate check runs.

## 2. Scanner tests (AC1, AC3, AC4, AC6)

Create `pkg/scanner/task_identifier_alias_internal_test.go` (package `scanner`, license header included). Reuse `captureGlogWarnings`, `extractFrontmatter` and `countLinesMatching` from the existing internal test file.

7. AC1 — `DescribeTable` over two rows, each processed once through `processFile` with a fresh `vaultScanner` (`metrics: metrics.New()`, `hashes: make(map[string]fileEntry)`, `ops` reading and writing a temp dir, `autoInject: true`):
   - `task_identifier: "501"` → expected alias value `"501"` (numeric-looking quoting case; spec-008 AC2 row 13).
   - `task_identifier: my-slug` → expected alias value `"my-slug"` (the live incident shape).

   Per row, on the written bytes assert: `countLinesMatching(finalBytes, regexp.MustCompile("^task_identifier_previous:")) == 1`; `yaml.Unmarshal` of the extracted frontmatter into `map[string]interface{}` yields `task_identifier_previous` equal to the expected string; `task_identifier` parses as a UUID; and `repairConverges(ctx, finalBytes, uuidString) == true`.

8. AC3 — over a local copy of spec-008's 16 fixture rows (rows 1-12 and 14-16; replicate the `ac2Fixtures` table locally) plus the key-absent fixture `---\nstatus: in_progress\nassignee: claude\n---\nbody\n`:
   - For every row, after one `processFile` cycle, `countLinesMatching(finalBytes, regexp.MustCompile("^task_identifier_previous:")) == 0`.
   - Capture glog warnings around the whole loop and assert `strings.Count(captured, "replacing invalid task_identifier of type") == 11`. The eleven rows are 1-4, 8-11 and 14-16; row 12 is a block scalar, parses as a Go string and logs `replacing non-UUID task_identifier` instead.
   - Use a fresh `vaultScanner` per row so a repair in one row cannot affect another, exactly as the spec-008 AC2 test does.

9. AC4 — seed `v.hashes["dup-first.md"] = fileEntry{taskIdentifier: lib.TaskIdentifier(sharedUUID)}` with `sharedUUID = "77777777-7777-4777-8777-777777777777"`, then `processFile` a second file whose frontmatter carries the same UUID. Capture glog warnings. Assert the written bytes of the second file have zero `^task_identifier_previous:` lines and the captured log contains `replacing duplicate task_identifier`.

10. AC6 — write a fixture with `task_identifier: slug-a`, cycle once; then read the file and replace only the line matching `regexp.MustCompile("(?m)^task_identifier: .*$")` with `task_identifier: slug-b`, leaving the `task_identifier_previous: slug-a` line in place, and cycle again. On the final bytes assert: exactly one `^task_identifier_previous:` line; `yaml.Unmarshal` of the frontmatter yields `task_identifier_previous == "slug-b"`; `task_identifier` is a valid UUID. Do not rewrite the whole file for the second cycle — that would pass without exercising replace-not-append.

## 3. Result walk: match the recorded predecessor

11. In `FindTaskFilePath` in `pkg/result/result_writer.go`, extend the per-file local struct and the match condition. Keep the duplicate check: any two files matching the requested identifier — by either key — are an error naming both paths.

```go
var fm struct {
	TaskIdentifier         string `yaml:"task_identifier"`
	TaskIdentifierPrevious string `yaml:"task_identifier_previous"`
}
```

Use this match logic inside the loop (replace the existing single `if lib.TaskIdentifier(fm.TaskIdentifier) == id { ... }` block):

```go
viaPrevious := false
switch {
case lib.TaskIdentifier(fm.TaskIdentifier) == id:
	// matched by the file's current identifier
case fm.TaskIdentifierPrevious != "" &&
	lib.TaskIdentifier(fm.TaskIdentifierPrevious) == id:
	viaPrevious = true
default:
	continue
}
if matchedRelPath != "" {
	return "", nil, errors.Errorf(
		ctx,
		"duplicate task_identifier %s in %s and %s",
		id, matchedRelPath, relPath,
	)
}
matchedRelPath = relPath
matchedByPrevious = viaPrevious
if viaPrevious {
	glog.V(2).Infof(
		"FindTaskFilePath: matched file %s for task %s via task_identifier_previous",
		relPath, id,
	)
} else {
	glog.V(2).Infof("FindTaskFilePath: matched file %s for task %s", matchedRelPath, id)
}
// ... the existing existingFrontmatter unmarshal stays here unchanged ...
```

Declare `matchedByPrevious bool` beside `matchedRelPath` before the loop. At the end, replace the unconditional `recordWalkMatch(...)` with:

```go
if !matchedByPrevious {
	recordWalkMatch(ctx, resolver, id, matchedRelPath)
}
return matchedRelPath, existingFrontmatter, nil
```

12. Update the `FindTaskFilePath` doc comment: it returns the file whose frontmatter has `task_identifier == id` **or** a non-empty `task_identifier_previous == id`; a match through `task_identifier_previous` is never recorded in the index, because the index is keyed on the current `task_identifier` only. `resolveFromIndex` is unchanged — an alias identifier is not in the index, so it falls through to the walk, and the existing "no longer carries" guard stays as it is.

## 4. Result tests (AC2, AC7, AC8)

Create `pkg/result/find_task_file_path_alias_test.go` (package `result_test`, license header included). Follow the fake patterns in `find_task_file_path_test.go`, `result_writer_index_test.go` and `result_writer_unowned_test.go`.

13. AC2 — two specs:
   - `FindTaskFilePath(ctx, fakeGC, "tasks", "my-slug", nil)` over one file with `task_identifier: <uuid>`, `task_identifier_previous: my-slug`, `target_vault: personal` returns that path with no error.
   - A `WriteResult` spec using a temp dir and the real-filesystem `AtomicReadModifyWriteAndCommitPush` stub from `result_writer_index_test.go`. Use `mocks.Metrics` whose `ResultsWrittenTotalReturns(prometheus.NewCounter(...))`, a real `libtime.NewWaiterDuration()`, and a nil resolver. The payload is `lib.Task{TaskIdentifier: "my-slug", Frontmatter: lib.TaskFrontmatter{"status": "done", "phase": "done"}, Content: "New content\n"}` (do not put `task_identifier` in the payload frontmatter). Assert `WriteResult` succeeds, `ResultsWrittenTotalCallCount() == 1` with argument `"success"`, and the written file still carries `task_identifier_previous: my-slug` after the result write re-marshals the frontmatter (unmarshal the extracted frontmatter and compare the value to `"my-slug"`).

14. AC7 — two files in `tasks/`: `tasks/first.md` with `task_identifier: my-slug`, and `tasks/second.md` with `task_identifier: <uuid>` plus `task_identifier_previous: my-slug`. `FindTaskFilePath(ctx, fakeGC, "tasks", "my-slug", nil)` returns an error whose message contains `duplicate task_identifier` and both paths. A `WriteResult` spec over the same two files returns an error containing `duplicate task_identifier` and `fakeGit.AtomicReadModifyWriteAndCommitPushCallCount() == 0`. Do not assert any metric increment on this path.

15. AC8 — with a `mocks.TaskPathResolver` whose `ResolveReturns("", false, nil)`, `FindTaskFilePath(ctx, fakeGC, "tasks", "my-slug", resolver)` over the alias-only file returns the path and `resolver.RecordCallCount() == 0`.

## 5. Documentation and changelog (AC10, AC11)

16. In `docs/controller-design.md`, add a paragraph immediately after the `**Identifier→path index (spec 016).**` paragraph in `### 1. Change Detection (git → Kafka)`. It must contain both literal strings `task_identifier_previous` and `or its recorded predecessor`, and state:
   - when the scanner replaces a non-blank, single-line, non-UUID string identifier it records the replaced value as `task_identifier_previous` in the same single write as the new UUID;
   - `FindTaskFilePath` accepts a file when the requested identifier equals its `task_identifier` or its recorded predecessor;
   - a second file claiming the same identifier — by either key — is a duplicate error, not a preference, so no result is written;
   - an alias match is never recorded in the identifier→path index, which stays keyed on the current `task_identifier` only.

17. In `CHANGELOG.md`, ensure `## Unreleased` exists exactly once and above every `## v...` heading, and that a bullet containing `task_identifier_previous` sits inside it. If the section and bullet are already present, leave them unchanged; do not add a duplicate. If absent, add the section at the top and this bullet:

```markdown
- fix: record a replaced `task_identifier` as `task_identifier_previous` and match that key in `FindTaskFilePath`'s walk, so a result that was already in flight when the scanner repaired a non-UUID identifier still resolves to its task file instead of being dropped
```
</requirements>

<constraints>
- **Spec 008's repair is frozen.** `pkg/scanner/vault_scanner.go` keeps stripping and minting: exactly one `task_identifier` key, a valid UUID, one write, no repeat on the next cycle. The alias is additive.
- **No spec-008 assertion is edited.** The only permitted change to an existing spec-008 test file is adding the new final `""` argument to the direct `v.injectAndStore(...)` call in `pkg/scanner/vault_scanner_internal_test.go` (around line 149). No assertion, fixture or expected byte string changes. The AC5b fixture uses the unquoted integer `501`, which is a non-string and therefore takes the no-alias path, so its byte-exact assertion is unaffected.
- **No new skip reason, no new metric.** `pkg/metrics/metrics.go` is untouched. The alias rides the existing repair path and its existing log line. The parity test's `skipCount` and `counterCount` stay at 10.
- **`AUTO_INJECT_TASK_IDENTIFIER` semantics are unchanged.** When `false`, every shape still skips without writing, and no alias is written either.
- **The alias is never written on the key-absent path.** There is no replaced value to record, and spec-008 AC3 asserts that path's bytes exactly.
- **The duplicate branch writes no alias.** A valid UUID is indexed, so an alias there would let `resolveFromIndex` resolve it to the other file and bypass the duplicate guard.
- **No bulk repair of existing files.** Files that already carry a non-UUID identifier are made resolvable, not rewritten.
- **The `typedTaskFields` allowlist (vault-api) is not touched.** The scanner writes through git-rest and the controller reads frontmatter directly, so the new key crosses no command API.
- **No new configuration knob.** No flag, env var, CLI option or tunable threshold is introduced.
- **Security:** the duplicate case is an error rather than a preference, so an alias cannot steal another task's result; the alias is only ever written by the scanner from a value already present in the file; the alias is excluded from the duplicate branch so it cannot shadow an indexed UUID.
- Do not commit; dark-factory handles git.
- Existing tests must still pass.
</constraints>

<verification>
Run from the repo root and confirm each step:

```bash
make precommit
```

```bash
go test -v ./pkg/scanner/... ./pkg/result/...
```

```bash
# The alias key exists in the code and the tests.
grep -rn 'task_identifier_previous' pkg/
```

```bash
# AC10: the Unreleased section exists once and the bullet is inside it.
grep -c '^## Unreleased$' CHANGELOG.md
awk '/^## v/{exit} /task_identifier_previous/{print}' CHANGELOG.md
```

```bash
# AC11: the design doc carries both literals.
grep -c 'task_identifier_previous' docs/controller-design.md
grep -c 'or its recorded predecessor' docs/controller-design.md
```

```bash
# The frozen repair shape is still one key and one valid UUID after a repair;
# the new tests assert this, but this grep proves no second task_identifier
# writer was introduced.
grep -rn 'task_identifier_previous' pkg/scanner/task_identifier.go
```

Before finishing, walk each numbered requirement against the diff. Every `grep -c` above must print a number greater than zero (and `grep -c '^## Unreleased$' CHANGELOG.md` must print exactly `1`), `go test` must pass for both packages, and `make precommit` must exit 0.
</verification>
