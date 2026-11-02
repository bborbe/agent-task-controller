---
status: completed
spec: [008-bug-task-identifier-non-uuid-loop]
summary: 'Added spec-008 fix: bullet under a new ## Unreleased section in CHANGELOG.md (above ## v0.7.3), verified placement and frozen substring, make precommit exits 0'
execution_id: agent-task-controller-identifier-convergence-exec-015-spec-008-changelog-and-gate
dark-factory-version: dev
created: "2026-09-05T10:48:59Z"
queued: "2026-09-05T11:23:57Z"
started: "2026-09-05T11:23:59Z"
completed: "2026-09-05T11:28:23Z"
branch: dark-factory/bug-task-identifier-non-uuid-loop
---

# CHANGELOG entry and build gate for the task_identifier convergence fix

<summary>

- The fix is recorded under a new `## Unreleased` section of the changelog with a `fix:` bullet naming the scanner's type-and-validity repair and the unbounded rewrite loop it closes.
- The bullet names the greppable repair log substring so operators can find the behavior change in release notes.
- The change is documentation-plus-verification only: no Go code is touched, and the changelog's existing version sections are left intact.
- `make precommit` is run once at the end as the final build gate, so the whole spec-008 change (prompt 1's fix plus this entry) lands lint-clean and test-green.

</summary>

<objective>

Add the spec-008 changelog entry under `## Unreleased` in `CHANGELOG.md` and run `make precommit` as the final acceptance gate (spec AC8/AC9). The entry must make `grep -n 'task_identifier' CHANGELOG.md` return a line inside `## Unreleased` describing the fix, above the first `## v` heading.

</objective>

<context>

There is no CLAUDE.md in this repo; the global YOLO container CLAUDE.md (already in your context) governs project conventions.

Read `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` for the entry format and anti-patterns.

Read `CHANGELOG.md`. The file currently has NO `## Unreleased` section — the top version heading is `## v0.7.2`. You add the `## Unreleased` section ABOVE `## v0.7.2` and append one `fix:` bullet to it. Do not create a second `## Unreleased`, do not modify any existing version section or bullet.

DEPENDENCY — verify prompt 1 shipped before writing the entry:

```
cd /workspace && grep -n 'replacing invalid task_identifier' pkg/scanner/vault_scanner.go
```

The type-and-validity fix (the `replacing invalid task_identifier` log line in `processFile`) MUST be in `pkg/scanner/vault_scanner.go`. If the grep returns no match, STOP and report `status: failed` with message `"task_identifier type-and-validity fix not yet deployed (prompt 1)"` — do not write a changelog entry for a fix that did not ship.

The `## Unreleased` placement check is done with a non-git position-aware awk (see `<verification>`). The spec's AC9 also references `git diff origin/master -- CHANGELOG.md | grep -E '^[-+]## '` — that comparison is host-side and is left to the operator on the spec's verification ladder. Do NOT run it here: the container may have no usable `.git` at all. This worktree's `.git` is a 125-byte pointer file rather than a directory, and it is masked whenever the daemon is launched with `--set hideGit=true` (the repo's `.dark-factory.yaml` does not set `hideGit`, and `workflow: direct` alone does not mask anything — the masking comes from the daemon launch flag). Because the daemon does not check verification exit codes, a git command that fails for either reason would silently produce a false-positive verification pass. In the container, enforce placement with the awk check only.

</context>

<requirements>

## 1. Add the changelog entry

Add to `CHANGELOG.md`, immediately above the `## v0.7.2` heading:

```markdown
## Unreleased

- fix: stop the vault scanner's task_identifier backfill from rewriting a file every scan cycle — a present-but-invalid `task_identifier` (unquoted integer, empty or whitespace string, sequence, mapping, block scalar, or any non-UUID spelling) is now stripped from the frontmatter before a fresh UUID is injected, converging in exactly one write instead of appending a UUID and keeping the bad key forever (2026-09-05: one file grew to 3007 keys / 163 KB over 50 hours); removal is frontmatter-region-scoped, key-aware (quoted/spaced key spellings), and span-aware (block-style values), and the repair log is greppable as `replacing invalid task_identifier` naming the value's Go type (spec 008)
```

Requirements for the bullet (per `changelog-guide.md` and spec AC9):
- The literal substring `task_identifier` must appear (AC9's `grep -n 'task_identifier' CHANGELOG.md` evidence).
- The frozen substring `replacing invalid task_identifier` must appear — the same string operators grep from `kubectl logs` after deploy.
- One logical change, one bullet. Do not split into multiple bullets; do not add bullets for anything else.
- Do not change any existing version section, heading, or bullet. Do not touch the `# Changelog` preamble.

## 2. Self-check before finishing

Before you finish, re-run `<verification>` and confirm it passes; confirm the changelog entry sits under `## Unreleased` (above the first `## v` heading) and that prompt 1's `pkg/scanner/vault_scanner.go` change is present (per the `<context>` dependency grep).

</requirements>

<constraints>

- Do NOT commit — dark-factory handles git.
- The only file this prompt AUTHORS is `CHANGELOG.md`. Two allowances follow from the build gate and neither is a licence for feature work:
  - `make precommit` runs `make format`, which runs goimports-reviser and `golines --max-len=100 -w` over every non-vendor `.go` file. That rewrites Go files in place on EVERY run, green or not. This is expected and allowed — do not try to avoid, revert, or work around it.
  - If `make precommit` reports a real lint/vet/test failure ORIGINATING IN prompt 1's `pkg/scanner/` change, you MAY make the minimal `pkg/scanner/` fix needed to turn that target green. No other Go change: no new features, no refactors, no edits outside `pkg/scanner/`, no changes to tests other than the minimum to make them compile and pass as written.
- Do NOT create a second `## Unreleased` section, and do NOT modify any existing version section or bullet.
- One `fix:` bullet only, prefix required, specific (never `- fix: fix bug`).
- The landing mechanism is a manually opened PR to `master` (`.dark-factory.yaml` is `workflow: direct`, `pr: false`, `autoMerge: false`) — do not attempt any git/gh/PR steps in this container.
- Do NOT run `kubectl*`, `docker`, `make buca`, `make build`, or any operator/deploy command — the Rung-3 post-deploy verification lives on the spec's operator-executable verification rung, not here.
- Do NOT run bare `git` commands — the container has no working `.git` (masked), and a failed git command would produce a false-positive verification pass.

</constraints>

<verification>

Run iteratively while implementing (fast loop, repo root):

```
cd /workspace && grep -n 'task_identifier' CHANGELOG.md || true
```

Position-aware placement check (spec AC9): the `task_identifier` line must appear ABOVE the first `## v` heading. Non-git equivalent of the spec's `git diff origin/master` check (the container has no working `.git`):

```
cd /workspace && awk '/^## v/{exit} /task_identifier/{f=1} END{exit !f}' CHANGELOG.md
```

Expect exit 0 (the new `## Unreleased` bullet containing `task_identifier` appears above the first `## v` heading; it exits 1 today). Note: `grep -n 'task_identifier' CHANGELOG.md` is wrapped in `|| true` because `grep` exits 1 when it finds nothing.

Frozen-substring presence in the bullet:

```
cd /workspace && grep -c 'replacing invalid task_identifier' CHANGELOG.md || true
```

Expect `1`.

Run ONCE at the end (spec AC8 — `make precommit` exits 0 at repo root):

```
cd /workspace && make precommit
```

Expected: exit 0 with the full Ginkgo suite green — including prompt 1's new spec-008 specs and the unmodified parity test.

If `make precommit` fails, iterate on the SPECIFIC failing target (`make lint`, `make vet`, `make test`) rather than re-running the whole chain, and apply only the minimal `pkg/scanner/` fix permitted by `<constraints>`. Only re-run full `cd /workspace && make precommit` once the individual targets pass. Note that `make precommit` runs `make format` first, so Go files being reformatted in place on every run is expected, not a failure.

NEVER loosen, weaken, or delete the `Equal(9)` parity assertions in `pkg/scanner/vault_scanner_test.go` to make a target green — if the parity test fails, the fix belongs in the gate structure of `processFile`, not in the assertion.

</verification>
