# memory-bootstrap

A one-time deep scan of your WhatsApp history that builds detailed long-term memory,
then goes dormant. The heavy reading/distillation runs in the Claude harness in small
batches (4 chats per call, 8 batches per tick) with a checkpoint file, so it survives
restarts and finishes on its own. Distilled facts go to memory; still-actionable items
(unfulfilled promises, unanswered requests) are queued for `act-on-pending`.

Skips newsletters/broadcasts, chats you don't participate in, and your own command
chats. Safe to trigger manually: `karmax loops run memory-bootstrap`.

## Config

- `KARMAX_LOOP_MEMORY_BOOTSTRAP_WACLI` — optional override path to the wacli binary.
