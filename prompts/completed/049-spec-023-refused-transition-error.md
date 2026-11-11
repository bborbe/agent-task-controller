---
status: completed
spec: [023-bug-conflict-error-relabels-transition-refusal]
summary: Classified vault-api 409s by error code, adding ErrRefused/RefusedError for refusals while preserving AlreadyExistsError fallback, with regression specs and changelog entry
execution_id: 754198a4-5873-50e2-a0b2-81969d795e9d-exec-049-spec-023-refused-transition-error
dark-factory-version: dev
created: "2026-11-10T09:30:00Z"
queued: "2026-11-10T08:34:06Z"
started: "2026-11-10T08:34:07Z"
completed: "2026-11-10T08:45:58Z"
branch: dark-factory/spec-023
---

<summary>
- A vault-api 409 that refuses a status or phase transition is now reported as that refusal, not as "task already exists".
- The reported error names the server's refusal code and the from/to states it refused, plus any allowed alternatives.
- A caller can distinguish "the server said no" from "the address is taken" with `errors.Is` alone, via a new refusal sentinel.
- A genuine `task_exists` 409 keeps its current typed error and occupant identifier/status, unchanged.
- A 409 whose body is malformed or carries no error code still falls back to the existing "already exists" error, so no caller sees a new error class on a broken response.
- No 409 error message names the operation twice; both the refusal and the duplicate shapes read cleanly once.
- No server-side status mapping changes, no new dependency, and the create/readdress callers keep their signatures and behaviour.
- The change is covered by regression specs that fail on the pre-fix build and pass after it.
- The changelog records the fix under an Unreleased section.
</summary>

<objective>
Make `conflictError` in `pkg/vaultapi/client.go` classify a 409 by the server's `error` code: `ambiguous_name` stays `ErrAmbiguous`, `task_exists` stays `AlreadyExistsError`, and every other named code becomes a typed refusal that unwraps to a new `ErrRefused` sentinel. Stop the duplicate-operation message prefix, and prove the new behaviour with specs that assert the refusal code/from/to, the negative control, and the single-operation message.
</objective>

<context>
Read these files before changing anything:

- `pkg/vaultapi/client.go` — the whole file, especially the sentinels at the top (`ErrNotFound`, `ErrAlreadyExists`, `ErrAmbiguous`), `AlreadyExistsError` and its `Error()`/`Unwrap()`, `decodeTask` (the `http.StatusConflict` branch), and `conflictError` (the function this change rewrites). Note the import alias `stderrors "errors"` and the `github.com/bborbe/errors` import.
- `pkg/vaultapi/client_create_test.go` — the shared `recordingServer` helper and the existing `CreateTask` / `ReaddressTask` conflict specs. These must keep passing unchanged (AC3). Reuse `recordingServer`; do not redefine it.
- `pkg/vaultapi/client_terminal_test.go` — style reference for a small focused spec in this package.
- `pkg/vaultapi/vaultapi_suite_test.go` — the Ginkgo suite bootstrap.
- `pkg/command/task_create_task_executor.go` — read only. It reads `*vaultapi.AlreadyExistsError` fields (`createThroughAPI`, `TaskIdentifier`, `Status`) and must not change.
- `CHANGELOG.md` — it currently opens with `## v0.26.6`; there is no `## Unreleased` section.

Relevant coding guides (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-doc-best-practices.md`

<!-- OPEN QUESTION FOR REVIEWER (spec 023): the spec names an `Allowed` field
but does not give its JSON shape, and the reproduction body omits it. This
prompt assumes `"allowed"` is a JSON array of strings and decodes it
defensively so a wrong-typed value never demotes a refusal to
AlreadyExistsError. If vault-api actually sends a single string, change
`RefusedError.Allowed` to `string` and drop the defensive unmarshal. -->
</context>

<requirements>
## 1. Add the refusal sentinel and typed refusal in `pkg/vaultapi/client.go`

Immediately after `ErrAmbiguous` (the last of the existing sentinels), add a new sentinel using the existing `stderrors` alias, following the repo idiom (`var ErrXxx = stderrors.New("...")`):

```go
// ErrRefused is returned when the API refuses an operation on its merits — a
// status or phase transition it will not perform — rather than because the
// address is taken. It is the sibling of ErrAlreadyExists for the 409s that
// mean "no", not "someone else has it".
var ErrRefused = stderrors.New("refused")
```

Immediately after the `AlreadyExistsError` type and its methods (before `IsTerminalStatus`), add the typed refusal:

```go
// RefusedError is ErrRefused carrying the refusal the API named in its 409:
// the code, the server's reason, the transition it refused, and any allowed
// alternatives. It exists so a refused transition is reported as itself instead
// of as "task already exists". Unwrap reports ErrRefused, so errors.Is can tell
// a refusal from an occupied address.
type RefusedError struct {
	// Code is the API's refusal code, e.g. invalid_transition or approval_required.
	Code string
	// Reason is the server's human-readable explanation, empty when absent.
	Reason string
	// From is the state the API refused to leave, empty when absent.
	From string
	// To is the state the API refused to enter, empty when absent.
	To string
	// Allowed lists the transitions the API would accept, empty when absent.
	Allowed []string
}

// Error renders the refusal by code, the server's reason and the transition,
// omitting the parts the API did not supply. The operation is named once by the
// caller's wrap, not here.
func (e *RefusedError) Error() string {
	message := e.Code
	if message == "" {
		message = "refused"
	}
	if e.Reason != "" {
		message += ": " + e.Reason
	}
	if e.From != "" || e.To != "" {
		message += " (" + e.From + " -> " + e.To + ")"
	}
	if len(e.Allowed) > 0 {
		message += " (allowed: " + strings.Join(e.Allowed, ", ") + ")"
	}
	return message
}

// Unwrap keeps errors.Is(err, ErrRefused) true for the typed form.
func (e *RefusedError) Unwrap() error { return ErrRefused }
```

`strings` is already imported in this file. Do not add a new dependency.

## 2. Rewrite `conflictError` to switch on the decoded error code

Update the `conflictError` doc comment so it no longer claims the function distinguishes only "already exists" from "ambiguous name". It must record the three-way split: an occupied address (`task_exists`), an ambiguous name (`ambiguous_name`), and any other named refusal (reported as `ErrRefused`), plus the deliberate malformed-body fallback to `AlreadyExistsError`.

Replace the body of `conflictError` with a decode that captures the refusal detail and a switch on `envelope.Error`. The exact resulting function:

```go
func conflictError(ctx context.Context, raw []byte, what string) error {
	var envelope struct {
		Error          string          `json:"error"`
		Reason         string          `json:"reason"`
		From           string          `json:"from"`
		To             string          `json:"to"`
		Allowed        json.RawMessage `json:"allowed"`
		TaskIdentifier string          `json:"task_identifier"`
		Status         string          `json:"status"`
	}
	if err := json.Unmarshal(raw, &envelope); err == nil {
		switch envelope.Error {
		case "ambiguous_name":
			return errors.Wrapf(ctx, ErrAmbiguous, "%s", what)
		case "", "task_exists":
			// Fall through to the occupant form below. An empty code is a
			// malformed response, not a refusal (AC5).
		default:
			refusal := &RefusedError{
				Code:   envelope.Error,
				Reason: envelope.Reason,
				From:   envelope.From,
				To:     envelope.To,
			}
			if len(envelope.Allowed) > 0 {
				// A wrong-typed allowed field must not demote a refusal to a
				// duplicate; the code still classifies it.
				var allowed []string
				if err := json.Unmarshal(envelope.Allowed, &allowed); err == nil {
					refusal.Allowed = allowed
				}
			}
			return errors.Wrapf(ctx, refusal, "%s", what)
		}
	}
	return errors.AddContextDataToError(ctx, &AlreadyExistsError{
		Code:           envelope.Error,
		TaskIdentifier: envelope.TaskIdentifier,
		Status:         envelope.Status,
		What:           what,
	})
}
```

Semantics, exactly:
- `ambiguous_name` → `ErrAmbiguous`, unchanged.
- `task_exists` → `*AlreadyExistsError` with `Code`/`TaskIdentifier`/`Status`/`What`, unchanged from today.
- Any other non-empty `error` code → `*RefusedError` carrying `Code`, `Reason`, `From`, `To`, `Allowed`, wrapped so the message names the operation once.
- A body that does not decode, or decodes with an empty `error`, → `*AlreadyExistsError`, exactly today's fallback (AC5). A hostile or truncated body must not panic or hang; `json.Unmarshal` on `[]byte` does neither.

Do not change `decodeTask`, the `http.StatusConflict` branch that calls `conflictError`, or any other status mapping.

## 3. Make the `AlreadyExistsError` wrapper stop repeating the operation

`AlreadyExistsError.Error()` already begins with `What` (`What + ": task already exists"`). The old `errors.Wrapf(ctx, err, "%s", what)` added `what` a second time, producing `patch task X: patch task X: ...`.

Requirement 2 above already changes this path to `errors.AddContextDataToError(ctx, &AlreadyExistsError{...})`. Keep that:
- Keep the `What` field on `AlreadyExistsError` and keep its `Error()` unchanged; `What` still renders once, in `Error()`.
- Do NOT use `errors.Wrapf(..., "%s", what)` for the `AlreadyExistsError` return, and do NOT set `What: ""`.
- `errors.AddContextDataToError` preserves the caller's structured context data without adding a message prefix. Update any stale comment that still claims the conflict is wrapped with `"%s"`; it must describe adding context data instead.

`ErrAlreadyExists`, `ErrAmbiguous`, `AlreadyExistsError` and every `CreateTask` signature stay unchanged.

## 4. Add the regression specs in a new `pkg/vaultapi/client_patch_test.go`

Create `pkg/vaultapi/client_patch_test.go` in package `vaultapi_test`. It reuses the `recordingServer` helper defined in `client_create_test.go` (same package); do not redefine it. Follow the existing `Describe`/`Context`/`BeforeEach`/`JustBeforeEach`/`AfterEach` shape from `client_create_test.go`, including the `AfterEach` assertion on `recorder.failure()`.

The file must contain exactly these three contexts under `Describe("PatchTask")`:

```go
var _ = Describe("PatchTask", func() {
	var (
		ctx      context.Context
		recorder *recordingServer
		server   *httptest.Server
		client   vaultapi.Client
	)

	BeforeEach(func() {
		ctx = context.Background()
		recorder = &recordingServer{}
	})

	JustBeforeEach(func() {
		client = vaultapi.NewClient(server.URL, "secret", "agent-task-controller")
	})

	AfterEach(func() {
		if server != nil {
			server.Close()
		}
		Expect(recorder.failure()).NotTo(HaveOccurred())
	})

	Context("when the API refuses the transition", func() {
		BeforeEach(func() {
			server = httptest.NewServer(recorder.handler(
				http.StatusConflict,
				`{"error":"invalid_transition","reason":"refusing phase transition \"done\" -> \"human_review\"","from":"done","to":"human_review"}`,
			))
		})

		It("reports the refusal, not a duplicate", func() {
			// The full Ginkgo name is "PatchTask when the API refuses the
			// transition reports the refusal, not a duplicate" (AC1).
			_, err := client.PatchTask(
				ctx, "task-1", map[string]any{"phase": "human_review"},
			)
			Expect(err).To(HaveOccurred())
			Expect(errors.Is(err, vaultapi.ErrAlreadyExists)).To(BeFalse())
			Expect(errors.Is(err, vaultapi.ErrRefused)).To(BeTrue())

			var refusal *vaultapi.RefusedError
			Expect(errors.As(err, &refusal)).To(BeTrue())
			Expect(refusal.Code).To(Equal("invalid_transition"))
			Expect(refusal.From).To(Equal("done"))
			Expect(refusal.To).To(Equal("human_review"))
			Expect(strings.Count(err.Error(), "patch task task-1")).To(Equal(1))
		})
	})

	Context("when the address is occupied", func() {
		BeforeEach(func() {
			server = httptest.NewServer(recorder.handler(
				http.StatusConflict,
				`{"error":"task_exists","task_identifier":"task-1","status":"completed"}`,
			))
		})

		It("names the operation once and keeps ErrAlreadyExists", func() {
			_, err := client.PatchTask(ctx, "task-1", map[string]any{"phase": "done"})
			Expect(err).To(HaveOccurred())
			Expect(errors.Is(err, vaultapi.ErrAlreadyExists)).To(BeTrue())
			Expect(errors.Is(err, vaultapi.ErrRefused)).To(BeFalse())
			Expect(strings.Count(err.Error(), "patch task task-1")).To(Equal(1))

			var conflict *vaultapi.AlreadyExistsError
			Expect(errors.As(err, &conflict)).To(BeTrue())
			Expect(conflict.What).To(Equal("patch task task-1"))
		})
	})

	Context("when the 409 body is not a conflict envelope", func() {
		BeforeEach(func() {
			server = httptest.NewServer(recorder.handler(
				http.StatusConflict,
				`conflict`,
			))
		})

		It("keeps the ErrAlreadyExists contract", func() {
			// Negative control for over-narrowing (AC5): a body that does not
			// parse must not become a refusal or a new error class.
			_, err := client.PatchTask(ctx, "task-1", map[string]any{"phase": "done"})
			Expect(err).To(HaveOccurred())
			Expect(errors.Is(err, vaultapi.ErrAlreadyExists)).To(BeTrue())
			Expect(errors.Is(err, vaultapi.ErrRefused)).To(BeFalse())
		})
	})
})
```

Imports for the new file: `context`, `errors`, `net/http`, `net/http/httptest`, `strings`, the Ginkgo/Gomega dot imports, and `github.com/bborbe/agent-task-controller/pkg/vaultapi`. Include the repo's BSD license header (copy the header from `client_create_test.go`).

Do not modify `client_create_test.go` or `client_terminal_test.go`; their create and readdress conflict specs are the AC3 evidence and must stay green.

## 5. Record the fix in `CHANGELOG.md`

Create a new `## Unreleased` section above `## v0.26.6`, with one bullet that mentions `ErrRefused`. For example:

```markdown
## Unreleased

- fix: report a vault-api 409 that refuses a status or phase transition as a typed `ErrRefused` refusal carrying the server's code and from/to/allowed detail, instead of folding it into "task already exists"; a genuine `task_exists` 409 keeps its occupant fields, and every 409 message names the operation once
```
</requirements>

<constraints>
- **Server contract unchanged.** Do NOT change vault-api's status mapping, and do not assume a refused transition will arrive as anything other than 409. A refused transition staying 409 is the server's contract.
- **`CreateTask` reopen/ambiguity handling unchanged.** Do not rework it; it already distinguishes its two 409 shapes correctly.
- **No retry or auto-recovery.** A refusal is terminal for that call; return it, do not retry.
- **Signatures and behaviour preserved.** `ErrAlreadyExists`, `ErrAmbiguous`, `AlreadyExistsError`, and every `CreateTask`/`PatchTask`/`ReaddressTask` signature stay unchanged. `pkg/command/task_create_task_executor.go` reads `AlreadyExistsError` fields and must not change.
- **Fallback is deliberate.** A 409 body that fails to decode, or carries an empty `error`, must fall through to `AlreadyExistsError` (AC5). Do not return `ErrRefused` or a bare unexpected-status error for that case.
- **Sentinel idiom.** `var ErrRefused = stderrors.New("refused")`; typed value with `Unwrap() error`.
- **No new dependency.** Use the already-imported `encoding/json`, `strings`, `github.com/bborbe/errors`, and `stderrors "errors"`.
- **Hostile input must not panic or hang.** No field from the 409 body may be used without a zero-value default.
- **Message discipline.** Every 409 error message names the operation exactly once, for both the refusal and the duplicate shape.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
</constraints>

<verification>
Run all of the following; each must hold.

- `go test ./pkg/vaultapi/ -count=1` exits 0. The new spec `PatchTask when the API refuses the transition reports the refusal, not a duplicate` passes, and the existing create/readdress specs still pass.
- `make precommit` exits 0.
- `grep -c 'ErrRefused' pkg/vaultapi/client.go` returns at least `3` (sentinel declaration, `RefusedError` docs, and `Unwrap`).
- `grep -c 'RefusedError' pkg/vaultapi/client.go` returns at least `2` (type declaration and `Error`/`Unwrap` receivers).
- `grep -c 'AddContextDataToError' pkg/vaultapi/client.go` returns at least `1` (the single-operation `AlreadyExistsError` wrapper).
- `grep -c 'strings.Count' pkg/vaultapi/client_patch_test.go` returns at least `2` (one per 409 shape, AC4).
- `grep -A2 '## Unreleased' CHANGELOG.md | grep -i 'ErrRefused'` returns at least one line.
- `git diff --name-only -- pkg/command/task_create_task_executor.go` prints nothing (the create executor must be untouched).
</verification>
