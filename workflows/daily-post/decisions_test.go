//go:build !wasip1

package main

import (
	"strings"
	"testing"
	"time"
)

func TestAnOrdinaryDayIsNotPosted(t *testing.T) {
	quiet := day{
		Date: "2026-08-10",
		Sent: []string{"ok", "sounds good", "on my way"},
	}
	if ok, why := quiet.worthPosting(); ok {
		t.Errorf("a day of three short replies was judged worth posting about")
	} else if !strings.Contains(why, "ordinary") {
		t.Errorf("the reason does not explain itself: %q", why)
	}
}

func TestARealDayIsPosted(t *testing.T) {
	busy := day{
		Date: "2026-08-10",
		Tasks: []string{
			"Extract the reporting service into its own repository and wire up deployment",
			"Add a privacy guard that refuses posts naming anyone",
		},
		Meetings: []string{"Design review"},
		Memory:   []string{"Working on a sandboxed automation runtime"},
	}
	if ok, why := busy.worthPosting(); !ok {
		t.Errorf("a day with two shipped tasks and a meeting was skipped: %s", why)
	}
}

// A day of meetings and nothing built is a day of meetings. It should not clear
// the bar on its own — the point of the threshold is that the account has
// something to say, not that the calendar was full.
func TestMeetingsAloneAreWeak(t *testing.T) {
	meetings := day{Date: "2026-08-10", Meetings: []string{"Standup", "1:1", "Sync"}}
	if ok, _ := meetings.worthPosting(); ok {
		t.Error("three meetings and no work was treated as a story")
	}
}

func TestNamesAreStrippedBeforeTheModelSeesThem(t *testing.T) {
	cases := []struct{ in, mustNotContain string }{
		{"Long call with Srikanth about the rollout", "Srikanth"},
		{"Shipped the first report for CampX", "CampX"},
		{"Pairing with Siva on the extraction", "Siva"},
		{"Replied to @nikhil.mello about scheduling", "@nikhil.mello"},
		{"Met Justin to talk about coaching", "Justin"},
	}
	for _, c := range cases {
		got := stripNames(c.in)
		if strings.Contains(got, c.mustNotContain) {
			t.Errorf("stripNames(%q) = %q, still contains %q", c.in, got, c.mustNotContain)
		}
	}
}

// The strip is a helper, not the guard. It must not mangle an ordinary
// sentence into nonsense — a loop whose material is destroyed produces drafts
// that say nothing, and the operator turns it off.
func TestStrippingLeavesOrdinaryTextAlone(t *testing.T) {
	for _, s := range []string{
		"Spent the afternoon on the sandbox and it finally holds",
		"rewrote the parser, it is half the size now",
		"three hours debugging a race that turned out to be a typo",
	} {
		if got := stripNames(s); got != s {
			t.Errorf("stripNames changed ordinary text:\n  in:  %q\n  out: %q", s, got)
		}
	}
}

func TestTheBriefCarriesTheRulesAndTheMaterial(t *testing.T) {
	d := day{
		Date:     "2026-08-10",
		Tasks:    []string{"Rewrote the scheduler to survive restarts"},
		Meetings: []string{"Review with Srikanth"},
	}
	b := d.brief("x", 280, false)

	if !strings.Contains(b, "Rewrote the scheduler") {
		t.Error("the brief does not contain what actually happened")
	}
	if strings.Contains(b, "Srikanth") {
		t.Error("a name reached the brief")
	}
	if !strings.Contains(b, "280") {
		t.Error("the brief does not state the length limit")
	}
	if !strings.Contains(b, "SKIP") {
		t.Error("the model is not told it may decline")
	}

	// Under FORCE the operator asked to see something, so declining is not on
	// offer — a dry run that says "nothing today" all week shows them nothing.
	forced := d.brief("x", 280, true)
	if strings.Contains(forced, "SKIP") {
		t.Error("a forced brief still offers the model a way out")
	}
	if !strings.Contains(forced, "quiet") {
		t.Error("a forced brief does not tell the model what to do with a quiet day")
	}
}

func TestModelWrappingIsStripped(t *testing.T) {
	cases := []struct{ in, want string }{
		{"  Spent the day on a sandbox.  ", "Spent the day on a sandbox."},
		{"\"Spent the day on a sandbox.\"", "Spent the day on a sandbox."},
		{"```\nSpent the day on a sandbox.\n```", "Spent the day on a sandbox."},
		{"Post: Spent the day on a sandbox.", "Spent the day on a sandbox."},
		{"Here's the post: Spent the day on a sandbox.", "Spent the day on a sandbox."},
	}
	for _, c := range cases {
		if got := cleanDraft(c.in); got != c.want {
			t.Errorf("cleanDraft(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// A post that legitimately ends on a quotation must keep it.
func TestAQuoteInsideThePostSurvives(t *testing.T) {
	in := `Someone told me "ship it and see", which turned out to be right.`
	if got := cleanDraft(in); got != in {
		t.Errorf("an internal quotation was mangled:\n  in:  %q\n  out: %q", in, got)
	}
}

func TestDecliningIsRecognised(t *testing.T) {
	for _, s := range []string{"SKIP", "skip", " SKIP ", "SKIP.", "```\nSKIP\n```", "\"SKIP\"", ""} {
		if !skipped(s) {
			t.Errorf("skipped(%q) = false — the loop would post the word SKIP", s)
		}
	}
	if skipped("Skipped the standup today and got three hours back.") {
		t.Error("a real post beginning with a form of 'skip' was treated as a decline")
	}
}

// Two drafts about the same day, worded differently, must be recognised as the
// same thing — otherwise a re-run posts today's news twice in different words.
// A model that argues with the brief instead of writing has declined, and its
// argument must not be treated as a draft. This is not hypothetical: asked to
// write "in the operator's voice" alongside memory context about an assistant
// that messages people on their behalf, a model answered "I can't help with
// this. The request asks me to write content impersonating a specific
// person..." — and that went out as a draft.
func TestAModelTalkingItsWayOutCountsAsDeclining(t *testing.T) {
	for _, s := range []string{
		"I can't help with this. The request asks me to write content impersonating a specific person.",
		"I cannot write this post.",
		"I'm not able to draft that.",
		"I'm sorry, but I won't be writing this.",
		"I don't feel comfortable writing this.",
		"I don't have any information about what actually happened today.",
		"There's nothing here to write about.",
	} {
		if !skipped(s) {
			t.Errorf("a refusal was treated as a draft: %q", s)
		}
	}
}

// And a real post that happens to contain those words is still a post.
func TestAPostMentioningRefusalIsStillAPost(t *testing.T) {
	for _, s := range []string{
		"Spent an hour on a bug I can't explain, then found it was a typo.",
		"The best thing I did today was say I cannot take that on.",
	} {
		if skipped(s) {
			t.Errorf("a real post was treated as a refusal: %q", s)
		}
	}
}

// KARMAX's own messages must never become material for a post.
//
// The dry-run drafts are sent to the operator's WhatsApp, which makes them
// outgoing messages from the operator's account — so without this the loop
// reads yesterday's draft back and writes today's post about it.
func TestItDoesNotEatItsOwnOutput(t *testing.T) {
	own := []string{
		"✅ would post to x\n\nSpent the day on a sandbox.\n\n(dry run — nothing was published. `karmax social dry-run off` to go live.)",
		"🚫 refused for linkedin\n\nShipped the report.\n\n— social: it names somebody",
	}
	for _, m := range own {
		if !isOwnOutput(m) {
			t.Errorf("KARMAX's own message was treated as the operator's: %.60s", m)
		}
	}

	real := []string{
		"I think we should ship the extraction before the review, it is lower risk that way.",
		"Would post the numbers tomorrow once the run finishes.",
	}
	for _, m := range real {
		if isOwnOutput(m) {
			t.Errorf("a real message was discarded as KARMAX's own: %q", m)
		}
	}
}

func TestTheSameDayTwiceIsRecognised(t *testing.T) {
	a := dedupeKey("Spent the day extracting the reporting service into its own repo. Deployment was the hard part.")
	b := dedupeKey("The hard part of extracting the reporting service into its own repo turned out to be deployment.")
	if !sameThing(a, b) {
		t.Errorf("the same observation reworded was not recognised (overlap %.2f):\n  %q\n  %q",
			overlap(a, b), a, b)
	}

	c := dedupeKey("Three hours lost to a race condition that was a typo all along.")
	if sameThing(a, c) {
		t.Errorf("two genuinely different posts collided (overlap %.2f)", overlap(a, c))
	}
}

// Same subject, different news. A loop that treats every post about one project
// as a repeat goes silent for three weeks after its first good day.
func TestSameSubjectDifferentNewsIsNotARepeat(t *testing.T) {
	a := dedupeKey("Spent the day extracting the reporting service into its own repo. Deployment was the hard part.")
	b := dedupeKey("The reporting service now runs its heavy extraction on demand instead of holding a machine open all day.")
	if sameThing(a, b) {
		t.Errorf("two different days on the same project were treated as one (overlap %.2f):\n  %q\n  %q",
			overlap(a, b), a, b)
	}
}

func TestDayKeyIsTheCalendarDay(t *testing.T) {
	morning := time.Date(2026, 8, 10, 6, 0, 0, 0, time.UTC)
	evening := time.Date(2026, 8, 10, 23, 30, 0, 0, time.UTC)
	if dayKey(morning) != dayKey(evening) {
		t.Error("morning and evening of one day produced different keys, so it would post twice")
	}
	if dayKey(morning) == dayKey(evening.Add(time.Hour)) {
		t.Error("crossing midnight did not start a new day")
	}
}
