---
status: completed
summary: Replaced the local patchImmutableFields denylist with the shared lib.TaskPatchFieldAdmitted rule, rewrote the spec to derive entries from the rule, and documented the change.
execution_id: agent-task-controller-task-patch-field-rule-exec-052-delete-patch-immutable-denylist
dark-factory-version: v0.198.17
created: "2026-11-22T18:23:27Z"
queued: "2026-11-22T18:23:27Z"
started: "2026-11-22T18:25:21Z"
completed: "2026-11-22T18:31:32Z"
---

# Delete the patch-immutable denylist and filter on the shared task-PATCH rule

<summary>
- The result writer stops keeping its own list of keys the vault API refuses.
- It asks the shared rule published by the task SDK instead, so the two cannot disagree.
- One field the API refuses can no longer cost the whole result write.
- The list is deleted, not extended: the next write-only field needs no change here.
- A regression spec reads the rule itself, so a key added upstream is covered without editing this repository.
- The mutable fields of the same write still land, and every dropped key is still reported by name.
</summary>

<objective>
Make the controller's result writer keep a frontmatter key only when the vault API's own task-PATCH rule admits it, so the writer cannot fall one field behind the schema and lose a whole result write to a single refused key.
</objective>

<context>
Read `pkg/result/result_writer.go` — `changedFieldsForWrite` and the `patchImmutableFields` map it consults.
Read `pkg/result/result_writer_immutable_key_test.go` — the existing specs pin the exclusion at the builder (`ChangedFieldsForWriteForTest`) and at the write (`WriteResult`); keep both shapes.
Read `pkg/result/export_test.go` — `ChangedFieldsForWriteForTest` is the test-only accessor.
Read `docs/controller-design.md` § Frontmatter Merge — the project's description of the result write-back path this key selection sits inside. Add one sentence to that section stating that the writer drops any frontmatter key the shared task-PATCH rule does not admit, because the vault API refuses the whole request on the first refused key.
The shared rule is the released `github.com/bborbe/agent` v0.148.0: `lib.TaskPatchFieldAdmitted(key) bool` is the predicate, and `lib.TaskPatchFields() map[string]bool` returns the enumerated set (the `metrics_` namespace is admitted by prefix and is deliberately not enumerated).
No spec covers this change, and none is needed: it adopts an already-released library rule, and the failure class it removes is already recorded in `CHANGELOG.md` v0.33.6.
</context>

<requirements>
1. In `go.mod`, raise `github.com/bborbe/agent` from v0.136.0 to v0.148.0. The package is already imported as `lib` in `pkg/result/result_writer.go`, so run `go get github.com/bborbe/agent@v0.148.0` and let `make ensure` tidy afterwards — do not tidy before the new call site exists.
2. Delete the `patchImmutableFields` map and its doc comment from `pkg/result/result_writer.go`.
3. In `changedFieldsForWrite`, replace the `patchImmutableFields[key]` test with `!lib.TaskPatchFieldAdmitted(key)`. Keep the surrounding behaviour exactly as it is: the unchanged check still runs first for admitted keys, the `excluded` slice still collects a dropped key only when its value differs, and the slice is still sorted before return.
4. Rewrite the function's doc comment so it states that the drop follows the vault API's own rule, imported from `github.com/bborbe/agent`, rather than a list kept here. Name the failure this removes: one refused key vetoes every mutable key in the same request, which is how an in-flight result was dropped after a re-address (dev, 2026-11-19).
5. In `pkg/result/result_writer_immutable_key_test.go`, add a second `DescribeTable` (requirement 7's five entries stay in the existing one) driven from the rule itself rather than from a hand-written key list — build its entries in a loop over `lib.TaskPatchFields()`: for every key in that set, a differing value must appear in the returned `fields` and must not appear in `excluded`. Add a separate entry for `metrics_future_counter`, which the rule admits by prefix and which `lib.TaskPatchFields()` therefore does not contain — it must land in `fields` and not in `excluded`.
6. Add one entry for a key the rule rejects and no producer in this repository writes (`run_claim`): it must be absent from `fields`, present in `excluded`, and the differing admitted key in the same map must still land. This is the boundary the change exists to hold.
7. Keep every existing entry in the current table — all five, including the two negative cases. They assert the same outcomes against the new rule and must pass unchanged. Add one entry for `approved_at`, the fourth key the deleted list dropped and the only one no existing entry covers: a differing value must be absent from `fields` and present in `excluded`.
8. Add a `## Unreleased` section directly above the highest version heading (`## v0.34.0`) and below the frozen preamble of `CHANGELOG.md`, with one `- fix:` bullet: the result writer now filters on the task SDK's shared PATCH rule instead of a local denylist, so a field the API refuses can no longer veto the whole result write; vault-api refuses the whole request on the first key it does not recognise, which is the failure `CHANGELOG.md` v0.33.6 documents, and it recurred on `agent-task-controller-personal-0` at 2026-11-20 19:52 CET with `400 unknown_field field=run_claim`, three patch attempts, then a dropped result.
9. Before finishing, re-run the `<verification>` commands and confirm they pass; walk every requirement above against the diff, and confirm each of the four keys the deleted list named (`task_identifier`, `task_identifier_previous`, `approved_by`, `approved_at`) is still rejected by the shared rule.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
- Do not add any key to a list in this repository to make a spec pass. If a key is missing, it is missing from the shared rule in `github.com/bborbe/agent`, and that is a change in another repository — report it instead.
- Do not add `run_claim` to the admitted set: it is a spawn-gate claim marker written at execution start whose only expiry is its TTL, and no producer in this repository writes it; a result write must not be able to set it.
- Keep the `WriteResult` log line that reports the excluded keys; it is the only record of a drop.
- Do not change the scanner's `changedFields` (`pkg/scanner/vault_scanner.go`) or the command path's `buildUpdatePatch` (`pkg/command/task_update_frontmatter_executor.go`); neither consults the rule, and adopting it there is a separate change.
</constraints>

<verification>
Run `ROOTDIR=/workspace make precommit` — must exit 0.
Run `awk '/^## /{sec=$0} /result writer now filters on the task SDK/{print sec}' CHANGELOG.md` — must print `## Unreleased`.
</verification>
