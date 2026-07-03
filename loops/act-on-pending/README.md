# act-on-pending

Executes the pending-actions QUEUE. Discovery loops (like `memory-bootstrap`) enqueue
actionable items they surface from WhatsApp history; every 2 hours this loop drains the
queue and, via the Claude harness, verifies each item is still open, completes what it
safely can (calendar/tasks via the gws CLI, replies in monitored chats), files approvals
for real decisions, and creates reminders for operator-only items. Failed batches are
re-queued so nothing is lost.

## Config

- `KARMAX_LOOP_ACT_ON_PENDING_WACLI` — optional override path to the wacli binary.
- `KARMAX_LOOP_ACT_ON_PENDING_GWS` — optional override path to the gws CLI.
