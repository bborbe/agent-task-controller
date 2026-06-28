---
status: completed
spec: [001-scanner-auto-inject-flag]
summary: Added required AUTO_INJECT_TASK_IDENTIFIER env flag to application config, plumbed boolean into vaultScanner constructors
execution_id: agent-task-controller-scanner-flag-exec-001-spec-001-config-and-factory-wiring
dark-factory-version: v0.187.10-3-g508361f-dirty
created: "2026-06-28T00:00:00Z"
queued: "2026-06-27T22:36:59Z"
started: "2026-06-27T22:37:00Z"
completed: "2026-06-27T22:43:51Z"
---

<summary>

- The controller now refuses to start when the new required deployment-time auto-inject flag is not set in the environment, so a misconfigured pod crash-loops instead of silently re-introducing the dual-writer race.
- A single boolean config decides whether this replica is allowed to backfill missing or invalid task identifiers; the value is plumbed from config into the vault scanner so a later prompt can gate the three injection sites on it.
- Existing required env vars and existing factory wiring for the command consumer remain unchanged.
- Existing scanner constructor call sites in tests are updated mechanically to pass the new boolean — this is in scope because the spec explicitly allows factory signatures to grow a new parameter.
- No production behavior change at runtime when the flag is `true`; parity with today is the regression bar.

</summary>

<objective>

Introduce a required `AUTO_INJECT_TASK_IDENTIFIER` env flag on the `application` config, parse it once at startup so a missing/unparseable value aborts the process before any scan runs, and plumb the resulting boolean into the vault scanner as a private field via a new constructor parameter. When the flag parses to `true`, behavior is identical to today; the gate logic at the three injection sites is added by prompt 2.

</objective>

<context>

Read `/workspace/CLAUDE.md` for project conventions.
Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-patterns.md` for interface + constructor conventions.
Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` for `errors.Errorf(ctx, ...)` usage (never `fmt.Errorf`).
Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` for the Ginkgo/Gomega test layout.
Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-precommit.md` for file-level lint limits.

Read `/workspace/main.go` end-to-end. The `application` struct (lines 48-64) holds env-driven config parsed by the `github.com/bborbe/argument/v2` library. The scanner is constructed at line 106 via `scanner.NewGitRestVaultScanner(gitClient, a.TaskDir, a.PollInterval, trigger, metrics.New())`.

Read `/workspace/pkg/scanner/vault_scanner.go` end-to-end. The two public constructors are `NewVaultScanner` (line 98) and `NewGitRestVaultScanner` (line 119). Both currently take five parameters: `gitClient gitclient.GitClient, taskDir string, pollInterval time.Duration, trigger <-chan struct{}, m metrics.Metrics`. Both return the `VaultScanner` interface and both build a `&vaultScanner{...}` struct literal internally (lines 105 and 126). The struct definition is at lines 54-62.

Read `/workspace/main_internal_test.go` end-to-end. It demonstrates the pattern for reflecting on `application{}` field tags (`TestApplicationVaultNameFieldExists` at line 59 is the closest template — same `required:"true"` shape).

Read `/workspace/pkg/scanner/vault_scanner_test.go` lines 185, 502, 511, 531, 547, 947 — these are the six existing call sites of `scanner.NewVaultScanner` / `scanner.NewGitRestVaultScanner` in tests. They will each gain `true` as the new final argument (one-line mechanical edit).

Read `/workspace/pkg/scanner/vault_scanner_internal_test.go` line 39 — the only `&vaultScanner{...}` struct-literal in any test file (the external `_test` file at `vault_scanner_test.go` is `package scanner_test` and cannot access the unexported struct, so the literal cannot exist there). This literal gains `autoInject: true`.

Read `/workspace/pkg/factory/factory.go` end-to-end. It currently only contains `CreateCommandConsumer`. This prompt does NOT add a factory function for the scanner — see Requirement 3 for why the constructor-parameter approach is used instead.

Library note: `github.com/bborbe/argument/v2`'s `validateRequiredField` treats `case bool:` as a no-op (a `required:"true"` bool always passes validation because Go's zero value is `false`, and the env parser uses `strconv.ParseBool` which silently maps unset → `false`). The field MUST therefore be declared as `string` with `required:"true"`, parsed once via `strconv.ParseBool` at the start of `Run()`, with an explicit error when parsing fails.

</context>

<requirements>

1. **Add the required env field to `application` in `/workspace/main.go`.**
   - Field name: `AutoInjectTaskIdentifier`.
   - Type: `string` (NOT `bool` — see the library note in `<context>`; `required:"true"` on a bool is silently bypassed).
   - Struct tag (preserve the column alignment of the surrounding fields):
     ```
     AutoInjectTaskIdentifier string `required:"true"  arg:"auto-inject-task-identifier" env:"AUTO_INJECT_TASK_IDENTIFIER" usage:"allow this replica to backfill missing/invalid task_identifier fields (set true on exactly one replica per shared vault; false on all others); required"`
     ```
   - Place the field after `VaultName` (currently the last field) so config grouping stays clean.
   - Do NOT add a `default:` tag. Spec Non-goals bullet (verbatim quote): *"Do NOT add a default value for `AUTO_INJECT_TASK_IDENTIFIER` — a default IS the regression we're preventing"*.

2. **Parse the flag once at the start of `Run()` in `/workspace/main.go`.**
   - Insert immediately after `routing.ValidateVaultName(...)` at line 68 and BEFORE `libmetrics.NewBuildInfoMetrics().SetBuildInfo(...)` at line 71:
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
   - Add `"strconv"` to the stdlib import block (alphabetical position between `"os"` and `"time"`).
   - `errors` (`github.com/bborbe/errors`) is already imported — reuse it.
   - This single parsed `autoInject` value is passed to the scanner constructor in Requirement 5.

3. **Extend the two public scanner constructors with `autoInject bool` as a new trailing parameter.**
   - Spec Constraints bullet (verbatim quote): *"Existing factory signatures in `pkg/factory/factory.go` may grow a new parameter but must not lose existing parameters."* The same additive-signature rule is applied here to `NewVaultScanner` / `NewGitRestVaultScanner` because that is the minimal idiomatic Go change (a construction-time mutator on the public interface would pollute the API with a one-caller setter and force a counterfeiter regen).
   - In `/workspace/pkg/scanner/vault_scanner.go`:
     - Change `NewVaultScanner` signature to:
       ```go
       func NewVaultScanner(
           gitClient gitclient.GitClient,
           taskDir string,
           pollInterval time.Duration,
           trigger <-chan struct{},
           m metrics.Metrics,
           autoInject bool,
       ) VaultScanner {
       ```
     - Change `NewGitRestVaultScanner` signature identically (same new trailing `autoInject bool`).
     - In both constructor bodies, set `autoInject: autoInject` on the returned `&vaultScanner{...}` literal.

4. **Add the private `autoInject` field to the `vaultScanner` struct.**
   - In `/workspace/pkg/scanner/vault_scanner.go`, add `autoInject bool` as the last field of the struct (after `ops fileOps` at line 61):
     ```go
     type vaultScanner struct {
         gitClient    gitclient.GitClient
         taskDir      string
         pollInterval time.Duration
         hashes       map[string]fileEntry
         trigger      <-chan struct{}
         metrics      metrics.Metrics
         ops          fileOps
         autoInject   bool
     }
     ```
   - Do NOT add any method on `*vaultScanner` for this field — it is set at construction time. The gate logic in `processFile` is added by prompt 2.
   - Do NOT add `autoInject` to the public `VaultScanner` interface — it is a private construction parameter, not a public capability.
   - Do NOT regenerate `/workspace/mocks/vault_scanner.go` — the public interface (`Run`, `RunCycle`) is unchanged, so the existing counterfeiter mock is still valid.

5. **Update the scanner construction in `/workspace/main.go` (line 106) to pass `autoInject`.**
   - Replace:
     ```go
     scanner.NewGitRestVaultScanner(
         gitClient,
         a.TaskDir,
         a.PollInterval,
         trigger,
         metrics.New(),
     ),
     ```
     with:
     ```go
     scanner.NewGitRestVaultScanner(
         gitClient,
         a.TaskDir,
         a.PollInterval,
         trigger,
         metrics.New(),
         autoInject,
     ),
     ```
   - The five existing arguments stay in the same order; `autoInject` is the new trailing arg.

6. **Update the six existing scanner-constructor call sites in `/workspace/pkg/scanner/vault_scanner_test.go` to pass `true`.**
   - The call sites are at approximately lines 185, 502, 511, 531, 547, 947. Use `grep -n 'NewVaultScanner\|NewGitRestVaultScanner' pkg/scanner/vault_scanner_test.go` to find them mechanically.
   - For each call, append `, true` as the new last argument. Example:
     - Before: `scanner.NewVaultScanner(fakeGit, taskDir, time.Hour, nil, metrics.New())`
     - After:  `scanner.NewVaultScanner(fakeGit, taskDir, time.Hour, nil, metrics.New(), true)`
   - All six call sites exercise the existing inject behavior, so `true` is the correct value (parity with today).
   - This per-call-site update IS in scope — see the spec constraint quoted in Requirement 3.

7. **Update the one `&vaultScanner{...}` struct literal in `/workspace/pkg/scanner/vault_scanner_internal_test.go` (line 39) to add `autoInject: true`.**
   - Verify there is only one such literal in any test file by running `grep -rn '&vaultScanner{' pkg/scanner/` — the external `vault_scanner_test.go` is `package scanner_test` and cannot access the unexported struct, so no literal exists there.
   - Add `autoInject: true,` as a new key inside the `&vaultScanner{...}` literal (the existing fields are `metrics:` and `ops:`).

8. **Add a reflection-based field assertion test in `/workspace/main_internal_test.go`.**
   - Append (do NOT modify existing tests) a new test mirroring `TestApplicationVaultNameFieldExists` (line 59):
     ```go
     func TestApplicationAutoInjectTaskIdentifierFieldExists(t *testing.T) {
         typ := reflect.TypeOf(application{})
         f, ok := typ.FieldByName("AutoInjectTaskIdentifier")
         if !ok {
             t.Fatalf("application struct is missing AutoInjectTaskIdentifier field")
         }
         if f.Type.Kind() != reflect.String {
             t.Fatalf("AutoInjectTaskIdentifier must be string (required:\"true\" on bool is silently bypassed by the argument library), got %s", f.Type.Kind())
         }
         if got, want := f.Tag.Get("env"), "AUTO_INJECT_TASK_IDENTIFIER"; got != want {
             t.Errorf("AutoInjectTaskIdentifier env tag = %q, want %q", got, want)
         }
         if got, want := f.Tag.Get("arg"), "auto-inject-task-identifier"; got != want {
             t.Errorf("AutoInjectTaskIdentifier arg tag = %q, want %q", got, want)
         }
         if got, want := f.Tag.Get("required"), "true"; got != want {
             t.Errorf("AutoInjectTaskIdentifier required tag = %q, want %q", got, want)
         }
         if got := f.Tag.Get("default"); got != "" {
             t.Errorf("AutoInjectTaskIdentifier default tag = %q, want empty (no default per spec Non-goals)", got)
         }
     }
     ```

9. **Add a Ginkgo test that calls the real `argument.Parse` to verify the `required:"true"` boundary.**
   - File: `/workspace/main_test.go` is currently the `TestSuite` bootstrap in `package main_test`. Add a NEW test file `/workspace/main_argument_parse_test.go` in `package main_test` (do NOT modify `main_test.go`).
   - Purpose: exercise the actual `github.com/bborbe/argument/v2` library validator against the `application` struct with `AUTO_INJECT_TASK_IDENTIFIER` unset, asserting that parsing returns a non-nil error referencing the env var name. This covers AC1 at the real library boundary (the reflection test in Requirement 8 only covers struct shape; this one exercises the parser).
   - Skeleton:
     ```go
     package main_test

     import (
         "context"
         "os"

         libargument "github.com/bborbe/argument/v2"
         . "github.com/onsi/ginkgo/v2"
         . "github.com/onsi/gomega"

         pkgmain "github.com/bborbe/agent-task-controller"
     )
     ```
     Note: the `application` struct is unexported (`package main`). The test cannot import it directly. Instead, declare a local struct in the test that pins the same `env:"AUTO_INJECT_TASK_IDENTIFIER"` + `required:"true"` shape, then assert the library returns a non-nil error when the env var is unset:
     ```go
     var _ = Describe("AUTO_INJECT_TASK_IDENTIFIER required-tag enforcement (AC1)", func() {
         type appShape struct {
             AutoInjectTaskIdentifier string `required:"true" arg:"auto-inject-task-identifier" env:"AUTO_INJECT_TASK_IDENTIFIER" usage:"x"`
         }

         var saved string
         var hadValue bool

         BeforeEach(func() {
             saved, hadValue = os.LookupEnv("AUTO_INJECT_TASK_IDENTIFIER")
             Expect(os.Unsetenv("AUTO_INJECT_TASK_IDENTIFIER")).To(Succeed())
             // Also clear the CLI arg path: argument library reads os.Args; tests run with `go test` args only.
         })

         AfterEach(func() {
             if hadValue {
                 Expect(os.Setenv("AUTO_INJECT_TASK_IDENTIFIER", saved)).To(Succeed())
             }
         })

         It("returns a non-nil error from argument.Parse when the env var is unset", func() {
             var app appShape
             err := libargument.Parse(context.Background(), &app)
             Expect(err).To(HaveOccurred())
             Expect(err.Error()).To(ContainSubstring("AUTO_INJECT_TASK_IDENTIFIER"))
         })
     })
     ```
   - Drop the `pkgmain` import if unused. Confirm the correct argument-library import path by running `grep -rn 'github.com/bborbe/argument' /workspace/` — use whatever path the rest of the codebase uses (likely `github.com/bborbe/argument/v2`).
   - If the substring `AUTO_INJECT_TASK_IDENTIFIER` is not present in the library's required-field error message, fall back to asserting the error message contains the field name `AutoInjectTaskIdentifier` or the arg `auto-inject-task-identifier`. Run the test once and inspect the actual error string before committing — pick whichever substring the library actually emits.

10. **Do NOT touch `/workspace/pkg/scanner/vault_scanner.go` inside `processFile` or `injectAndStore`.** The three injection trigger sites (currently lines 261/264/268) are gated by prompt 2; this prompt only adds the field plumbing.

11. **Do NOT add a `CHANGELOG.md` entry.** Spec Non-goals bullet (verbatim quote): *"Do NOT add a `CHANGELOG.md` entry — this repo has no `CHANGELOG.md` (verified)."*

12. **Do NOT add a factory function in `/workspace/pkg/factory/factory.go`.** The constructor-parameter approach (Requirement 3) makes a separate factory wrapper redundant — it would be a one-line wrapper around `NewGitRestVaultScanner` with no business logic. `CreateCommandConsumer` stays exactly as it is today.

</requirements>

<constraints>

- Spec Non-goals bullets that apply: "Do NOT add a default value for `AUTO_INJECT_TASK_IDENTIFIER`"; "Do NOT change scanner behavior for files that already have a valid unique UUID `task_identifier`"; "Do NOT modify `~/Documents/workspaces/quant/vault/obsidian-personal/vault-obsidian-personal-sts.yaml`"; "Do NOT add a `CHANGELOG.md` entry".
- Spec Constraints bullet (verbatim quote): *"Existing scanner public API and existing `vault_scanner_test.go` / `vault_scanner_internal_test.go` fixtures for the three trigger sites must continue to pass when the flag is `true` (parity is the regression bar)."* — verified by running the existing suite unchanged after the six call-site edits in Requirement 6 and the one struct-literal edit in Requirement 7 (all pass `true` / `autoInject: true`).
- Spec Constraints bullet (verbatim quote): *"Existing factory signatures in `pkg/factory/factory.go` may grow a new parameter but must not lose existing parameters."* — `CreateCommandConsumer` keeps all eight existing parameters unchanged.
- Per `go-error-wrapping-guide.md`: errors wrap with `errors.Errorf(ctx, ...)` (never `fmt.Errorf`).
- Per `go-precommit.md`: file-level lint limits (funlen 80, nestif 4, golines 100) apply. The `Run()` function already carries `//nolint:funlen` at line 66 — adding the 7-line parse block stays within tolerance; do not remove the existing nolint directive.
- Do NOT commit — dark-factory handles git.
- All existing tests in `/workspace` must still pass after this prompt's changes.

</constraints>

<verification>

Run iteratively while implementing (fast feedback):

```
cd /workspace && go build ./...
cd /workspace && go test ./pkg/scanner/... -v
cd /workspace && go test ./... -run TestApplication -v
```

Run ONCE at the end:

```
cd /workspace && make precommit
```

Expected new test names in `go test -v` output (AC1):
- `TestApplicationAutoInjectTaskIdentifierFieldExists`
- `AUTO_INJECT_TASK_IDENTIFIER required-tag enforcement (AC1) > returns a non-nil error from argument.Parse when the env var is unset`

</verification>
