# wa-monitor

The proactive WhatsApp proxy, fully EVENT-DRIVEN: it fires on each incoming
`comms.message` event (pushed by the wacli webhook — no polling, zero idle LLM spend)
and handles messages from MONITORED, non-operator chats on your behalf:

- **routine** (acks, availability, known info, simple scheduling) → replies in your
  natural voice via wacli, then notifies you (`ACTED`)
- **a real decision, commitment, money, anything sensitive** → files an approval in
  your inbox; approving executes the suggested action (`APPROVE`)
- **only you can do it** (a document it doesn't have, a personal reply) → creates a
  phone reminder (`REMIND`)

Group chats are handled conservatively: it replies only when you are directly
addressed, and surfaces meaningful project/deal updates as approvals instead of
replying. Trivial acks ("ok", "thanks", emoji) are filtered in Go for free. Messages
from YOUR own chats are commands to KARMAX and are handled by the agent, not this loop.

Which chats are monitored is decided by the wacli webhook scope — manage it with the
agent's `whatsapp.monitor` tool ("keep an eye on X", "stop watching Y").

## Config

- `KARMAX_LOOP_WA_MONITOR_WACLI` — optional override path to the wacli binary.
