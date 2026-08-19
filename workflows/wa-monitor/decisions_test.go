//go:build !wasip1

package main

import (
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
