# cold-scan

The cold-memory pipeline. Every 45 minutes it examines WhatsApp chats you are NO
LONGER actively texting in (hot/active ones are left to `hot-sync`) and distills a
durable per-chat summary — who the person is to you, ongoing topics, commitments,
decisions — using the agent's cheap SUMMARY model, not the main model. Summaries are
stored per chat and power the memory retrieval sub-agent's context.

Hot vs cold is decided by YOUR last message in the chat, so a group that stays busy
without you correctly goes cold. Large community/promo groups you barely write in are
skipped. Each chat is re-examined at most once a day.

## Config

- `KARMAX_LOOP_COLD_SCAN_WACLI` — optional override path to the wacli binary.
- `KARMAX_LOOP_COLD_SCAN_PER_TICK` — chats summarized per run (default 3).
- `KARMAX_LOOP_COLD_SCAN_HOT_DAYS` — activity window that keeps a chat hot (default 14).
- `KARMAX_LOOP_COLD_SCAN_MIN_GROUP_OWN` — min own messages for a group (default 5).
- `KARMAX_LOOP_COLD_SCAN_MIN_GROUP_OWN_RATIO` — min own-message fraction (default 0.2).
