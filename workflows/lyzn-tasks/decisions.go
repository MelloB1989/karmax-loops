// The decisions lyzn-tasks makes, separated from the calls it makes to reach
// them.
//
// Three of them are worth testing and none needs a network: which of the tasks
// LYZN offered are worth claiming, what to tell a coding harness about one, and
// what the harness actually said when it finished. The last is the one that
// matters. A harness prints refusals to stdout and exits 0, so "it came back"
// and "it did the work" are different questions, and the only thing separating
// them is the contract line the brief asks for. Everything here is pure, so it
// is tested on a normal machine with no WASM, no daemon and no LYZN account —
// see decisions_test.go.
package main

import (
	"fmt"
	"strings"
)

// How a run can end.
//
// LYZN records only success or failure — a receipt is printed either way and
// there is no third stamp. Blocked is kept separate on this side because it is
// the one ending a person can do something about, and it is what decides
// whether the operator is interrupted. It is reported to LYZN as a failure,
// with the blocker as the summary.
const (
	StatusDone    = "done"
	StatusBlocked = "blocked"
	StatusFailed  = "failed"
)

// The two words POST /daemons/work/:id/result accepts. Anything that is not
// exactly "success" is treated as a failure by LYZN, which is also the rule
// this file follows.
const (
	OutcomeSuccess = "success"
	OutcomeFailure = "failure"
)

// WorkItem is one approved commitment, exactly as GET /daemons/work serves it.
//
// Quote is the sentence the person actually said, from the transcript, and
// never changes; Text is the tidied phrasing they approved in the app. Both go
// to the harness: the tidied version is the instruction, the raw sentence is
// the evidence of what was really promised, and the two disagree often enough
// to be worth carrying separately.
type WorkItem struct {
	TaskID      string  `json:"taskId"`
	Text        string  `json:"text"`
	Kind        string  `json:"kind"`
	Quote       string  `json:"quote"`
	DueAt       string  `json:"dueAt"`
	RecordingID string  `json:"recordingId"`
	CreatedAt   string  `json:"createdAt"`
	Context     Context `json:"context"`
}

// Context is the conversation the promise was made in, in LYZN's own words.
// Every field can be empty: it is empty-stringed when the conversation could
// not be read.
type Context struct {
	Title   string `json:"title"`
	Summary string `json:"summary"`
	Facts   []Fact `json:"facts"`
}

// Fact is something that conversation taught LYZN about its wearer — who a
// person is, usually, which is exactly what a harness cannot infer from a
// first name in a promise.
type Fact struct {
	Text string `json:"text"`
	Kind string `json:"kind"`
}

// Outcome is what one attempt produced.
type Outcome struct {
	Status  string
	Summary string
}

// Done reports whether this outcome may close the task as kept.
func (o Outcome) Done() bool { return o.Status == StatusDone }

// APIOutcome is the word LYZN is told. Only a harness that reported done gets
// "success" — everything else, including every reply we could not read, is a
// failure, because a receipt stamped DONE is a claim that a promise was kept.
func (o Outcome) APIOutcome() string {
	if o.Done() {
		return OutcomeSuccess
	}
	return OutcomeFailure
}

// NeedsOperator reports whether a person has to pick this up.
//
// Blocked is the interesting case and the reason this is not just !Done: the
// work is fine, it is waiting on the one thing only the operator has — a
// password, a decision, an account nobody is signed in to. That is worth
// interrupting them for. A failure is worth a line in the log and the FAILED
// receipt LYZN already prints.
func (o Outcome) NeedsOperator() bool { return o.Status == StatusBlocked }

// maxPerRun is how many tasks one sweep will attempt.
//
// The harness runs synchronously and a loop run is killed at twelve minutes,
// so a sweep that took the ten LYZN offers would be cut off part-way through
// the fourth and leave the rest claimed by a laptop that had stopped working
// on them — and a claimed task nobody finishes is worse than one left waiting.
// The poll is every two minutes; a queue drains at three per sweep quickly
// enough.
const maxPerRun = 3

// attemptable picks the work this sweep will claim, oldest first, as served.
//
// A task with no id cannot be claimed or reported on, and a task with no text
// is nothing to hand a harness — both would spend a claim to discover a
// problem that is ours rather than the work's.
func attemptable(tasks []WorkItem) []WorkItem {
	out := make([]WorkItem, 0, len(tasks))
	seen := map[string]bool{}
	for _, t := range tasks {
		id := strings.TrimSpace(t.TaskID)
		if id == "" || seen[id] || strings.TrimSpace(t.Text) == "" {
			continue
		}
		seen[id] = true
		out = append(out, t)
		if len(out) == maxPerRun {
			break
		}
	}
	return out
}

// brief is the prompt for one task.
//
// It carries the four things the harness cannot work out for itself — the
// approved instruction, the sentence that was actually said, the due date, and
// the conversation it came out of — and the facts LYZN kept about the people
// in it, because "send Priya the deck" is not actionable until you know which
// Priya. Context comes last because it is context: a summary of a call about a
// rollout is background for "email the client the new date", not the job.
//
// It ends with the contract, and that is why this is a function rather than a
// format string at the call site: the last two lines are what parseOutcome
// reads, and they have to be asked for in exactly the shape it parses.
func brief(t WorkItem) string {
	var b strings.Builder
	b.WriteString("The person wearing a LYZN pendant promised this out loud, and has now approved it in the app for you to do.\n\n")
	fmt.Fprintf(&b, "TASK: %s\n", strings.TrimSpace(t.Text))
	if q := strings.TrimSpace(t.Quote); q != "" {
		fmt.Fprintf(&b, "WHAT THEY ACTUALLY SAID: %q\n", q)
	}
	if d := strings.TrimSpace(t.DueAt); d != "" {
		fmt.Fprintf(&b, "DUE: %s\n", d)
	}
	if k := strings.TrimSpace(t.Kind); k != "" && k != "other" {
		fmt.Fprintf(&b, "KIND: %s\n", k)
	}
	if title := strings.TrimSpace(t.Context.Title); title != "" {
		fmt.Fprintf(&b, "FROM THE CONVERSATION: %s\n", title)
	}
	if sum := strings.TrimSpace(t.Context.Summary); sum != "" {
		fmt.Fprintf(&b, "WHAT THAT CONVERSATION WAS ABOUT: %s\n", sum)
	}
	for _, f := range t.Context.Facts {
		if s := strings.TrimSpace(f.Text); s != "" {
			fmt.Fprintf(&b, "WORTH KNOWING: %s\n", s)
		}
	}
	b.WriteString(`
Do it now, for real — run the commands, make the change, write the message. You
are on the operator's own machine, with their shell, their files, their signed-in
accounts and the whole karmax CLI. Do not describe what you would do.

If you cannot finish because you need something only they can give you — a
password, an account, a decision that is theirs — stop and say so rather than
guessing.

End your reply with exactly these two lines and nothing after them:
STATUS: done|blocked|failed
SUMMARY: <one sentence saying what you actually did, or what you are waiting on>`)
	return b.String()
}

// parseOutcome turns a harness reply into what LYZN will be told.
//
// The default is failure, and that is deliberate. A coding harness that
// refuses, runs out of turns, or answers the question instead of doing the
// work still exits 0 with prose on stdout — so an unparseable reply is not an
// ambiguous result, it is a result there is no evidence for, and recording it
// as success would print a receipt saying a promise was kept when nobody kept
// it. Only the contract line can say done.
func parseOutcome(reply string, err error) Outcome {
	if err != nil {
		return Outcome{StatusFailed, "the coding harness could not be run: " + err.Error()}
	}
	trimmed := strings.TrimSpace(reply)
	if trimmed == "" {
		return Outcome{StatusFailed, "the coding harness returned nothing"}
	}

	lines := strings.Split(trimmed, "\n")
	status := field(lines, "STATUS:")
	summary := field(lines, "SUMMARY:")

	switch strings.ToLower(firstWord(status)) {
	case StatusDone:
		return Outcome{StatusDone, orLastWords(summary, lines, "it finished but did not say what it did")}
	case StatusBlocked:
		return Outcome{StatusBlocked, orLastWords(summary, lines, "it is blocked but did not say on what")}
	case StatusFailed:
		return Outcome{StatusFailed, orLastWords(summary, lines, "it failed without saying why")}
	}
	// No contract line, or a status nobody asked for. The tail of the reply
	// travels with it because that is usually the refusal itself, and an
	// operator reading "no status was reported" learns nothing they can act on.
	return Outcome{StatusFailed, "the coding harness did not report a status; it ended with: " +
		truncate(lastWords(lines), 220)}
}

// field reads the LAST line that opens with name, tolerating the bullets,
// asterisks and backticks a model wraps a line in when it is being helpful.
//
// Last rather than first: the contract is asked for at the end, and a harness
// that narrates its plan on the way ("I'll set STATUS: done once the deploy
// finishes") would otherwise be taken at its word before it had done anything.
func field(lines []string, name string) string {
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimLeft(strings.TrimSpace(lines[i]), "*_-#`> ")
		if len(l) < len(name) || !strings.EqualFold(l[:len(name)], name) {
			continue
		}
		return strings.TrimSpace(strings.Trim(strings.TrimSpace(l[len(name):]), "*_`"))
	}
	return ""
}

func firstWord(s string) string {
	if f := strings.Fields(s); len(f) > 0 {
		return strings.Trim(f[0], ".,:;|")
	}
	return ""
}

// orLastWords falls back from a missing SUMMARY to the reply's own last line,
// and from that to a fixed phrase, so the receipt LYZN prints is never blank.
// The cap is LYZN's: summary is documented as at most 1000 characters.
func orLastWords(summary string, lines []string, fallback string) string {
	if s := strings.TrimSpace(summary); s != "" {
		return truncate(s, 400)
	}
	if w := lastWords(lines); w != "" {
		return truncate(w, 400)
	}
	return fallback
}

// lastWords is the last line of real prose, skipping the contract lines
// themselves — quoting "STATUS: done" back at somebody explains nothing.
func lastWords(lines []string) string {
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimLeft(strings.TrimSpace(lines[i]), "*_-#`> ")
		if l == "" {
			continue
		}
		up := strings.ToUpper(l)
		if strings.HasPrefix(up, "STATUS:") || strings.HasPrefix(up, "SUMMARY:") {
			continue
		}
		return l
	}
	return ""
}

func truncate(s string, n int) string {
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}
