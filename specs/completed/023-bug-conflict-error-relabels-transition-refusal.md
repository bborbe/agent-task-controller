---
status: completed
approved: "2026-11-10T07:50:29Z"
generating: "2026-11-10T08:20:20Z"
prompted: "2026-11-10T08:28:37Z"
verifying: "2026-11-10T08:45:58Z"
completed: "2026-11-10T08:46:04Z"
branch: dark-factory/spec-023
---

## Summary

- vault-api answers a refused status or phase transition with HTTP 409, and the controller's `conflictError` turns every 409 that is not `ambiguous_name` into "task already exists".
- A refused transition is therefore reported as a duplicate create — a different failure with a different remedy.
- That mislabel cost a live investigation several wrong turns: the "task already exists" string sent readers hunting a create/patch route collision in vault-api, when the server had correctly refused `done → human_review`.
- The fix maps only the code that means a conflict of identity, and surfaces the rest as themselves.

## Problem

vault-api's `refusalStatus` (`pkg/handler/write.go`) maps every refusal code it does not enumerate to `http.StatusConflict`. A refused transition (`invalid_transition`, `approval_required`) therefore arrives as 409, sharing a status with the create route's genuine `task_exists`. The controller's `conflictError` (`pkg/vaultapi/client.go`) reads only the `error` field, treats `ambiguous_name` specially, and folds everything else into `AlreadyExistsError`, whose `Error()` reads "task already exists". A caller — and a human reading the result topic — is told a duplicate exists when the truth is that the transition is illegal. The same function also wraps with the operation name while `AlreadyExistsError.Error()` already begins with it, producing a doubled `patch task X: patch task X:` prefix.

## Reproduction

A stub vault-api answers `PATCH /api/v1/tasks/task-1` with `409 {"error":"invalid_transition","reason":"refusing phase transition \"done\" -> \"human_review\"","from":"done","to":"human_review"}`. On the current build:

```
client.PatchTask(ctx, "task-1", map[string]any{"phase": "human_review"})
```

returns an error whose text is:

```
patch task task-1: patch task task-1: task already exists
```

`errors.Is(err, vaultapi.ErrAlreadyExists)` is `true`, and the server's `invalid_transition` code appears nowhere in the error.

## Expected vs Actual

- **Expected** — an error naming the refusal: its code, and the from/to it names.
- **Actual** — "task already exists", repeated twice with the operation, carrying neither the code nor the transition.

## Goal

A 409 whose `error` code names a refusal is reported as that refusal, carrying the server's code and its `from`/`to`/`allowed` detail, and never satisfies `errors.Is(err, ErrAlreadyExists)`. A 409 that means a conflict of identity (`task_exists`) keeps its current typed error and occupant fields. Every 409 error message names the operation exactly once.

## Non-goals

- Changing vault-api's status mapping — a refused transition staying 409 is the server's contract and other clients may rely on it.
- Reworking `CreateTask`'s reopen/ambiguity handling, which already distinguishes its two 409 shapes correctly.
- Retrying or auto-recovering a refused transition; a refusal is terminal for that call.

## Desired Behavior

1. `conflictError` switches on the decoded `error` code: `ambiguous_name` → `ErrAmbiguous` (unchanged), `task_exists` → `AlreadyExistsError` with the occupant (unchanged), any other named code → a typed refusal carrying `Code`, `Reason`, `From`, `To`, `Allowed`.
2. The refusal type unwraps to a new sentinel `ErrRefused`, so a caller distinguishes "the server said no" from "the address is taken" with `errors.Is` alone.
3. A body that fails to decode, or decodes with an empty `error`, falls through to `AlreadyExistsError` — unchanged behaviour, so no caller sees a new error class on a malformed response.
4. Error messages name the operation once. The `What` field stays on `AlreadyExistsError` (it is used for logging), but the wrapping stops repeating it.

## Acceptance Criteria

- [ ] **AC1** — A `PATCH` answered `409 {"error":"invalid_transition",…}` returns an error for which `errors.Is(err, ErrAlreadyExists)` is false. Evidence: `go test ./pkg/vaultapi/ -count=1` exits 0 with spec `PatchTask when the API refuses the transition reports the refusal, not a duplicate`.
- [ ] **AC2** — That same error satisfies `errors.Is(err, ErrRefused)` and `errors.As` into a typed refusal carrying `Code`, `From` and `To` from the body. Evidence: the same spec asserts `Code == "invalid_transition"`, `From == "done"`, `To == "human_review"`; the assertion fails on the pre-fix build.
- [ ] **AC3** — A `409 {"error":"task_exists",…}` still satisfies `errors.Is(err, ErrAlreadyExists)` and still carries the occupant's identifier and status. Evidence: the existing `client_create_test.go` create and readdress specs stay green under `go test ./pkg/vaultapi/ -count=1`.
- [ ] **AC4** — No 409 message names the operation twice. Evidence: a spec asserts `strings.Count(err.Error(), "patch task task-1") == 1` for both shapes.
- [ ] **AC5** — A 409 whose body does not parse, or carries no `error` field, keeps the previous contract (`ErrAlreadyExists`). Evidence: a spec posting a bare `conflict` body asserts `errors.Is(err, ErrAlreadyExists)` is true — the negative control for over-narrowing.
- [ ] **AC6** — `make precommit` passes at the change commit.

## Verification

Rungs, container-executable in order:

```
go test ./pkg/vaultapi/ -count=1     # AC1-AC5; must fail before the fix, pass after
make precommit                        # AC6
```

Repro replay: the `## Reproduction` stub test is part of the same package, so the pre-fix failure and the post-fix pass are the same command — run it on the base commit to see `task already exists`, then on the change commit to see the refusal.

## Constraints

- `ErrAlreadyExists`, `ErrAmbiguous`, `AlreadyExistsError` and `CreateTask`'s callers keep their signatures and behaviour; `task_create_task_executor.go` reads `AlreadyExistsError` fields and must not change.
- The new sentinel and type follow the repo's idiom: `var ErrRefused = stderrors.New("refused")`, typed value with `Unwrap()`.
- No new dependency.

## Assumptions

- vault-api keeps answering a refused transition with 409 rather than introducing a distinct status; the fix must not depend on a status change.

## Failure Modes

| Trigger | Expected behaviour | Recovery |
|---|---|---|
| vault-api adds a new refusal code | It arrives as `ErrRefused` with the code visible, not as "already exists" | None needed — the code is in the message |
| 409 body is not JSON | Falls back to `ErrAlreadyExists`, as today | None; the fallback is deliberate (AC5) |
| A caller keys on `ErrAlreadyExists` for a real create conflict | Unchanged | None |

## Security / Abuse Cases

The 409 body is HTTP input from another service: a malformed, truncated or hostile body must not panic or hang. Decode failure is handled by falling through to `AlreadyExistsError` (AC5), and no field from the body is used without a zero-value default.

## Do-Nothing Option

The mislabel stays. Each future refusal is again reported as a duplicate, and the next reader re-derives from the topic that the server said "invalid transition" — the cost already paid once, at roughly half a session of wrong-layer investigation.
