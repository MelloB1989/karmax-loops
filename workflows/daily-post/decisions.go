// The decisions daily-post makes, separated from the calls it makes to reach
// them. Everything here is pure, so it is tested on a normal machine with no
// WASM, no daemon and no social account — see decisions_test.go.
package main

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// A day, as gathered.
type day struct {
	Date     string
	Tasks    []string // engineering work, from activity.recent
	Meetings []string // titles only, from the calendar
	Sent     []string // the operator's OWN outgoing messages
	Memory   []string // what long-term memory has on the current work
}

// substance is how much there is to write about.
//
// The default answer to "should I post today" is no. Most days are ordinary,
// and an account that posts every single day about an ordinary day is one
// nobody reads. This counts real signals rather than characters: a day with
// four meetings and no shipped work is a day of meetings, not a story.
func (d day) substance() int {
	n := 0
	for _, t := range d.Tasks {
		if len(strings.Fields(t)) >= 4 {
			n += 2 // shipped work is the strongest signal
		}
	}
	n += len(d.Meetings)
	for _, s := range d.Sent {
		if len(strings.Fields(s)) >= 12 {
			n++ // a considered message, not "ok"
		}
	}
	if len(d.Memory) > 0 {
		n++
	}
	return n
}

// minSubstance is the bar. Two shipped tasks clear it; a day of three short
// messages does not.
const minSubstance = 4

// worthPosting reports whether the day is worth writing about, and why not.
func (d day) worthPosting() (bool, string) {
	if s := d.substance(); s < minSubstance {
		return false, fmt.Sprintf("an ordinary day (%d signals, %d needed) — nothing to say", s, minSubstance)
	}
	return true, ""
}

// stripNames removes @handles, and any Capitalised Word immediately following a
// relationship word, from the raw material BEFORE the model sees it.
//
// The host guard is what actually stops a post naming somebody, and it is not
// bypassable. This is the cheaper, earlier half: a model that never reads the
// name is a model that does not have to be trusted not to write it, and every
// draft it produces is one the guard lets through. Working against the guard
// rather than relying on it produces a loop that refuses everything.
var (
	handlePattern = regexp.MustCompile(`(?:^|\s)@[\w.]+`)
	// "call with Siva", "meeting with the CampX team", "from Srikanth".
	//
	// Deliberately NOT case-insensitive: the capital letter is the whole signal.
	// With (?i) this matched "and it finally holds" and replaced half of every
	// ordinary sentence, which is why the relation words are spelled both ways
	// instead. Weak relations ("and", "to") are left out — they precede a
	// capitalised word far more often in ordinary prose than before a name.
	afterRelation = regexp.MustCompile(`\b(?:[Ww]ith|[Ff]rom|[Ff]or|[Mm]et|[Cc]all(?:ed)?|[Mm]eeting|[Ss]poke|[Pp]airing)\s+((?:[A-Z][\w'-]+\s*){1,3})`)
)

func stripNames(s string) string {
	s = handlePattern.ReplaceAllString(s, " someone")
	s = afterRelation.ReplaceAllStringFunc(s, func(m string) string {
		parts := strings.SplitN(m, " ", 2)
		if len(parts) < 2 {
			return m
		}
		return parts[0] + " someone"
	})
	return s
}

// brief turns a day into the prompt the model writes from.
//
// The material is stripped and the rules are stated, but neither is what makes
// this safe — the host checks the result. The rules are here so the model
// spends its attempt on something publishable rather than producing five drafts
// that all get refused.
func (d day) brief(platform string, limit int, force bool) string {
	var b strings.Builder
	// Who is asking and why, stated first.
	//
	// An earlier version opened with "write in the operator's own voice", and
	// models refused it outright — with memory context alongside describing an
	// assistant that messages people as its principal, that reads as a request
	// to help impersonate somebody. It is not: this is a personal assistant
	// drafting for the person it works for, on that person's own account, and
	// saying so plainly is the difference between a draft and a lecture.
	fmt.Fprintf(&b, `You are the personal assistant of the person described below.
Draft a short %s post for their own account, about their own day.

They asked you to do this. It is their account, their day and their words —
the way a chief of staff drafts something their principal then puts out.

What they did today:
`, platform)

	writeList := func(label string, items []string) {
		if len(items) == 0 {
			return
		}
		fmt.Fprintf(&b, "\n%s:\n", label)
		for _, it := range items {
			fmt.Fprintf(&b, "- %s\n", stripNames(it))
		}
	}
	writeList("Built or shipped", d.Tasks)
	writeList("Time spent", d.Meetings)
	writeList("What they were saying", d.Sent)
	writeList("Longer-running context", d.Memory)

	fmt.Fprintf(&b, `
Rules, all of them hard:
- Never name a person, a company, a client, a product that is not public, or a place of work. Not even initials. Say "a client", "a team", "someone".
- No money, no numbers from a deal, no phone numbers, no email addresses, no links to anything private.
- Nothing that has not been announced publicly. If it is not obviously public already, it is not.
- Write about what they DID and what they THOUGHT, not who they did it with.
- %d characters at the very most. Shorter is better.
- No hashtags unless one is genuinely doing work. No "excited to share". No em dashes as a stylistic tic.
- Plain, specific, and true. One idea. If the day was ordinary, say something small and honest rather than inflating it.

Reply with the post and nothing else. No preamble, no quotes around it, no alternatives.
`, limit)

	// Whether declining is allowed.
	//
	// Normally it is, and SKIP is the right answer on a day with nothing in it.
	// Under FORCE it is not — the operator has explicitly asked to see what
	// would be written, and a dry run that answers "nothing today" for a week
	// teaches them nothing about what this thing would say in their name.
	if force {
		b.WriteString("Even if the day was quiet, write something small and true rather than declining. " +
			"A modest observation is fine. Do not inflate it, and do not invent anything that is not above.")
	} else {
		b.WriteString("If the day genuinely contains nothing publishable under those rules, reply with exactly: SKIP")
	}
	return b.String()
}

// cleanDraft strips what a model puts around a post it was asked for bare.
func cleanDraft(s string) string {
	s = strings.TrimSpace(s)
	// Fenced blocks, which some models return despite being asked not to.
	if strings.HasPrefix(s, "```") {
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = s[i+1:]
		}
		s = strings.TrimSuffix(strings.TrimSpace(s), "```")
	}
	s = strings.TrimSpace(s)
	// Wrapping quotes, but only when they wrap the WHOLE thing — a post may
	// legitimately end on a quotation.
	for _, q := range []string{`"`, `'`, "“"} {
		if strings.HasPrefix(s, q) && (strings.HasSuffix(s, q) || strings.HasSuffix(s, "”")) {
			s = strings.TrimSpace(s[len(q) : len(s)-len(q)])
		}
	}
	// A leading label, which is the other thing they do: "Post:", "Here's a post:"
	for _, prefix := range []string{"post:", "draft:", "here's the post:", "here is the post:", "tweet:"} {
		if len(s) > len(prefix) && strings.EqualFold(s[:len(prefix)], prefix) {
			s = strings.TrimSpace(s[len(prefix):])
		}
	}
	return s
}

// skipped reports whether the model declined, which is a valid answer and not
// an error. Checked after cleaning, since it may arrive fenced or quoted.
//
// A model that talks its way out of the task counts as declining too. Without
// this, "I can't help with this. The request asks me to write content
// impersonating a specific person..." gets treated as a draft — sent to the
// operator during a dry run, and offered to the platform outside one.
func skipped(draft string) bool {
	d := strings.TrimSpace(strings.TrimRight(cleanDraft(draft), ".!"))
	if strings.EqualFold(d, "SKIP") || d == "" {
		return true
	}
	lower := strings.ToLower(d)
	for _, opener := range refusalOpeners {
		if strings.HasPrefix(lower, opener) {
			return true
		}
	}
	return false
}

// refusalOpeners are how a model says no. Matched at the start only, so a post
// that happens to contain "I can't" halfway through is still a post.
var refusalOpeners = []string{
	"i can't", "i cannot", "i can not", "i won't", "i will not",
	"i'm not able", "i am not able", "i'm unable", "i am unable",
	"i don't feel comfortable", "i do not feel comfortable",
	"sorry, i", "i'm sorry", "i apologize", "i apologise",
	// Not a refusal on principle but the same outcome: the model saying there
	// is nothing to write from. It arrives when the day really is empty, and it
	// is not a post.
	"i don't have any", "i do not have any", "there's nothing", "there is nothing",
	"the \"what they did today\"", "no information about what",
}

// dedupeKey is what makes a post recognisable as one already made.
//
// The content words, sorted and lowercased. A model asked the same question
// about the same day twice produces different sentences with the same content,
// and this is what lets those be compared.
func dedupeKey(draft string) string {
	words := strings.FieldsFunc(strings.ToLower(draft), func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9')
	})
	seen := map[string]bool{}
	var kept []string
	for _, w := range words {
		if len(w) > 3 && !common[w] && !seen[w] {
			seen[w] = true
			kept = append(kept, w)
		}
	}
	sort.Strings(kept)
	if len(kept) > 24 {
		kept = kept[:24]
	}
	return strings.Join(kept, " ")
}

// sameThing reports whether two keys describe the same post.
//
// Overlap rather than equality, which is the correction to the obvious design:
// two drafts about one day differ by a word or two ("spent the day extracting"
// vs "extracting ... turned out to be"), and an exact match lets the second one
// through as though it were news.
func sameThing(a, b string) bool { return overlap(a, b) >= sameThreshold }

// sameThreshold was picked against real rewordings: the same observation twice
// scores around 0.8, two genuinely different days score near zero. There is a
// lot of room between those, so the exact number is not delicate.
const sameThreshold = 0.6

func overlap(a, b string) float64 {
	setA := map[string]bool{}
	for _, w := range strings.Fields(a) {
		setA[w] = true
	}
	setB := map[string]bool{}
	for _, w := range strings.Fields(b) {
		setB[w] = true
	}
	if len(setA) == 0 || len(setB) == 0 {
		return 0
	}
	both := 0
	for w := range setA {
		if setB[w] {
			both++
		}
	}
	union := len(setA) + len(setB) - both
	return float64(both) / float64(union)
}

// common are words that carry no meaning for deduplication.
var common = map[string]bool{
	"that": true, "this": true, "with": true, "from": true, "have": true,
	"been": true, "were": true, "what": true, "when": true, "then": true,
	"they": true, "them": true, "there": true, "about": true, "would": true,
	"could": true, "just": true, "like": true, "some": true, "into": true,
	"today": true, "than": true, "your": true, "will": true, "more": true,
	"most": true, "which": true, "while": true, "still": true,
}

// isOwnOutput reports whether a message is something KARMAX itself sent.
//
// The operator-chat filter catches most of it; this catches the rest, which is
// anything KARMAX sent to a third party in the operator's name. Neither is the
// operator's own thinking, and both would otherwise feed a post about the day.
func isOwnOutput(content string) bool {
	for _, marker := range ownOutputMarkers {
		if strings.Contains(content, marker) {
			return true
		}
	}
	return false
}

var ownOutputMarkers = []string{
	"(dry run — nothing was published",
	"would post to ",
	"🚫 refused for ",
	"✅ would post",
}

// dayKey is the calendar day a run belongs to, used to post once per day.
func dayKey(t time.Time) string { return t.Format("2006-01-02") }
