# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## Unreleased

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
