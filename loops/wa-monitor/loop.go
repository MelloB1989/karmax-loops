// Package wamonitor is the proactive WhatsApp proxy as an EVENT-DRIVEN loop:
// it fires on every incoming comms.message event (pushed by the wacli webhook —
// no polling, no scheduled LLM spend) and, for messages from MONITORED
// non-operator chats, has the Claude harness act on the operator's behalf:
// routine replies are sent in their voice, real decisions become approvals,
// operator-only items become phone reminders. Which chats are monitored is
// decided by the wacli webhook's scope (managed via the agent's
// whatsapp.monitor tool) — nothing is hardcoded here.
package wamonitor

import (
	"context"
	"strings"

	"github.com/MelloB1989/karmax-loops/internal/shared"
	"github.com/MelloB1989/karmax/pkg/loopkit"
)

func init() {
	loopkit.Register(loopkit.Loop{
		Name:        "wa-monitor",
		Description: "Event-driven WhatsApp proxy: on each message in a monitored chat, acts for the operator (routine replies), files approvals for decisions, reminders for operator-only items.",
		Events:      []string{"comms.message"},
		Run:         run,
	})
}

func run(ctx context.Context, k loopkit.Kit) error {
	t := k.Trigger()
	if t.Kind != loopkit.TriggerEvent {
		k.Logf("wa-monitor: fires on comms.message events; a %s run does nothing", t.Kind)
		return nil
	}
	content, _ := t.Payload["content"].(string)
	chatID, _ := t.Payload["channel_id"].(string)
	karmaxChannelID, _ := t.Payload["karmax_channel_id"].(string)
	senderName, _ := t.Payload["sender_name"].(string)
	if senderName == "" {
		senderName, _ = t.Payload["chat_name"].(string)
	}
	isGroup, _ := t.Payload["is_group"].(bool)

	// Only third-party (non-operator) chats are proxied. Unknown/empty chat ids
	// default to OPERATOR so we never accidentally auto-proxy — mirroring the
	// agent's own routing, which handles operator chats as commands.
	operator := shared.OperatorChatSet()
	if len(operator) == 0 || chatID == "" || operator[shared.NormalizeChatID(chatID)] {
		return nil
	}
	// Deterministic mention detection: WhatsApp embeds an @-mention inline as
	// "@<number>", so we can tell in Go (not via the model) whether the operator
	// was directly addressed — the model was unreliable at noticing it.
	mentioned := isOperatorMentioned(content, operator)

	// Skip trivial acks (save tokens) and non-chat events — but NEVER skip a
	// message that directly @-mentions the operator, even if it's short.
	if karmaxChannelID == "" || (!mentioned && isTrivial(content)) {
		return nil
	}

	who := senderName
	if who == "" {
		who = chatID
	}
	k.Logf("wa-monitor: proxying message in %q (group=%v, mentioned=%v)", who, isGroup, mentioned)

	wacli := strings.TrimSpace(k.Config("wacli"))
	if wacli == "" {
		wacli = k.HostTool("wacli")
	}

	operatorDesc := "the operator"
	if len(operator) > 0 {
		ids := make([]string, 0, len(operator))
		for id := range operator {
			ids = append(ids, id)
		}
		operatorDesc = "the operator (their own numbers/JIDs: " + strings.Join(ids, ", ") + ")"
	}

	context_ := "A monitored 1:1 chat just messaged " + operatorDesc + "."
	policy := "   - If a reply/action is ROUTINE and you're confident how the operator would respond (acknowledgements, simple scheduling, sharing already-known info, confirming availability), DO IT NOW: send with `" + wacli + " send --to " + chatID + " --text \"...\"` in the operator's natural human voice (concise; never say you're an AI/assistant). Use the `gws` CLI for calendar/email if clearly asked.\n" +
		"   - If it involves a real DECISION, a commitment, money, or anything sensitive/ambiguous, or you're not confident → DO NOT send anything; flag it as APPROVE.\n" +
		"   - If it's something ONLY the operator can personally do (send a document/file you don't have, a personal reply, an offline task): flag it as REMIND.\n"
	if isGroup && mentioned {
		// The operator was DIRECTLY @-mentioned — they are unambiguously being
		// addressed. A mention must never be silently ignored.
		context_ = "A monitored GROUP chat just @-MENTIONED " + operatorDesc + " directly — they are being addressed and a response is expected."
		policy = "   - The operator was DIRECTLY @-mentioned, so you MUST respond somehow — never SKIP this.\n" +
			"   - If you can answer routinely in the operator's voice (a question you know the answer to, an acknowledgement, availability, a simple follow-up): reply NOW via `" + wacli + " send --to " + chatID + " --text \"...\"` (concise, human, never reveal you're an AI).\n" +
			"   - If it needs a real DECISION, a commitment, money, or anything sensitive/ambiguous, or you're not confident: DO NOT send — flag it as APPROVE with your suggested reply so the operator decides.\n" +
			"   - Only if it's something ONLY the operator can personally do: flag REMIND. But a plain mention with a question defaults to a reply.\n"
	} else if isGroup {
		context_ = "A monitored GROUP chat just had a new message. " + operatorDesc + " is a member but was NOT @-mentioned."
		policy = "   - This is a GROUP and the operator was NOT directly @-mentioned. Only SEND a reply if the operator is clearly being asked a question they must answer. Reply via `" + wacli + " send --to " + chatID + " --text \"...\"` in the operator's casual voice, and only for genuinely routine/known answers.\n" +
			"   - Do NOT reply to general group discussion or messages meant for other members.\n" +
			"   - If the message is a meaningful update on an active project/deal/commitment (e.g. a client saying they'll get back, a deadline, a decision) but needs no reply, flag it as APPROVE so the operator sees it — do not silently skip important client/deal activity.\n" +
			"   - Only truly irrelevant chatter is SKIP.\n"
	}

	prompt := "You are the proactive WhatsApp assistant managing the operator's WhatsApp account via the wacli CLI. " + context_ + "\n\n" +
		"Chat: " + who + "\n" +
		"Chat id: " + chatID + "\n" +
		"Latest message: " + content + "\n\n" +
		"Steps:\n" +
		"1. Read recent context: run `" + wacli + " messages --chat " + chatID + " --limit 15` (newest last). If it's already handled/answered and nothing new is needed, do nothing.\n" +
		"2. Decide on the operator's behalf:\n" + policy +
		"3. Output EXACTLY one line, beginning with one of:\n" +
		"   ACTED: <one line on what you sent/did>\n" +
		"   APPROVE: <what it is + your suggested reply/action, for the operator>\n" +
		"   REMIND: <something ONLY the operator can personally do> | due: <ISO-8601 with timezone; omit '| due:' if no concrete deadline>\n" +
		"   SKIP: <why nothing was needed>"

	out, err := k.Harness(ctx, prompt)
	if err != nil || shared.LooksLikeError(out) {
		// Never fail silently: the operator must know a monitored message went
		// unhandled (especially while they sleep).
		k.Logf("wa-monitor: harness failed for %s: %v %.120s", who, err, out)
		_ = k.Notify("⚠️ Couldn't handle — "+who,
			"A monitored message needs you (assistant failed): "+truncate(content, 200))
		return nil
	}
	report(k, who, lastLine(out))
	return nil
}

// report routes the harness outcome deterministically: ACTED → informational
// notification, APPROVE → approvals inbox, REMIND → phone reminder, SKIP → log.
func report(k loopkit.Kit, who, outcome string) {
	outcome = strings.TrimSpace(outcome)
	if outcome == "" {
		return
	}
	upper := strings.ToUpper(outcome)
	detail := func(prefix string) string {
		d := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(outcome[len(prefix):]), ":"))
		if d == "" {
			d = outcome
		}
		return d
	}
	switch {
	case strings.HasPrefix(upper, "SKIP"):
		k.Logf("wa-monitor: nothing needed for %s", who)
	case strings.HasPrefix(upper, "APPROVE"):
		if err := k.Propose("Decision — "+who,
			"The wa-monitor loop flagged this while handling a monitored chat.", detail("APPROVE")); err != nil {
			k.Logf("wa-monitor: propose failed: %v (falling back to notification)", err)
			_ = k.Notify("🤔 Needs your decision — "+who, outcome)
		}
	case strings.HasPrefix(upper, "REMIND"):
		item := detail("REMIND")
		due := ""
		if head, tail, ok := strings.Cut(item, "| due:"); ok {
			item, due = strings.TrimSpace(head), strings.TrimSpace(tail)
		}
		if err := k.Remind(truncate(item, 100), due, "From "+who+" (wa-monitor): only you can do this one."); err != nil {
			k.Logf("wa-monitor: remind failed: %v (falling back to notification)", err)
			_ = k.Notify("⏰ You need to do this — "+who, item)
		}
	default: // ACTED or freeform
		_ = k.Notify("✅ Handled — "+who, outcome)
	}
}

// isOperatorMentioned reports whether the operator's own WhatsApp number was
// @-mentioned in the message. WhatsApp embeds mentions inline in the message
// body as "@<number-digits>" (the display name is resolved client-side), so a
// mention of the operator appears as "@" followed by their number. `operator`
// holds the operator's normalized numbers/JIDs (digits, no @domain).
func isOperatorMentioned(content string, operator map[string]bool) bool {
	if !strings.Contains(content, "@") {
		return false
	}
	// Digits-only copy of the content so "@ 91 76..." / formatting variations
	// still match the operator's digit string.
	var digits strings.Builder
	for _, r := range content {
		if r >= '0' && r <= '9' {
			digits.WriteRune(r)
		}
	}
	contentDigits := digits.String()
	for num := range operator {
		if num == "" || len(num) < 6 {
			continue
		}
		if strings.Contains(content, "@"+num) || strings.Contains(contentDigits, num) {
			return true
		}
	}
	return false
}

// isTrivial reports whether an incoming message is too trivial to warrant
// spinning up the assistant (acks, emoji, one-word replies).
func isTrivial(s string) bool {
	t := strings.TrimSpace(s)
	if t == "" || len([]rune(t)) <= 3 {
		return true
	}
	switch strings.ToLower(t) {
	case "ok", "okay", "okk", "thanks", "thank you", "thx", "ty", "cool", "nice",
		"great", "done", "haha", "lol", "yep", "nope", "sure", "fine", "hmm", "hmmm":
		return true
	}
	return false
}

// lastLine returns the final non-empty line of the harness output (the loop
// instructs it to end with the one-line outcome), truncated for display.
func lastLine(s string) string {
	s = strings.TrimSpace(s)
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return truncate(l, 600)
		}
	}
	return ""
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
