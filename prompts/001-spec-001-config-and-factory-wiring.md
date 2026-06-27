---
status: draft
spec: [001-scanner-auto-inject-flag]
created: "2026-06-27T21:30:00Z"
branch: dark-factory/scanner-auto-inject-flag
---

<summary>

- The controller now refuses to start if `AUTO_INJECT_TASK_IDENTIFIER` is not set in the environment.
- A new required deployment-time env flag is added to the application config (no default value, no implicit zero).
- A new factory function in `pkg/factory/` constructs the vault scanner with the flag, replacing the direct `scanner.NewGitRestVaultScanner` call in `main.go`.
- The scanner gains a private `autoInject` boolean field that prompt 2 will gate the three injection trigger sites on.
- Existing required env vars (`LISTEN`, `KAFKA_BROKERS`, etc.) and all factory signatures for the command consumer remain unchanged.
- Existing scanner tests continue to pass without modification (they use `NewVaultScanner`/`NewGitRestVaultScanner` directly with no `autoInject` arg; this prompt makes the parameter optional by defaulting to `true` when callers omit it).
- No production behavior change at runtime when `AUTO_INJECT_TASK_IDENTIFIER=true` — parity is the regression bar.
</summary>

<objective>

Introduce the required `AUTO_INJECT_TASK_IDENTIFIER` env flag into `application`, plumb it through a new factory function that constructs the vault scanner, and surface it on `vaultScanner` as a private boolean field. The flag must be required (no default), so a missing env var crashes the process before any scan runs. When set to `true`, behavior is identical to today; the gate logic is added by prompt 2.

</objective>

<context>

Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-patterns.md` for interface + constructor + struct conventions.
Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-factory-pattern.md` for `Create*` factory naming and zero-logic rule.
Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` for test layout (external test packages, Ginkgo/Gomega).
Read `/workspace/main.go` end-to-end. The `application` struct at lines 48–64 holds the env-driven config and uses the `bborbe/argument` library for parsing — `required:"true"` enforces presence for string fields.
Read `/workspace/pkg/scanner/vault_scanner.go` end-to-end. The `vaultScanner` struct at line 54 holds scanner fields; `NewVaultScanner` (line 98) and `NewGitRestVaultScanner` (line 119) are the only public constructors.
Read `/workspace/pkg/factory/factory.go` end-to-end. It currently only contains `CreateCommandConsumer`; the spec asks for scanner wiring to live here too.
Read `/workspace/pkg/metrics/metrics.go` to understand the `Reason*` constants and `init()` pre-initialization (lines 147–210). A new `ReasonAutoInjectDisabled` will be added in prompt 2 — this prompt does NOT touch metrics.
Read `/workspace/main_internal_test.go` to understand the existing pattern for asserting `application` struct field tags via reflection. The same pattern is used here to assert `AutoInjectTaskIdentifier` is wired correctly.

The library `github.com/bborbe/argument/v2` at `/home/node/go/pkg/mod/github.com/bborbe/argument/v2@v2.12.27/argument_validate.go` line 110 shows that `case bool:` in `validateRequiredField` is empty — a `required:"true"` bool field always passes validation because Go's zero value is `false`. The env parser at `argument_env.go:60` uses `strconv.ParseBool`, so unset bools default to `false` and never error. **Therefore the field MUST be declared as `string` with `required:"true"`, parsed to bool inside `Run()` via `strconv.ParseBool`, with an explicit error when the env var is unset or unparseable.** This is a critical correctness constraint — do not declare it as `bool`.

</context>

<requirements>

1. **Add the required env field to `application` in `/workspace/main.go`.**
   - Field name: `AutoInjectTaskIdentifier string`.
   - Field type: `string` (NOT `bool` — see context for why `required:"true"` on bool is silently bypassed).
   - Struct tags (preserving alignment style of the surrounding fields):
     ```
     AutoInjectTaskIdentifier string `required:"true"  arg:"auto-inject-task-identifier" env:"AUTO_INJECT_TASK_IDENTIFIER" usage:"allow this replica to backfill missing/invalid task_identifier fields (set true on exactly one replica per shared vault; false on all others); required"`
     ```
   - Place the field after `VaultName` (last field in the current struct) to keep config-grouping clean.
   - Do NOT add a `default:` tag. The spec Non-goal #1 forbids it.

2. **Parse and validate the field at the start of `Run()` in `/workspace/main.go`.**
   - Immediately after `routing.ValidateVaultName(...)` (line 68) and BEFORE `libmetrics.NewBuildInfoMetrics().SetBuildInfo(...)` (line 71), add:
     ```go
     autoInject, err := strconv.ParseBool(a.AutoInjectTaskIdentifier)
     if err != nil {
         return errors.Errorf(
             ctx,
             "AUTO_INJECT_TASK_IDENTIFIER must be parseable as bool (true/false), got %q",
             a.AutoInjectTaskIdentifier,
         )
     }
     ```
   - Add `"strconv"` to the imports block if not already present.
   - The existing `errors.Errorf` import is already present — use it (per `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`, never use `fmt.Errorf` in `pkg/`-style code).

3. **Add a factory function in `/workspace/pkg/factory/factory.go` that constructs the vault scanner with the flag plumbed in.**
   - Function signature:
     ```go
     func CreateVaultScanner(
         gitClient gitclient.GitClient,
         taskDir string,
         pollInterval time.Duration,
         trigger <-chan struct{},
         m metrics.Metrics,
         autoInject bool,
     ) scanner.VaultScanner
     ```
   - Import paths (add to the import block):
     - `"time"` (stdlib)
     - `"github.com/bborbe/agent-task-controller/pkg/metrics"`
     - `"github.com/bborbe/agent-task-controller/pkg/scanner"`
   - Body: thin wrapper — call `scanner.NewGitRestVaultScanner(gitClient, taskDir, pollInterval, trigger, m)` and set `autoInject: autoInject` on the returned struct via type assertion. The factory has zero business logic per `/home/node/.claude/plugins/marketplaces/coding/docs/go-factory-pattern.md`.
   - Implementation note: since `vaultScanner` is unexported and `NewGitRestVaultScanner` returns the `VaultScanner` interface, the factory cannot directly set the private `autoInject` field. Either:
     - (a) Expose a package-private `WithAutoInject` setter in `pkg/scanner/` (low-risk: package-private, scoped to tests + factory), OR
     - (b) Add a private-package constructor `newVaultScannerWithAutoInject(...)` that takes the flag and call that from the factory.
   - Pick option (b): it keeps the public `NewGitRestVaultScanner` signature unchanged (preserving the existing `vault_scanner_test.go` and `vault_scanner_internal_test.go` fixtures that pass `nil` for the trigger channel — adding a new required parameter to the public function would break them and force prompt 1 to also touch test files, which violates the spec's "config + factory wiring" scope).
   - Concrete shape:
     ```go
     func newVaultScannerWithAutoInject(
         gitClient gitclient.GitClient,
         taskDir string,
         pollInterval time.Duration,
         trigger <-chan struct{},
         m metrics.Metrics,
         autoInject bool,
     ) scanner.VaultScanner {
         return scanner.NewGitRestVaultScanner(gitClient, taskDir, pollInterval, trigger, m) //nolint:staticcheck // see test coverage
     }
     ```
     But this returns a `VaultScanner` interface, not a `*vaultScanner`. So **instead, do this in `pkg/scanner/`** (not in `pkg/factory/`):
     - In `/workspace/pkg/scanner/vault_scanner.go`, add a private constructor `newVaultScannerWithAutoInject` that returns `*vaultScanner` (the concrete type). Place it next to `NewVaultScanner`/`NewGitRestVaultScanner`.
     - The factory in `pkg/factory/` then calls this private constructor — but it can't, because it's in a different package.
   - **Revised correct shape**: The factory does the wiring by calling `NewGitRestVaultScanner` and then exposing a setter. Add a new exported method to `VaultScanner` interface in `pkg/scanner/vault_scanner.go`:
     ```go
     type VaultScanner interface {
         Run(ctx context.Context, results chan<- ScanResult) error
         RunCycle(ctx context.Context, results chan<- ScanResult)
         SetAutoInject(enabled bool)
     }
     ```
     Add the method on `*vaultScanner` (assigns `v.autoInject = enabled`). Regenerate the counterfeiter mock at `/workspace/mocks/vault_scanner.go` via `go generate ./pkg/scanner/...` (the existing `//counterfeiter:generate -o ../../mocks/vault_scanner.go --fake-name VaultScanner . VaultScanner` directive will pick it up).
   - **Why this shape**: it preserves all existing callers (`NewVaultScanner`, `NewGitRestVaultScanner` keep their signatures; existing tests don't need to change), and lets the factory set the flag post-construction.
   - The factory body becomes:
     ```go
     func CreateVaultScanner(
         gitClient gitclient.GitClient,
         taskDir string,
         pollInterval time.Duration,
         trigger <-chan struct{},
         m metrics.Metrics,
         autoInject bool,
     ) scanner.VaultScanner {
         vs := scanner.NewGitRestVaultScanner(gitClient, taskDir, pollInterval, trigger, m)
         vs.SetAutoInject(autoInject)
         return vs
     }
     ```

4. **Add the private `autoInject` field to `vaultScanner` in `/workspace/pkg/scanner/vault_scanner.go`.**
   - Add `autoInject bool` as a new field on the `vaultScanner` struct (after `ops fileOps` at line 61).
   - Initialize it to `true` in BOTH `NewVaultScanner` (line 105) and `NewGitRestVaultScanner` (line 126). Default `true` is critical — it preserves today's behavior for all existing test fixtures and any external callers (the parity regression bar).
   - Add the `SetAutoInject` method on `*vaultScanner`:
     ```go
     func (v *vaultScanner) SetAutoInject(enabled bool) {
         v.autoInject = enabled
     }
     ```

5. **Regenerate the counterfeiter mock.**
   - Run `cd /workspace && go generate ./pkg/scanner/...`.
   - This regenerates `/workspace/mocks/vault_scanner.go` with a `SetAutoInject` no-op method on the fake.
   - Verify `/workspace/mocks/vault_scanner.go` now contains `func (fake *VaultScanner) SetAutoInject(enabled bool)` (or equivalent).

6. **Replace the direct scanner construction in `/workspace/main.go` (line 106).**
   - Replace `scanner.NewGitRestVaultScanner(...)` at line 106 with `factory.CreateVaultScanner(..., autoInject)`.
   - Pass the locally-parsed `autoInject` bool (from step 2) as the last argument.
   - The five existing arguments (`gitClient`, `a.TaskDir`, `a.PollInterval`, `trigger`, `metrics.New()`) stay in the same order. Adding `autoInject` at the end is non-breaking.

7. **Add a unit test that asserts `AutoInjectTaskIdentifier` is wired with the required tag.**
   - File: `/workspace/main_internal_test.go` (append, do NOT modify existing tests).
   - Pattern mirrors the existing `TestApplicationBuildGitVersionFieldExists` (line 12) and `TestApplicationVaultNameFieldExists` (line 59). Use the same `reflect.TypeOf(application{})` + `FieldByName` style.
   - Test name: `TestApplicationAutoInjectTaskIdentifierFieldExists`.
   - Assertions:
     - `f.Tag.Get("env") == "AUTO_INJECT_TASK_IDENTIFIER"`
     - `f.Tag.Get("required") == "true"`
     - `f.Tag.Get("default") == ""` (no default — explicit guard against regression to default)
     - `f.Type.Kind() == reflect.String` (the bool-not-allowed invariant)
   - This covers AC1's "process refuses to start if unset" by pinning the field shape that the `argument` library uses for `required:"true"` enforcement.

8. **Add a Ginkgo unit test that loads config without `AUTO_INJECT_TASK_IDENTIFIER` and asserts the error.**
   - This covers AC1's "process refuses to start if unset" behaviorally, not just structurally.
   - File: `/workspace/main_test.go` (the existing `TestSuite` test file in `package main_test`). Append a new `Describe("config AUTO_INJECT_TASK_IDENTIFIER", ...)` block.
   - The test sets `os.Unsetenv("AUTO_INJECT_TASK_IDENTIFIER")` in `BeforeEach` (cleanup via `os.Setenv(..., "")` in `AfterEach` to avoid polluting other tests).
   - It invokes `argument.Parse(ctx, &app)` directly (the library used by `service.Main`). If parsing succeeds, fail. If parsing fails, assert the error message contains the substring `"AUTO_INJECT_TASK_IDENTIFIER"`.
   - Verify the `argument` package is importable from `main_test.go` — if not (because it's `package main_test` external test), use the import path `"github.com/bborbe/argument/v2"`.

9. **Do NOT modify `/workspace/pkg/scanner/vault_scanner.go` gate logic.** This prompt only adds the field, setter, and counterfeiter mock. The actual gate logic at the three injection trigger sites (lines 261, 264, 268) is prompt 2's job.

10. **Do NOT add a `CHANGELOG.md` entry.** The spec Non-goal #9 explicitly forbids it (this repo has no `CHANGELOG.md`).

</requirements>

<constraints>

- Spec Non-goals #1 (no default), #4 (no git-rest changes), #5 (no admission webhook), #7 (no dev replica removal), #9 (no CHANGELOG.md) all apply.
- Spec Constraint #52 ("existing factory signatures may grow a new parameter but must not lose existing parameters") applies — `CreateCommandConsumer` keeps all 8 existing parameters unchanged. The new factory is additive.
- Spec Constraint #53 (frozen log substring) applies to prompt 2, not this one.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass: `go test ./...` in `/workspace` must succeed after this prompt's changes.
- When adding a method to an exported interface (`VaultScanner`), regenerate the counterfeiter mock so production code and mock code stay in sync. Failing to regenerate causes `go build` to fail in any test file that uses the mock.
- Per `/home/node/.claude/plugins/marketplaces/coding/docs/go-patterns.md`: errors wrap with `errors.Errorf(ctx, ...)` (never `fmt.Errorf`); no `pkg.Function()` calls from business logic; small interfaces (1-2 methods per concern — `SetAutoInject` is a single-method extension, acceptable).
- Per `/home/node/.claude/plugins/marketplaces/coding/docs/go-factory-pattern.md`: factory has zero business logic (no loops, no conditionals) — the body is two function calls and a return.

</constraints>

<verification>

Run iteratively while implementing (fast feedback):

```
cd /workspace && go test ./...
cd /workspace && go vet ./...
cd /workspace && go generate ./pkg/scanner/...
```

Run ONCE at the end:

```
cd /workspace && make precommit
```

Expected new test names in `go test -v` output (AC1):
- `TestApplicationAutoInjectTaskIdentifierFieldExists`
- The new `Describe("config AUTO_INJECT_TASK_IDENTIFIER")` Ginkgo `It(...)` block

</verification>