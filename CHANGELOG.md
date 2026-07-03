# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## v0.1.0

- feat: Bump `github.com/bborbe/agent` v0.70.0 → v0.72.0, `github.com/bborbe/cqrs` v0.5.2 → v0.6.0
- feat: Add explicit `TopicPrefix base.TopicPrefix` config field (`arg:"topic-prefix"`, `env:"TOPIC_PREFIX"`, optional) alongside the existing `Branch base.Branch` field; Kafka topics are now built from `TopicPrefix` only (empty means unprefixed, no leading dash) — `Branch` is retained unchanged for its other non-topic uses
- test: Add golden test proving published event topic literals — `develop-agent-task-v1-event` (non-empty prefix) and `agent-task-v1-event` (empty prefix) — via `cdb.NewEventObjectSender` wired to the real `github.com/bborbe/kafka/mocks.KafkaSyncProducer` fake
- chore: k8s manifest (`k8s/agent-task-controller-sts.yaml`) now also sets `TOPIC_PREFIX`; `dev.env`/`prod.env` pin it to `develop`/`master` respectively to keep existing deployments' topic names byte-identical to the previous implicit `BRANCH`-derived mapping
