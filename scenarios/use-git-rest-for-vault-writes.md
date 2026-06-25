---
status: draft
---

# Scenario: use-git-rest-for-vault-writes

Validates that the controller reads and writes vault task files via the git-rest HTTP API end-to-end: task creation, frontmatter update, result writeback, force-push reset (real `git push --force`), and a second writeback. Covers the full Acceptance Criteria sequence from spec 018.

## Setup

- [ ] `vault-obsidian-openclaw` StatefulSet running in the target namespace: `kubectlquant -n dev get sts vault-obsidian-openclaw`
- [ ] `agent-task-controller` running with `USE_GIT_REST=true`: `kubectlquant -n dev get sts agent-task-controller -o jsonpath='{.spec.template.spec.containers[0].env}' | jq '.[] | select(.name=="USE_GIT_REST")'`
- [ ] Controller log shows git-rest in use: `kubectlquant -n dev logs agent-task-controller-0 --tail=50 | grep "git-rest"`
- [ ] Local clone of `bborbe/obsidian-openclaw` available at `~/Documents/Obsidian/OpenClaw` for the force-push step
- [ ] `TEST_ID=018test01-1111-2222-3333-444444444444` (greppable test identifier)
- [ ] No existing `tasks/$TEST_ID.md` in the vault repo

## Action

1. [ ] **CreateTask** — publish `CreateTaskCommand`:
   ```bash
   ~/Documents/Obsidian/OpenClaw/.claude/scripts/trading-api-write.sh dev \
     "/api/1.0/command/agent-task-v1/create-task" \
     '{"taskIdentifier":"'"$TEST_ID"'","frontmatter":{"assignee":"backtest-agent","status":"todo","phase":"todo"},"body":"Test task for spec-018 git-rest scenario.\n"}'
   ```
   Wait 10 s.

2. [ ] **UpdateFrontmatter** — transition to `in_progress`:
   ```bash
   ~/Documents/Obsidian/OpenClaw/.claude/scripts/trading-api-write.sh dev \
     "/api/1.0/command/agent-task-v1/update-frontmatter" \
     '{"taskIdentifier":"'"$TEST_ID"'","updates":{"status":"in_progress","phase":"in_progress"}}'
   ```
   Wait 10 s.

3. [ ] **WriteResult #1** — agent posts initial review:
   ```bash
   ~/Documents/Obsidian/OpenClaw/.claude/scripts/trading-api-write.sh dev \
     "/api/1.0/command/agent-task-v1/update" \
     '{"taskIdentifier":"'"$TEST_ID"'","frontmatter":{"status":"completed","phase":"ai_review"},"content":"Test task for spec-018 git-rest scenario.\n\n## Result\n\nTask completed by backtest-agent.\n\n## Review\n\nInitial review content from spec-018 scenario.\n"}'
   ```
   Wait 10 s.

4. [ ] **Real force-push** — rewrite history and force-push (NOT a fast-forward push). The pre-write SHA is the parent we reset to:
   ```bash
   cd ~/Documents/Obsidian/OpenClaw && git fetch && git reset --hard origin/master
   PRE_WRITE_SHA=$(git rev-list -n 1 HEAD~1 -- "tasks/$TEST_ID.md" 2>/dev/null || git rev-parse HEAD~1)
   git reset --hard "$PRE_WRITE_SHA"
   git push --force origin HEAD:master
   RESET_SHA=$(git rev-parse HEAD)
   echo "RESET_SHA=$RESET_SHA"
   ```
   Wait 60 s for the controller's poll cycle to observe the force-push.

5. [ ] **WriteResult #2** — agent posts second review after the reset:
   ```bash
   ~/Documents/Obsidian/OpenClaw/.claude/scripts/trading-api-write.sh dev \
     "/api/1.0/command/agent-task-v1/update" \
     '{"taskIdentifier":"'"$TEST_ID"'","frontmatter":{"status":"completed","phase":"done"},"content":"Test task for spec-018 git-rest scenario.\n\n## Result\n\nSecond agent result after force-push reset.\n\n## Review\n\nSecond review content.\n"}'
   ```
   Wait 10 s.

6. [ ] **Restart idempotency** — kill the controller pod and replay the last write:
   ```bash
   kubectlquant -n dev delete pod agent-task-controller-0
   kubectlquant -n dev wait --for=condition=Ready pod/agent-task-controller-0 --timeout=120s
   ```
   Re-send the WriteResult #2 command verbatim.

## Expected

- [ ] After step 1: file `tasks/$TEST_ID.md` exists in the vault — fetch via `curl -s http://$(kubectlquant -n dev get svc vault-obsidian-openclaw -o jsonpath='{.spec.clusterIP}'):9090/api/v1/files/tasks/$TEST_ID.md`. Frontmatter has `status: todo`, `assignee: backtest-agent`, `task_identifier: $TEST_ID`.
- [ ] After step 2: frontmatter shows `status: in_progress`, `phase: in_progress`. `assignee` and `task_identifier` preserved.
- [ ] After step 3: file contains a `## Review` section with text "Initial review content from spec-018 scenario.". Frontmatter has `status: completed`, `phase: ai_review`.
- [ ] After step 4 (force-push): controller log shows the reset was observed. The file contents include the prior `## Review` text — either in place under `## Review`, OR moved under a `## Outdated by force-push <sha>` marker. The text "Initial review content from spec-018 scenario." MUST still appear somewhere in the file. (Spec marks the rename-on-force-push as a follow-up controller-logic fix; the assertion here is the weaker, non-negotiable contract: prior review content is never silently destroyed.)
- [ ] After step 5: frontmatter shows `status: completed`, `phase: done`. The new "Second review content." is present. Prior review text from step 3 is still present (somewhere — see step 4 expected).
- [ ] After step 6 (restart + replay): file bytes are identical to those produced by step 5 (idempotent replay; no extra `## Review` duplication, no missing content).
- [ ] One git commit per Kafka command on the vault repo: `cd ~/Documents/Obsidian/OpenClaw && git log --oneline -- "tasks/$TEST_ID.md"` shows a sequence with one commit per Action step (the force-push commit + the controller writes).
- [ ] Metrics: `controller_gitrest_calls_total{op="post"}` non-zero. `controller_kafka_consume_paused_total` is 0 (no git-rest unavailability during the test).

## Cleanup

- [ ] Remove the test task file:
  ```bash
  cd ~/Documents/Obsidian/OpenClaw && git pull && git rm "tasks/$TEST_ID.md" && git commit -m "spec-018 scenario: cleanup" && git push
  ```
