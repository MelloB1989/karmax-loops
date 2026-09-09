//go:build !wasip1

package main

import (
	"errors"
	"strings"
	"testing"
)

// The case this whole file exists for. A coding harness that declines the work
// still exits 0 with a polite paragraph, and LYZN prints a receipt from what
// it is told — so "it replied" must never be mistaken for "it is done".
func TestARefusalIsNotSuccess(t *testing.T) {
	o := parseOutcome("I'm not able to send emails on your behalf. You may want to do this manually.", nil)
	if o.Done() {
		t.Fatalf("a refusal was recorded as done: %+v", o)
	}
	if o.Status != StatusFailed {
		t.Errorf("status = %q, want %q", o.Status, StatusFailed)
	}
	if !strings.Contains(o.Summary, "not able to send emails") {
		t.Errorf("the summary drops the reason the operator needs to read: %q", o.Summary)
	}
}

func TestAnEmptyReplyIsNotSuccess(t *testing.T) {
	for _, reply := range []string{"", "   ", "\n\n\t\n"} {
		if o := parseOutcome(reply, nil); o.Done() {
			t.Errorf("parseOutcome(%q) = %+v, want a failure", reply, o)
		}
	}
}

func TestAHarnessThatCouldNotRunIsAFailure(t *testing.T) {
	o := parseOutcome("", errors.New("harness: not configured"))
	if o.Status != StatusFailed {
		t.Errorf("status = %q, want %q", o.Status, StatusFailed)
	}
	if !strings.Contains(o.Summary, "not configured") {
		t.Errorf("the summary hides the error: %q", o.Summary)
	}
}

func TestTheContractIsRead(t *testing.T) {
	reply := `Booked the table for Thursday at 8 and replied to the thread.

STATUS: done
SUMMARY: Booked Table 12 at Olive for Thursday 20:00 and confirmed by email.`
	o := parseOutcome(reply, nil)
	if !o.Done() {
		t.Fatalf("a clean contract was not read as done: %+v", o)
	}
	if !strings.HasPrefix(o.Summary, "Booked Table 12") {
		t.Errorf("summary = %q", o.Summary)
	}
	if o.NeedsOperator() {
		t.Error("finished work asked for the operator's attention")
	}
}

// A harness narrates on the way to the answer, and narration contains the word
// STATUS. The contract is asked for at the END, so the last line wins — a plan
// to report done later is not a report of done.
func TestTheLastContractLineWins(t *testing.T) {
	reply := `First I'll try the deploy, and if it works I'll write STATUS: done at the end.
The deploy failed on the migration step.

STATUS: failed
SUMMARY: The migration would not apply; the column already exists.`
	o := parseOutcome(reply, nil)
	if o.Done() {
		t.Fatalf("a plan to succeed was read as success: %+v", o)
	}
	if !strings.Contains(o.Summary, "migration") {
		t.Errorf("summary = %q", o.Summary)
	}
}

// Models decorate. The contract is a machine contract, but refusing a reply
// because it arrived in bold would fail work that was actually done.
func TestDecoratedContractLinesStillParse(t *testing.T) {
	for _, reply := range []string{
		"**STATUS:** done\n**SUMMARY:** Sent the invoice.",
		"- STATUS: done\n- SUMMARY: Sent the invoice.",
		"`STATUS: done`\n`SUMMARY: Sent the invoice.`",
		"status: DONE\nsummary: Sent the invoice.",
	} {
		o := parseOutcome(reply, nil)
		if !o.Done() {
			t.Errorf("parseOutcome(%q) = %+v, want done", reply, o)
		}
		if !strings.Contains(o.Summary, "Sent the invoice") {
			t.Errorf("parseOutcome(%q) summary = %q", reply, o.Summary)
		}
	}
}

// Blocked is not failed. The work is fine; it is waiting on a person, and that
// person is the one being notified.
func TestBlockedAsksForTheOperator(t *testing.T) {
	o := parseOutcome("STATUS: blocked\nSUMMARY: The staging deploy needs the 1Password code.", nil)
	if o.Status != StatusBlocked {
		t.Fatalf("status = %q, want %q", o.Status, StatusBlocked)
	}
	if !o.NeedsOperator() {
		t.Error("blocked work did not ask for the operator")
	}
	if o.Done() {
		t.Error("blocked work was reported as done")
	}
}

func TestAStatusNobodyAskedForIsAFailure(t *testing.T) {
	o := parseOutcome("STATUS: mostly done\nSUMMARY: Half of it is out.", nil)
	if o.Status != StatusFailed {
		t.Errorf("status = %q, want %q — an invented status is not a claim we can act on", o.Status, StatusFailed)
	}
}

// A status with no summary still has to say something, because LYZN prints
// what it is told and an empty receipt is worse than a vague one.
func TestAMissingSummaryFallsBackToTheReply(t *testing.T) {
	o := parseOutcome("Replied to Anita with the new date.\nSTATUS: done", nil)
	if !o.Done() {
		t.Fatalf("%+v", o)
	}
	if o.Summary == "" || strings.HasPrefix(strings.ToUpper(o.Summary), "STATUS") {
		t.Errorf("summary = %q, want the reply's own last line", o.Summary)
	}
}

func TestOnlyWorkableTasksAreClaimed(t *testing.T) {
	got := attemptable([]WorkItem{
		{TaskID: "a", Text: "Email the client the new date"},
		{TaskID: "", Text: "no id, so it can never be claimed or reported"},
		{TaskID: "c", Text: "   "},
		{TaskID: "a", Text: "the same task twice in one page"},
		{TaskID: "d", Text: "Book the table"},
	})
	if len(got) != 2 || got[0].TaskID != "a" || got[1].TaskID != "d" {
		t.Fatalf("attemptable kept %+v", got)
	}
}

// The harness runs synchronously inside a run that is killed at twelve
// minutes, and LYZN offers up to ten. Taking all ten leaves the last ones
// claimed by a laptop that stopped working on them.
func TestASweepIsCapped(t *testing.T) {
	var many []WorkItem
	for _, id := range []string{"a", "b", "c", "d", "e", "f"} {
		many = append(many, WorkItem{TaskID: id, Text: "something real to do"})
	}
	if got := attemptable(many); len(got) != maxPerRun {
		t.Errorf("attemptable claimed %d tasks, want %d", len(got), maxPerRun)
	}
}

// LYZN records success or failure and prints a receipt either way. Only a
// harness that said done may produce the DONE stamp.
func TestOnlyDoneIsReportedAsSuccess(t *testing.T) {
	cases := map[string]string{
		StatusDone:    OutcomeSuccess,
		StatusBlocked: OutcomeFailure,
		StatusFailed:  OutcomeFailure,
		"":            OutcomeFailure,
	}
	for status, want := range cases {
		if got := (Outcome{Status: status}).APIOutcome(); got != want {
			t.Errorf("Outcome{%q}.APIOutcome() = %q, want %q", status, got, want)
		}
	}
}

func TestTheBriefCarriesWhatTheHarnessCannotGuess(t *testing.T) {
	p := brief(WorkItem{
		TaskID: "t1",
		Text:   "Send Priya the revised quote",
		Quote:  "yeah I'll get the revised quote over to Priya tonight",
		DueAt:  "2026-09-10",
		Context: Context{
			Title:   "Pricing call with Northwind",
			Summary: "They asked for a 10% discount and a two-week trial.",
			Facts:   []Fact{{Text: "Priya is the buyer at Northwind", Kind: "person"}},
		},
	})
	for _, want := range []string{
		"Send Priya the revised quote",
		"revised quote over to Priya tonight",
		"2026-09-10",
		"Pricing call with Northwind",
		"two-week trial",
		"Priya is the buyer at Northwind",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("the brief does not carry %q:\n%s", want, p)
		}
	}
	if !strings.HasSuffix(strings.TrimSpace(p), "SUMMARY: <one sentence saying what you actually did, or what you are waiting on>") {
		t.Errorf("the brief does not end with the contract parseOutcome reads:\n%s", p)
	}
	if !strings.Contains(p, "STATUS: done|blocked|failed") {
		t.Errorf("the brief does not ask for the status line:\n%s", p)
	}
}

// A task with no due date and no conversation must not produce "DUE:" with
// nothing after it — a blank field reads as a fact the model then invents.
func TestTheBriefOmitsWhatItDoesNotHave(t *testing.T) {
	p := brief(WorkItem{TaskID: "t2", Text: "Cancel the Tuesday standup"})
	for _, unwanted := range []string{"DUE:", "WHAT THEY ACTUALLY SAID:", "FROM THE CONVERSATION:", "WORTH KNOWING:"} {
		if strings.Contains(p, unwanted) {
			t.Errorf("the brief invented an empty %q field:\n%s", unwanted, p)
		}
	}
}

// The round trip the loop actually performs: whatever comes back, exactly one
// of these three words goes to LYZN.
func TestEveryOutcomeIsOneOfThreeWords(t *testing.T) {
	replies := []string{
		"", "no", "STATUS: done", "STATUS: blocked\nSUMMARY: x", "STATUS: failed",
		"I cannot help with that.", "STATUS: DONE\nSUMMARY: y", "STATUS: unknown",
	}
	for _, r := range replies {
		s := parseOutcome(r, nil).Status
		if s != StatusDone && s != StatusBlocked && s != StatusFailed {
			t.Errorf("parseOutcome(%q).Status = %q", r, s)
		}
	}
}
