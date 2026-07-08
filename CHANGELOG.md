# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## Unreleased

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
