---
status: completed
spec: [013-coalesce-escalation-notifications]
summary: Added the escalation coalescing gate at the single shared publish point in pkg/result/result_writer.go — an anchored (repo, PR number) key parse, a 30-minute window on the injected clock with an atomic mutex-guarded check-and-record released on send failure, and a V(1) 'escalation notification coalesced' log line — plus eight new Ginkgo specs in result_writer_escalation_test.go (coalesced repeat, park unaffected, three distinct-key cases, window boundary + non-extension, fail-safe/anchored parse, failed publish, concurrent pair) and the stub race fix; CHANGELOG.md was deliberately left untouched because the prompt's <constraints> assign it to prompt 2.
execution_id: agent-task-controller-escalation-dedup-exec-023-spec-013-coalescing-gate-and-specs
dark-factory-version: v0.193.0
created: "2026-09-16T22:08:04Z"
queued: "2026-09-17T05:23:40Z"
started: "2026-09-17T05:23:42Z"
completed: "2026-09-17T05:31:41Z"
branch: dark-factory/coalesce-escalation-notifications
---

# Coalesce repeat escalation notifications at the publish point

<summary>

- A repeat escalation of one PR inside a 30-minute window now reaches the operator as a single notification instead of one per task file, so a retried PR review no longer re-announces an escalation the operator has already been told about.
- The coalescing key is the owner-qualified repository plus the PR number, read from the fixed prefix of the task name — a retry that mints a new task file with a new identifier and a new head SHA collapses onto the same key.
- Distinct PRs always publish their own notification: a different repository, a different PR number, and two tasks carrying the same head SHA in different repositories each get one.
- A task name the parse does not recognise publishes exactly as today and leaves no window state, so a future change to the task-name format degrades to one ping per file rather than to silence.
- The window is anchored to the last published ping, so a suppressed escalation does not extend it — a PR that keeps escalating is re-announced once per window instead of being silenced for as long as it keeps escalating.
- A failed publish does not consume the window: when the sender rejects the command the claim is released, so the next escalation of that key publishes normally and a broker outage cannot swallow the escalation that follows.
- Two escalations of one key processed at the same time publish exactly once — the check and the record are one atomic step.
- The write, the commit, the park, the message, the metadata, the notification type and the deeplink are unchanged; only the repeat ping is dropped, and one log line records each suppression so a suppressed ping is distinguishable from a delivered one.
- No new configuration field, environment variable, CLI flag or metric, and no persistence: the window is in-process and a restart clears it.

</summary>

<objective>

Stop the escalation channel degrading into noise: repeat escalations of one PR inside a 30-minute window publish exactly one `agent-escalation` notification, while the task is still written, committed and parked exactly as before, distinct PRs still publish, and a task name the parse does not recognise still publishes uncoalesced. The gate is one atomic check-and-record at the single shared publish point, measured on the injected clock, plus the unit specs that pin every branch (spec 013, Desired Behavior 1-7, AC 1-11).

</objective>

<context>

This repo has no root `CLAUDE.md`; the global YOLO container CLAUDE.md (already in your context) governs project conventions.

Read the spec: `specs/in-progress/013-coalesce-escalation-notifications.md` — Goal, Desired Behavior 1-7, Constraints, Failure Modes, Security / Abuse Cases, Acceptance Criteria (AC 1-11), and the "Suggested Decomposition" table (this prompt is row 1 and carries the whole behaviour; there is deliberately no third prompt).

Read these files before writing anything:

- `pkg/result/result_writer.go` — in full. The pieces this prompt changes:
  - `publishEscalation` (the shared publish point) — note the `r.notificationSender == nil` early return, the `agent-escalation` command with its three metadata keys and no `Target`, the swallowed send error with its `publish agent-escalation notification … failed: …` warning, and the V(1) `assignee cleared → notification published for task %s (%s) escalated by %s` line. Every one of those stays exactly as it is.
  - `writeAndPublish` — calls `r.publishEscalation(ctx, *escalated)` only after `AtomicReadModifyWriteAndCommitPush` returns. This is the frozen publish point; `writeAndPublish` itself does not change.
  - `buildResultModifyFn` — the modify closure that re-runs on every git retry and records the escalation. Do not put any part of the coalescing in here.
  - the private `resultWriter` struct and `NewResultWriter`.
  - `taskNameFromRelPath` — the task name the gate parses is `e.taskName`, already set from the matched file's basename.
  - the `notFoundAttempts` / `notFoundBackoff` const block — the frozen-constant style to follow.
- `pkg/result/result_writer_escalation_test.go` — in full. The harness you extend: `writeTaskFile(name, content)`, `retryCapTask(assignee)`, `fakeGit *mocks.GitClient` (its `ListFilesStub` globs the real filesystem, its `ReadFileStub` reads from disk, its `AtomicReadModifyWriteAndCommitPushStub` reads `absPath`, runs the modify closure `modifyRuns` times and writes the bytes back), `fakeTime *libtimemocks.CurrentDateTimeGetter` seeded to `2026-09-13T12:00:00Z`, and `published []notifcmd.NotificationPublishCommand` recorded through `notifcmd.NotificationPublishCommandSenderFunc`.
- `pkg/result/result_writer_test.go` — the sibling harness and assertion style (Ginkgo `Context`/`It`, `HaveLen`, file-content reads with `os.ReadFile`). Do not change this file.
- `docs/controller-design.md` § "Assignee-Clear on Escalation" — the doctrine the gate extends. Prompt 2 documents it; do NOT edit the doc here.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-time-injection.md` — `CurrentDateTimeGetter` injection, `libtime.DateTime`/`libtime.Duration` over stdlib types in struct fields, never `time.Now()` outside tests.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` (Ginkgo/Gomega style, counterfeiter mocks, coverage), `/home/node/.claude/plugins/marketplaces/coding/docs/definition-of-done.md` (≥80% statement coverage for new code, error paths tested), `/home/node/.claude/plugins/marketplaces/coding/docs/go-logging-guide.md` (V-level semantics), `/home/node/.claude/plugins/marketplaces/coding/docs/go-concurrency-patterns.md` (`run.CancelOnFirstErrorWait` over raw `go func()`), `/home/node/.claude/plugins/marketplaces/coding/docs/go-precommit.md` (funlen 80, nestif 4, golines 100).

Verified library facts you may rely on (grep-verified against the module source, do not re-derive from memory):

- `github.com/bborbe/time` v1.27.14 (`libtime`): `type DateTime stdtime.Time`; `func (d DateTime) Now() DateTime` on the getter interface `CurrentDateTimeGetter interface { Now() DateTime }`; `func (d DateTime) Sub(time HasTime) Duration`; `type Duration stdtime.Duration` with `const Minute = 60 * Second` typed as `Duration`.
- `github.com/bborbe/time/mocks` (`libtimemocks`): `func (fake *CurrentDateTimeGetter) NowReturns(result1 time.DateTime)` — sets a fixed value for every subsequent `Now()` call.
- `github.com/bborbe/run` v1.10.3: `type Func func(context.Context) error`; `func CancelOnFirstErrorWait(ctx context.Context, funcs ...Func) error` — runs every func, waits for all of them, returns `errors.Join` of the failures (nil when none failed).
- `mocks.GitClient` has `func (fake *GitClient) AtomicReadModifyWriteAndCommitPushCallCount() int`.

Environment facts that shape this prompt:

- The execution container's `.git` is masked (the daemon runs with `hideGit=true`; `git status` fails with `fatal: not a git repository`). Do NOT run any `git` command — a failed git command in `<verification>` reads as a pass. Spec AC 11's `git diff origin/master...HEAD -- pkg/result/result_writer_escalation_test.go | grep '^-[^-]' | grep -c 'Expect('` = 0 check is a host-side evaluation; the in-container proxies for it are the `Expect(` floor count and the assertion anchors in `<verification>` below, plus the full suite staying green.
- Do NOT run `kubectl*`, `docker`, `make build`, `make buca`, `gh`, or any deploy command — the spec's Post-Deploy ACs (14, 15) are operator-side and belong to spec verification.
- Do NOT edit `docs/controller-design.md` or `CHANGELOG.md` — that is prompt 2's scope.

</context>

<requirements>

All production changes are in `pkg/result/result_writer.go`; all spec changes are in `pkg/result/result_writer_escalation_test.go`. No other file changes.

## 1. Add the frozen constants and the anchored parse to `pkg/result/result_writer.go`

Add `"regexp"` and `"sync"` to the stdlib import group (alphabetical order; `goimports-reviser` re-sorts it during `make precommit` anyway).

Immediately after the `notFoundAttempts` / `notFoundBackoff` const block, add:

```go
// escalationCoalescingWindow is the fixed window inside which a repeat escalation of one
// PR publishes nothing. Frozen at 30 minutes: derived from the measured same-key gap
// distribution (median 18.2 minutes; 534 of 877 consecutive same-key creations inside 30
// minutes) and matching the operator's reported 8-11 duplicate pings a day. There is no
// config surface — a tunable window has no named consumer, and a switch that disables the
// coalescing re-opens the flood this closes.
const escalationCoalescingWindow = 30 * libtime.Minute

// escalationTaskNamePattern is the frozen, anchored parse that derives the coalescing key
// from a task name. The format is owned by github-pr-watcher (computeTaskTitle, built in
// pkg/filename.go, with appendRetryToken folding " - retry-<taskid[:8]>" into the suffix):
// "PR <kind> <provider> - <owner>-<repo> - <number> - <shortSHA>[- <slug>][- <suffix>]".
// Only the owner-qualified repo token and the PR number are captured, because everything
// after the short SHA — the title slug, the retry suffix, the task kind word and the
// provider word — varies between retries of one PR while those two do not. Anchored at the
// start with every captured group a space-delimited token, so neither " - " nor a newline
// in a crafted PR title can reach a captured value.
var escalationTaskNamePattern = regexp.MustCompile(`^PR \S+ \S+ - (\S+) - ([0-9]+) - \S+`)
```

`escalationCoalescingWindow` must be a `const` (it is a typed-constant expression, not a function call) and its type is `libtime.Duration`. Never write `time.Duration` or `time.Now()` here — see the go-time-injection rules.

## 2. Add the key derivation helper

```go
// escalationCoalescingKey derives the coalescing key for a task name: the owner-qualified
// repo token and the PR number, joined as "<owner>-<repo>#<number>". Reports false for a
// task name the frozen pattern does not match, and the caller then publishes uncoalesced —
// fail-open, so a format change degrades to one ping per file and never to silence. A
// matched name always yields a non-empty token and a numeric number: \S+ requires at least
// one non-space byte and [0-9]+ at least one digit, and the match is anchored at the start,
// so a flood of arbitrary names cannot grow the window map.
func escalationCoalescingKey(taskName string) (string, bool) {
	match := escalationTaskNamePattern.FindStringSubmatch(taskName)
	if match == nil {
		return "", false
	}
	return match[1] + "#" + match[2], true
}
```

## 3. Add the in-process window state

Add two fields to the private `resultWriter` struct, after `notificationSender`:

```go
	// escalationCoalescingMutex guards escalationPublishedAt. The check and the record in
	// claimEscalationCoalescingSlot are one atomic step under it, so two concurrent
	// escalations of one key cannot both publish. In-process only, never serialized: a
	// restart clears the window.
	escalationCoalescingMutex sync.Mutex
	// escalationPublishedAt records the time of the last *published* ping per coalescing
	// key — never the time of the last escalation, so a suppressed escalation does not
	// extend the window. Pruned on every claim, so its size is bounded by the distinct
	// keys seen in the last escalationCoalescingWindow.
	escalationPublishedAt map[string]libtime.DateTime
```

In `NewResultWriter`, initialise it in the returned struct literal: `escalationPublishedAt: make(map[string]libtime.DateTime),`. **Do NOT change `NewResultWriter`'s signature** — the window is internal state, so `main.go` and the six existing test-file `result.NewResultWriter(...)` call sites (seven in total) keep compiling unchanged. Do not add a constructor parameter, a functional option, or a setter.

## 4. Add the atomic claim and its release

```go
// claimEscalationCoalescingSlot reports whether an escalation of key may publish, and
// records now as that key's last published ping when it may. The check and the record are
// one atomic step under the mutex. The window is pruned here, so the map holds only keys
// seen inside the last escalationCoalescingWindow. Expiry is read from the injected clock,
// never time.Now(), so the boundary is testable without sleeping.
func (r *resultWriter) claimEscalationCoalescingSlot(key string, now libtime.DateTime) bool {
	r.escalationCoalescingMutex.Lock()
	defer r.escalationCoalescingMutex.Unlock()
	for existingKey, publishedAt := range r.escalationPublishedAt {
		if now.Sub(publishedAt) >= escalationCoalescingWindow {
			delete(r.escalationPublishedAt, existingKey)
		}
	}
	if _, published := r.escalationPublishedAt[key]; published {
		return false
	}
	r.escalationPublishedAt[key] = now
	return true
}

// releaseEscalationCoalescingSlot releases a claim taken by claimEscalationCoalescingSlot
// when the sender rejected the command, so a broker outage is followed by the next
// escalation for that key publishing normally. The entry it deletes was outside the window
// it replaced (the claim overwrote it only because its window had elapsed), so deleting it
// leaves the map in the same state as no record at all.
func (r *resultWriter) releaseEscalationCoalescingSlot(key string) {
	r.escalationCoalescingMutex.Lock()
	defer r.escalationCoalescingMutex.Unlock()
	delete(r.escalationPublishedAt, key)
}
```

`now.Sub(publishedAt) >= escalationCoalescingWindow` is the frozen boundary: strictly less than 30 minutes suppresses, 30 minutes or more publishes. A backwards clock step therefore extends the suppression for that key and a forwards step ends it early — both bounded to one ping per key, per the spec's Failure Modes table. Pruning before the membership check is what makes "a suppressed escalation does not extend the window" true: the pruned entry is exactly the one that would have published.

## 5. Wire the gate into `publishEscalation`

Extend `publishEscalation` — and only `publishEscalation`. Keep the `r.notificationSender == nil` early return first (nothing was published, so nothing may be claimed), then the gate, then the existing message/command construction and send:

```go
func (r *resultWriter) publishEscalation(ctx context.Context, e escalation) {
	if r.notificationSender == nil {
		return
	}
	// Fail-open: a task name the frozen pattern does not match publishes exactly as it
	// does today and creates no window state, so a format change degrades to one ping per
	// file and never to silence.
	coalescingKey, coalescable := escalationCoalescingKey(e.taskName)
	claimed := false
	if coalescable {
		if !r.claimEscalationCoalescingSlot(coalescingKey, r.currentDateTime.Now()) {
			// V(1): the only signal separating a coalesced repeat from a delivered ping.
			// Both deployed controllers run -v=2, so this stays visible in the pod logs.
			glog.V(1).Infof(
				"escalation notification coalesced for key %s, task %s",
				coalescingKey,
				e.taskName,
			)
			return
		}
		claimed = true
	}
	// ... the existing message, command and send are unchanged from here ...
}
```

Then, in the existing send-error branch only, release the claim before the warning and return:

```go
	if err := r.notificationSender.SendPublishNotificationCommand(ctx, command); err != nil {
		if claimed {
			r.releaseEscalationCoalescingSlot(coalescingKey)
		}
		glog.Warningf(
			"publish agent-escalation notification for task %s (%s) escalated by %s failed: %v",
			e.taskIdentifier,
			e.taskName,
			e.previousAssignee,
			err,
		)
		return
	}
```

Everything else in `publishEscalation` stays byte-for-byte: the message text, `notifcore.AgentEscalationNotificationType`, the nil `Target`, the three `Metadata` keys (`taskIdentifier`, `taskName`, `previousAssignee`), the Obsidian deeplink, the swallowed error and the existing V(1) `assignee cleared → notification published for task %s (%s) escalated by %s` line.

The frozen log substring `escalation notification coalesced` must land on one line inside a single string literal — `golines --max-len=100` must not split it. Keep the format string on its own short line as written above.

Do not touch `writeAndPublish`, `buildResultModifyFn`, `applyRetryCounter`, `applyTriggerCap`, `applyRetryCap`, `clearAssignee`, `ClearAssigneeIfHumanReview`, `escalationSection`, `triggerEscalationSection`, `FindTaskFilePath`, `WriteResult`, the four escalation rows, the terminal-status short-circuit or the body merge. Do not add a metric, a counter, a config field, an env var, a CLI flag or a per-repo allowlist — the spec's Non-goals forbid all of them.

## 6. Extend `pkg/result/result_writer_escalation_test.go`

Add `"sync"` to the stdlib import group and `"github.com/bborbe/run"` to the third-party group. Change nothing that already exists in this file except the one race fix in 6a and the additions in 6b and 6c — every existing `Expect(` line stays exactly as written (spec AC 11).

**6a. Fix the harness stub's captured error variable — REQUIRED, the race run is not clean without it.** The `AtomicReadModifyWriteAndCommitPushStub` in the outer `BeforeEach` assigns into `err`, the variable declared at the top of that same `BeforeEach` and captured by the closure:

```go
			var updated []byte
			for i := 0; i < modifyRuns; i++ {
				updated, err = modify(current)   // <- `err` is the BeforeEach's variable, shared by every caller
				if err != nil {
					return err
				}
			}
```

Two goroutines calling the stub concurrently therefore write one shared variable, and `go test -mod=mod -race -count=1 ./pkg/result/...` fails intermittently with `WARNING: DATA RACE` pointing into that stub — which makes spec AC 9's race requirement unsatisfiable. Make the error local to the stub invocation:

```go
			var updated []byte
			var modifyErr error
			for i := 0; i < modifyRuns; i++ {
				updated, modifyErr = modify(current)
				if modifyErr != nil {
					return modifyErr
				}
			}
```

This is the only change to pre-existing code in this file. It touches no assertion and no fixture — do not restructure the stub further, and do not add locking around it (the counterfeiter fake already calls the stub outside its own mutex, so concurrent calls are genuine).

**6b. Add the fixture helper** inside the `Describe` closure, directly below `retryCapTask`. It builds a retry-cap task whose *name* is the coalescing input, so the name is what the spec varies:

```go
	// prTaskFile builds a retry-cap task file whose *name* is the coalescing input and
	// returns the lib.Task that targets it. Distinct task files for one PR carry distinct
	// task_identifier values and distinct short SHAs — exactly the shape the retry path
	// mints in production, where --force appends " - retry-<taskid[:8]>" and a new head SHA
	// produces its own file.
	prTaskFile := func(taskName string, id lib.TaskIdentifier) lib.Task {
		writeTaskFile(
			taskName+".md",
			"---\ntask_identifier: "+string(id)+"\nstatus: in_progress\nphase: execution\n"+
				"retry_count: 3\nmax_retries: 3\nassignee: claude\n---\n## Result\nStatus: failed\n",
		)
		return lib.Task{
			TaskIdentifier: id,
			Frontmatter: lib.TaskFrontmatter{
				"task_identifier": string(id),
				"status":          "in_progress",
				"phase":           "execution",
				"retry_count":     3,
				"max_retries":     3,
				"assignee":        "claude",
			},
			Content: lib.TaskContent("## Result\nStatus: failed\n"),
		}
	}
```

Every escalation in the new specs is a retry-cap escalation (`retry_count: 3`, `max_retries: 3`, `assignee: claude`), which is the same row the existing specs drive: the modify closure records the escalation because the on-disk assignee is non-empty and the merged assignee is empty after `clearAssignee`.

**Two rules the fixtures must obey, because they are what the falsifiers depend on:**

- **Each escalation needs its own task file with its own `task_identifier`.** `FindTaskFilePath` errors on a duplicate `task_identifier`, and a second `WriteResult` against an *already parked* file records no escalation at all (the on-disk assignee is already empty), so a fixture that reuses one file cannot produce a second escalation.
- **Task names must not carry a ` - retry-` suffix in the coalesced-repeat case.** Spec AC 2 pins the dominant measured shape: two files of one PR that differ in their short SHA, with no retry token. A fixture where one name merely contains `retry-` does not satisfy it.

**6c. Add one new `Context("escalation notification coalescing (spec 013)", func() { ... })`** after the existing `Context("publish failure")` block, containing the eight `It`s below. Declare the base time for the clock-driven specs as `base := libtime.DateTime(time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC))` (the harness's seeded value); drive expiry by calling `fakeTime.NowReturns(base.Add(libtime.Duration(29 * time.Minute)))` and so on — `libtime.DateTime.Add` takes a `libtime.HasDuration`, so the argument must be a `libtime.Duration` (`29 * libtime.Minute` also works), never a stdlib `time.Duration`, which does not implement it. Never advance the clock by sleeping (`grep -c 'time.Sleep'` on this file must stay 0).

**It 1 — the coalesced repeat (spec AC 2 + AC 3).** Two files of one PR inside the window publish exactly one notification, and both files are still parked:

- file 1 name `PR Review github - bborbe-go-version-watcher - 15 - 28915b31 - feat-publish-go-release-notification`, identifier `lib.TaskIdentifier("11111111-1111-5111-8111-111111111111")`
- file 2 name `PR Review github - bborbe-go-version-watcher - 15 - 2d12d9f5 - feat-publish-go-release-notification`, identifier `lib.TaskIdentifier("22222222-2222-5222-8222-222222222222")`

Call `WriteResult` for each, both `To(Succeed())`, then assert:

- `Expect(published).To(HaveLen(1))` — the second task file for the same PR must not re-ping.
- Both files on disk carry `previous_assignee: claude` and no non-empty `assignee`: read each with `os.ReadFile(filepath.Join(tmpDir, taskDir, <name>+".md"))`, assert `ContainSubstring("previous_assignee: claude")` and `NotTo(ContainSubstring("\nassignee: claude"))`.
- `Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(2))` — coalescing drops the ping, never the write.

**It 2, It 3, It 4 — distinct keys never coalesce (spec AC 4).** Three separate `It`s, each with two `WriteResult` calls inside one window (the harness clock does not move) and `Expect(published).To(HaveLen(2))`:

- same number, different repo: `PR Review github - bborbe-alpha - 10 - aaaa1111 - fix-thing` then `PR Review github - bborbe-beta - 10 - bbbb2222 - fix-thing`
- same repo, different number: `PR Review github - bborbe-alpha - 10 - aaaa1111 - fix-thing` then `PR Review github - bborbe-alpha - 11 - bbbb2222 - fix-thing`
- same short SHA, different repos: `PR Review github - bborbe-alpha - 10 - aaaa1111 - fix-thing` then `PR Review github - bborbe-beta - 11 - aaaa1111 - fix-thing`

**It 5 — the window boundary and the non-extension (spec AC 5 + AC 6).** One fixture carries both ACs, because both read the same three calls. Three files of one PR — `PR Review github - bborbe-go-version-watcher - 15 - 28915b31 - feat-publish-go-release-notification`, `… - 2d12d9f5 - …`, `… - 5e33e5ff - …` — with three distinct identifiers:

1. at `base`: `WriteResult` → `Expect(published).To(HaveLen(1))`
2. `fakeTime.NowReturns(base.Add(libtime.Duration(29 * time.Minute)))`, `WriteResult` → `Expect(published).To(HaveLen(1), "the same key at T+29min must be suppressed")`
3. `fakeTime.NowReturns(base.Add(libtime.Duration(30 * time.Minute)))`, `WriteResult` → `Expect(published).To(HaveLen(2), "three escalations, two publishes")`

Step 3 is the non-extension proof: had the suppressed escalation at T+29min re-recorded the key, the T+30min escalation would still read inside the window and `published` would stay at 1.

**It 6 — fail-safe on an unrecognised task name (spec AC 7).** Five calls inside one window, asserting the count after each:

1. `Build Failure github - bborbe-agent - deadbeef` → publishes, `Expect(published).To(HaveLen(1))`
2. `Update Go bborbe-vault-cli f9b19bd` → publishes, `Expect(published).To(HaveLen(2))` — this is spec AC 7's exact evidence: two escalations whose names do not match the frozen format publish two
3. `PR Review github - bborbe-other - 99 - aaaa1111 - fix-thing` → publishes, `HaveLen(3)` — claims the key `bborbe-other#99` so the anchor check below has a key to collide with
4. `Dark Factory Implement github - bborbe-agent - PR Review github - bborbe-other - 99 - aaaa1111` → publishes, `HaveLen(4)`, with the message `"the parse is anchored: a PR-shaped fragment later in the name must not move the key"` — this name embeds a parseable PR fragment but does not *start* with one, so it must not collide with the key claimed in step 3
5. `Build Failure github - bborbe-agent - deadbeef` again → publishes, `HaveLen(5)`, with the message `"an unmatched name creates no window state"`

Step 4 is the anchor falsifier the spec's Security / Abuse Cases section names as the protection against a crafted PR title moving the key: with an unanchored pattern the embedded fragment would match, coalesce with step 3's key, and the count would stay at 3. Step 5 pins the second half of the fail-safe — an unmatched name must not create window state. Every name must be a valid single path segment (no `/`), and each needs its own `task_identifier`.

**It 7 — a failed publish does not consume the window (spec AC 8).** Two files of one PR (names as in It 1). Set `sendErr = errors.New("broker unavailable")` before the first `WriteResult` and `sendErr = nil` before the second; both `WriteResult` calls must return `Succeed()`, and `Expect(published).To(HaveLen(2))` — both attempts reached the sender, so the failed send did not record the key. Note the harness appends to `published` before returning `sendErr`, so the count is send *attempts*; if the window had been consumed by the failure the second escalation would be suppressed and the count would be 1.

**It 8 — concurrent escalations of one key publish once (spec AC 9).** Two files of one PR (`PR Review github - bborbe-agent - 9 - aaaa1111 - fix-thing` and `PR Review github - bborbe-agent - 9 - bbbb2222 - fix-thing`, two distinct identifiers), **both written to disk before any goroutine starts**. Because the shared `published` slice in the outer `BeforeEach` is appended without a lock, build a writer local to this spec whose sender records under a `sync.Mutex`:

```go
		var publishMutex sync.Mutex
		var concurrentPublished []notifcmd.NotificationPublishCommand
		concurrentWriter := result.NewResultWriter(
			fakeGit,
			taskDir,
			"openclaw",
			fakeTime,
			metrics.New(),
			libtime.NewWaiterDuration(),
			notifcmd.NotificationPublishCommandSenderFunc(
				func(_ context.Context, command notifcmd.NotificationPublishCommand) error {
					publishMutex.Lock()
					defer publishMutex.Unlock()
					concurrentPublished = append(concurrentPublished, command)
					return nil
				},
			),
		)
```

Drive the pair with `run.CancelOnFirstErrorWait(ctx, func(ctx context.Context) error { return concurrentWriter.WriteResult(ctx, first) }, func(ctx context.Context) error { return concurrentWriter.WriteResult(ctx, second) })` — it waits for both funcs, so no raw `go func()` and no `sync.WaitGroup` in the spec. Assert `Expect(...).To(Succeed())` and then `Expect(concurrentPublished).To(HaveLen(1))`. The mutex-guarded recorder is what keeps `go test -race -count=1 ./pkg/result/...` clean; the atomic claim is what makes the count 1 whichever goroutine wins.

**6d. Do not add any other spec, fixture or helper.** In particular do not add a spec for the `notificationSender == nil` path (no existing spec drives it and the spec's ACs do not ask for one), do not add a coverage-only spec, and do not add a `time.Sleep`-based variant of any spec.

## 7. Self-check before finishing

Re-run `<verification>` and confirm every line passes: both focused `go test` runs exit 0 (the race run with no data race on the window state), the frozen-substring grep prints a line, `grep -c 'time.Sleep'` prints `0`, the `Expect(` count is at or above the pre-change count with the assertion anchors still present, and `make precommit` exits 0 at repo root with the whole suite green. Confirm the race run is green on repeated invocations (the harness fix in 6a is what makes it deterministic — re-run `go test -mod=mod -race -count=1 ./pkg/result/...` a few times). Then walk each of the spec's AC 1-11 against the change and name, for each, the assertion that would fail if the behaviour regressed: a build that always publishes fails It 1 and It 5; one that always suppresses fails It 2-4, It 5 and It 6; one that records the key before the send succeeds fails It 7; one that checks and records in two separate steps fails It 8.

</requirements>

<constraints>

- **Frozen key: `(repoToken, prNumber)`.** It excludes `ref` (head SHA), `title` and `task_identifier`, because all three vary between retries of the same PR — verified on disk: two retries of one PR share `ref: 28915b31…` but carry different `task_identifier`s, and one PR's six files carry five distinct `ref` values. It must not merge two genuinely different issues: GitHub numbers PRs monotonically within a repository, so the owner-qualified repo token plus the number is unique per PR. A counter-example that rules out `(repo, title)`: `bborbe-agent-pi` PRs 9 and 12 both carry the title `update-go-module-dependencies`.
- **Frozen parse: `^PR \S+ \S+ - (\S+) - ([0-9]+) - \S+`**, anchored at the start of the task name. The format is owned by `github-pr-watcher` (`computeTaskTitle`, built at `pkg/filename.go`), so this is a cross-repo parse: the controller matches the prefix it needs and tolerates any suffix, including the slug, the retry token and the title truncation `computeTaskTitle` applies.
- **Frozen window: 30 minutes**, a fixed value with no config surface. Derived from the measured same-key gap distribution (median 18.2 minutes; 534 of 877 consecutive same-key creations inside 30 minutes, matching the operator's reported 8-11 duplicate pings a day).
- **Frozen clock:** the window is measured on `r.currentDateTime`, the `libtime.CurrentDateTimeGetter` the result writer already holds. Never `time.Now()`, and never a stdlib `time.Duration` in a struct field — use `libtime.Duration` / `libtime.DateTime`.
- **Frozen publish point:** `writeAndPublish` → `publishEscalation` in `pkg/result/result_writer.go`, after the commit succeeds. NOT inside `buildResultModifyFn`'s modify closure, which re-runs on every git retry, and not in any of the four escalation rows.
- **Frozen delivery:** `agent-escalation` is reused; no new notification type, no `Target`, the three existing metadata keys (`taskIdentifier`, `taskName`, `previousAssignee`), the message text and the deeplink unchanged. The routing table is not touched.
- **Frozen write path:** the escalation recorder, the four escalation rows, the counter logic, the terminal-status short-circuit and the body merge are untouched. No change to which task files are created, committed or parked.
- **Atomic claim, released on failure:** the check-and-record is one step under the mutex, so two concurrent escalations of one key cannot both publish; the claim is released when the sender rejects the command, so a broker outage cannot suppress the escalation that follows. The two rules are one mechanism, not two.
- **In-process state only:** no persistence, no BoltDB, no file, no shared cache, no cross-process coordination. A restart clears the window.
- **No new config field, env var, CLI flag, per-repo allowlist, tunable window, or metric** (spec Non-goal, invariant). Do NOT add a coalesced-notifications counter — the V(1) `escalation notification coalesced` log line is the observable.
- **No backfill:** the 2325 historical escalated task files are left exactly as they are; coalescing starts at the first escalation after this ships.
- **The four escalation rows keep their behaviour** (trigger-cap, retry-cap, `human_review` and the spec-042 partial-update path); the coalescing sits at the single shared publish point.
- **`NewResultWriter`'s signature does not change** — the window is internal state. `main.go` and the six existing test-file call sites (seven in total) must keep compiling untouched.
- **The existing specs in `pkg/result/result_writer_escalation_test.go` pass with unmodified `Expect(` lines** — no assertion may be deleted or weakened (spec AC 11). `pkg/result/result_writer_test.go`, `result_writer_guard_test.go`, `result_writer_body_merge_test.go`, `result_writer_accumulate_test.go`, `result_writer_unowned_test.go` and `find_task_file_path_test.go` are not modified and keep passing.
- **`make precommit` runs from the repo root** (single Go module; `pkg/result/` carries no Makefile). The focused run for the changed package is `go test -count=1 ./pkg/result/...`.
- Use `-mod=mod` for any `go test` / coverage command; never `-mod=vendor` (this repo does not commit `vendor/`).
- Do NOT commit — dark-factory handles git.
- Do NOT run any `git` command — the container's `.git` is masked; a failed git command would read as a pass.
- Do NOT run `kubectl*`, `docker`, `make build`, `make buca`, `gh`, or any operator/deploy command — the spec's Post-Deploy ACs (14, 15), the image build and the dev/prod deploys are operator-side.
- Do NOT edit `docs/controller-design.md` or `CHANGELOG.md` in this prompt — prompt 2 owns both.

</constraints>

<verification>

Spec AC 1 — the full gate, run ONCE at the end, at repo root, plus the two focused runs (these three are the container-executable rung of the spec's Verification section):

```
cd /workspace && go test -mod=mod -count=1 ./pkg/result/...
cd /workspace && go test -mod=mod -race -count=1 ./pkg/result/...
cd /workspace && make precommit
```

Expect exit `0` for all three, the full Ginkgo suite green, and no data race reported by the `-race` run. Use the focused `go test` runs iteratively while implementing and reserve `make precommit` for the end (it runs trivy and the full linter suite and is slow); if `make precommit` fails, iterate on the specific failing target (`make test`, `make check`, `make lint`) and re-run the full chain once the individual targets pass.

Spec AC 10 — the frozen coalesce log substring is present in the production file (must print ≥1 line; `grep -c` exits 1 on a zero count, so every count is wrapped in `|| true`):

```
cd /workspace && grep -n 'escalation notification coalesced' pkg/result/result_writer.go
cd /workspace && grep -c 'escalation notification coalesced' pkg/result/result_writer.go || true
```

Spec AC 5's no-sleep rule and AC 11's in-container regression proxy (the `git diff origin/master...HEAD` half of AC 11 is a host-side evaluation; these are its container-executable proxies — the pre-change `Expect(` count on this file is `24`):

```
cd /workspace && grep -c 'time.Sleep' pkg/result/result_writer_escalation_test.go || true
cd /workspace && ! grep -q 'time.Sleep' pkg/result/result_writer_escalation_test.go
cd /workspace && grep -c 'Expect(' pkg/result/result_writer_escalation_test.go || true
cd /workspace && grep -c 'AgentEscalationNotificationType' pkg/result/result_writer_escalation_test.go || true
cd /workspace && grep -c 'obsidian://open?vault=openclaw' pkg/result/result_writer_escalation_test.go || true
cd /workspace && grep -c 'published\[0\].Target).To(BeNil())' pkg/result/result_writer_escalation_test.go || true
```

Expect the first to print `0`, the `! grep -q` form to exit `0`, the `Expect(` count to print `24` or more (never fewer — a lower count means an existing assertion was deleted; the eight specs below land around `63`), and each anchor grep to print `1` or more.

The new behaviour itself (spec AC 2-9) is proven by the focused runs above: the eight new specs are the only place those counts are asserted.

</verification>
