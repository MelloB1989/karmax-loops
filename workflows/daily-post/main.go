//go:build wasip1

// Package main is daily-post: it reads the operator's day and, when the day
// was worth it, writes one post and publishes it to X and LinkedIn.
//
// Nobody approves a draft. That is the operator's decision and it is what
// shapes everything here: the loop reads only the operator's OWN outgoing
// messages rather than other people's, strips names out of the material before
// the model sees any of it, and posts through host tools that check the result
// again and refuse anything naming a person, a client, an amount or a
// credential. The loop cannot talk its way past that check — it is in KARMAX,
// not in here.
//
// It also does the smaller, duller job of not being embarrassing: at most one
// post per platform per day, nothing on an ordinary day, and no repeat of
// something already said.
//
// Config (KARMAX_LOOP_DAILY_POST_*):
//
//	PLATFORMS  comma-separated, default "x,linkedin"
//	MIN_HOUR   earliest local hour it will post, default 17 (so it sees a day)
//	TZ         the operator's timezone, e.g. Asia/Kolkata. UTC if unset.
package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
	// The sandbox has no filesystem, so there is no zoneinfo to read and every
	// timezone would silently resolve to UTC. Embedding the database costs
	// ~450KB in a 4MB module and is the difference between "post in the evening"
	// meaning the operator's evening and meaning somebody else's.
	_ "time/tzdata"

	"github.com/MelloB1989/karmax/pkg/loopwasm"
)

// Where the once-a-day and no-repeats state lives.
const (
	stateGroup = "daily-post"
	// keptDays is how long a post's fingerprint is remembered. Long enough that
	// a slow week does not produce the same observation twice.
	keptDays = 21
)

// limits are what each platform accepts. The host enforces these too; having
// them here means the model is asked for something that fits.
var limits = map[string]int{"x": 280, "linkedin": 3000}

//go:wasmexport run
func run() {
	if err := post(); err != nil {
		loopwasm.Log("daily-post: %v", err)
	}
}

func post() error {
	now := time.Now().In(where())
	if h := minHour(); now.Hour() < h {
		loopwasm.Log("daily-post: it is %02d:00, waiting until %02d:00 to see the whole day", now.Hour(), h)
		return nil
	}

	today := dayKey(now)
	d := gather(today)

	if ok, why := d.worthPosting(); !ok {
		loopwasm.Log("daily-post: %s", why)
		// Recorded so a run later today does not re-gather and re-decide.
		_ = loopwasm.ShortSet(stateGroup, "quiet:"+today, why, 12*3600)
		return nil
	}

	for _, platform := range platforms() {
		if done, _, _ := loopwasm.ShortGet(stateGroup, "posted:"+platform+":"+today); done != "" {
			loopwasm.Log("daily-post: %s already had today's post", platform)
			continue
		}
		if err := postTo(platform, d); err != nil {
			// One platform failing is not a reason to skip the other.
			loopwasm.Log("daily-post: %s: %v", platform, err)
			continue
		}
	}
	return nil
}

func postTo(platform string, d day) error {
	limit := limits[platform]
	if limit == 0 {
		limit = 280
	}

	raw, err := loopwasm.Ask(d.brief(platform, limit))
	if err != nil {
		return fmt.Errorf("could not write a draft: %w", err)
	}
	if skipped(raw) {
		loopwasm.Log("daily-post: the model found nothing publishable in today for %s", platform)
		return nil
	}
	draft := cleanDraft(raw)

	// Compared against everything said recently, not just today, and by overlap
	// rather than by string — a model asked about the same week twice writes the
	// same observation in different words, and that is still a repeat.
	key := dedupeKey(draft)
	if when := alreadySaid(key); when != "" {
		return fmt.Errorf("this is the same thing that was posted on %s", when)
	}

	out, err := loopwasm.Tool(platform+".post", map[string]any{"text": draft})
	if err != nil {
		// A refusal is the guard doing its job and is worth seeing in full, since
		// it says exactly what was wrong with the draft.
		return fmt.Errorf("refused: %w", err)
	}

	_ = loopwasm.ShortSet(stateGroup, "posted:"+platform+":"+d.Date, draft, 36*3600)
	_ = loopwasm.ShortSet(stateGroup, "said:"+d.Date+":"+platform, key, keptDays*24*3600)
	loopwasm.Log("daily-post: posted to %s: %s", platform, out)
	return nil
}

// alreadySaid reports when this was last posted, or "" if it has not been.
func alreadySaid(key string) string {
	entries, err := loopwasm.ShortAll(stateGroup)
	if err != nil {
		// Not knowing is not a reason to post. A duplicate is public and
		// permanent; a skipped day is neither.
		loopwasm.Log("daily-post: cannot check what was already said, so not posting: %v", err)
		return "an earlier run (state unreadable)"
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Key, "said:") {
			continue
		}
		if sameThing(key, e.Value) {
			return strings.TrimPrefix(e.Key, "said:")
		}
	}
	return ""
}

// gather reads the four sources. A source that fails is a source that is
// missing, not a run that fails: a day with no calendar is still a day.
func gather(today string) day {
	d := day{Date: today}

	// What was built. KARMAX's own record of every task it ran.
	var activity struct {
		Tasks []struct {
			Task   string `json:"task"`
			Status string `json:"status"`
		} `json:"tasks"`
	}
	if err := loopwasm.ToolJSON("activity.recent", map[string]any{"days": 1, "limit": 15}, &activity); err != nil {
		loopwasm.Log("daily-post: no engineering activity: %v", err)
	}
	for _, t := range activity.Tasks {
		// Only work that finished. A task that failed or is still running is not
		// something to write about having done.
		if strings.EqualFold(t.Status, "completed") || strings.EqualFold(t.Status, "success") {
			d.Tasks = append(d.Tasks, t.Task)
		}
	}

	d.Meetings = meetings()
	d.Sent = ownMessages()

	// The longer arc. Keeps a post from reading as though today happened in a
	// vacuum, which is what a day of raw activity looks like on its own.
	if facts, err := loopwasm.Recall("what I am currently working on and thinking about", 5); err == nil {
		d.Memory = facts
	}
	return d
}

// meetings returns today's calendar event titles.
//
// Titles only. A calendar entry's description and attendee list are the two
// things most likely to name somebody, and a post never needs either.
func meetings() []string {
	start := time.Now().Truncate(24 * time.Hour)
	params, _ := json.Marshal(map[string]any{
		"timeMin":      start.Format(time.RFC3339),
		"timeMax":      start.Add(24 * time.Hour).Format(time.RFC3339),
		"singleEvents": true,
		"maxResults":   20,
	})

	var cal struct {
		Items []struct {
			Summary string `json:"summary"`
		} `json:"items"`
	}
	err := loopwasm.ToolJSON("google_workspace", map[string]any{
		"service": "calendar", "resource": "events", "method": "list",
		"calendarId": "primary", "params": string(params),
	}, &cal)
	if err != nil {
		loopwasm.Log("daily-post: no calendar: %v", err)
		return nil
	}

	var out []string
	for _, e := range cal.Items {
		if s := strings.TrimSpace(e.Summary); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// ownMessages returns what the operator themselves said today.
//
// from_me is the whole point. Reading other people's messages to write a public
// post means publishing, in paraphrase, things people said in private to one
// person. The operator's own words are theirs to reuse; nobody else's are.
func ownMessages() []string {
	var found struct {
		Messages []struct {
			Content   string    `json:"content"`
			Timestamp time.Time `json:"timestamp"`
		} `json:"messages"`
	}
	if err := loopwasm.ToolJSON("whatsapp_search_messages",
		map[string]any{"from_me": "true", "limit": 60}, &found); err != nil {
		loopwasm.Log("daily-post: no messages: %v", err)
		return nil
	}

	cutoff := time.Now().Add(-18 * time.Hour)
	var out []string
	for _, m := range found.Messages {
		if m.Timestamp.Before(cutoff) {
			continue
		}
		// Only messages with something in them. "ok", "sure", "on my way" are
		// most of what anybody sends and none of it is material.
		if len(strings.Fields(m.Content)) >= 8 {
			out = append(out, m.Content)
		}
		if len(out) >= 15 {
			break
		}
	}
	return out
}

func platforms() []string {
	raw := loopwasm.Config("PLATFORMS")
	if strings.TrimSpace(raw) == "" {
		return []string{"x", "linkedin"}
	}
	var out []string
	for _, p := range strings.Split(raw, ",") {
		if p = strings.ToLower(strings.TrimSpace(p)); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// where is the operator's timezone. "Evening" is a local idea, and a loop that
// waits for 17:00 UTC posts at half past ten at night in India.
func where() *time.Location {
	name := strings.TrimSpace(loopwasm.Config("TZ"))
	if name == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		loopwasm.Log("daily-post: unknown timezone %q, using UTC: %v", name, err)
		return time.UTC
	}
	return loc
}

func minHour() int {
	if n, err := strconv.Atoi(strings.TrimSpace(loopwasm.Config("MIN_HOUR"))); err == nil && n >= 0 && n <= 23 {
		return n
	}
	return 17
}

func main() {}
