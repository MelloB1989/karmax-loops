// Package hotsync is a default KARMAX loop: it has the main agent scan ACTIVE
// WhatsApp chats every few hours and ingest durable facts to long-term memory.
package hotsync

import (
	"context"

	"github.com/MelloB1989/karmax/pkg/loopkit"
)

func init() {
	loopkit.Register(loopkit.Loop{
		Name:        "hot-sync",
		Description: "Scans active WhatsApp chats every few hours and ingests durable facts to memory.",
		Schedule:    loopkit.Every("4h"),
		Run:         run,
	})
}

const prompt = `Scan the operator's ACTIVE WhatsApp chats (people and groups messaged in roughly the last two ` +
	`weeks) using whatsapp.read. IGNORE large community/promotional groups the operator rarely texts in. For each ` +
	`genuinely important item, call memory.ingest with ONE distilled FACT per entry — who a person is, a commitment, ` +
	`a deadline, a decision, a project update — written as a clean standalone statement. NEVER ingest raw message ` +
	`text, greetings, casual chatter, your own replies, or whole-conversation dumps. If a chat has nothing durable, ` +
	`skip it. Only message your operator if something is urgent.`

func run(ctx context.Context, k loopkit.Kit) error {
	_, err := k.Ask(ctx, prompt)
	return err
}
