---
status: completed
spec: [015-bug-escalation-deeplink-plus-encoded]
summary: Escalation deeplink now encodes spaces as %20 instead of +, with a six-row case table, an append-only deeplink on the V(1) publish line, the design-doc contract paragraph and the changelog entry
execution_id: agent-task-controller-deeplink-encoding-exec-026-spec-015-escalation-deeplink-percent-encoding
dark-factory-version: v0.196.0
created: "2026-09-19T15:10:00Z"
queued: "2026-09-19T15:23:06Z"
started: "2026-09-19T15:23:08Z"
completed: "2026-09-19T15:30:09Z"
branch: dark-factory/bug-escalation-deeplink-plus-encoded
---

# Encode a space as percent-20 in the escalation deeplink

<summary>

- An escalation notification carries a link meant to open the parked task file in Obsidian; today that link is inert and tapping it lands nowhere.
- The link's file path encodes a space as `+`, which Obsidian's URI handler does not decode, so the path it resolves carries literal `+` characters and matches no file.
- That makes the link broken for every escalation whose task name contains a space — which most of them do — and for every escalation from a vault whose task folder contains one.
- The link now encodes a space as `%20`, the encoding the vault's own convention documents and every other Obsidian link in the vault already uses.
- A real `+` in a task name still survives as `%2B`, so the fix cannot turn genuine data into a space, and the path separator still encodes as `%2F`.
- Task names with no spaces and non-ASCII characters keep exactly the bytes they emit today — the change is confined to the space case.
- The notification's type, routing, metadata and message text are unchanged; only the characters inside the link change.
- The publish log line now carries the emitted link, so the encoding a deployed controller produces can be read from the pod log instead of inferred from the delivered message.
- The controller design document records the encoding rule as a contract, and the changelog records the fix.
- The specs read the emitted link as a raw string and never through a URL decoder, because a decoder turns `+` back into a space and would pass on the broken link.

</summary>

<objective>

Make an escalation notification's Obsidian deeplink resolve when it is opened: the URI's `file=` and `vault=` values percent-encode a space as `%20` and never as `+`, so tapping the link in the notification reaches the parked task file instead of leaving the operator to search the vault by hand — while every other byte of the published command stays exactly as it is today.

</objective>

<context>

This repo has no root `CLAUDE.md`; the global YOLO container CLAUDE.md already in your context governs project conventions.

Read the spec first: `specs/in-progress/015-bug-escalation-deeplink-plus-encoded.md` — Summary, Problem, Reproduction, Goal, Non-goals, Desired Behavior 1-7, Design Decisions, Constraints, Assumptions, Failure Modes (every row), Security / Abuse Cases, Acceptance Criteria 1-7, and the "Suggested Decomposition" table (this prompt is its single row). Acceptance Criteria 8 and 9 are the Post-Deploy rung — they read a running pod's log on dev and prod and are **not** in this prompt's scope.

Read these files IN FULL before writing anything:

- `pkg/result/result_writer.go` (1114 lines) — the whole file. The two edit sites are `vaultDeeplink` (its doc comment starts "vaultDeeplink renders an Obsidian URI…") and the V(1) publish line inside `publishEscalation`. Note where `relPath` is computed in `publishEscalation` (`filepath.Join(r.taskDir, e.taskName+".md")`) and that the `message` is built from `vaultDeeplink(r.vaultName, relPath)` a few lines above the log call, so `relPath` is in scope at the log call. Note also that the file already imports `fmt`, `net/url`, `path/filepath` and `strings`.
- `pkg/result/result_writer_escalation_test.go` (461 lines) — the whole file. This is the harness you mirror: the top-level `BeforeEach` (`fakeGit` is `*mocks.GitClient`, `fakeGit.PathReturns(tmpDir)`, the `ListFilesStub` / `ReadFileStub` / `AtomicReadModifyWriteAndCommitPushStub` trio, `fakeTime` is `*libtimemocks.CurrentDateTimeGetter`, the `writeTaskFile` closure, and the `result.NewResultWriter(...)` call with `notifcmd.NotificationPublishCommandSenderFunc` as the recording seam). Its `Context("a real escalation", ...)` block and its `retryCapTask` closure show the fixture shape that makes a write clear the assignee and publish exactly one escalation.
- `pkg/result/result_suite_test.go` — the Ginkgo bootstrap. New spec files in this package are `package result_test` with a top-level `var _ = Describe(...)`; there is no `TestXxx` function to add.
- `docs/controller-design.md` (263 lines) — the section you extend is `## Assignee-Clear on Escalation (spec 021, refined by spec 039, completed by spec 042)`. It currently ends with the `**Coalescing the repeat escalation ping (spec 013).**` paragraph, followed by the `## Empty-to-Named Reset (spec 021)` heading. Note the doc's bold-lead-in paragraph style and that its paragraphs are single unwrapped lines.
- `CHANGELOG.md` — the frozen preamble is the `# Changelog` title, the "All notable changes…" line, the Keep-a-Changelog line and the two SemVer lines. There is currently NO `## Unreleased` section; the newest section is `## v0.11.0`.

Read the coding-plugin docs (in-container paths):

- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo/Gomega suite and spec style, `DescribeTable` / `Entry`, counterfeiter mocks, external test package.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-precommit.md` — the linter and formatter budgets: `funlen` 80 lines / 50 statements, `nestif` 4, `gocognit` 20, and `golines --max-len=100` which `make format` runs over every `.go` file including tests.
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — entry format, the required prefix, one bullet per logical change, `## Unreleased` placement.
- `/home/node/.claude/plugins/marketplaces/coding/docs/documentation-guide.md` — prose style for the doc paragraph.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-doc-best-practices.md` — GoDoc comment style for the helper's doc comment.

Environment facts that shape this prompt:

- The execution container's `.git` is masked — the daemon runs with `hideGit=true` (see `hideGit=true hideGitSource=arg` in `.dark-factory.log`; this repo's `.dark-factory.yaml` sets `workflow: direct` and no `hideGit`, so the masking comes from the launch flags, not the repo config). Do NOT run any `git` command anywhere in this prompt — it dies with `fatal: not a git repository`, and the daemon does not check `<verification>` exit codes, so a failed git command reads as a pass. The spec's `git diff HEAD` evidence for AC 4 and the Post-Deploy rung for ACs 8-9 are operator-side; the container-side equivalents are stated in `<verification>`.
- Do NOT run `kubectl*`, `docker`, `make build`, `make buca`, `gh`, or any deploy command.
- `make precommit` runs `make format`, which rewrites `.go` files in place (go-modtool, goimports-reviser, golines, gofmt) and `make generate`, which wipes and regenerates `mocks/`. Both are expected, not failures.
- `make precommit` runs from the **repo root** — this is a single Go module and `pkg/result/` carries no Makefile, so there is no per-directory target to run. The focused equivalent for the changed package is `go test -count=1 ./pkg/result/...`.
- Both deployed controllers run `-v=2` (`logLevel: "2"` in `nuke/agent/values-{dev,prod}.yaml` — that file lives in the nuke config repo, not this one; do not attempt to read it), so a `glog.V(1)` line is visible in the pod logs the Post-Deploy ACs read.

</context>

<requirements>

## 1. Fix the encoder in `vaultDeeplink` (`pkg/result/result_writer.go`)

`vaultDeeplink` is the single owner of the URI contract and has exactly one call site (`publishEscalation`). Its signature is frozen: `func vaultDeeplink(vaultName, relPath string) string`. Change only its body, and replace its doc comment with the version below.

Current body:

```go
func vaultDeeplink(vaultName, relPath string) string {
	return fmt.Sprintf(
		"obsidian://open?vault=%s&file=%s",
		url.QueryEscape(vaultName),
		url.QueryEscape(strings.TrimSuffix(relPath, ".md")),
	)
}
```

New body:

```go
func vaultDeeplink(vaultName, relPath string) string {
	return strings.ReplaceAll(
		fmt.Sprintf(
			"obsidian://open?vault=%s&file=%s",
			url.QueryEscape(vaultName),
			url.QueryEscape(strings.TrimSuffix(relPath, ".md")),
		),
		"+",
		"%20",
	)
}
```

Requirements for this function:

- The rewrite is a **single post-step over the whole rendered URI**, not a per-argument rewrite and not a rewrite of the raw inputs. `url.QueryEscape` is form encoding: it renders a space as `+`, which is the only way a bare `+` can appear in the escaped output, because escaping has already turned a literal `+` in a task name into `%2B`. One `strings.ReplaceAll` over the rendered string therefore covers `vault=` and `file=` at once and cannot touch real data.
- Do NOT apply the replacement to `vaultName` or `relPath` before escaping. `strings.ReplaceAll(name, " ", "%20")` on the raw input would leave a literal `+` in the name to be escaped as `%2B` only by accident of ordering and would have to be repeated per escaped segment; the post-step form is the one the spec's Design Decisions section requires, and it is what the literal-`+` spec row in requirement 3 falsifies.
- The `+` → `%20` rewrite must be unconditional and unconfigurable. No env var, no config field, no CLI flag, no allowlist, no opt-out — a switch that restores `+` re-opens this bug (spec Non-goals: "Any knob").
- `url.QueryEscape` stays on both values. This is what keeps `&`, `=`, `?`, `#`, `%` and newlines in a crafted PR title from breaking out of the query parameter (spec Security / Abuse Cases) — that property is unchanged by this fix and must stay.
- Every import the new body needs (`fmt`, `net/url`, `strings`) is already imported by the file. Add no import.
- Replace the existing doc comment with this one, which states the raw-input contract the Failure Modes table depends on:

```go
// vaultDeeplink renders an Obsidian URI for the task file, so the escalation
// message is one click from the notification into the parked task.
//
// url.QueryEscape is form encoding: it renders a space as "+". Obsidian's URI
// handler does not decode "+" as a space, so the path it resolves carries literal
// "+" characters and matches no file. Every "+" in the escaped result is therefore
// rewritten to "%20" — the encoding the vault's own convention documents and every
// other obsidian:// link in the vault uses.
//
// The rewrite runs on the escaped result, never on the input: escaping has already
// turned a literal "+" in a task name into "%2B", so no genuine character can be
// rewritten, and one post-step covers both query values at once.
//
// vaultName and relPath are raw, unescaped values. A caller passing an
// already-escaped value would have it escaped a second time.
```

## 2. Extend the V(1) publish line to carry the deeplink (`pkg/result/result_writer.go`)

In `publishEscalation`, the success log call is currently:

```go
	glog.V(1).Infof(
		"assignee cleared → notification published for task %s (%s) escalated by %s",
		e.taskName,
		e.taskIdentifier,
		e.previousAssignee,
	)
```

Change it to:

```go
	glog.V(1).Infof(
		"assignee cleared → notification published for task %s (%s) escalated by %s deeplink %s",
		e.taskName,
		e.taskIdentifier,
		e.previousAssignee,
		vaultDeeplink(r.vaultName, relPath),
	)
```

- **Append-only.** The existing prefix `assignee cleared → notification published for task ` is preserved byte-for-byte; the deeplink is a new trailing `%s`. No new log line, no level change, no change to the adjacent coalescing `glog.V(1)` line or the `glog.Warningf` failure line.
- The argument must be an **inline `vaultDeeplink(...)` call in the call's own argument list**, not a variable assigned elsewhere. `vaultDeeplink` must stay the single owner of the URI format string; an assertion on the literal `obsidian://open` near the log statement would fail the correct implementation and could only pass by duplicating the format string.
- `relPath` is already in scope at this point — it is computed above the `message` construction. Do not recompute it, and do not derive a path from `e.taskName` here.
- This makes the deployed encoder observable in the pod log: the line now renders `… escalated by claude deeplink obsidian://open?vault=…&file=…`, which is what the Post-Deploy ACs grep for.

## 3. Add `pkg/result/result_writer_deeplink_test.go`

Create a new file at that exact path. It is `package result_test` with the BSD license header the sibling spec files carry, a top-level `var _ = Describe("resultWriter escalation deeplink encoding", func() { … })`, and no `TestXxx` function (`result_suite_test.go` owns the bootstrap).

**3a. The assertion shape is load-bearing.** Every row asserts on the **raw** captured `command.Message` string with `ContainSubstring` / `NotTo(ContainSubstring)`. The file must NOT contain `url.Parse`, `ParseQuery` or `QueryUnescape` anywhere — not in code, not in a comment, not in a string. Go's query decoder turns `+` back into a space, so a spec that parsed the URI before asserting would be green on the buggy form and prove nothing. This is spec AC 3 and it is checked by a grep in `<verification>`.

**3b. The harness.** Mirror `pkg/result/result_writer_escalation_test.go`: a `BeforeEach` that makes `tmpDir` with `os.MkdirTemp`, builds `fakeGit *mocks.GitClient` with `PathReturns(tmpDir)` and the same three stubs (`ListFilesStub` globbing `filepath.Join(tmpDir, glob)`, `ReadFileStub` reading `filepath.Join(tmpDir, relPath)`, `AtomicReadModifyWriteAndCommitPushStub` reading, running the modify closure and writing back), and builds `fakeTime *libtimemocks.CurrentDateTimeGetter` with `NowReturns(libtime.DateTime(time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)))`.

Define one local helper that writes the fixture and returns the one published message as a raw string:

```go
	// publishMessage writes the escalation fixture into taskDir and returns the one
	// published message, read as a raw string.
	publishMessage := func(taskDir, vaultName, taskName string) string {
		Expect(os.MkdirAll(filepath.Join(tmpDir, taskDir), 0750)).To(Succeed())
		Expect(
			os.WriteFile(
				filepath.Join(tmpDir, taskDir, taskName+".md"),
				[]byte(deeplinkTaskContent),
				0600,
			),
		).To(Succeed())

		var published []notifcmd.NotificationPublishCommand
		writer := result.NewResultWriter(
			fakeGit,
			taskDir,
			vaultName,
			fakeTime,
			metrics.New(),
			libtime.NewWaiterDuration(),
			notifcmd.NotificationPublishCommandSenderFunc(
				func(_ context.Context, command notifcmd.NotificationPublishCommand) error {
					published = append(published, command)
					return nil
				},
			),
		)

		Expect(writer.WriteResult(ctx, lib.Task{
			TaskIdentifier: lib.TaskIdentifier(deeplinkTaskIdentifier),
			Frontmatter: lib.TaskFrontmatter{
				"task_identifier": deeplinkTaskIdentifier,
				"status":          "in_progress",
				"phase":           "execution",
				"retry_count":     3,
				"max_retries":     3,
				"assignee":        "claude",
			},
			Content: lib.TaskContent("## Result\nStatus: failed\n"),
		})).To(Succeed())

		Expect(published).To(HaveLen(1), "the fixture must escalate exactly once")
		return string(published[0].Message)
	}
```

with these constants declared in the same `Describe` block, **above** the helper — Go scoping begins a const's scope at the end of its own spec, so a closure declared first cannot reference it and the file will not compile:

```go
	const (
		deeplinkTaskIdentifier = "deeplink-task-uuid"
		deeplinkTaskContent    = "---\ntask_identifier: deeplink-task-uuid\n" +
			"status: in_progress\nphase: execution\nretry_count: 3\nmax_retries: 3\n" +
			"assignee: claude\n---\n## Result\nStatus: failed\n"

		// The six encoding inputs.
		spacedTaskName  = "PR Review github - bborbe-pr-review-fixtures - 2 - 03448af5 - retry-288998c8"
		plainTaskName   = "Update-Go-bborbe-vault-cli-f9b19bd"
		emDashTaskName  = "Em Dash — Task"
		plusTaskName    = "Fix plus+encoded task"
		nestedTaskName  = "Nested Task"
		spacedVaultName = "my vault"
	)
```

A fresh `result.NewResultWriter` per row is deliberate: the coalescing window is per-writer in-process state, so a writer reused across rows would suppress the second PR-shaped escalation and leave `published` empty.

**3c. The table — one `DescribeTable`, six `Entry` rows, one rule.** Use this shape, so every row asserts the same rule on the raw string:

```go
	DescribeTable(
		"encodes a space as %20 and never as +",
		func(taskDir, vaultName, taskName string, wantContains, wantAbsent []string) {
			message := publishMessage(taskDir, vaultName, taskName)

			for _, want := range wantContains {
				Expect(message).To(ContainSubstring(want))
			}
			for _, absent := range wantAbsent {
				Expect(message).NotTo(ContainSubstring(absent))
			}
		},
		…
	)
```

The six rows, in this order, with exactly these arguments:

```go
		Entry(
			"a name containing spaces",
			"tasks",
			"openclaw",
			spacedTaskName,
			[]string{
				"obsidian://open?vault=openclaw&file=tasks%2FPR%20Review%20github%20-%20",
			},
			[]string{"+"},
		),
		Entry(
			"a name containing no spaces",
			"tasks",
			"openclaw",
			plainTaskName,
			[]string{
				"obsidian://open?vault=openclaw&file=tasks%2FUpdate-Go-bborbe-vault-cli-f9b19bd",
			},
			[]string{"+", "%20"},
		),
		Entry(
			"a name containing an em dash",
			"tasks",
			"openclaw",
			emDashTaskName,
			[]string{
				"obsidian://open?vault=openclaw&file=tasks%2FEm%20Dash%20%E2%80%94%20Task",
			},
			[]string{"+"},
		),
		Entry(
			"a name containing a literal plus",
			"tasks",
			"openclaw",
			plusTaskName,
			[]string{
				"obsidian://open?vault=openclaw&file=tasks%2FFix%20plus%2Bencoded%20task",
			},
			[]string{"+"},
		),
		Entry(
			"a nested vault-relative path",
			"25 Tasks/nested",
			"openclaw",
			nestedTaskName,
			[]string{
				"obsidian://open?vault=openclaw&file=25%20Tasks%2Fnested%2FNested%20Task",
			},
			[]string{"+"},
		),
		Entry(
			"a vault name containing a space",
			"tasks",
			spacedVaultName,
			"Spaced Vault Task",
			[]string{
				"obsidian://open?vault=my%20vault&file=tasks%2FSpaced%20Vault%20Task",
			},
			[]string{"+"},
		),
```

Why each row is the row it is:

- **Row 1** is the reported defect: a `PR Review github - …` name, the shape that escalates most often. `NotTo(ContainSubstring("+"))` over the whole message is the strong form — the message prefix `escalation: claude cleared its assignee — status in_progress, phase execution` contains no `+`, so a bare `+` anywhere can only come from the encoder.
- **Row 2** pins Desired Behavior 4: a name with no spaces emits neither `+` nor `%20`. Both absence assertions are required — a fix that always emitted `%20` for every character would pass the first and fail the second.
- **Row 3** pins that a non-ASCII character keeps its percent-encoded UTF-8 bytes (`—` is U+2014 → `%E2%80%94`) and that the surrounding spaces became `%20`.
- **Row 4** is the **falsifier** for a replacement applied before escaping. Escaping first turns the literal `+` into `%2B`; a `strings.ReplaceAll(name, " ", "%20")` applied to the raw input would instead have to leave `plus+encoded` intact for `url.QueryEscape` to produce `plus%2Bencoded`, and a replacement of `+` → `%20` applied to the raw input would turn the genuine `+` into a space and emit `plus%20encoded`. The `%2B` assertion fails on both.
- **Row 5** pins Desired Behavior 3's second half: the path separator stays `%2F` while the spaces in the directory component become `%20`. It is the only row whose `taskDir` contains both a space and a separator.
- **Row 6** is the **falsifier** for a fix applied only to `file=`. The writer is constructed with `spacedVaultName`, so `vault=my%20vault` can only pass if the replacement covers the whole rendered URI — which is exactly why requirement 1 puts the post-step outside the `fmt.Sprintf`.

**3d. Do not touch the existing escalation specs.** `pkg/result/result_writer_escalation_test.go` must survive verbatim, including its `Expect(string(published[0].Message)).To(ContainSubstring("obsidian://open?vault=openclaw"))` assertion. Do not edit, reorder, reformat or weaken any line of it, and do not change any pre-existing assertion to accommodate the new behaviour. That file is the regression lock for the rest of the published command; it is checked by a grep in `<verification>`.

## 4. Record the encoding rule in `docs/controller-design.md`

Insert a new paragraph into `## Assignee-Clear on Escalation (spec 021, refined by spec 039, completed by spec 042)`, immediately after the paragraph that ends "…the message, the metadata and the deeplink are unchanged, and only the repeat ping is dropped." and immediately before the `## Empty-to-Named Reset (spec 021)` heading.

Write it as **one single unwrapped line** — the surrounding paragraphs are unwrapped long lines — using the doc's bold-lead-in style:

```
**Deeplink encoding (spec 015).** The escalation message's final line is an `obsidian://open?vault=<vault>&file=<path>` link built by `result.vaultDeeplink`, and Obsidian's URI handler resolves `file=` as a vault-relative path. Both query values are percent-encoded, and a space is emitted as `%20` and never as `+`. Form encoding would render a space as `+`, which Obsidian does not decode, so the path it resolved carried literal `+` characters and matched no file — inert for every escalation whose task name contains a space and for every escalation from a vault whose task directory contains one. The rewrite runs on the already-escaped result, so a literal `+` in a task name still round-trips as `%2B` and the path separator still encodes as `%2F`; a name with no spaces emits neither `+` nor `%20`, and a non-ASCII character keeps its percent-encoded UTF-8 bytes. The encoding is not configurable — no env var, config field, CLI flag or allowlist. The V(1) `assignee cleared → notification published for task …` line is extended to carry the emitted deeplink after its existing prefix, so the encoding a deployed controller produces can be read from the pod log rather than inferred from the delivered message.
```

- The literal `%20` MUST appear in the paragraph — the spec's evidence greps this section for it, so the rule has to be **stated** here and not merely referenced.
- The paragraph must sit inside the section, i.e. after the `## Assignee-Clear on Escalation …` heading and before the `## Empty-to-Named Reset …` heading.
- Do not modify any other paragraph, and do not document a config knob, an opt-out flag or a fallback encoding — the spec's Non-goals forbid all of them.

## 5. Add the changelog entry

Insert a new `## Unreleased` section into `CHANGELOG.md` immediately after the frozen preamble (after the "and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html)." line) and immediately above the `## v0.11.0` heading, containing exactly one bullet:

```markdown
## Unreleased

- fix: encode a space as `%20` in the escalation notification's Obsidian deeplink, so tapping the link opens the parked task file instead of landing nowhere. The deeplink's `file=` value was built with `url.QueryEscape`, whose form encoding renders a space as `+`; Obsidian's URI handler does not decode `+` as a space, so the path it resolved carried literal `+` characters and matched no file — inert for every escalation whose task name contains a space, which every `PR Review github - <repo> - <n> - <sha> - <slug>` name does, and for every escalation from a vault whose task directory contains one. `result.vaultDeeplink` now rewrites every `+` in the escaped URI to `%20`, the encoding the vault's own convention documents and every other `obsidian://` link in the vault uses; the rewrite runs on the already-escaped result, so a literal `+` in a task name still round-trips as `%2B` and the path separator still encodes as `%2F`. The notification type, the routing, the metadata keys and the message text are unchanged, the encoding is not configurable, and the V(1) publish line now carries the emitted deeplink so a deployed encoder is observable in the pod log (spec 015)
```

- The `fix:` prefix is required and is the only prefix used here: the spec frames this as a defect, so it ships a patch bump, not a new capability.
- The word `deeplink` MUST appear above the first `## vX.Y.Z` heading. One bullet, one logical change. Do not create a second `## Unreleased` section, do not touch any existing version section or bullet, and leave the `# Changelog` title and the SemVer preamble byte-for-byte unchanged.

## 6. Self-check before finishing

Re-run every command in `<verification>` and confirm each one passes. Then walk each of the spec's Acceptance Criteria 1-7 against the change and confirm the file you wrote satisfies it: the case table exists with six rows (AC 2), the new spec file contains no URI decoder (AC 3), the existing escalation spec is untouched and still asserts `obsidian://open?vault=` (AC 4), the log call site names `vaultDeeplink(` (AC 5), the design doc section states the `%20` rule (AC 6), and the changelog bullet is above the first released heading (AC 7). If any of them does not hold, fix it before finishing.

</requirements>

<constraints>

- **Frozen public surface:** `vaultDeeplink(vaultName, relPath string) string` keeps its signature. It is unexported and the test package is external (`package result_test`), so the specs drive `WriteResult` and read the captured `NotificationPublishCommand.Message` — the production path, not the helper.
- **Frozen call site:** the single call site in `publishEscalation` is not restructured. No new caller, no signature change, no move to another package, no new helper function beside `vaultDeeplink`.
- **Frozen message contract:** the `escalation: %s cleared its assignee — status %s, phase %s\n%s` format and the position of the deeplink as its final line are unchanged. Only the encoding of the URI that fills the last `%s` changes.
- **Frozen delivery:** `Type: agent-escalation` is reused; no `Target` is set; the `taskIdentifier` / `taskName` / `previousAssignee` metadata keys are unchanged; the routing table is not touched.
- **Frozen write path:** the escalation recorder, the four escalation rows, the counter logic, the terminal-status short-circuit, the body merge and the spec-013 coalescing window are untouched.
- **Append-only log change:** the V(1) publish line keeps its existing prefix and gains the deeplink. No new log line, no level change.
- **No new config field, env var, CLI flag, or metric.** The encoding is not configurable — invariant. A switch that restores `+` re-opens this bug.
- **No new dependency and no new import** in `pkg/result/result_writer.go`. `fmt`, `net/url` and `strings` are already imported.
- The replacement must run on the **escaped** result, never on the raw `vaultName` / `relPath` inputs. A replacement applied before escaping turns a genuine `+` in a task name into a space — the literal-`+` row of the case table is the guard.
- The existing specs in `pkg/result/result_writer_escalation_test.go` pass with unmodified `Expect(` lines. Do not edit that file.
- `make precommit` runs from the **repo root** (single Go module; `pkg/result/` carries no Makefile). The focused run for the changed package is `go test -count=1 ./pkg/result/...`.
- The new spec file must contain no `url.Parse`, `ParseQuery` or `QueryUnescape` — not in code, not in a comment, not in a string. A round-trip through a query decoder passes on the buggy form.
- Out of scope, and not to be touched: `bborbe/notification`, `notification-controller`, `notification-telegram`, `notification-discord`; the Telegram plain-text-not-`text_link` defect in `notification-telegram/pkg/message-sender.go`; the Discord leg's absent link entity; and backfilling already-delivered escalations.
- Per `go-precommit.md`: keep `funlen` under 80 lines / 50 statements, `nestif` under complexity 4, `gocognit` under 20, and every line under 100 characters. `make format` runs `golines --max-len=100` over test files too, so write the new spec file already within that budget.
- Do NOT commit — dark-factory handles git.
- Do NOT run any `git` command — the container's `.git` is masked (`hideGit=true`, from the daemon's launch flags) and a failed git command reads as a pass. The spec's `git diff HEAD` evidence for AC 4 and the Post-Deploy rung for ACs 8-9 are operator-side.
- Do NOT run `kubectl*`, `docker`, `make build`, `make buca`, `gh`, or any operator/deploy command.

</constraints>

<verification>

Run from the repo root. Fast loop while iterating:

```
go test -count=1 ./pkg/result/...
make test
```

The full gate, run ONCE at the end (spec AC 1):

```
make precommit
```

Expect exit 0. If `make precommit` fails, fix the failing target and re-run only that target (`make lint`, `make vet`, `make gosec`, `make test`) until it passes, then re-run the full `make precommit` once.

Spec AC 3 — the assertion shape. The new spec file must not decode the URI before asserting, so this must print nothing and exit 0:

```
! grep -qE 'ParseQuery|QueryUnescape|url\.Parse' pkg/result/result_writer_deeplink_test.go
```

Spec AC 5 — the frozen log line exists and its call site names the helper. The count form is used because the expected count is non-zero:

```
grep -n 'assignee cleared → notification published' pkg/result/result_writer.go
test "$(grep -A8 -n 'assignee cleared → notification published' pkg/result/result_writer.go | grep -cE 'vaultDeeplink\(')" -ge 1
```

Expect the first to print exactly one line (the `glog.V(1).Infof` call in `publishEscalation`) and the second to exit 0.

Spec AC 4, the container-side half — the pre-existing escalation assertions survive. The `git diff HEAD -- pkg/result/result_writer_escalation_test.go` half of AC 4 is **not run here**: `.git` is masked in this container, so that command dies with `fatal: not a git repository` and the daemon does not check exit codes, which would read as a pass. This is the context-independent half the spec names:

```
test "$(grep -c 'obsidian://open?vault=' pkg/result/result_writer_escalation_test.go)" -ge 1
test "$(grep -c 'ContainSubstring' pkg/result/result_writer_escalation_test.go)" -ge 5
```

Expect exit 0 for both. The first is the assertion spec 013 left behind; a deleted or weakened file fails it.

Spec AC 6 — the doc section states the `%20` rule:

```
sed -n '/^## Assignee-Clear on Escalation/,/^## Empty-to-Named Reset/p' docs/controller-design.md | grep -cE '%20'
```

Expect a count of at least 1 and exit 0.

Spec AC 7 — the changelog bullet names the deeplink above the first released heading:

```
awk '/^## v/{exit} tolower($0) ~ /deeplink/{f=1} END{exit !f}' CHANGELOG.md
```

Expect exit 0. Note the position-awareness: the v0.10.1 bullet also contains the word `deeplink`, and this `awk` exits at the first `## v` heading, so only the new `## Unreleased` bullet can satisfy it.

The case table itself is verified by the suite: `go test -count=1 ./pkg/result/...` exits 0 only if all six `Entry` rows pass, including the literal-`+` row and the spaced-vault-name row. The suite proves the rows that exist pass; it does not prove six exist — and a dropped row 4 or row 6 would let exactly the two wrong fixes the spec names pass — so count them:

```
test "$(grep -cE '^[[:space:]]*Entry\(' pkg/result/result_writer_deeplink_test.go)" -ge 6
```

NOT run here, and deliberately so — these are the operator-side rungs of the spec's Verification ladder and the container cannot execute them: `git diff HEAD -- pkg/result/result_writer_escalation_test.go`, the Post-Deploy ACs 8 and 9 (they read a running controller's pod log on dev and prod), and the image build, pin bump and deploy steps.

</verification>
