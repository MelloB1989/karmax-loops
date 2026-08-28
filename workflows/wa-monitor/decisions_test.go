//go:build !wasip1

package main

import (
	"github.com/MelloB1989/karmax-loops/workflows/internal/shared"
	"strings"
	"testing"
)

// wa-monitor's judgement, tested without WhatsApp.
//
// The loop is 900 lines that decide whether to answer as the operator, escalate,
// or say nothing — decisions taken on somebody's real conversations, in their
// voice, and previously covered by no tests at all. The parts that matter are
// pure given a message and some state, and loopwasm compiles off-target with
// every host call refusing, so they can be checked here.

// The model narrates before committing, and the verb has to survive it.
//
// Requiring the verb at position 0 of the response failed once the model had
// tools: it would write "Let me check that chat…" first, parsing would fail, and
// the loop escalated to a full Claude Code run — which then sent its own reply.
// Every parsing failure was a duplicate message.
func TestTheOutcomeVerbSurvivesHowModelsActuallyWrite(t *testing.T) {
	for _, tc := range []struct {
		name, out, verb, payload string
	}{
		{"bare", "REPLY yes that works", "REPLY", "yes that works"},
		{"with a colon", "REPLY: yes that works", "REPLY", "yes that works"},
		{"markdown bold", "**REPLY**: yes that works", "REPLY", "yes that works"},
		{"a bullet", "- REPLY yes that works", "REPLY", "yes that works"},
		{"after narration", "Let me check that chat first.\nREPLY yes that works", "REPLY", "yes that works"},
		{"lowercase", "reply yes that works", "REPLY", "yes that works"},
		{"skip", "SKIP", "SKIP", ""},
		{"escalate with reason", "ESCALATE needs a decision about money", "ESCALATE", "needs a decision about money"},
		{"nothing usable", "I am not sure what to do here.", "", ""},
		{"empty", "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			verb, payload := parseGatewayOutcome(tc.out)
			if verb != tc.verb {
				t.Errorf("verb = %q, want %q", verb, tc.verb)
			}
			if payload != tc.payload {
				t.Errorf("payload = %q, want %q", payload, tc.payload)
			}
		})
	}
}

// A multi-line reply keeps its body — a message cut at the first newline is a
// message sent half-written.
func TestAMultiLineReplyKeepsItsWholeBody(t *testing.T) {
	verb, payload := parseGatewayOutcome("REPLY Sure — two things:\nfirst one\nsecond one")
	if verb != "REPLY" {
		t.Fatalf("verb = %q", verb)
	}
	for _, want := range []string{"Sure", "first one", "second one"} {
		if !strings.Contains(payload, want) {
			t.Errorf("the body lost %q: %q", want, payload)
		}
	}
}

// The verb must never leak into what is sent.
func TestTheVerbIsNeverPartOfTheMessage(t *testing.T) {
	for _, out := range []string{
		"**REPLY**: on my way", "REPLY: on my way", "- REPLY on my way", "reply on my way",
	} {
		_, payload := parseGatewayOutcome(out)
		if strings.Contains(strings.ToUpper(payload), "REPLY") {
			t.Errorf("%q left the verb in the message: %q", out, payload)
		}
	}
}

// Acknowledgements are not worth a model call, let alone a reply.
func TestSmallTalkIsRecognisedAsNotWorthAnswering(t *testing.T) {
	for _, s := range []string{"ok", "OK", " thanks ", "👍", "", "lol", "hmm", "done"} {
		if !isTrivial(s) {
			t.Errorf("%q should not be worth answering", s)
		}
	}
	for _, s := range []string{
		"can you send the invoice", "are we still on for 3pm?",
		"what did the client say about the deadline",
	} {
		if isTrivial(s) {
			t.Errorf("%q is a real question and was treated as small talk", s)
		}
	}
}

// Two messages arriving at once must not both start a run for the same chat.
//
// The gate is what stops a burst of three messages producing three independent
// replies to the same question. Its contract is deliberately not a plain mutex:
// when messages arrived while a pass was running, release keeps the gate HELD
// and returns true, meaning "you make exactly one more pass". Releasing
// properly there would let the queued messages start their own runs, which is
// the duplicate this exists to prevent.
func TestOneChatRunsOneAtATime(t *testing.T) {
	g := gateFor("chat-a")
	if !g.acquire() {
		t.Fatal("the first message could not start")
	}
	if g.acquire() {
		t.Error("a second message started while the first was still running")
	}

	// Something arrived while we were busy, so we owe exactly one more pass —
	// and the gate stays ours while we make it.
	if again := g.release(); !again {
		t.Error("the message that arrived mid-run was forgotten")
	}
	// Note this probe is itself "another message arriving" — a rejected acquire
	// records that there is more to do, which is exactly how the real burst is
	// remembered.
	if g.acquire() {
		t.Error("the gate was handed to somebody else while a follow-up pass was owed")
	}
	if again := g.release(); !again {
		t.Error("the message that arrived during the follow-up was forgotten")
	}

	// Nothing arrived during THAT pass, so now it really ends.
	if again := g.release(); again {
		t.Error("a follow-up pass was owed when nothing had arrived")
	}
	if !g.acquire() {
		t.Error("the chat stayed locked after its run finished")
	}
	g.release()

	// A different chat is unaffected — conversations are concurrent with each
	// other and serial within themselves.
	other := gateFor("chat-b")
	if !other.acquire() {
		t.Error("one chat's run blocked another chat")
	}
	other.release()
}

// The operator being @-mentioned is decided in Go, not by a model, because it
// changes whether KARMAX speaks at all.
func TestOperatorMentionsAreDetectedWithoutAModel(t *testing.T) {
	operator := map[string]bool{"919999999999": true}
	for _, tc := range []struct {
		content string
		want    bool
	}{
		{"@919999999999 can you confirm", true},
		{"hey @919999999999", true},
		{"nothing to do with anyone", false},
		{"@918888888888 not the operator", false},
		{"", false},
	} {
		if got := isOperatorMentioned(tc.content, operator); got != tc.want {
			t.Errorf("isOperatorMentioned(%q) = %v, want %v", tc.content, got, tc.want)
		}
	}
}

// Normalising is what makes two attempts at the same sentence compare equal.
func TestNormalisationCatchesRephrasedDuplicates(t *testing.T) {
	a := normalizeSent("See you post 2!")
	for _, same := range []string{"see you post 2!", "  See  you   post 2! ", "SEE YOU POST 2!"} {
		if normalizeSent(same) != a {
			t.Errorf("%q did not normalise to the same thing", same)
		}
	}
	if normalizeSent("See you post 3") == a {
		t.Error("two genuinely different messages normalised together")
	}
}

// The recall query is what connects an event to what memory knows. It must
// carry the sender and the subject, not the filler — a query of stopwords
// retrieves everything and therefore nothing.
func TestRecallQueryCarriesSenderAndSubject(t *testing.T) {
	q := recallQuery("Shiva Charan", "@229896781574324 karmax call kartik until he responds")
	if !strings.Contains(q, "Shiva") {
		t.Errorf("the sender's name must be in the query, got %q", q)
	}
	if !strings.Contains(q, "kartik") {
		t.Errorf("the subject must be in the query, got %q", q)
	}
	if strings.Contains(q, "karmax") || strings.Contains(q, "until") {
		t.Errorf("filler words retrieve nothing, got %q", q)
	}
	// No sender, short message: still something rather than empty when a
	// distinctive word exists.
	if q := recallQuery("", "tailscale is down again"); !strings.Contains(q, "tailscale") {
		t.Errorf("got %q", q)
	}
}

func TestDisclosureReasonReadsTheLineTheModelActuallyWrites(t *testing.T) {
	for _, tc := range []struct {
		name string
		out  string
		want string
	}{
		{"plain", "KNOWS_KARMAX: this message was sent by your ai right\nACTED: replied", "this message was sent by your ai right"},
		{"bulleted", "- **KNOWS_KARMAX:** you don't sound like karthik at all\nSKIP: nothing", "you don't sound like karthik at all"},
		{"lowercase", "knows_karmax: asked if I was a bot", "asked if I was a bot"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := disclosureReason(tc.out)
			if !ok || got != tc.want {
				t.Fatalf("disclosureReason() = %q, %v; want %q, true", got, ok, tc.want)
			}
		})
	}
}

// A message that never questioned who it was talking to must keep the
// operator's voice: a false positive here outs KARMAX to someone unprompted.
func TestDisclosureReasonStaysQuietWithoutTheLine(t *testing.T) {
	for _, out := range []string{
		"ACTED: replied that the APK is coming tomorrow",
		"The sender mentioned karmax in passing but not as a question.\nSKIP: chatter",
		"KNOWS_KARMAX: none\nSKIP: chatter",
		"KNOWS_KARMAX:\nSKIP: chatter",
	} {
		if got, ok := disclosureReason(out); ok {
			t.Fatalf("disclosureReason(%q) = %q, true; want no disclosure", out, got)
		}
	}
}

func TestBroadcastFeedsAreNotConversations(t *testing.T) {
	for _, id := range []string{
		"120363169975121665@newsletter",
		"status@broadcast",
		"120363422879965343@NEWSLETTER",
	} {
		if !isBroadcastFeed(id) {
			t.Errorf("isBroadcastFeed(%q) = false; want true — replying here reaches nobody", id)
		}
	}
	for _, id := range []string{
		"919652162459@s.whatsapp.net",
		"120363169975121665@g.us",
		"148292419731593@lid",
	} {
		if isBroadcastFeed(id) {
			t.Errorf("isBroadcastFeed(%q) = true; want false — this is a real chat", id)
		}
	}
}

func TestATagOfTheOperatorIsNotARequestToKarmax(t *testing.T) {
	// The exact case: a group, the operator's number tagged, KARMAX not addressed.
	if answersInChat(true, true, false, false) {
		t.Error("a group tag of the operator must NOT produce a reply from KARMAX — the operator is there and answers themselves")
	}
	// KARMAX itself tagged, or a reply to something it sent.
	if !answersInChat(true, false, false, true) {
		t.Error("KARMAX being addressed directly must still be answered")
	}
	// A group the operator explicitly handed over.
	if !answersInChat(true, true, true, false) {
		t.Error("a configured reply-group must still be answered as the operator")
	}
	// A 1:1 chat is the proxy case and is unchanged.
	if !answersInChat(false, false, false, false) {
		t.Error("a DM must still be answered")
	}
}

// Recall is a semantic search, and it was being handed keyword soup: a filter
// that kept only words of five characters or more turned the message that
// mattered into the sender's name. Memory held the answer and was never asked.
func TestRecallQueryKeepsTheWordsThatCarryTheMeaning(t *testing.T) {
	q := recallQuery("Shiva Charan", "Got reply from that rich guy")
	for _, want := range []string{"rich", "guy"} {
		if !strings.Contains(q, want) {
			t.Errorf("recallQuery dropped %q — the query was %q", want, q)
		}
	}
	if !strings.Contains(q, "Shiva") {
		t.Errorf("the sender should anchor the query, got %q", q)
	}
}

func TestRecallQuerySurvivesAVeryShortMessage(t *testing.T) {
	q := recallQuery("Shiva Charan", "Wt he said")
	if !strings.Contains(q, "said") || !strings.Contains(q, "wt") {
		t.Errorf("a short message must still produce a usable query, got %q", q)
	}
}

func TestRecallQueryIsCapped(t *testing.T) {
	long := strings.Repeat("投 milestone deliverable ", 60)
	if q := recallQuery("Shiva", long); len(q) > 240 {
		t.Errorf("query is %d chars, must be capped", len(q))
	}
}

// KARMAX and the operator send from the SAME account, so "from me" cannot tell
// them apart. What separates them is that KARMAX records every message it
// sends, keyed by chat and normalized text — so a send it has no record of was
// typed by the operator. This pins that key, because if it ever stops matching
// what sendViaWacli writes, the proxy silently starts talking over the
// operator again.
func TestSendKeyMatchesWhatWasRecorded(t *testing.T) {
	const chat = "150285251002514@lid"
	const text = "Bruh crazy"

	if shared.SendKey(chat, text) != shared.SendKey(chat, text) {
		t.Fatal("the key must be stable for the same chat and text")
	}
	// The model rewraps and recases its own drafts; those are the same message.
	if shared.SendKey(chat, "Bruh crazy") != shared.SendKey(chat, "  bruh   CRAZY ") {
		t.Error("whitespace and case must not produce a different key")
	}
	// A genuinely different message must not collide.
	if shared.SendKey(chat, text) == shared.SendKey(chat, "Wt he said") {
		t.Error("different messages must not share a key")
	}
	// The same words in another chat are another message.
	if shared.SendKey(chat, text) == shared.SendKey("90391898509410@lid", text) {
		t.Error("the chat must be part of the key")
	}
}

// The exact message that went out seven times, ending with the contact's own
// insult quoted back at him as a control line.
func TestTheDirectiveNeverReachesTheReader(t *testing.T) {
	sent := `KARMAX here. I called Kartik and he hasn't answered yet — I'll keep trying.
Do you want me to keep calling, call someone else, or go there?
KNOWS_KARMAX: "fk you karmax"`

	got := stripDirectives(sent)
	if strings.Contains(got, "KNOWS_KARMAX") {
		t.Fatalf("the control line was sent to the reader: %q", got)
	}
	if !strings.Contains(got, "keep calling") {
		t.Error("the actual message must survive")
	}
	if strings.HasSuffix(got, "\n") {
		t.Error("the trailing blank left by the removed line should be trimmed")
	}
}

// A model that puts its outcome verb first would otherwise send it.
func TestOutcomeVerbsAreStrippedWhereverTheyLand(t *testing.T) {
	got := stripDirectives("ACTED: replied to Shiva\nHey — 5am Tuesday works 👍")
	if strings.Contains(got, "ACTED") {
		t.Errorf("an outcome line must not be sent: %q", got)
	}
	if got != "Hey — 5am Tuesday works 👍" {
		t.Errorf("got %q", got)
	}
}

// A sentence that mentions a directive word is a sentence, not a directive.
func TestOrdinaryProseIsLeftAlone(t *testing.T) {
	for _, msg := range []string{
		"I'll remind you tomorrow about the demo",
		"Approve the invoice when you get a sec",
		"Note that the meet moved to 10:30",
		"skip lunch, I'm running late",
	} {
		if got := stripDirectives(msg); got != msg {
			t.Errorf("stripDirectives(%q) = %q — ordinary text was eaten", msg, got)
		}
	}
}

// The exact message that muted KARMAX: it sent the reply text, WhatsApp stored
// it with the quote in front, and the two hashed differently — so its own reply
// looked like the operator typing and it stood down for twelve minutes. Shiva
// asked it to do two things in that window and got silence.
func TestKarmaxRecognisesItsOwnQuotedReply(t *testing.T) {
	const chat = "150285251002514@lid"
	sent := "KARMAX here — I can’t open websites directly for you."
	stored := "[replying to: karmax open xnxx.live] " + sent

	if shared.SendKey(chat, withoutReplyPrefix(stored)) != shared.SendKey(chat, sent) {
		t.Fatal("a quoted reply must hash to the same key as the text that was sent")
	}
}

// A reply to a reply quotes the quote.
func TestNestedQuotesAreUnwrapped(t *testing.T) {
	stored := "[replying to: [replying to: original] middle] the actual message"
	if got := withoutReplyPrefix(stored); got != "the actual message" {
		t.Errorf("got %q, want the message with every quote layer removed", got)
	}
}

// A message that merely contains a bracket is not a quote.
func TestOrdinaryTextWithBracketsSurvives(t *testing.T) {
	for _, msg := range []string{
		"push truststrike to main [urgent]",
		"see [1] in the doc",
		"plain message",
	} {
		if got := withoutReplyPrefix(msg); got != msg {
			t.Errorf("withoutReplyPrefix(%q) = %q", msg, got)
		}
	}
}
