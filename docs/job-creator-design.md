# Task Executor Design (task/executor)

The task executor bridges Kafka and Kubernetes. It consumes task events, filters by status/phase/assignee, resolves the assignee to a container image, and spawns K8s Jobs. It is the only component that talks to the K8s API.

## Inputs / Outputs

| Direction | Source/Target | Purpose |
|-----------|--------------|---------|
| Consumes | `agent-task-v1-event` (Kafka) | Task changed in vault |
| Creates | K8s Job API | Spawn agent container |

## Logic

```
On startup:
  │
  ├── install/update Config CRD (configs.agent.benjamin-borbe.de)
  ├── start informer → watches Config CRs in namespace
  └── in-memory store updated on add/update/delete

On agent-task-v1-event:
  │
  ├── filter: status must be in_progress
  ├── filter: phase must be planning, in_progress, or ai_review
  ├── filter: stage must match executor's branch (tasks without stage default to prod)
  ├── filter: assignee must match a known Config CR
  ├── deduplicate: skip if K8s Job already active for same task
  │
  ├── resolve assignee → AgentConfiguration via ConfigResolver (CRD-backed)
  ├── create K8s Job:
  │     image: spec.image + ":" + branch
  │     env: TASK_CONTENT, TASK_ID, KAFKA_BROKERS, BRANCH + spec.env
  │     envFrom: spec.secretName (if set)
  │     volume: spec.volumeClaim → spec.volumeMountPath (if set)
  │
  └── done — does NOT watch the Job
```

## Agent Configuration via CRD

Agent types are declared as `Config` custom resources (`agent.benjamin-borbe.de/v1`). The executor watches these via a Kubernetes informer and maintains an in-memory store. See [agent-crd-specification.md](agent-crd-specification.md) for the full schema.

Adding a new agent requires only: build image + `kubectl apply` a Config CR. No executor code changes or redeploy needed.

## HTTP Endpoints

| Endpoint | Purpose |
|----------|---------|
| `/healthz` | Liveness probe |
| `/readiness` | Readiness probe |
| `/metrics` | Prometheus metrics |
| `/agents` | List all known agent configs (JSON) |
| `/setloglevel/{level}` | Temporary log level change (5-min auto-reset) |

## What task/executor Does NOT Do

- Does NOT watch Jobs for completion
- Does NOT read stdout/logs from Jobs
- Does NOT publish results to Kafka
- Does NOT manage retries or heartbeats

The agent inside the Job publishes its own result directly to `agent-task-v1-request`. See [agent-job-interface.md](agent-job-interface.md) for the full contract.

## Why This Component Exists

Decoupling task/controller from K8s means:
- Controller is pure git + Kafka — testable, simple
- Execution runtime is swappable:

| Today | Tomorrow |
|-------|----------|
| K8s Jobs | Docker containers |
| | Lambda functions |
| | Permanent deployments |
| | Local process |

Swap the executor, everything else stays the same.
