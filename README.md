# Agent Task Controller

Polls an Obsidian vault git repository on a configurable interval, scans the `24 Tasks` directory for Markdown files with assigned tasks, and publishes changed and deleted task events to Kafka using the CQRS EventObjectSender stack.

## Links

Dev:
https://dev.quant.benjamin-borbe.de/admin/agent-task-controller/setloglevel/3
https://dev.quant.benjamin-borbe.de/admin/agent-task-controller/trigger

Prod:
https://prod.quant.benjamin-borbe.de/admin/agent-task-controller/setloglevel/3
https://prod.quant.benjamin-borbe.de/admin/agent-task-controller/trigger
