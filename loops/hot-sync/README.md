# hot-sync

**Default loop.** Every 4 hours the main agent scans your ACTIVE WhatsApp chats
(people and groups you've messaged in roughly the last two weeks) via `whatsapp.read`
and ingests one distilled fact per genuinely important item — who someone is, a
commitment, a deadline, a decision, a project update. Community/promo groups are
ignored; raw chatter is never stored.

This keeps the agent's working memory current between the deeper `cold-scan` and
`memory-bootstrap` passes. Only messages you if something is urgent. No configuration.
