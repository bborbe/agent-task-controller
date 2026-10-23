---
status: completed
summary: Removed the controller-side cluster publish filter so every changed task is published with its pin intact, deleted ClusterFilter and its test, dropped the CLUSTER/PUBLISH_UNTAGGED settings and skipped_foreign_cluster metric, added a regression test and CHANGELOG entry
execution_id: agent-task-controller-publish-foreign-exec-035-publish-foreign-cluster-tasks
dark-factory-version: v0.198.7
created: "2026-10-23T09:27:29Z"
queued: "2026-10-23T09:27:29Z"
started: "2026-10-23T09:28:29Z"
completed: "2026-10-23T09:34:08Z"
---

# Publish tasks pinned to another cluster instead of dropping them

<summary>
- The controller publishes every changed task, including one whose cluster pin names a different cluster
- Each executor already skips a task pinned to another cluster, so routing by pin moves entirely to the executor side
- A task the operator pins after it was once published untagged stops running on the wrong cluster, because executors now receive the pinned version
- Untagged tasks are published exactly as before
- The controller's own cluster filter, its two settings, and its "skipped foreign cluster" publish counter are removed
- A regression test proves a task pinned to another cluster is published with its pin intact
</summary>

<objective>
The sync loop must publish every changed task regardless of its `cluster` frontmatter. Today it drops a task pinned to another cluster and publishes nothing. The task topic keeps only the latest event per task, so an executor keeps the last version this controller published, often an untagged one from before the operator pinned the task, and runs a task pinned elsewhere. Executors filter by cluster themselves, so the controller-side filter is both redundant and the cause of the bug.
</objective>

<context>
Read CLAUDE.md for project conventions.
Read `pkg/sync/sync_loop.go` — `NewSyncLoop`, the `syncLoop` struct, and `processResult`, which calls `s.clusterFilter.ShouldPublish(task)` and skips the task with `continue`.
Read `pkg/sync/cluster_filter.go` and `pkg/sync/cluster_filter_test.go` — `ClusterFilter` and its `ShouldPublish` table test; both go away.
Read `pkg/sync/sync_loop_test.go` — the existing `It("calls PublishChanged for a changed task in scan result", ...)` case is the pattern for the new test; `NewSyncLoop` is constructed in its `BeforeEach`.
Read `main.go` — the `application` struct fields `Cluster` (env `CLUSTER`) and `PublishUntagged` (env `PUBLISH_UNTAGGED`), and the `pkgsync.NewSyncLoop(...)` call that passes `pkgsync.ClusterFilter{...}`.
Read `pkg/metrics/metrics.go` — the pre-initialisation line `TasksPublishedTotal.WithLabelValues("skipped_foreign_cluster").Add(0)`.
Read `CHANGELOG.md` — add the entry under a new `## Unreleased` section above `## v0.16.1`, in the style of the existing `- fix: **...**` bullets.
</context>

<requirements>
1. In `pkg/sync/sync_loop.go`, remove the `clusterFilter ClusterFilter` parameter from `NewSyncLoop`, the field from `syncLoop`, and the `ShouldPublish` check (with its log line and counter increment) from `processResult`. Every task in `result.Changed` goes to `s.publisher.PublishChanged`. Add a short comment above the loop: executors filter by cluster, and dropping a foreign task here left an executor's view on a stale pre-pin event.
2. Delete `pkg/sync/cluster_filter.go` and `pkg/sync/cluster_filter_test.go`.
3. In `main.go`, remove the `Cluster` and `PublishUntagged` fields from the `application` struct and drop the `ClusterFilter` argument from the `NewSyncLoop` call. Leave every other field unchanged. Deployed manifests still set `CLUSTER` and `PUBLISH_UNTAGGED`. The argument library reads env per declared field, so unset fields are harmless. Confirm this by checking that nothing in `main.go` rejects unknown env vars.
4. In `pkg/metrics/metrics.go`, remove the `skipped_foreign_cluster` pre-initialisation line. Keep the other `TasksPublishedTotal` labels.
5. Update `pkg/sync/sync_loop_test.go`: drop the `ClusterFilter` argument from `NewSyncLoop` and add a case alongside the existing PublishChanged case. A `ScanResult` whose only changed task has frontmatter `{"status": "in_progress", "cluster": "sun"}` results in exactly one `PublishChanged` call, and the published task's `Frontmatter["cluster"]` equals `"sun"`. This test crosses the publish boundary with a pinned value, which is the regression lock.
6. Update `docs/controller-design.md` if it describes the publish-side cluster filter or the `CLUSTER` / `PUBLISH_UNTAGGED` settings. Leave the operator-pinned `cluster` row of the ownership-guard table untouched. That guard is a separate, still-valid mechanism.
7. Add a CHANGELOG `## Unreleased` bullet: `- fix: **a task pinned to another cluster is published instead of dropped.** ...` explaining the stale-latest-event cause (observed 2026-10-23: task `a2b59b2e` pinned `cluster: sun` ran twice on nuke prod) and that the `CLUSTER` / `PUBLISH_UNTAGGED` settings and the `skipped_foreign_cluster` publish label are removed.
8. Run `go generate ./...` only if a `//counterfeiter:generate` annotation referenced `ClusterFilter` (none is expected). Run `grep -rn "ClusterFilter\|PublishUntagged" --include='*.go' . | grep -v '^./vendor'` and confirm it prints nothing.
9. Before you finish, re-run `<verification>` and confirm it passes. Walk each summary bullet against the change.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
- Do not touch the result writer, the ownership guard, or `operatorPinnedFields` in `pkg/result/result_writer.go`. `cluster` stays operator-pinned there
- Do not change how untagged tasks are published (they were always published, since every deployed controller runs with `PUBLISH_UNTAGGED=true`)
- Use the project's `github.com/bborbe/errors` wrapping and Ginkgo/Gomega test style; no `fmt.Errorf`
- Do not run `go mod vendor`
</constraints>

<verification>
Run `ROOTDIR=/workspace make precommit` — must pass.
Run `! grep -rqn "ClusterFilter\|PublishUntagged\|skipped_foreign_cluster" --include='*.go' pkg main.go` — must succeed (no matches).
Run `grep -q '"sun"' pkg/sync/sync_loop_test.go` — must succeed.
</verification>
