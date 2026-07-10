// Package hotsync is a default KARMAX loop: the active memory-capture engine.
// Every couple of hours the main agent scans recent activity and ingests
// durable facts to long-term memory, so memory grows steadily instead of
// depending on the agent happening to save something mid-conversation.
package hotsync

import (
	"context"

	"github.com/MelloB1989/karmax/pkg/loopkit"
)

func init() {
	loopkit.Register(loopkit.Loop{
		Name:        "hot-sync",
		Description: "Active memory capture: every 2h the agent scans recent WhatsApp + conversations and ingests durable NEW facts to long-term memory (deduplicated).",
		Schedule:    loopkit.Every("2h"),
		Run:         run,
	})
}

const prompt = `You are running the ACTIVE MEMORY-CAPTURE pass. Your job: turn what has happened recently into durable long-term memory, so nothing important is lost.

Scan RECENT activity for anything worth remembering:
- The operator's ACTIVE WhatsApp chats (people and groups messaged in roughly the last week) via whatsapp.read. IGNORE large community/promotional groups they rarely text in.
- Recent decisions, commitments, deadlines, project updates, new people, changed facts, and stated preferences.

For each genuinely durable item, call memory.ingest with ONE clean, standalone FACT (who a person is, a commitment + its date, a decision + why, a project's new state, a preference). Set category and importance; add ttl_days for time-bound facts. Deduplication is automatic, so when a fact CHANGED, ingest the corrected version — the merge pass reconciles it with the old one.

NEVER ingest raw message text, greetings, casual chatter, your own replies, or whole-conversation dumps. If nothing durable happened in a chat, skip it. Aim for a handful of high-signal facts, not volume. Only message the operator if something is genuinely urgent.`

func run(ctx context.Context, k loopkit.Kit) error {
	_, err := k.Ask(ctx, prompt)
	return err
}
