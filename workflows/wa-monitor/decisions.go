// The decisions, separated from the doing.
//
// Everything here is pure given a message and some in-memory state: whether a
// chat is already being handled, whether a message is worth answering, what
// verb a model committed to, whether the operator was named. Split out of
// main.go so it compiles OFF the wasm target and can be tested — the loop
// decides things about somebody's real conversations, in their voice, and that
// judgement was previously covered by no tests at all.

package main

import (
	"strings"
	"sync"
)

// chatGates serialises work per conversation: ordered within a chat,
// concurrent across chats, so a burst of three messages in one chat cannot
// produce three independent replies to the same question.
var chatGates sync.Map // chatID -> *chatGate

func gateFor(chatID string) *chatGate {
	g, _ := chatGates.LoadOrStore(chatID, &chatGate{})
	return g.(*chatGate)
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

// normalizeSent reduces a message to a comparable form for duplicate detection.
func normalizeSent(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(s))), " ")
}

// parseGatewayOutcome pulls the leading verb and its payload out of a gateway
// reply. The verb is on the first line; the payload is everything after it (so
// a REPLY can span multiple lines). Returns ("","") when there's no known verb.
func parseGatewayOutcome(out string) (verb, payload string) {
	trimmed := strings.TrimSpace(out)
	if trimmed == "" {
		return "", ""
	}
	verbs := []string{"ESCALATE", "APPROVE", "REMIND", "INFORM", "REPLY", "SKIP"}
	// Scan LINES for the first one that opens with a verb. Requiring the verb at
	// position 0 of the whole response was too strict: once the model has tools
	// it often narrates a line before committing ("Let me check that chat…"),
	// which made parsing fail and forced a needless claude_code escalation —
	// and each escalated run then sent its own message, duplicating replies.
	lines := strings.Split(trimmed, "\n")
	for i, line := range lines {
		l := strings.TrimSpace(line)
		l = strings.TrimLeft(l, "*_-# ") // tolerate markdown emphasis/bullets
		upper := strings.ToUpper(l)
		for _, v := range verbs {
			if !strings.HasPrefix(upper, v) {
				continue
			}
			// Strip any markdown/punctuation the model wrapped the verb in
			// (e.g. "**REPLY**:") so it never leaks into the sent message.
			rest := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(l[len(v):]), "*_: "))
			// A REPLY body may continue on following lines.
			if v == "REPLY" && i+1 < len(lines) {
				if tail := strings.TrimSpace(strings.Join(lines[i+1:], "\n")); tail != "" {
					if rest == "" {
						rest = tail
					} else {
						rest = rest + "\n" + tail
					}
				}
			}
			return v, strings.TrimSpace(rest)
		}
	}
	return "", ""
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

// mentionsAnyID reports whether any of a comma-separated list of phone/LID
// digit strings was @-mentioned in the content.
//
// The list is a parameter rather than a config read, so the decision is
// testable and the host lookup stays in main.go — which is the split this file
// exists for.
func mentionsAnyID(content, raw string) bool {
	raw = strings.TrimSpace(raw)
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

// groupKey returns the local (pre-@) part of a JID, lowercased.
func groupKey(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if i := strings.IndexByte(s, '@'); i >= 0 {
		s = s[:i]
	}
	return s
}

func oneLineTrunc(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

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

// recallQuery picks the words worth searching memory for: the sender's first
// name and the message's distinctive words.
func recallQuery(senderName, content string) string {
	// The message itself is the query. Long-term recall is a semantic search,
	// and it was being handed keyword soup: this kept only words of five
	// characters or more, so "Got reply from that rich guy" asked memory about
	// "Shiva reply" — the two words that carried the whole meaning were three
	// and four letters long and were thrown away. "Wt he said" reduced to the
	// sender's name alone. Memory held the answer (an investor pitch from
	// Sachin Sharma at Tracxn, with Shiva sending the intro mail first) and was
	// never asked for it, so the proxy said it did not remember.
	//
	// Short words are most of a WhatsApp message. The filter now only trims
	// obvious filler, keeps everything else in the order it was written, and
	// caps by length rather than by word count.
	words := []string{}
	if f := strings.Fields(senderName); len(f) > 0 && len(f[0]) >= 3 {
		words = append(words, f[0])
	}
	for _, w := range strings.Fields(content) {
		w = strings.Trim(strings.ToLower(w), ".,!?()[]\"'@:;")
		if len(w) < 2 || recallStop[w] {
			continue
		}
		words = append(words, w)
	}
	q := strings.Join(words, " ")
	const maxQuery = 220
	if len(q) > maxQuery {
		q = strings.TrimSpace(q[:maxQuery])
	}
	return q
}

// recallStop is filler that adds nothing to a semantic search. Kept small on
// purpose: with short words now included, an over-eager list would strip the
// meaning out of exactly the messages this was widened to serve.
var recallStop = map[string]bool{
	"about": true, "there": true, "please": true, "should": true, "would": true,
	"could": true, "until": true, "karmax": true, "replying": true, "message": true,
	"the": true, "and": true, "for": true, "that": true, "this": true, "with": true,
	"from": true, "have": true, "has": true, "was": true, "are": true, "is": true,
	"it": true, "to": true, "of": true, "in": true, "on": true, "at": true,
	"me": true, "my": true, "you": true, "your": true, "we": true, "us": true,
}

// disclosureReason pulls the KNOWS_KARMAX line out of a model's answer.
//
// Separate from the recording so it can be tested without a host: the line
// arrives amid whatever else the model wrote, sometimes bulleted, sometimes
// bolded, and getting it wrong in either direction is costly — a miss keeps
// the impersonation going, a false positive drops the operator's voice with
// somebody who never questioned it.
func disclosureReason(modelOutput string) (string, bool) {
	for _, line := range strings.Split(modelOutput, "\n") {
		line = strings.TrimSpace(strings.Trim(strings.TrimSpace(line), "-*#> "))
		if !strings.HasPrefix(strings.ToUpper(line), "KNOWS_KARMAX") {
			continue
		}
		why := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line[len("KNOWS_KARMAX"):]), ":"))
		why = strings.TrimSpace(strings.Trim(why, "*_"))
		if why == "" || strings.EqualFold(why, "no") || strings.EqualFold(why, "none") || strings.EqualFold(why, "n/a") {
			return "", false
		}
		return why, true
	}
	return "", false
}

// isBroadcastFeed reports a JID that publishes AT the operator rather than
// talking to them: WhatsApp channels/newsletters and the status feed.
//
// Both are delivered as normal incoming messages and neither is marked as a
// group, so without this they look exactly like a DM worth answering.
func isBroadcastFeed(chatID string) bool {
	id := strings.ToLower(strings.TrimSpace(chatID))
	return strings.HasSuffix(id, "@newsletter") ||
		strings.HasSuffix(id, "@broadcast") ||
		strings.HasSuffix(id, "@status")
}

// answersInChat reports whether KARMAX should put a message in the chat itself,
// as opposed to making sure the operator sees it.
//
// A tag of the OPERATOR in a group is not a request to KARMAX. The operator is
// in that group and answers for themselves; a reply sent on their behalf lands
// seconds before theirs and makes them look absent. The group tag that started
// this got "causing misunderstandings" and "it doesn't feel professional" from
// the person who sent it.
//
// KARMAX answers when it is itself addressed, in a DM it proxies, or in a group
// the operator has explicitly handed over.
func answersInChat(isGroup, operatorMentioned, replyGroup, commanded bool) bool {
	if commanded {
		return true
	}
	if !isGroup || replyGroup {
		return true
	}
	_ = operatorMentioned
	return false
}

// stripDirectives removes the control lines the model is asked to emit
// alongside its answer, so they never reach the person being written to.
//
// The gateway is told to end with an outcome verb and, when someone has worked
// out they are talking to KARMAX, an extra KNOWS_KARMAX line. The parser reads
// the verb and treats everything after it as the message, so the directive rode
// along into the text and was sent. Seven messages to one contact ended with
// `KNOWS_KARMAX: "fk you karmax"` — his own insult, quoted back at him, once
// per reply.
//
// Only whole lines that OPEN with a known directive and a colon are removed. A
// sentence that happens to mention one is a sentence.
func stripDirectives(text string) string {
	lines := strings.Split(text, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if isDirectiveLine(line) {
			continue
		}
		kept = append(kept, line)
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}

func isDirectiveLine(line string) bool {
	l := strings.ToUpper(strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "-*#> ")))
	for _, d := range directiveTokens {
		if strings.HasPrefix(l, d+":") || strings.HasPrefix(l, "**"+d+":") {
			return true
		}
	}
	return false
}

// directiveTokens are the loop's own control vocabulary. The outcome verbs are
// here as well as KNOWS_KARMAX: the outcome line is meant for the loop, and a
// model that puts it before its reply instead of after would otherwise send it.
var directiveTokens = []string{
	"KNOWS_KARMAX", "ACTED", "APPROVE", "REMIND", "INFORM", "SKIP", "ESCALATE", "REPLY", "NOTE",
}

// withoutReplyPrefix removes the "[replying to: ...]" that WhatsApp prepends to
// a quoted message's stored text.
//
// KARMAX records what it SENT; the store holds what WhatsApp KEPT, and for a
// quoted reply those differ by this prefix. Hashing the stored form therefore
// never matched the recorded one, so KARMAX read its own replies as the
// operator typing and muted itself for twelve minutes each time — which is how
// two direct requests in a row got no answer at all.
//
// The prefix nests, because a reply to a reply quotes the quote, so the
// brackets are matched rather than searched for.
func withoutReplyPrefix(content string) string {
	for {
		s := strings.TrimSpace(content)
		if !strings.HasPrefix(s, "[replying to:") {
			return s
		}
		depth := 0
		end := -1
		for i, r := range s {
			switch r {
			case '[':
				depth++
			case ']':
				depth--
				if depth == 0 {
					end = i
				}
			}
			if end >= 0 {
				break
			}
		}
		if end < 0 || end+1 >= len(s) {
			return s
		}
		content = s[end+1:]
	}
}
