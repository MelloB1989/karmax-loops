# gchat-watch

Watches Google Chat through the gws CLI. The Go side polls the spaces list cheaply
every 2 minutes (no LLM); only when a space has NEW activity does the Claude harness
read the thread and act: routine dev asks (close/merge a PR a teammate requested,
quick answers, scheduling) are done immediately and answered in your casual voice;
real decisions are filed as approvals; operator-only items become reminders. First run
looks back 24 hours.

## Config

- `KARMAX_LOOP_GCHAT_WATCH_GWS` — optional override path to the gws CLI.
