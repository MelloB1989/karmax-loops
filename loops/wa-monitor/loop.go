// Package wamonitor is the proactive WhatsApp proxy as an EVENT-DRIVEN loop:
// it fires on every incoming comms.message event (pushed by the wacli webhook —
// no polling, no scheduled LLM spend) and, for messages from MONITORED
// non-operator chats, has the Claude harness act on the operator's behalf:
// routine replies are sent in their voice, real decisions become approvals,
// operator-only items become phone reminders. Which chats are monitored is
// decided by the wacli webhook's scope (managed via the agent's
// whatsapp.monitor tool) — nothing is hardcoded here.
//
// NO MESSAGE THAT EXPECTS A RESPONSE GOES UNANSWERED: when the harness can't
// (or shouldn't) reply in the operator's voice — it flagged APPROVE/REMIND, or
// it failed outright — the loop itself sends a brief assistant note ("Kartik's
// away; I'm KARMAX, I've notified him") in DMs and group-mentions, rate-limited
// per chat so the same conversation never gets it twice in a row.
package wamonitor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MelloB1989/karmax-loops/internal/shared"
	"github.com/MelloB1989/karmax/pkg/loopkit"
)

func init() {
	loopkit.Register(loopkit.Loop{
		Name:        "wa-monitor",
		Description: "Event-driven WhatsApp proxy: on each message in a monitored chat, acts for the operator (routine replies), files approvals for decisions, reminders for operator-only items — and when it can't reply in the operator's voice, acknowledges the sender with an assistant away-note so no one is left hanging.",
		Events:      []string{"comms.message"},
		Run:         run,
	})
}

// awayNoteCooldown is how long after sending an away-note to a chat before the
// loop will send another one there — the flag/approval still happens every
// time; only the sender-facing note is deduplicated.
const awayNoteCooldown = 6 * time.Hour

// ---- per-chat serialization -------------------------------------------------
//
// Every incoming message fires its own loop run. In a busy group several
// messages land within seconds, so multiple runs used to execute CONCURRENTLY:
// each independently read the same recent history, each decided the open
// question still needed answering, and each sent its own reply — the operator
// saw KARMAX answer the same thing two or three times seconds apart.
//
// Fix: at most ONE run per chat at a time. If a run is already in flight for
// this chat, the new event doesn't spawn a second reply — it just marks the
// chat dirty, and the in-flight run does exactly one more pass when it
// finishes. That both removes duplicates and guarantees the late message is
// still considered (the harness re-reads the thread, so it sees whatever was
// already answered and skips it).
type chatGate struct {
	mu      sync.Mutex
	running bool
	pending bool
}

var chatGates sync.Map // chatID -> *chatGate

func gateFor(chatID string) *chatGate {
	g, _ := chatGates.LoadOrStore(chatID, &chatGate{})
	return g.(*chatGate)
}

// acquire reports whether the caller may run now. If another run holds the
// chat, it records that more work arrived and returns false.
func (g *chatGate) acquire() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.running {
		g.pending = true
		return false
	}
	g.running = true
	g.pending = false
	return true
}

// release ends this pass and reports whether new messages arrived meanwhile
// (in which case the caller should make exactly one more pass).
func (g *chatGate) release() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.pending {
		g.pending = false
		return true // stay "running": we immediately do the follow-up pass
	}
	g.running = false
	return false
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
	// Generic "KARMAX is being directly addressed" signals computed by wacli from
	// its OWN identity (no configured numbers): the bot was @-mentioned, or this
	// message is a reply to something the bot sent. The quoted text is already in
	// `content` as "[replying to: …]".
	mentionsMe, _ := t.Payload["mentions_me"].(bool)
	quotedReplyToMe, _ := t.Payload["quoted_is_from_me"].(bool)
	mentionCount := payloadInt(t.Payload["mention_count"])
	triggerMsgID, _ := t.Payload["wacli_message_id"].(string)

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

	// A "reply group" is a trusted working group (e.g. a client project group)
	// the operator wants KARMAX to act in AS them — reply-happy even without an
	// @-mention. Configured via KARMAX_LOOP_WA_MONITOR_REPLY_GROUPS (comma-sep
	// JIDs); nothing hardcoded. Real decisions still become approvals.
	replyGroup := isGroup && isReplyGroup(chatID, k)

	// commanded = the operator @-mentioned KARMAX's OWN number/LID (the bot) in
	// a monitored chat — a direct "KARMAX, do this" instruction to carry out and
	// post the result here. Bot ids come from KARMAX_LOOP_WA_MONITOR_BOT_MENTIONS
	// (comma-sep phone/LID digits); nothing hardcoded.
	// Direct engagement with KARMAX — generic signals first (wacli-provided),
	// then the optional configured bot-mention list as a fallback.
	commanded := mentionsMe || quotedReplyToMe || isBotMentioned(content, k)

	// RULE: being @-mentioned ALWAYS earns a response — in ANY group, whether or
	// not that group is tracked. wacli delivers out-of-scope mentions so this
	// loop gets the chance to decide.
	//
	// EXCEPTION: an "@all"/"@everyone"-style blast mentions every participant,
	// so it sweeps KARMAX up with everyone else — that is not being addressed.
	// In a group we DON'T track, ignore it (in tracked groups we still look,
	// since those conversations are the operator's own). Threshold is
	// configurable via KARMAX_LOOP_WA_MONITOR_MASS_MENTION_MIN.
	if mentionsMe && isGroup && mentionCount >= massMentionMin(k) && !isTrackedChat(ctx, k, chatID) {
		k.Logf("wa-monitor: ignoring @all-style mention (%d mentions) in untracked group %q", mentionCount, senderName)
		return nil
	}

	// Skip trivial acks (save tokens) and non-chat events — but NEVER skip a
	// message that @-mentions the operator/KARMAX or lands in a reply group.
	if karmaxChannelID == "" || (!mentioned && !replyGroup && !commanded && isTrivial(content)) {
		return nil
	}

	who := senderName
	if who == "" {
		who = chatID
	}
	// One pass at a time per chat: if another run already holds this chat, fold
	// this message into it instead of composing a second, duplicate reply.
	gate := gateFor(chatID)
	if !gate.acquire() {
		k.Logf("wa-monitor: already replying in %q — folded this message into the in-flight pass", who)
		return nil
	}

	// sentThisRun records whether a pass actually put a message in the chat, so
	// the follow-up pass knows to hold back.
	justReplied := false
	sentThisRun := false

	// justReplied is true on a follow-up pass that comes straight after this run
	// already sent a message. Without it the second pass would answer the same
	// conversational beat again — which is exactly how the group got two
	// near-identical replies seconds apart.
	doPass := func(justReplied bool) error {
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

		// addressed = someone is talking TO the operator and expects a response
		// (any DM, a group @-mention, or a trusted reply-group where the operator
		// acts as themselves). These must never end in silence: either the harness
		// replies, or the loop sends the assistant away-note below.
		addressed := !isGroup || mentioned || replyGroup || commanded

		context_ := "A monitored 1:1 chat just messaged " + operatorDesc + "."
		policy := "   - LEAN TOWARD REPLYING. If a reply/action is routine and you're reasonably sure how the operator would respond (acknowledgements, answering things you know from context, simple scheduling, sharing already-known info, confirming availability, a natural conversational reply), SEND IT NOW: `" + wacli + " send --to " + chatID + " --text \"...\"` in the operator's natural human voice (concise; never say you're an AI/assistant when speaking AS the operator). Use the `gws` CLI for calendar/email if clearly asked. When in doubt between replying and staying silent, REPLY.\n" +
			"   - Flag APPROVE only for a real DECISION, a commitment, money, or something genuinely sensitive where a wrong reply causes harm — include your suggested reply. Do NOT send anything yourself in that case, and do NOT send any \"he's away\" placeholder — the system automatically acknowledges the sender when you flag APPROVE or REMIND.\n" +
			"   - If it's something ONLY the operator can personally do (send a document/file you don't have, a personal reply, an offline task): flag it as REMIND.\n" +
			"   - SKIP is ONLY for messages that need no response at all (chatter, FYIs, spam). If the sender expects ANY response, never SKIP — reply or flag it.\n"
		if commanded {
			// KARMAX is being DIRECTLY engaged — @-mentioned, or someone replied to a
			// message KARMAX sent (the quoted text is inline as "[replying to: …]").
			// Highest priority: always respond, reading the full quoted context.
			context_ = "You (KARMAX) are being DIRECTLY ENGAGED here — either @-mentioned, or someone replied to a message YOU sent. If it's a reply, the message you sent is shown inline as \"[replying to: …]\"; read BOTH it and the new message so you have the full thread. A response is ALWAYS expected — never ignore this."
			policy = "   - Read the FULL context: the new message AND, for a reply, the quoted text it is responding to.\n" +
				"   - If it's an instruction/request/question you can handle (find something, do X, send Y, answer a question) — CARRY IT OUT FULLY using your tools/shell (research the web, run commands, use gws/gh, generate the answer), then POST the result in THIS chat via `" + wacli + " send --to " + chatID + " --text \"...\"` (use `--media <path>` if a file is wanted). Do the actual work, don't just acknowledge.\n" +
				"   - If it's a conversational reply or follow-up to what you said (a correction, a 'yes do it', a reaction), respond naturally HERE in the operator's voice to continue the thread.\n" +
				"   - Report ACTED with what you did/sent. Never SKIP a direct engagement. Only flag APPROVE if fulfilling it would spend money, post something risky publicly, or delete data.\n"
		} else if isGroup && mentioned {
			// The operator was DIRECTLY @-mentioned — they are unambiguously being
			// addressed. A mention must never be silently ignored.
			context_ = "A monitored GROUP chat just @-MENTIONED " + operatorDesc + " directly — they are being addressed and a response is expected."
			policy = "   - The operator was DIRECTLY @-mentioned, so you MUST respond somehow — never SKIP this.\n" +
				"   - LEAN TOWARD REPLYING in the operator's voice (a question you can answer, an acknowledgement, availability, a follow-up): reply NOW via `" + wacli + " send --to " + chatID + " --text \"...\"` (concise, human, never reveal you're an AI when speaking AS the operator).\n" +
				"   - Flag APPROVE (with your suggested reply) only for a real DECISION, commitment, money, or something genuinely sensitive. Do NOT send a \"he's away\" placeholder yourself — the system acknowledges the sender automatically when you flag.\n" +
				"   - Only if it's something ONLY the operator can personally do: flag REMIND. A plain mention with a question defaults to a reply.\n"
		} else if replyGroup {
			// Trusted working group: the operator wants KARMAX to act as them here,
			// like a small client/project group where a reply is expected.
			context_ = "A monitored TRUSTED WORKING GROUP just had a new message. " + operatorDesc + " actively participates here as themselves and WANTS you to reply on their behalf — treat it like a 1:1 with the operator's team."
			policy = "   - LEAN TOWARD REPLYING as the operator. For routine/known things — acknowledging an update, answering something you know, confirming availability/next steps, a natural conversational reply to a teammate/client — SEND IT NOW: `" + wacli + " send --to " + chatID + " --text \"...\"` in the operator's natural voice (concise, human, never reveal you're an AI when speaking AS the operator). When in doubt between replying and staying silent, REPLY.\n" +
				"   - Flag APPROVE (with your suggested reply) only for a real DECISION, commitment, money, pricing, scope, or anything genuinely sensitive where a wrong reply is costly. Don't send a placeholder yourself — the system acknowledges the sender when you flag.\n" +
				"   - Ignore messages clearly aimed at another specific member and not the operator's side. Only truly irrelevant chatter is SKIP.\n"
		} else if isGroup {
			context_ = "A monitored GROUP chat just had a new message. " + operatorDesc + " is a member but was NOT @-mentioned."
			policy = "   - This is a GROUP and the operator was NOT directly @-mentioned. Only SEND a reply if the operator is clearly being asked a question they must answer. Reply via `" + wacli + " send --to " + chatID + " --text \"...\"` in the operator's casual voice, and only for genuinely routine/known answers.\n" +
				"   - Do NOT reply to general group discussion or messages meant for other members.\n" +
				"   - If the message is a meaningful update on an active project/deal/commitment (e.g. a client saying they'll get back, a payment confirmation, a deadline) but needs no reply or decision, use INFORM so the operator gets a notification — do NOT file it as an APPROVE (that inbox is for real decisions only), and do not silently skip important client/deal activity.\n" +
				"   - Reserve APPROVE for a genuine decision the operator must make (spend/pricing/scope/commitment/sensitive).\n" +
				"   - Only truly irrelevant chatter is SKIP.\n"
		}

		// Per-chat SHORT-TERM MEMORY: what KARMAX already said/decided in THIS
		// chat recently, rendered straight into the prompt so the harness has
		// continuity and doesn't re-answer something it just handled.
		shortMem := renderShortMemory(k, chatID)

		// Let the harness quote the exact message that triggered this run, so
		// the reply threads under it instead of floating at the end of the chat.
		replyHint := ""
		if strings.TrimSpace(triggerMsgID) != "" {
			replyHint = "Reply id: " + triggerMsgID + " — answer by QUOTING this message: add `--reply-to " + triggerMsgID + "` to your wacli send so it threads under the message you're replying to.\n"
		}

		prompt := "You are the proactive WhatsApp assistant managing the operator's WhatsApp account via the wacli CLI. " + context_ + "\n\n" +
			"Chat: " + who + "\n" +
			"Chat id: " + chatID + "\n" +
			replyHint +
			"Latest message: " + content + "\n\n" +
			shortMem +
			"Steps:\n" +
			"1. Read recent context: run `" + wacli + " messages --chat " + chatID + " --limit 15` (newest last). If it's already handled/answered and nothing new is needed, do nothing.\n" +
			"2. Decide on the operator's behalf:\n" + policy +
			"3. REQUIRED: end your response with EXACTLY one outcome line — the VERY LAST line, beginning with one of these verbs (mandatory even if you already replied or acted; if you omit it the message is treated as unhandled and escalated). Choose CAREFULLY — do NOT use APPROVE for things you can handle yourself or for pure updates:\n" +
			"   ACTED: <what you sent/did on the operator's behalf — prefer this for anything routine>\n" +
			"   APPROVE: <ONLY a real decision the operator must personally make — approving spend/pricing/scope, a commitment, something risky/irreversible/sensitive — plus your suggested reply. If you could handle it, ACT. If it just needs them to know, INFORM.>\n" +
			"   REMIND: <something ONLY the operator can personally do> | due: <ISO-8601 with timezone; omit '| due:' if no concrete deadline>\n" +
			"   INFORM: <an update the operator should simply KNOW — a payment/receipt confirmation, a status update, 'they'll get back to us', a doc received — needs NO decision and NO reply. Becomes a notification, not an approval.>\n" +
			"   SKIP: <nothing worth surfacing — chatter, noise, already handled>"

		// ---- GATEWAY FIRST -------------------------------------------------
		// Try one cheap main-model call before spawning a Claude Code run. The
		// gateway has NO tools, so it either writes the reply itself, routes the
		// message, or asks to escalate. Claude Code is the exception, not the
		// default: it used to run for EVERY incoming message.
		thread, _ := k.ReadWhatsApp(ctx, chatID, 15)
		gwPrompt := "You are the operator's WhatsApp assistant. " + context_ + "\n\n" +
			"Chat: " + who + "\n" +
			"Latest message: " + content + "\n\n" +
			shortMem +
			"Recent thread (oldest first):\n" + truncate(thread, 4000) + "\n\n" +
			"You have ONE tool: `wacli`, the operator's WhatsApp CLI. Use it to look things up before answering — e.g. read another conversation with\n" +
			"  args: [\"messages\", \"--chat\", \"<name|phone|jid>\", \"--limit\", \"15\"]\n" +
			"or resolve a person with args: [\"resolve\", \"<name>\"]. If someone asks what another chat said, LOOK IT UP instead of saying you can't see it.\n\n" +
			justRepliedNote(justReplied) +
			"How to decide:\n" + policy + "\n" +
			"Answer with ONE verb on the FIRST line, then its content:\n" +
			"REPLY: <the exact message to send, in the operator's voice — use this whenever you can simply answer>\n" +
			"ESCALATE: <why> — ONLY when it needs tools you don't have: web research, running commands, reading files/media, calendar/email actions, or looking something up you don't know.\n" +
			"APPROVE: <a real decision the operator must make + your suggested reply>\n" +
			"REMIND: <something only the operator can personally do> | due: <ISO-8601 or omit>\n" +
			"INFORM: <an update they should just know; no reply needed>\n" +
			"SKIP: <nothing worth doing>"

		var out string
		var err error
		outcome := ""
		escalate := true

		if gwOut, gwErr := k.Gateway(ctx, gwPrompt, wacliTool(wacli)); gwErr != nil {
			k.Logf("wa-monitor: gateway call failed for %q (%v) — escalating to harness", who, gwErr)
		} else if shared.LooksLikeError(gwOut) {
			k.Logf("wa-monitor: gateway returned an error/refusal for %q — escalating", who)
		} else {
			verb, payload := parseGatewayOutcome(gwOut)
			switch verb {
			case "REPLY":
				if strings.TrimSpace(payload) == "" {
					k.Logf("wa-monitor: gateway REPLY was empty for %q — escalating", who)
				} else if serr := sendViaWacli(ctx, k, chatID, payload, triggerMsgID); serr != nil {
					k.Logf("wa-monitor: gateway reply send failed for %q (%v) — escalating", who, serr)
				} else {
					outcome = "ACTED: replied — " + oneLineTrunc(payload, 220)
					escalate = false
					sentThisRun = true
					k.Logf("wa-monitor: gateway handled %q without claude_code", who)
				}
			case "ESCALATE":
				k.Logf("wa-monitor: gateway escalating %q — %s", who, oneLineTrunc(payload, 140))
			case "APPROVE", "REMIND", "INFORM", "SKIP":
				outcome = verb + ": " + oneLineTrunc(payload, 400)
				escalate = false
				k.Logf("wa-monitor: gateway routed %q as %s (no claude_code)", who, verb)
			default:
				k.Logf("wa-monitor: gateway gave no usable verb for %q — escalating", who)
			}
		}

		if !escalate {
			kind := report(k, who, outcome)
			if kind == "acted" || kind == "inform" {
				_ = k.ShortSet(chatID, "did_"+time.Now().UTC().Format("150405"), truncate(outcome, 300), shortMemoryTTL)
			}
			if addressed && (kind == "approve" || kind == "remind") {
				sendAwayNote(ctx, k, chatID, who, content, isGroup)
			}
			return nil
		}

		// ---- ESCALATED: full Claude Code harness (tools/shell/research) ------
		out, err = k.Harness(ctx, prompt)
		if err != nil || shared.LooksLikeError(out) {
			// Never fail silently: the operator must know a monitored message went
			// unhandled (especially while they sleep) — and the SENDER shouldn't be
			// left hanging either.
			k.Logf("wa-monitor: harness failed for %s: %v %.120s", who, err, out)
			_ = k.Notify("⚠️ Couldn't handle — "+who,
				"A monitored message needs you (assistant failed): "+truncate(content, 200))
			if addressed {
				sendAwayNote(ctx, k, chatID, who, content, isGroup)
			}
			return nil
		}
		outcome = lastLine(out)
		kind := report(k, who, outcome)

		// Record what we just did in this chat's short-term memory (durable but
		// self-expiring), so the next message in the thread carries the context
		// and KARMAX doesn't repeat itself.
		if kind == "acted" || kind == "inform" {
			_ = k.ShortSet(chatID, "did_"+time.Now().UTC().Format("150405"), truncate(outcome, 300), shortMemoryTTL)
		}

		// A monitored message must NEVER silently vanish. If the harness produced no
		// clean outcome (empty) or unrecognized prose (unknown) — e.g. it read the
		// chat but didn't declare a decision — surface it to the operator instead of
		// dropping it. This is the "Siva replied but KARMAX did nothing" gap.
		if kind == "empty" || kind == "unknown" {
			_ = k.Notify("👀 Needs a look — "+who,
				"I saw a message in a monitored chat but couldn't cleanly decide what to do — take a look:\n"+truncate(content, 300))
			if addressed {
				sendAwayNote(ctx, k, chatID, who, content, isGroup)
			}
			return nil
		}

		// The harness flagged instead of replying (APPROVE/REMIND) while the sender
		// was talking TO the operator — acknowledge them as the assistant so the
		// message never just hangs. The TRIGGER is deterministic (Go decides an
		// acknowledgement must happen, rate-limited); the WORDING is the LLM's.
		if addressed && (kind == "approve" || kind == "remind") {
			sendAwayNote(ctx, k, chatID, who, content, isGroup)
		}
		return nil
	}

	// Run, then make exactly one more pass if messages arrived while we worked
	// (the harness re-reads the thread, so it skips anything already answered).
	for {
		err := doPass(justReplied)
		if !gate.release() {
			return err
		}
		justReplied = sentThisRun
		k.Logf("wa-monitor: new messages arrived in %q while replying — one more pass (just replied: %v)", who, justReplied)
	}
}

// sendAwayNote tells the sender the operator is away and KARMAX has notified
// them. The note itself is COMPOSED BY THE LLM (contextual to the sender and
// their message — nothing canned); Go only guarantees it happens and
// rate-limits it: at most one note per chat per awayNoteCooldown. The
// flag/approval itself still files for every message.
func sendAwayNote(ctx context.Context, k loopkit.Kit, chatID, who string, incoming string, isGroup bool) {
	state, path := loadAwayState()
	if last, ok := state[chatID]; ok && time.Since(time.Unix(last, 0)) < awayNoteCooldown {
		return
	}

	setting := "a 1:1 WhatsApp chat"
	if isGroup {
		setting = "a WhatsApp group where the operator was @-mentioned"
	}
	// Who the operator is, for the note's wording. Configurable per install
	// (KARMAX_LOOP_WA_MONITOR_OPERATOR_NAME); generic when unset.
	operatorRef := strings.TrimSpace(k.Config("operator_name"))
	if operatorRef == "" {
		operatorRef = "the account owner"
	}
	note, err := k.Summarize(ctx,
		"Compose a short WhatsApp message (1-2 sentences) to send in "+setting+" on behalf of the operator ("+operatorRef+"), who is currently away.\n\n"+
			"Sender/chat (the OTHER person — NOT the operator; never present yourself as their assistant): "+who+"\n"+
			"Their message: "+truncate(incoming, 400)+"\n\n"+
			"The message must, in your own natural words: identify itself as KARMAX, the assistant of the operator ("+operatorRef+"); say the operator is away from their phone right now; briefly acknowledge what the sender asked/said (so it doesn't feel canned); and assure them the operator has been notified and will get back to them. "+
			"Warm, human, concise. No emojis unless natural, no markdown, no quotes around the text, no signature. Output ONLY the message text.")
	note = strings.TrimSpace(strings.Trim(strings.TrimSpace(note), `"“”`))
	if err != nil || note == "" || shared.LooksLikeError(note) {
		// Couldn't compose — don't send canned text; the operator is already
		// notified via the APPROVE/notify path, so just log it.
		k.Logf("wa-monitor: away-note compose failed for %s: %v %.80s", who, err, note)
		return
	}
	if err := k.SendWhatsApp(ctx, chatID, truncate(note, 500)); err != nil {
		k.Logf("wa-monitor: away-note to %s failed: %v", who, err)
		return
	}
	k.Logf("wa-monitor: sent away-note to %s", who)
	state[chatID] = time.Now().Unix()
	saveAwayState(path, state)
}

func awayStatePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".karmax", "wa-monitor-away.json")
}

func loadAwayState() (map[string]int64, string) {
	path := awayStatePath()
	state := map[string]int64{}
	if path == "" {
		return state, path
	}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &state)
	}
	return state, path
}

func saveAwayState(path string, state map[string]int64) {
	if path == "" {
		return
	}
	b, err := json.Marshal(state)
	if err != nil {
		return
	}
	_ = os.WriteFile(path, b, 0o644)
}

// report routes the harness outcome deterministically and ALWAYS logs the
// decision (so a "why didn't it act?" is answerable from the journal). Returns
// the classified kind: "acted" | "approve" | "remind" | "skip" | "empty"
// (harness emitted no outcome line) | "unknown" (emitted prose that matches no
// verb). The caller surfaces empty/unknown so a message is never silently lost.
func report(k loopkit.Kit, who, outcome string) string {
	outcome = strings.TrimSpace(outcome)
	upper := strings.ToUpper(outcome)
	detail := func(prefix string) string {
		d := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(outcome[len(prefix):]), ":"))
		if d == "" {
			d = outcome
		}
		return d
	}
	switch {
	case outcome == "":
		k.Logf("wa-monitor: %s — harness returned NO outcome line (will surface)", who)
		return "empty"
	case strings.HasPrefix(upper, "SKIP"):
		k.Logf("wa-monitor: SKIP %s — %s", who, truncate(detail("SKIP"), 160))
		return "skip"
	case strings.HasPrefix(upper, "APPROVE"):
		k.Logf("wa-monitor: APPROVE %s — %s", who, truncate(detail("APPROVE"), 160))
		if err := k.Propose("Decision — "+who,
			"The wa-monitor loop flagged this while handling a monitored chat.", detail("APPROVE")); err != nil {
			k.Logf("wa-monitor: propose failed: %v (falling back to notification)", err)
			_ = k.Notify("🤔 Needs your decision — "+who, outcome)
		}
		return "approve"
	case strings.HasPrefix(upper, "REMIND"):
		item := detail("REMIND")
		due := ""
		if head, tail, ok := strings.Cut(item, "| due:"); ok {
			item, due = strings.TrimSpace(head), strings.TrimSpace(tail)
		}
		k.Logf("wa-monitor: REMIND %s — %s", who, truncate(item, 160))
		if err := k.Remind(truncate(item, 100), due, "From "+who+" (wa-monitor): only you can do this one."); err != nil {
			k.Logf("wa-monitor: remind failed: %v (falling back to notification)", err)
			_ = k.Notify("⏰ You need to do this — "+who, item)
		}
		return "remind"
	case strings.HasPrefix(upper, "INFORM"):
		// FYI update the operator should know but that needs NO decision — a
		// notification, NOT an approval. This is what stops "notification sent
		// as an approval".
		k.Logf("wa-monitor: INFORM %s — %s", who, truncate(detail("INFORM"), 160))
		_ = k.Notify("📣 Update — "+who, detail("INFORM"))
		return "inform"
	case strings.HasPrefix(upper, "ACTED"):
		k.Logf("wa-monitor: ACTED %s — %s", who, truncate(detail("ACTED"), 160))
		_ = k.Notify("✅ Handled — "+who, outcome)
		return "acted"
	default:
		// Non-empty but no recognized verb — the harness did something/rambled
		// but didn't declare a clean outcome. Don't fake "Handled"; surface it.
		k.Logf("wa-monitor: UNPARSEABLE outcome for %s — %s", who, truncate(outcome, 160))
		return "unknown"
	}
}

// parseGatewayOutcome pulls the leading verb and its payload out of a gateway
// reply. The verb is on the first line; the payload is everything after it (so
// a REPLY can span multiple lines). Returns ("","") when there's no known verb.
func parseGatewayOutcome(out string) (verb, payload string) {
	trimmed := strings.TrimSpace(out)
	if trimmed == "" {
		return "", ""
	}
	upper := strings.ToUpper(trimmed)
	for _, v := range []string{"ESCALATE", "APPROVE", "REMIND", "INFORM", "REPLY", "SKIP"} {
		if strings.HasPrefix(upper, v) {
			rest := strings.TrimSpace(trimmed[len(v):])
			rest = strings.TrimSpace(strings.TrimPrefix(rest, ":"))
			return v, rest
		}
	}
	return "", ""
}

// sendViaWacli posts a message through the local wacli HTTP API, quoting
// replyToID when set so the reply threads under the message it answers. Used by
// the gateway path, which composes the text itself instead of delegating the
// send to a Claude Code run.
func sendViaWacli(ctx context.Context, k loopkit.Kit, chatID, text, replyToID string) error {
	payload := map[string]string{"to": chatID, "text": text}
	if strings.TrimSpace(replyToID) != "" {
		payload["reply_to"] = replyToID
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	resp, status, err := k.HTTP(ctx, "POST", k.HostTool("wacli-api")+"/send",
		map[string]string{"Content-Type": "application/json"}, string(body))
	if err != nil {
		return err
	}
	if status < 200 || status > 299 {
		return fmt.Errorf("wacli send: HTTP %d: %s", status, oneLineTrunc(resp, 160))
	}
	return nil
}

// shortMemoryTTL is how long this loop's per-chat scratch notes live. Long
// enough to carry a conversation, short enough that stale context expires on
// its own — the memory engine handles the expiry.
const shortMemoryTTL = 12 * time.Hour

// renderShortMemory formats this chat's short-term memory for the prompt. The
// group is the chat id, so every conversation gets its own scratch space
// (namespaced per-loop by the engine). Empty string when there's nothing yet.
func renderShortMemory(k loopkit.Kit, chatID string) string {
	entries, err := k.ShortAll(chatID)
	if err != nil || len(entries) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("What you already did/noted in THIS chat recently (short-term memory — don't repeat these):\n")
	for i, e := range entries {
		if i >= 8 {
			break
		}
		sb.WriteString("- " + e.Key + ": " + oneLineTrunc(e.Value, 220) + "\n")
	}
	sb.WriteString("\n")
	return sb.String()
}

func oneLineTrunc(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// payloadInt reads a numeric payload field (JSON round-trips make it float64).
func payloadInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return 0
}

// massMentionMin is how many @-mentions in one message make it an
// "@all"/"@everyone" blast rather than someone addressing KARMAX. Override with
// KARMAX_LOOP_WA_MONITOR_MASS_MENTION_MIN.
func massMentionMin(k loopkit.Kit) int {
	if raw := strings.TrimSpace(k.Config("mass_mention_min")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 1 {
			return n
		}
	}
	return 5
}

// isTrackedChat reports whether this chat is one KARMAX actively monitors (it's
// in the wacli webhook's scope). Untracked chats only reach us because they
// @-mentioned the bot, so they get stricter treatment.
func isTrackedChat(ctx context.Context, k loopkit.Kit, chatID string) bool {
	chats, err := shared.MonitoredChats(ctx, k)
	if err != nil {
		return true // can't tell — assume tracked rather than wrongly ignoring
	}
	target := shared.NormalizeChatID(chatID)
	for _, c := range chats {
		if shared.NormalizeChatID(c) == target {
			return true
		}
	}
	return false
}

// isReplyGroup reports whether chatID is a configured trusted "reply group"
// (KARMAX_LOOP_WA_MONITOR_REPLY_GROUPS, comma-separated JIDs) — a group where
// KARMAX replies as the operator without needing an @-mention. Matching is on
// the JID's local part so "120…@g.us" and a bare "120…" both work.
func isReplyGroup(chatID string, k loopkit.Kit) bool {
	raw := strings.TrimSpace(k.Config("reply_groups"))
	if raw == "" {
		return false
	}
	target := groupKey(chatID)
	if target == "" {
		return false
	}
	for _, part := range strings.Split(raw, ",") {
		if groupKey(strings.TrimSpace(part)) == target {
			return true
		}
	}
	return false
}

// groupKey returns the local (pre-@) part of a JID, lowercased.
func groupKey(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if i := strings.IndexByte(s, '@'); i >= 0 {
		s = s[:i]
	}
	return s
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

// isBotMentioned reports whether KARMAX's own number/LID was @-mentioned — the
// operator (or anyone) explicitly summoning the bot to do something. Bot ids
// come from KARMAX_LOOP_WA_MONITOR_BOT_MENTIONS (comma-separated phone/LID
// digit strings — the account's number AND its group @lid, since WhatsApp
// mentions in groups often use the LID). Matches the same way as an operator
// mention (inline "@<digits>").
func isBotMentioned(content string, k loopkit.Kit) bool {
	raw := strings.TrimSpace(k.Config("bot_mentions"))
	if raw == "" || !strings.Contains(content, "@") {
		return false
	}
	var digits strings.Builder
	for _, r := range content {
		if r >= '0' && r <= '9' {
			digits.WriteRune(r)
		}
	}
	contentDigits := digits.String()
	for _, id := range strings.Split(raw, ",") {
		id = strings.TrimSpace(id)
		if i := strings.IndexAny(id, "@:"); i >= 0 {
			id = id[:i]
		}
		if len(id) < 6 {
			continue
		}
		if strings.Contains(content, "@"+id) || strings.Contains(contentDigits, id) {
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

// wacliTool lends the gateway model the operator's WhatsApp CLI for one call.
//
// This is the pattern for making KARMAX capable without crowding its core: the
// capability lives in the loop that needs it, is scoped to a single gateway
// call, and never becomes a globally registered tool. It's what lets the
// gateway answer "what did Siva say?" by actually reading that chat instead of
// claiming it can't see other conversations.
//
// READ-ONLY on purpose: sending is the loop's job (it owns reply-to threading
// and the outcome grammar), so the model can look things up but cannot message
// anyone behind the loop's back.
func wacliTool(wacliPath string) loopkit.Tool {
	allowed := map[string]bool{
		"messages": true, "chats": true, "resolve": true, "contacts": true, "receipts": true,
	}
	return loopkit.Tool{
		Name: "wacli",
		Description: "Run the operator's WhatsApp CLI to LOOK THINGS UP: read another chat " +
			"(messages --chat <name|phone|jid> --limit N), list chats, resolve a name to a " +
			"contact, or inspect contacts/receipts. Read-only — it cannot send messages.",
		Schema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"args": {
					"type": "array",
					"items": {"type": "string"},
					"description": "CLI arguments, e.g. [\"messages\",\"--chat\",\"Siva\",\"--limit\",\"15\"]"
				}
			},
			"required": ["args"]
		}`),
		Run: func(ctx context.Context, in map[string]any) (string, error) {
			raw, _ := in["args"].([]any)
			args := make([]string, 0, len(raw))
			for _, a := range raw {
				if str, ok := a.(string); ok && strings.TrimSpace(str) != "" {
					args = append(args, str)
				}
			}
			if len(args) == 0 {
				return "", fmt.Errorf("args is required, e.g. [\"messages\",\"--chat\",\"<name>\",\"--limit\",\"15\"]")
			}
			if !allowed[args[0]] {
				return "", fmt.Errorf("subcommand %q is not permitted here (read-only: messages, chats, resolve, contacts, receipts)", args[0])
			}
			cctx, cancel := context.WithTimeout(ctx, 25*time.Second)
			defer cancel()
			out, err := exec.CommandContext(cctx, wacliPath, args...).CombinedOutput()
			if err != nil {
				return "", fmt.Errorf("wacli %s: %v: %s", args[0], err, oneLineTrunc(string(out), 200))
			}
			return truncate(string(out), 6000), nil
		},
	}
}

// justRepliedNote warns the model that this run ALREADY sent a message moments
// ago. New messages arrived while it was composing, so it gets one more look —
// but continuing the same beat produces the double-reply the group complained
// about, so the bar for speaking again is deliberately high.
func justRepliedNote(justReplied bool) string {
	if !justReplied {
		return ""
	}
	return "IMPORTANT: you ALREADY replied in this chat seconds ago — your message is the most recent one you sent. " +
		"These are just the messages that landed while you were typing. Only send something again if they raise a " +
		"genuinely NEW point that your last message did not address. If they are reactions to it, banter, or the same " +
		"topic continuing — answer SKIP. Do not restate or rephrase what you just said.\n\n"
}
