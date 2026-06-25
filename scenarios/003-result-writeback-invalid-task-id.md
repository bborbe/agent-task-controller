---
status: draft
---

# Scenario 003: UpdateResult with unknown task_identifier is skipped

Validates that a command with a non-existent task_identifier does not crash the controller.

## Setup
- [ ] task/controller deployed to dev (v0.20.12+)

## Action
- [ ] Send UpdateResult command with a UUID that matches no file:
  ```bash
  ~/Documents/Obsidian/OpenClaw/.claude/scripts/trading-api-write.sh dev \
    "/api/1.0/command/agent-task-v1/update" \
    '{
      "taskIdentifier": "00000000-0000-0000-0000-000000000000",
      "frontmatter": {"status": "completed", "phase": "done"},
      "content": "test content"
    }'
  ```
- [ ] Wait 15 seconds

## Expected
- [ ] Controller logs show error for unmatched task_identifier: `kubectlquant -n dev logs agent-task-controller-0 --tail=20 | grep -i "error\|skip\|not found"`
- [ ] Controller pod is still running: `kubectlquant -n dev get pod agent-task-controller-0 -o jsonpath='{.status.phase}'` outputs `Running`
- [ ] No new commits in vault: `cd ~/Documents/Obsidian/OpenClaw && git pull` shows "Already up to date"

## Cleanup
None required — no files modified.
