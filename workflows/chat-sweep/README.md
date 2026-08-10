# chat-sweep

The proactive proxy's counterpart for the BACKLOG. The event-driven `wa-monitor` loop
reacts to NEW messages; this sweep runs every 4 hours over your MONITORED WhatsApp
chats looking for items already pending on your side — an unanswered question, a
promised action, an approaching deadline.

For each finding, the Claude harness acts with the standard discipline:

- **routine** → replies in your natural voice, then notifies you (`ACTED`)
- **a real decision** → files an approval in your inbox (`APPROVE`)
- **only you can do it** → creates a phone reminder (`REMIND`)

Which chats are monitored is governed by the wacli webhook scope (managed with the
agent's `whatsapp.monitor` tool).

## Config

- `KARMAX_LOOP_CHAT_SWEEP_WACLI` — optional override path to the wacli binary.
