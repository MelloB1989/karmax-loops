//go:build wasip1

// chat-sweep reviews the monitored WhatsApp chats for items already pending —
// an unanswered question, a promised action, an approaching deadline — and acts
// on the operator's behalf, or flags a decision for approval.
//
// It is the backlog counterpart to the webhook proxy, which only sees NEW
// messages. Ported from the compiled-in loop of the same name.
package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/MelloB1989/karmax-loops/workflows/internal/shared"
	"github.com/MelloB1989/karmax/pkg/loopwasm"
)

// maxChats bounds one sweep. Reviewing everything in one harness call produces
// a worse review of each, and a longer one.
const maxChats = 10

//go:wasmexport run
func run() {
	if err := sweep(); err != nil {
		loopwasm.Log("chat-sweep: %v", err)
	}
}

func sweep() error {
	// Single-flight came from a mutex in the compiled-in version. It is not
	// needed here: KARMAX takes a lease per loop, so a run that is still going
	// means this one is never started.
	chats, err := shared.MonitoredChats()
	if err != nil {
		return fmt.Errorf("listing monitored chats: %w", err)
	}
	if len(chats) == 0 {
		loopwasm.Log("chat-sweep: no monitored chats beyond the operator's own; nothing to do")
		return nil
	}
	if len(chats) > maxChats {
		chats = chats[:maxChats]
	}

	wacli := strings.TrimSpace(loopwasm.Config("wacli"))
	if wacli == "" {
		wacli = loopwasm.HostTool("wacli")
	}

	// What the last sweep concluded about each chat, so this one builds on it
	// instead of rediscovering it.
	//
	// The sweep used to read every chat cold, four to six times a day, with no
	// memory of its own previous conclusions or of what the proxy had done in
	// between. An item flagged at noon was still "pending" at four — freshly
	// reworded, so no text guard could catch it — and the operator was nagged
	// about the same thing on every cycle. The model was not wrong; it
	// genuinely could not tell handled from unhandled, because nothing told it.
	var list strings.Builder
	prev := map[string]*loopwasm.ChatSummary{}
	for _, c := range chats {
		fmt.Fprintf(&list, "- %s", c)
		if sum, err := loopwasm.GetChatSummary(c); err == nil && sum != nil && sum.Summary != "" {
			prev[c] = sum
			age := "some time"
			if sum.SummarizedAt > 0 {
				age = fmt.Sprintf("%.0fh", time.Since(time.Unix(sum.SummarizedAt, 0)).Hours())
			}
			fmt.Fprintf(&list, "\n    last sweep (%s ago, status %q): %s", age, sum.Status, oneLine(sum.Summary, 220))
		}
		list.WriteString("\n")
	}

	// Actions taken recently in ANY chat — by this loop's counterpart, the
	// event proxy — because "already handled" usually means handled there.
	actions := ""
	if entries, err := loopwasm.ShortAll("~actions"); err == nil && len(entries) > 0 {
		var ab strings.Builder
		ab.WriteString("Actions KARMAX took recently (do not re-do or re-flag these):\n")
		for i := len(entries) - 1; i >= 0 && len(entries)-i <= 10; i-- {
			ab.WriteString("- " + oneLine(entries[i].Value, 180) + "\n")
		}
		actions = ab.String() + "\n"
	}

	prompt := "You are the operator's proactive WhatsApp assistant, managing their account via the wacli CLI at " + wacli + ".\n" +
		"It is now " + time.Now().Format("Monday 2 Jan, 3:04 PM") + ".\n\n" +
		actions +
		"Review each of these monitored chats for PENDING items. Each chat may carry the previous sweep's conclusion — trust it: something it records as handled, flagged or drafted is NOT pending again unless the chat has NEWER messages that reopen it.\n" + list.String() + "\n" +
		"For EACH chat:\n" +
		fmt.Sprintf("1. Run: %s messages --chat \"<jid>\" --limit 20   (oldest-first; is_from_me=true is the operator)\n", wacli) +
		"2. Determine whether something is pending on the OPERATOR'S side: an unanswered question to them, something they promised and haven't delivered, a deadline near, a follow-up they owe. Already-resolved threads or ones simply awaiting the OTHER person are NOT pending.\n" +
		"3. If pending and ROUTINE (acknowledgement, confirming availability, a simple follow-up nudge, sharing already-known info) and you're confident how the operator would respond: DRAFT the reply and report it as SEND — do NOT send it yourself, and do NOT run any send command. Write it in the operator's natural, human voice (concise; never reveal you're an AI).\n" +
		"4. If it's a real DECISION, commitment, money, or anything sensitive/ambiguous: do NOT send — flag it as APPROVE.\n" +
		"5. If the pending item is something ONLY the operator can personally do (send a document/file you don't have, a personal/family reply, an offline task): flag it as REMIND.\n\n" +
		shared.ScanOutputSpec +
		"\nThen, REQUIRED, one line per chat (even quiet ones):\n" +
		"NOTE: <jid> | <status: nothing-pending|queued-reply|flagged|reminded|informed> | <one-line state of this chat for the next sweep>\n"

	out, err := loopwasm.Harness(prompt)
	if err != nil {
		return fmt.Errorf("harness: %w", err)
	}
	if shared.LooksLikeError(out) {
		return fmt.Errorf("the harness refused or errored: %.120s", out)
	}

	send, approve, remind, inform := shared.ParseScanOutcomes(out)

	// Queued, not sent. This loop no longer talks to WhatsApp: wa-monitor owns
	// the send path, so a reply drafted here and a reply composed there cannot
	// both go out.
	queued, unparsed := shared.QueueScanSends(send, "drafted by chat-sweep")
	loopwasm.Log("chat-sweep: %d chats reviewed — %d queued to send, %d need approval, %d reminders, %d fyi",
		len(chats), queued, len(approve), len(remind), len(inform))

	if queued > 0 {
		_ = loopwasm.Notify("✉️ Replies queued", "• "+strings.Join(send, "\n• "))
	}
	// A draft that could not be parsed is shown rather than dropped: it was
	// worth writing, and silently discarding it looks identical to never having
	// thought of it.
	if len(unparsed) > 0 {
		shared.InformItems("Drafted but not queued — see the reason on each line", unparsed)
	}
	shared.ProposeItems("Flagged by the chat-sweep loop while reviewing monitored WhatsApp chats.", approve)
	shared.RemindItems("Flagged by the chat-sweep loop: only you can do this one.", remind)
	shared.InformItems("📣 Update from your chats", inform)

	// What this sweep concluded, written where the next sweep — and the rest
	// of KARMAX — will look. The per-chat note is the sweep's working memory;
	// the long-term ingest is the journal every other part of the system
	// (the proxy, a phone call, the review pass) reads.
	saveNotes(out, prev)
	return nil
}

// saveNotes persists the per-chat NOTE lines and ingests the actionable ones.
func saveNotes(out string, prev map[string]*loopwasm.ChatSummary) {
	saved, ingested := 0, 0
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "NOTE:") {
			continue
		}
		parts := strings.SplitN(strings.TrimSpace(strings.TrimPrefix(line, "NOTE:")), "|", 3)
		if len(parts) != 3 {
			continue
		}
		jid, status, state := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), strings.TrimSpace(parts[2])
		if jid == "" || state == "" {
			continue
		}
		sum := loopwasm.ChatSummary{ChatJID: jid, Status: status, Summary: state}
		if p := prev[jid]; p != nil {
			sum.ChatName, sum.IsGroup = p.ChatName, p.IsGroup
		}
		sum.SummarizedAt = time.Now().Unix()
		if err := loopwasm.SaveChatSummary(sum); err == nil {
			saved++
		}
		// Quiet chats stay out of long-term memory: six sweeps a day of
		// "nothing pending" would drown the facts worth keeping.
		if status != "" && status != "nothing-pending" {
			if err := loopwasm.Remember("(chat-sweep, " + time.Now().Format("2 Jan 15:04") + ") " +
				jid + " [" + status + "]: " + oneLine(state, 300)); err == nil {
				ingested++
			}
		}
	}
	loopwasm.Log("chat-sweep: %d chat notes saved, %d ingested to memory", saved, ingested)
}

// oneLine collapses text to a single bounded line.
func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

func main() {}
