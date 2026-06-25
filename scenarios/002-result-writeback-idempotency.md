---
status: draft
---

# Scenario 002: Repeated UpdateResult command is idempotent

Validates that sending the same UpdateResult command twice produces identical file content without duplicate Result sections.

## Setup
- [ ] task/controller deployed to dev (v0.20.12+)
- [ ] Create a test task file with a known task_identifier:
  ```bash
  cat > ~/Documents/Obsidian/OpenClaw/24\ Tasks/Test\ Idempotency.md << 'EOF'
  ---
  status: in_progress
  phase: in_progress
  assignee: backtest-agent
  task_identifier: 11111111-2222-3333-4444-555555555555
  tags:
    - agent-task
    - test
  ---
  Tags: [[Build Backtest Agent as First Controller Job]]

  ---

  Test task for scenario 002. Idempotency check.
  EOF
  ```
- [ ] Commit and push: `cd ~/Documents/Obsidian/OpenClaw && git add -A && git commit -m "add test task for scenario 002" && git push`
- [ ] Wait for task/controller to pick up the file (check logs or wait 60s)

## Action
- [ ] Send first UpdateResult command:
  ```bash
  ~/Documents/Obsidian/OpenClaw/.claude/scripts/trading-api-write.sh dev \
    "/api/1.0/command/agent-task-v1/update" \
    '{
      "taskIdentifier": "11111111-2222-3333-4444-555555555555",
      "frontmatter": {
        "status": "completed",
        "phase": "done",
        "task_identifier": "11111111-2222-3333-4444-555555555555"
      },
      "content": "Test task for scenario 002. Idempotency check.\n\n## Result\n\nBacktest completed.\n"
    }'
  ```
- [ ] Wait 30 seconds, then pull: `cd ~/Documents/Obsidian/OpenClaw && git pull`
- [ ] Save checksum after first write: `md5 ~/Documents/Obsidian/OpenClaw/24\ Tasks/Test\ Idempotency.md`
- [ ] Verify `## Result` section exists: `grep -c "## Result" ~/Documents/Obsidian/OpenClaw/24\ Tasks/Test\ Idempotency.md`
- [ ] Send the exact same command again (repeat the command above)
- [ ] Wait 30 seconds, then pull: `cd ~/Documents/Obsidian/OpenClaw && git pull`

## Expected
- [ ] Checksum after second write matches first: `md5 ~/Documents/Obsidian/OpenClaw/24\ Tasks/Test\ Idempotency.md`
- [ ] Exactly one `## Result` section: `grep -c "## Result" ~/Documents/Obsidian/OpenClaw/24\ Tasks/Test\ Idempotency.md` outputs `1`
- [ ] Controller logs show both commands processed without error: `kubectlquant -n dev logs agent-task-controller-0 --tail=40 | grep -i "result\|write\|error"`

## Cleanup
- Remove test task file: `cd ~/Documents/Obsidian/OpenClaw && git rm "tasks/Test Idempotency.md" && git commit -m "remove test task" && git push`
