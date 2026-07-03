// Package dailybriefing is a default KARMAX loop: the agent COMPILES the
// morning briefing, then the loop delivers it deterministically (app feed +
// optional WhatsApp) rather than relying on the agent to call the delivery
// tools — smaller models skip them. The WhatsApp recipient comes from
// KARMAX_LOOP_DAILY_BRIEFING_WHATSAPP via Kit.Config, so no number in source.
package dailybriefing

import (
	"context"
	"fmt"
	"strings"

	"github.com/MelloB1989/karmax-loops/internal/shared"
	"github.com/MelloB1989/karmax/pkg/loopkit"
)

func init() {
	loopkit.Register(loopkit.Loop{
		Name:        "daily-briefing",
		Description: "Morning briefing to the app feed (+ WhatsApp, if KARMAX_LOOP_DAILY_BRIEFING_WHATSAPP is set).",
		Schedule:    loopkit.Cron("0 30 8 * * *"), // 08:30 daily
		Run:         run,
	})
}

const prompt = `Write the operator's morning briefing. Gather what you can — today's calendar and reminders, ` +
	`the latest tech-news digest from your memory, any open coding sessions, and anything pending or urgent from recent ` +
	`WhatsApp; skip any source that errors, don't get stuck. Then RETURN the briefing itself as your reply: a few short, ` +
	`skimmable bullet lines, no preamble. Do NOT call app.push or comms.send — just return the briefing text; delivery is ` +
	`handled for you.`

func run(ctx context.Context, k loopkit.Kit) error {
	text, err := k.Ask(ctx, prompt)
	if err != nil {
		return err
	}
	text = strings.TrimSpace(text)
	if text == "" || shared.LooksLikeError(text) {
		return fmt.Errorf("daily-briefing: no usable briefing (%.120s)", text)
	}
	if err := k.Notify("Morning briefing", text); err != nil {
		k.Logf("app push failed: %v", err)
	}
	if num := strings.TrimSpace(k.Config("whatsapp")); num != "" {
		if err := k.SendWhatsApp(ctx, num, text); err != nil {
			return fmt.Errorf("daily-briefing: whatsapp send failed: %w", err)
		}
	}
	return nil
}
