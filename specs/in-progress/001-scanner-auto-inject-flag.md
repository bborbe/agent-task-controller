---
status: prompted
tags:
    - dark-factory
    - spec
approved: "2026-06-27T21:07:10Z"
generating: "2026-06-27T21:29:41Z"
prompted: "2026-06-27T21:36:53Z"
branch: dark-factory/scanner-auto-inject-flag
---

## Summary

- Two replicas of `agent-task-controller` (dev + prod, same shared vault) raced today and both injected fresh UUIDs into the same UUID-less task file, producing quarantined `_conflicts/` files and one git merge-marker commit.
- Add a required environment flag `AUTO_INJECT_TASK_IDENTIFIER` that gates the scanner's three injection trigger sites; the process refuses to start if the variable is unset.
- When the flag is `false`, the scanner observes a UUID-less / non-UUID / duplicate task file and skips it (warn log + no write), matching the existing "invalid frontmatter" skip return shape.
- When the flag is `true`, current behavior is preserved — exactly one replica per shared vault gets `true`; all others get `false`.
- Eliminates the "two replicas mint, both push, git-rest quarantines" race without leader election, deterministic UUIDs, or a dev-only subdeployment.

## Problem

`agent-task-controller` is deployed twice against the same Personal vault (dev + prod replica of the same NAMESPACE-templated STS). Both replicas scan the vault on their own poll cycle. When an operator drops a task file without a `task_identifier`, both replicas hit `pkg/scanner/vault_scanner.go:261` within seconds of each other, each generates a fresh `uuid.New()`, each writes the file back through its local git-rest, and both push to the shared remote. Today's incident produced two quarantined `_conflicts/24 Tasks/...1782559254.md` files (originals gone) and one task committed with unresolved `<<<<<<<` markers (recovered as vault commit `fb3ad3cc1`). The race recurs for every operator-authored task file. The prior vault-cli hardening wave (`[[Concurrent vault-cli writes to legacy tasks cause git-rest merge conflicts]]`, vault-cli v0.79.0) closed the creation paths but explicitly left the scanner's backfill as a defensive net — and the scanner is the one write path that runs on two replicas at once.

## Goal

`agent-task-controller` exposes a single deployment-time decision — `AUTO_INJECT_TASK_IDENTIFIER` — that decides whether this replica is allowed to backfill missing/invalid `task_identifier` fields. Operators set exactly one replica per shared vault to `true`; all others get `false` and become observe-only for the backfill path. The variable is required, so a deployment that forgets to set it crashes immediately at startup instead of silently re-introducing the race.

## Non-goals

- Do NOT add a default value for `AUTO_INJECT_TASK_IDENTIFIER` — a default IS the regression we're preventing; if a future caller wants ergonomics, that's a separate spec.
- Do NOT remove vault-cli's `WriteTask` backfill — defensive net, decided in the prior wave.
- Do NOT change scanner behavior for files that already have a valid unique UUID `task_identifier` — that path is unchanged regardless of flag value.
- Do NOT change server-side git-rest quarantine/auto-resolve behavior — covered by earlier waves.
- Do NOT add an admission-webhook / launch-time peer-poll to detect "both replicas configured `true`" — future hardening, separate spec.
- Do NOT modify `~/Documents/workspaces/quant/vault/obsidian-personal/vault-obsidian-personal-sts.yaml` — different repo, handled as a same-day companion commit in `bborbe/quant`.
- Do NOT clean up the two `_conflicts/24 Tasks/...1782559254.md` files from today's incident — tracked as subtask #6 of the parent task.
- Do NOT eliminate the dev replica — dev consumers depend on the service.
- Do NOT add a `CHANGELOG.md` entry — this repo has no `CHANGELOG.md` (verified).
- Do NOT gate the `writeCounterReset` path (empty→named assignee transition at `pkg/scanner/vault_scanner.go:275`) — that path does not mint a `task_identifier`; AC7 pins this.

## Desired Behavior

1. The controller's config struct gains an `AUTO_INJECT_TASK_IDENTIFIER` boolean loaded via envconfig with `required:"true"` and no default. Starting the process without the variable set returns a config-load error and the process exits non-zero before any scan runs.
2. The flag value is plumbed from config through the existing factory wiring in `pkg/factory/` into the `vaultScanner` constructor, surfacing on the scanner as a private boolean field.
3. The three injection trigger sites in `pkg/scanner/vault_scanner.go` (currently lines 261 empty `taskID`, 264 non-UUID, 268 duplicate UUID) consult the flag before calling `injectAndStore`. When the flag is `true`, behavior is identical to today. When the flag is `false`, the scanner emits a warn log line and returns the same `(nil, "", false)` skip shape used by the "invalid frontmatter" sites at lines 240–256, without calling `writeFile`.
4. When the flag is `false` and a skip occurs, the warn log line contains the frozen substring `AUTO_INJECT_TASK_IDENTIFIER=false; skipping task without valid task_identifier: <relPath>`. This substring is load-bearing for the post-deploy probe greps and must not change without updating the verification ACs.
5. A skip caused by the disabled flag is observable enough for an operator to distinguish it from other skip reasons (invalid frontmatter, read failed, etc.) using only `kubectl logs | grep`.
6. The flag has no effect on the unrelated `writeCounterReset` path (empty→named assignee transition at line 275) — that path does not mint a `task_identifier` and is out of scope.

## Constraints

- Existing scanner public API and existing `vault_scanner_test.go` / `vault_scanner_internal_test.go` fixtures for the three trigger sites must continue to pass when the flag is `true` (parity is the regression bar).
- Existing factory signatures in `pkg/factory/factory.go` may grow a new parameter but must not lose existing parameters.
- The warn log substring `AUTO_INJECT_TASK_IDENTIFIER=false; skipping task without valid task_identifier:` is frozen — AC4 greps for it.
- The return shape for a flag-disabled skip must be exactly `(nil, "", false)` — matching the existing skip paths at lines 240–256; downstream code already handles this shape and must not need changes.
- `.dark-factory.yaml` stays at `autoRelease: false` — the post-deploy ACs depend on operator-driven `BRANCH=dev make buca` / `BRANCH=prod make buca`.
- Repo has no `CHANGELOG.md`; do not introduce one for this change.

## Failure Modes

| Trigger | Expected behavior | Recovery | Detection | Reversibility | Concurrency |
|---------|-------------------|----------|-----------|---------------|-------------|
| Pod starts with `AUTO_INJECT_TASK_IDENTIFIER` unset | envconfig returns error before scanner construction; process exits non-zero; `CrashLoopBackOff` in k8s | Operator sets the env var on the deployment and re-rolls | `kubectl get pod` shows `CrashLoopBackOff`; logs show envconfig required-field error | Reversible: re-deploy with var set | Safe — pod never reaches a state where it can write |
| Pod starts with `AUTO_INJECT_TASK_IDENTIFIER=true` on a replica that should be `false` (operator misconfig) | Scanner mints UUIDs; race re-emerges if another replica is also `true` | Operator sets to `false` on the wrong replica and re-rolls | git-rest commits show two near-simultaneous `update` commits adding `task_identifier`; quarantined `_conflicts/` files reappear | Reversible (config) but already-quarantined files require manual cleanup | This spec does NOT prevent the misconfig; future spec (admission webhook) covers it |
| Pod has `AUTO_INJECT_TASK_IDENTIFIER=false` and operator drops a UUID-less task file | Scanner warns and skips on every poll; file remains UUID-less indefinitely until the other replica (with `true`) backfills it, or operator runs vault-cli locally | None needed if the partner replica is healthy; if both replicas are `false`, operator runs vault-cli to inject locally | `kubectl logs | grep AUTO_INJECT_TASK_IDENTIFIER=false` shows recurring warns for the same `relPath` | Reversible: any UUID-injecting path (other replica, vault-cli) resolves it | Safe — skip is idempotent across replicas |
| Pod has `AUTO_INJECT_TASK_IDENTIFIER=false` and observes a duplicate-UUID file | Scanner warns and skips; file keeps the duplicate UUID until the other replica resolves it | Same as above | Same grep | Reversible | Safe |
| Flag is `true` and `writeFile` fails mid-write (e.g., git-rest quarantine) | Unchanged from today — `injectAndStore` returns `(nil, "", true)` (write error) at line 332 | Unchanged from today | Unchanged | Unchanged | Unchanged |
| Two replicas both set `true` and race on the same file (today's exact failure) | Same as today — race re-occurs | Operator fixes config | Same as today | Same as today | This spec mitigates by allowing operator to set only one replica `true`; does not eliminate misconfig |

## Security / Abuse Cases

Not applicable — the change is a single boolean env flag gating an internal scan/write path. No new user input, no new HTTP surface, no new trust boundary. The flag is operator-controlled at deploy time only.

## Acceptance Criteria

- [ ] **AC1 — Required env enforced.** A Ginkgo unit test loads the config via the existing config-loader factory in `pkg/factory/` with `AUTO_INJECT_TASK_IDENTIFIER` unset in the test environment and asserts that the returned error is non-nil. — evidence: `go test ./pkg/factory/...` exit code 0 with the new test name visible in `go test -v` output; failure mode of the test is the assertion `Expect(err).To(HaveOccurred())` matching an envconfig required-field error string containing `AUTO_INJECT_TASK_IDENTIFIER`.
- [ ] **AC2 — Scanner gate respects flag=true (parity).** A Ginkgo unit test constructs `vaultScanner` with `autoInject=true` and feeds three fixture files (one empty `task_identifier`, one non-UUID, one duplicate UUID). For each fixture, the mock `writeFile` is invoked exactly once and the written content contains a fresh UUID `task_identifier`. — evidence: `go test ./pkg/scanner/...` exit code 0; mock call counts asserted via the existing `mocks/` package style.
- [ ] **AC3 — Scanner gate respects flag=false (skip).** A Ginkgo unit test constructs `vaultScanner` with `autoInject=false` and feeds the same three fixtures. For each fixture: `writeFile` is invoked zero times, `processFile` returns `(nil, "", false)`, and the captured glog buffer contains the substring `AUTO_INJECT_TASK_IDENTIFIER=false; skipping task without valid task_identifier:` followed by the fixture's `relPath`. — evidence: `go test ./pkg/scanner/...` exit code 0; mock `writeFile.CallCount() == 0` asserted; glog capture matched with `ContainSubstring`.
- [ ] **AC4 — Post-Deploy (Rung-2):** `deploy_target: dev` — `deploy_check: kubectlquant -n dev describe deploy agent-task-controller | grep Image` shows the image SHA built by `BRANCH=dev make buca` from this PR's merge commit. Probe procedure: (1) `cd ~/Documents/Obsidian/Personal && touch "_test-probe-$(date +%s).md"` with frontmatter `page_type: task` + `status: in_progress` + NO `task_identifier`, commit + push to `obsidian-personal.git`; (2) wait 60s for one scanner poll; (3) capture evidence (step 4) BEFORE cleanup so a race during `rm` doesn't lose the log line; (4) `kubectlquant -n dev logs -l app=agent-task-controller --tail=200 | grep 'AUTO_INJECT_TASK_IDENTIFIER=false; skipping task without valid task_identifier: _test-probe-'` returns exactly one matching line referencing the probe file's `relPath`; (5) **Cleanup mandatory:** in the vault worktree run `rm _test-probe-<ts>.md && git add -u && git commit -m "test: cleanup scanner-flag probe" && git push`. — evidence: the grep in step 4 returns ≥1 matching line; AC4 asserts ONLY dev's gate behavior (whether prod's pod subsequently injects is AC5's concern, not AC4's).
- [ ] **AC5 — Post-Deploy (Rung-3):** `deploy_target: prod` — `deploy_check: kubectlquant -n prod describe deploy agent-task-controller | grep Image` shows the image SHA built by `BRANCH=prod make buca` from this PR's merge commit, AND the prod pod's env has `AUTO_INJECT_TASK_IDENTIFIER=true` (verify via `kubectlquant -n prod describe pod -l app=agent-task-controller | grep AUTO_INJECT_TASK_IDENTIFIER`). Same probe procedure as AC4 with the same capture-before-cleanup ordering. — evidence: `cd ~/Documents/Obsidian/Personal && git log --since='5 minutes ago' --grep='git-rest: update' -- ':(glob)_test-probe-*.md'` returns exactly ONE commit (from the prod pod) AND the probe file in the resulting tree contains a non-empty UUID `task_identifier` (`grep '^task_identifier:' _test-probe-*.md` returns one line with a UUID-shaped value), AND `ls _conflicts/_test-probe-*.md 2>/dev/null` returns nothing. **Cleanup mandatory** (after evidence captured): `rm _test-probe-<ts>.md && git add -u && git commit -m "test: cleanup scanner-flag probe" && git push`. If a `_conflicts/_test-probe-*.md` file appears (it should not — that would mean the race still happened), `rm` it too and flag the AC as failed. **Depends on companion commit in `bborbe/quant` adding the env vars to `vault-obsidian-personal-sts.yaml` (`true` on prod, `false` on dev) — without that, prod pod will crash on rolling update per the AC1 failure mode.**
- [ ] **AC6 — `make precommit` green.** — evidence: `make precommit` exit code 0 in the repo root.
- [ ] **AC7 — `writeCounterReset` path unaffected by flag.** A Ginkgo unit test constructs `vaultScanner` with `autoInject=false` and feeds a fixture representing a previously-parked task transitioning empty → named assignee (matching the `writeCounterReset` branch at `pkg/scanner/vault_scanner.go:275`: prior `fileEntry` has non-empty `taskIdentifier` and empty `assignee`; current frontmatter has a valid UUID `task_identifier` and a non-empty assignee). The mock `writeFile` is invoked exactly once for this fixture (the counter-reset write); no warn log containing `AUTO_INJECT_TASK_IDENTIFIER=false` is emitted. — evidence: `go test ./pkg/scanner/...` exit code 0; mock `writeFile.CallCount() == 1`; glog capture asserted NOT to contain the flag substring for this fixture.

Scenario coverage: NO new scenario. AC2 / AC3 / AC7 cover the gate logic at the unit level with mocked filesystem; AC4 and AC5 cover the end-to-end behavior via real-cluster post-deploy probes. No additional E2E scenario is justified — the unit tests cover the branch logic, and the post-deploy probes cover the integration of the env-var → scanner → git-rest path against real infrastructure.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Config + factory wiring: add `AUTO_INJECT_TASK_IDENTIFIER` to config struct with `envconfig:"AUTO_INJECT_TASK_IDENTIFIER,required"`; plumb through `pkg/factory/` to scanner constructor | DB1, DB2 | AC1, AC6 | — |
| 2 | Scanner gate + unit tests: gate the three injection trigger sites in `pkg/scanner/vault_scanner.go` (lines 261/264/268); emit frozen-substring warn log; preserve `(nil, "", false)` skip return shape; do NOT touch `writeCounterReset` at line 275 | DB3, DB4, DB5, DB6 | AC2, AC3, AC7 | prompt 1 |
| 3 | Post-deploy verification on dev + prod: probe procedures, log/git-log evidence capture, cleanup commits | — (verification only) | AC4, AC5 | code merged to master + image built via `BRANCH=dev/prod make buca` + companion `bborbe/quant` config commit landed |

Rationale: 3 layers benefit from independent commits — config wiring can land and be running before the scanner gate is ready (config-only PR is harmless because no code reads the new field yet), and post-deploy probes can't run until the image is in the cluster and the quant companion commit has rolled out the env vars.

## Verification

```
make precommit
go test -v ./pkg/factory/... ./pkg/scanner/...
```

Then post-merge:

```
# Rung-2 (dev)
cd ~/Documents/workspaces/agent-dev
git pull && git merge master
cd task/controller && BRANCH=dev make buca   # via /make-buca
# … run AC4 probe procedure …

# Rung-3 (prod) — only after the bborbe/quant companion commit lands
cd ~/Documents/workspaces/agent-prod
git pull && git merge master
cd task/controller && BRANCH=prod make buca
# … run AC5 probe procedure …
```

## Do-Nothing Option

The race recurs every time an operator drops a task file without a `task_identifier`. Today produced two quarantined files plus one committed-with-merge-markers file requiring manual recovery. Frequency is roughly one incident per operator-authored task burst (multiple per week). The recovery cost is manual git archaeology in the shared vault, with risk of permanent data loss for files quarantined twice. Not acceptable.
