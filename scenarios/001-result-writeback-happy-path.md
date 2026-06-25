---
status: draft
---

# Scenario 001: UpdateResult command writes result back to vault task file

Validates that sending an UpdateResult command via the trading API updates the task file in the vault with new frontmatter and content.

## Setup
- [ ] task/controller deployed to dev (v0.20.12+)
- [ ] Create a test task file with a known task_identifier:
  ```bash
  cat > ~/Documents/Obsidian/OpenClaw/24\ Tasks/Test\ Result\ Writeback.md << 'EOF'
  ---
  status: in_progress
  phase: in_progress
  assignee: backtest-agent
  task_identifier: aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee
  tags:
    - agent-task
    - test
  ---
  Tags: [[Build Backtest Agent as First Controller Job]]

  ---

  Test task for scenario 001. Run backtest for BBR-EURUSD-1H.
  EOF
  ```
- [ ] Commit and push: `cd ~/Documents/Obsidian/OpenClaw && git add -A && git commit -m "add test task for scenario 001" && git push`
- [ ] Wait for task/controller to pick up the file (check logs or wait 60s)

## Action
- [ ] Send UpdateResult command:
  ```bash
  ~/Documents/Obsidian/OpenClaw/.claude/scripts/trading-api-write.sh dev \
    "/api/1.0/command/agent-task-v1/update" \
    '{
      "taskIdentifier": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
      "frontmatter": {
        "status": "completed",
        "phase": "done",
        "task_identifier": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
      },
      "content": "Test task for scenario 001. Run backtest for BBR-EURUSD-1H.\n\n## Result\n\nBacktest completed successfully.\n- Strategy: BBR-EURUSD-1H\n- Trades: 42\n- Win rate: 58%\n"
    }'
  ```
- [ ] Wait 30 seconds for controller to process

## Expected
- [ ] Controller logs show command consumed: `kubectlquant -n dev logs agent-task-controller-0 --tail=20 | grep -i "result\|write\|commit\|push"`
- [ ] Git pull in vault shows new commit: `cd ~/Documents/Obsidian/OpenClaw && git pull`
- [ ] Task file frontmatter has `status: completed`: `grep "status:" ~/Documents/Obsidian/OpenClaw/24\ Tasks/Test\ Result\ Writeback.md`
- [ ] Task file frontmatter has `phase: done`: `grep "phase:" ~/Documents/Obsidian/OpenClaw/24\ Tasks/Test\ Result\ Writeback.md`
- [ ] Task file body contains `## Result` section: `grep "## Result" ~/Documents/Obsidian/OpenClaw/24\ Tasks/Test\ Result\ Writeback.md`

## Cleanup
- Remove test task file: `cd ~/Documents/Obsidian/OpenClaw && git rm "tasks/Test Result Writeback.md" && git commit -m "remove test task" && git push`
