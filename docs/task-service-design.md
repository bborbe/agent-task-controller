# Task Service Design (task/controller)

Task service abstracts all task storage behind CQRS over Kafka. No consumer knows about git, markdown, or file paths. Internally uses vault-cli library for all operations. Single writer to the task repo.

## Inputs / Outputs

| Direction | Topic | Purpose |
|-----------|-------|---------|
| Produces | `agent-task-v1-event` | Task created or status changed |
| Consumes | `agent-task-v1-request` | Create/update/complete task commands |
| Produces | `agent-task-v1-result` | Query responses |

## Two Responsibilities

### 1. Change Detection (git → Kafka)

Detects when humans edit tasks in Obsidian and publishes events.

```
Obsidian edit → obsidian-git push
  → task service detects (fsnotify or git pull + diff)
  → reads task via vault-cli library
  → publishes agent-task-v1-event
```

### 2. Command Processing (Kafka → git)

Processes commands from the controller and writes back to git.

```
agent-task-v1-request (from controller)
  → task service consumes
  → vault-cli library updates task file
  → git commit + push
  → publishes agent-task-v1-event (confirms change)
```

## Why a Service

Without the task service, every component would need git access:
- Git merge conflicts from concurrent writers
- Tight coupling to markdown + frontmatter format
- No clean abstraction boundary

The task service is the **single writer** — same pattern as other trading services own their data.

## Storage Agnosticism

```
Kafka (stable protocol)
  └── Task Service (stable interface)
        └── vault-cli library (stable API)
              └── markdown + git (swappable)
```

Could swap to postgres or SQLite without changing any consumer.

## Relationship to Existing Infrastructure

| Component | Relationship |
|-----------|-------------|
| vault-cli (CLI) | Library reused internally, CLI still works for human use |
| task-watcher | Functionality absorbed (fsnotify change detection) |
| obsidian-git | Handles Obsidian-side sync, task service handles agent-side writes |
| Trading Kafka | Same cluster, same patterns, new `agent-*` topics |

## Task Identifier Contract

`task_identifier` MUST be a UUID. The vault scanner (`task/controller/pkg/scanner/vault_scanner.go`) enforces this: any task file whose frontmatter contains a non-UUID `task_identifier` is rewritten with a freshly generated UUID on the next scan cycle, breaking any caller that depends on a deterministic identifier.

Publishers (executor probe loop, agents, manual operators) MUST therefore construct UUID task identifiers. For deterministic-per-agent identifiers, prefer `uuid.NewSHA1(namespace, []byte(agentName))` over `uuid.New()` so the value is stable across process restarts and re-deploys.
