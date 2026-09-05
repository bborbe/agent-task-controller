# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## Unreleased

- feat: refuse any `task_identifier` repair write that would not clear its own trigger — before persisting, the vault scanner re-evaluates the candidate bytes through the exact read-path pipeline the next cycle uses (frontmatter extraction, last-wins deduplication, YAML unmarshal) and requires the resolved `task_identifier` to be a string equal to the freshly minted UUID. A repair failing this convergence check writes nothing, commits nothing, logs one ERROR line `task_identifier repair did not converge, halting repair for: <path>`, and increments `agent_controller_vault_scanner_skipped_files_total{reason="repair_not_converging"}`; candidate bytes that cannot be parsed are treated as non-converging (fail-closed). The halt is keyed on the file's on-disk content hash, so it re-arms exactly once per distinct file state and clears itself as soon as any writer changes the file, and halt bookkeeping never leaks an empty task identifier into the deleted-tasks stream. The guard is unconditional and root-cause-agnostic: it bounds any non-converging repair — including causes nobody has enumerated — to one log line and one counter increment instead of an unbounded rewrite loop against the shared vault repository (spec 009)

## v0.7.4

- fix: stop the vault scanner's task_identifier backfill from rewriting a file every scan cycle — a present-but-invalid `task_identifier` (unquoted integer, empty or whitespace string, sequence, mapping, block scalar, or any non-UUID spelling) is now stripped from the frontmatter before a fresh UUID is injected, converging in exactly one write instead of appending a UUID and keeping the bad key forever (2026-09-05: one file grew to 3007 keys / 163 KB over 50 hours); removal is frontmatter-region-scoped, key-aware (quoted/spaced key spellings), and span-aware (block-style values), and the repair is greppable from logs alone via two substrings — `replacing non-UUID task_identifier` quoting the offending value for string-shaped values, and `replacing invalid task_identifier of type` naming the Go type for every other shape, so a large sequence or mapping cannot blow up a log line (spec 008)

## v0.7.3

- fix: `Dockerfile` `ARG DOCKER_REGISTRY` default now points at `docker.prod.nuke.benjamin-borbe.de:443` instead of the decommissioned `docker.quant.benjamin-borbe.de:443`. The default is inert in CI (the build passes `DOCKER_REGISTRY` explicitly), but a local `docker build` with no override silently targets a dead host. Matches the convention already applied in `agent-task-executor` and `github-update-go-agent`.

## v0.7.2

- chore: update github.com/bborbe/agent to v0.87.0, github.com/bborbe/argument/v2 to v2.13.2, github.com/bborbe/boltkv to v1.15.2, github.com/bborbe/cqrs to v0.6.10, github.com/bborbe/kafka to v1.25.11, github.com/bborbe/kv to v1.21.13, github.com/bborbe/metrics to v0.6.1, github.com/bborbe/run to v1.10.2, github.com/bborbe/sentry to v1.10.1, github.com/bborbe/service to v1.10.11, github.com/bborbe/time to v1.27.12, github.com/bborbe/vault-cli to v0.121.3

## v0.7.1

- fix: stop result writes from clobbering operator edits — `assignee`/`previous_assignee` are now operator-owned in the frontmatter merge (the on-disk value always wins over a stale spawn-time snapshot, an incoming value may introduce an absent key, and an incoming empty `assignee` is always honored as the deliverer's Failed/needs_input clear), and the body is merged by section instead of replaced wholesale — an on-disk `## Parked` and other operator-authored headings survive every write, same-named headings are replaced by the fresh incoming content, and the on-disk preamble survives when the incoming body starts with a heading (spec 007)

## v0.7.0

- feat: route the atomic frontmatter commands (`update-frontmatter`, `increment-frontmatter`, `complete-task`) by target vault — each executor now applies `pkg/routing.ShouldProcessFrontmatterCommand` before the task-file lookup. A non-empty `TargetVault` (lib v0.86.0) differing from `VAULT_NAME` is skipped with one `glog.V(2)` line, no metric increment, no git write, and an error wrapping `cdb.ErrCommandObjectSkipped` (a nil return would publish a spurious Success event on the shared result topic); an empty `TargetVault` falls through so legacy unstamped commands keep working (deliberately not defaulted to `openclaw`, which would route legacy personal-vault tasks to the wrong controller permanently). Eliminates the false `AgentControllerResultNotFound` alerts from cross-vault frontmatter traffic (10 drops observed 2026-09-03 on nuke-prod)
- feat: heal-on-write stamps `target_vault` — every write path that touches a task file (result write-back, update-frontmatter, increment-frontmatter, complete-task, and both planning-retry-gate writes) records the writing controller's `VAULT_NAME` onto files lacking the key via `result.HealTargetVault`, never overriding an existing value. A legacy unstamped file falls through to both controllers exactly once: the owner writes and stamps it, after which `ShouldProcessResult`/`ShouldProcessFrontmatterCommand` return false for the non-owner forever (permanently stops double-scanning legacy files; the supersede prior-file write intentionally does not heal)

## v0.6.7

- fix: stop permanently losing results when two different `task_identifier`s collide on one title path — when a `create-task` finds its title path occupied by a live task of a DIFFERENT identifier (a filename collision between two distinct tasks, not a re-publish of the same one), the create now disambiguates the path with a short-identifier suffix (`{Title} - {id[:8]}.md`) and materializes the task anyway, instead of returning `ErrTaskAlreadyExists` and orphaning the losing identifier. Previously the loser never got a file, so `WriteResult`'s bounded git-rest-lag retry could never resolve it and every result for that identifier was dropped silently forever (the `not_found` skip) at ~88/hour on prod. Same-identifier re-publishes keep the existing benign `ErrTaskAlreadyExists` outcome (idempotent, nothing lost), terminal-status files keep their reopen / recurring-hold semantics, and unreadable occupiers still fail closed. Also bumps `golang.org/x/crypto` v0.55.0 → v0.56.0 (GO-2026-6354/GO-2026-6355 ssh DoS) to clear the vulncheck gate.

## v0.6.6

- fix: recover task results lost to git-rest pull lag — `WriteResult` now retries the task-file lookup up to 3 times with a 1s pause before giving up, instead of dropping the result on the first miss. The controller lists files over git-rest's HTTP API rather than a local clone, and git-rest pulls on a timer, so a result can arrive before the file it belongs to is visible; that result was previously discarded permanently (warning + `results_written{result="not_found"}` + `nil`, so the Kafka offset advanced regardless). The retry is deliberately in-process rather than an error return: the consumer runs `SkipCorruptBatches:false` on an offset consumer, so erroring on a permanently-missing file (deleted or renamed task) would block the partition and halt the controller. Give-up behaviour and the `not_found` label are unchanged, so the `AgentControllerResultNotFound` alert keeps working. A recovery logs `task file for identifier X resolved on attempt N of 3` unconditionally — the only signal separating transient lag from permanent loss, and what the alert's provisional threshold should be tuned from.

## v0.6.5

- docs: correct the ownership-guard scope in `docs/controller-design.md` — v0.6.3 stated the scanner's Empty-to-Named Reset is the only mechanism that may lower a controller-owned counter, which is not accurate: `MergeFrontmatter` has a single call site (the result write-back path), so the atomic frontmatter commands write to disk without passing the guard at all. The section now states what the guard does and does not cover, why that separation is deliberate, and both legitimate counter-lowering paths (the Empty-to-Named Reset, and a trigger-scope reset writing `trigger_count: 1` on scope change). Also documents why the guard's value comparison uses `frontmatterValueEqual` rather than `==`, which panics on uncomparable `any` values decoded from YAML/JSON

## v0.6.4

- fix: `FindTaskFilePath` now errors when two task files share one `task_identifier`, instead of silently keeping the last match from an unsorted `ListFiles`. Picking either file writes an agent's result onto a file that may belong to a different task. Observed 2026-08-31: two Schedule CRs sharing a name across the dev and prod fleets minted the same UUID5 (`recurring-task-creator` derives the identifier from the CR name, and both fleets publish into one vault), so `Sentry Alert Fan-Out - 2026-08-31` and `Daily Sentry Triage - 2026-08-31` both carried `a1651737-…`; the controller logged `matched file 24 Tasks/Sentry Alert Fan-Out - 2026-08-31.md for task a1651737`, writing a result addressed to `Daily Sentry Triage` onto the fan-out's file and marking it `phase: done` / `status: completed` with every Success Criteria box still unticked. The error names both colliding paths and returns an empty path so no caller writes. All four call sites already propagate. The producer-side collision is fixed separately in bborbe/nuke#137.
## v0.6.3

- docs: document the frontmatter field-ownership contract in `docs/controller-design.md` — the `## Frontmatter Merge` section described blanket agent precedence, which v0.6.2 replaced; it now carries an ownership table (controller-owned `trigger_count`/`retry_count`, the terminal `status` pin, agent-owned everything else), a merge example demonstrating all three rules, the terminal short-circuit out of the escalation machinery, and the `ownership guard kept on-disk` log plus the Empty-to-Named Reset as the only counter-lowering path (spec 006)

## v0.6.2

- fix: stop result writes from rolling back controller-owned state — the result writer now keeps the on-disk `trigger_count`/`retry_count` (an incoming payload can never reset them, so the trigger/retry caps compare against real spawn counts) and pins a terminal on-disk `status` (`aborted`/`completed`), so an operator abort survives every publish and a pinned-terminal task no longer accrues escalation sections (spec 006)

## v0.6.1

- fix: recurring-task instances never reopen a terminal task file — a `create-task` from the `recurring-task-creator` publisher now holds the title path when the existing file's status is `completed`/`aborted` (the title-path file IS the recurring dedup state), so an hourly always-fire tick can no longer overwrite a completed recurring task with a blank `in_progress` instance; non-recurring per-alert commands keep the v0.6.0 reopen behavior
- fix: non-reopen creates use a create-only write — the create path now calls git-rest's `POST ?create_only=1` (`AtomicWriteIfAbsentAndCommitPush`/`PostIfAbsent`), and a 409 Conflict is mapped to the benign `ErrTaskAlreadyExists` (no write, no git commit) instead of overwriting; the pre-write title-path read stays only as a fast-path, never authoritative, so a falsely-free read can no longer lead to an overwrite

## v0.6.0

- feat: `create-task` reopens a title path held by a terminal task — a path whose existing file has frontmatter status `completed` or `aborted` is treated as free, so a create command materializes a fresh non-terminal task at that path instead of returning `ErrTaskAlreadyExists`; any non-terminal status, absent/empty/unknown status, missing frontmatter delimiters, or unparseable YAML still holds the path (dedup decision moved from file existence to the existing task's status)
- fix: make terminal-task reopens operator-visible — a create-task that materializes a fresh task over a `completed`/`aborted` file now emits an unconditional `create-task: reopening terminal task` INFO log (naming the path and prior status) at default verbosity and commits with a distinct `[agent-task-controller] reopen terminal task <id>` message, so a reopen is greppable in prod logs and vault history without raising the log level

## v0.5.2

- fix: make the trigger/retry caps opt-in — an absent `max_triggers`/`max_retries` now means no cap, so a recurring task that accumulates `trigger_count` across zombie-failure re-dispatches never has its routing `assignee` stripped and never appends spurious escalation sections (increment-frontmatter executor + result-writer caps). Previously the lib default-3 fallback stripped `assignee` on the 3rd trigger, silently killing the re-dispatch loop (2026-08-27 prod incident: Daily Sentry Triage assignee strip).

## v0.5.1

- chore: update github.com/bborbe/metrics to v0.5.15, github.com/bborbe/vault-cli to v0.116.1
## v0.5.0

- feat: vault-routing guard on the result path — `target_vault` stamped into every created task; the result executor skips cross-vault results before the write scan (no write, no not_found count, no result event), mirroring the create-path `routing.ShouldProcess` guard (spec 044)

## v0.4.0

- feat: handle `complete-task` command — transitions an open vault task to `status: completed`, `phase: done` with a `# Resolution` body block (recovery SHA, closed-at); idempotent (no duplicate Resolution) — closes build-failure tasks on build red→green (spec 076)

## v0.3.6

- chore: update Go to 1.27.0 and github.com/bborbe/agent to v0.82.1, github.com/bborbe/argument/v2 to v2.12.37, github.com/bborbe/boltkv to v1.14.9, github.com/bborbe/cqrs to v0.6.8, github.com/bborbe/errors to v1.5.20, github.com/bborbe/http to v1.26.24, github.com/bborbe/kafka to v1.25.9, github.com/bborbe/kv to v1.21.11, github.com/bborbe/log to v1.6.24, github.com/bborbe/metrics to v0.5.14, github.com/bborbe/run to v1.9.37, github.com/bborbe/sentry to v1.9.27, github.com/bborbe/service to v1.10.9, github.com/bborbe/time to v1.27.10, github.com/bborbe/validation to v1.4.22, github.com/bborbe/vault-cli to v0.115.0

## v0.3.5

- fix: bump golang base image to 1.26.6 in the Dockerfile — go.mod was already on 1.26.6 but the Dockerfile pinned `golang:1.26.5`, making the image unbuildable (GOTOOLCHAIN=local + go.mod requires >= 1.26.6). v0.3.4's image could not be built.

## v0.3.4

- chore: bump `github.com/bborbe/agent` v0.81.1 -> v0.81.3 — carries the retry-vs-escalate fix: `failed` results preserve `assignee` so the controller's trigger-cap escalation (`applyTriggerCap`) can fire; escalation records `previous_assignee` (spec 010/021/027).

## v0.3.3

- chore(security): bump Go 1.26.5 -> 1.26.6 (stdlib GO-2026-5026 / GO-2026-5972 / GO-2026-6090 / GO-2026-6218)
- chore: update module dependencies — `github.com/bborbe/agent` v0.79.0 -> v0.81.1, `github.com/bborbe/cqrs` v0.6.4 -> v0.6.6, `github.com/bborbe/kafka` v1.25.5 -> v1.25.8, `github.com/bborbe/service` v1.10.8, `github.com/bborbe/vault-cli` v0.101.3 -> v0.111.4, `github.com/IBM/sarama` v1.50.3 -> v1.60.1, `github.com/go-openapi/swag` v0.27.0 -> v0.28.0, and `k8s.io/*` v0.36.2 -> v0.36.3

## v0.3.2

- docs: add a License section to the README

## v0.3.1

- fix(create-task): stop requiring a non-empty `assignee` on create. An empty assignee is the fleet's operator-inbox signal (escalation clears it so no agent claims the task), so requiring it made a task that is *born* parked unrepresentable: `github-pr-watcher`'s untrusted-author path stamps `assignee: "", phase: human_review`, and every such command was rejected with `validate frontmatter: frontmatter missing required field: assignee` — the PR silently never reached the operator queue and the only evidence was one controller log line (`bborbe/git-sync#5`, 2026-07-28). `vault_scanner` already treats an empty assignee as unclaimed, so accepting it at create matches how update and dispatch already behave. `status` remains required

## v0.3.0

- refactor(metrics): route the remaining direct package-global metric accesses through the injected `Metrics` interface. Add the missing `PlanningRetryTotal` method and inject `metrics.Metrics` into the result writer, both frontmatter executors, and the planning-retry gate — production code no longer reaches package-global collectors directly. Convert the vault-scanner skip reasons to a typed `SkipReason` enum with an `AvailableSkipReasons` collection, and add a boundary-outcome log to the pr-commenter GitHub call. Addresses the pre-existing go-architecture (interface bypass), go-enum-type, and go-logging findings surfaced on #12. The Prometheus collector `var`s intentionally stay package-level (the registry is a process singleton — a metric registers once per process; `main` builds `metrics.New()` multiple times), so the mechanical no-globals flag on them is a known false-positive for metric collectors.

## v0.2.4

- chore: remove `tools.go` — the 6 CLI tools it pinned (counterfeiter, addlicense, ginkgo, golines, goimports, govulncheck) are already invoked via `go run pkg@$(VERSION)` from `tools.env` in the Makefile, so `tools.go` only polluted `go.mod` with tool-only dependencies. `go mod tidy` drops them. Resolves go-tools-versioning/no-tools-go-for-clis (MUST).

## v0.2.3

- chore: gitignore `/vendor/` — build-check-generated vendor dir should never be committed (repo follows the no-vendor convention)

## v0.2.2

- Update bborbe module dependencies (agent, argument, boltkv, cqrs, errors, http, kafka, kv, log, metrics, run, sentry, service, time, validation, vault-cli)
- Bump prometheus/client_golang, prometheus/common, sentry-go
- Bump golang.org/x tooling (tools, vuln, crypto, mod, net, sync, sys, telemetry, term, text)
- Bump sigs.k8s.io/structured-merge-diff/v6

## v0.2.1

- chore: update dependencies and toolchain — Go 1.26.4→1.26.5, golang/alpine base images, bborbe libraries, k8s deps; regenerate mocks; ignore govulncheck GO-2026-5932 (`golang.org/x/crypto/openpgp` unmaintained advisory, not reachable)
- docs: correct spec-004 replay-verification method — re-triggering an already-materialized date returns `ErrTaskAlreadyExists` and the supersede hook never runs; the correct replay triggers the next not-yet-materialized date so its scan collapses open same-week priors; also fix stale make-buca deploy reference to the mirrored-semver model

## v0.2.0

- refactor: replace single-prior `period_token_decrementor` arithmetic with pure `period_token_ranking` ordinal core — `parsePeriodTokenOrdinal` returns a `time.Time.Unix()`-based comparable ordinal that correctly orders all six recurrence kinds across ISO-week and year boundaries; `rankSameSlugCandidatesDescending` sorts same-slug candidates most-recent-first via stable sort; obsolete decrementor module and its tests deleted [spec-004 prompt 1]

- feat: add bounded scan-and-collapse supersede logic to `CreateTaskExecutor` — after a recurring-task instance is materialized, lists same-slug candidates via `ListFiles`, ranks them most-recent-first, and transitions every still-in_progress candidate older than the new instance to `aborted`; capped at look-back bound `k` (default 7); glob-injection and path-traversal safe; best-effort per file [spec-004 prompt 2]

- feat: expose the supersede look-back bound K as `SUPERSEDE_LOOKBACK` controller env var (default 7); wired from application config through factory into `CreateTaskExecutor`; non-positive value fails startup with a clear error; the scan-and-collapse now self-heals missed-day gaps and multi-stream weekday schedules [spec-004 prompt 3]

## v0.1.1

- refactor: converge build to the bborbe/kafka-topic-reader publish-only model — make buca now builds and pushes docker.io/bborbe/agent-task-controller:$(VERSION); deploy machinery removed (moves to the quant config repo / helm chart).

## v0.1.0

- feat: Bump `github.com/bborbe/agent` v0.70.0 → v0.72.0, `github.com/bborbe/cqrs` v0.5.2 → v0.6.0
- feat: Add explicit `TopicPrefix base.TopicPrefix` config field (`arg:"topic-prefix"`, `env:"TOPIC_PREFIX"`, optional) alongside the existing `Branch base.Branch` field; Kafka topics are now built from `TopicPrefix` only (empty means unprefixed, no leading dash) — `Branch` is retained unchanged for its other non-topic uses
- test: Add golden test proving published event topic literals — `develop-agent-task-v1-event` (non-empty prefix) and `agent-task-v1-event` (empty prefix) — via `cdb.NewEventObjectSender` wired to the real `github.com/bborbe/kafka/mocks.KafkaSyncProducer` fake
- chore: k8s manifest (`k8s/agent-task-controller-sts.yaml`) now also sets `TOPIC_PREFIX`; `dev.env`/`prod.env` pin it to `develop`/`master` respectively to keep existing deployments' topic names byte-identical to the previous implicit `BRANCH`-derived mapping
