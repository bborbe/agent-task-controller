---
status: completed
approved: "2026-11-27T06:24:53.587443371Z"
generating: "2026-11-27T07:45:25Z"
prompted: "2026-11-27T07:52:51Z"
verifying: "2026-11-27T08:19:42Z"
completed: "2026-11-27T08:19:48Z"
---

## Summary

Sentry issue `NUKE-DEV-HM` is a real bug in `bborbe/agent-task-controller` at `bborbe/agent-task-controller/pkg/sync/sync_loop.go:96`, filed automatically by the sentry-fix-agent from the High/High real bug deep verdict — that verdict is the approval gate, so this spec is approved rather than drafted.

## Problem

A transient Sarama broker produce error (kafka server: Tried to send a message..., most consistent with ErrNotLeader) exhausts the producer's ~1s retry budget and the sync loop's fail-fast PublishChanged path propagates it through run.CancelOnFirstErrorWait into service.Run, terminating the controller process.

## Reproduction

The implicated source location from the deep verdict is `bborbe/agent-task-controller/pkg/sync/sync_loop.go:96` in `bborbe/agent-task-controller` (resolution rule: `candidate position 1 (bborbe/agent-task-controller)`), Sentry issue `NUKE-DEV-HM`.

## Recommended Fix

Add a context-aware bounded-retry TaskPublisher decorator at the main.go wiring (1s/2s/4s/8s, ~4 attempts) that preserves the existing fatal fallback on exhaustion and emits retry/failure metrics; optionally add in-process re-queue of failed tasks and make RunCycle's send blocking. file:line: bborbe/agent-task-controller/pkg/sync/sync_loop.go:96
