# gchat-watch

Watches Google Chat through the [gog](https://github.com/openclaw/gogcli) CLI. The Go
side polls cheaply every four hours (no LLM): the space list, then one newest-message
probe per space. Only when a space has NEW activity does the Claude harness read the
thread and act: routine dev asks (close/merge a PR a teammate requested, quick answers,
scheduling) are done immediately and answered in your casual voice; real decisions are
filed as approvals; operator-only items become reminders. First run looks back 24 hours.

Google Chat is a Workspace API: a personal `gmail.com` account cannot use it, and gog
says so rather than returning an empty list.

## Config

- `KARMAX_LOOP_GCHAT_WATCH_GOG` — optional override path to the gog CLI.
- `KARMAX_LOOP_GCHAT_WATCH_ACCOUNT` — which Google account to act as, when more than
  one is authorized. Omit it and gog uses its default.
