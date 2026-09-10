//go:build wasip1

// Package gchatwatch watches Google Chat (via gog) and proactively acts on
// new messages: routine dev chores immediately, real decisions flagged.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/MelloB1989/karmax-loops/workflows/internal/shared"
	"github.com/MelloB1989/karmax/pkg/loopwasm"
)

// gchat-watch: event-based monitoring of Google Chat via the gog CLI. The Go
// side polls cheaply (spaces, then one newest-message probe each, no LLM) and
// only when a space has NEW activity does it hand the thread to the Claude
// harness executor, which acts on the operator's behalf: routine dev chores
// (close/merge a PR a teammate asked for, quick acks, calendar) are done
// immediately; real decisions are flagged for approval. First run looks back
// 24h so pending asks are handled.
const (
	gchatMaxSpaces   = 5  // spaces handed to the harness per tick
	gchatMaxProbes   = 40 // spaces whose newest message we look at per tick
	gchatFirstRunAge = 24 * time.Hour
)

var gchatMu sync.Mutex

// gchatAuthDown latches the Google-auth state so the operator is alerted ONCE
// when gog cannot reach Chat — no authorized account, a revoked refresh token,
// or an account Chat will not serve — and once more when it recovers, instead
// of failing silently every few hours. Guarded by gchatMu, which the run holds.
// errGchatAuth is the sentinel for that state.
var gchatAuthDown bool

type gchatAuthError struct{ detail string }

func (e *gchatAuthError) Error() string { return "google auth: " + e.detail }

//go:wasmexport run
func run() {
	if err := watch(); err != nil {
		loopwasm.Log("gchat-watch: %v", err)
	}
}

func watch() error {
	if !gchatMu.TryLock() {
		return nil // previous tick still working
	}
	defer gchatMu.Unlock()

	gog := strings.TrimSpace(loopwasm.Config("gog"))
	if gog == "" {
		gog = loopwasm.HostTool("gog")
	}
	// Which mailbox, when the operator has more than one. Empty lets gog pick
	// its own default, which is right for the single-account case.
	account := strings.TrimSpace(loopwasm.Config("account"))

	state := loadGchatState()

	spaces, err := listGchatSpaces(account)
	if err != nil {
		if down := latchAuth(err); down {
			return nil
		}
		return fmt.Errorf("gchat-watch: list spaces: %w", err)
	}
	if gchatAuthDown {
		gchatAuthDown = false
		_ = loopwasm.Notify("✅ Google access restored", "Google is reconnected — I'm watching Google Chat again.")
		loopwasm.Log("gchat-watch: google auth restored")
	}

	// Find spaces whose newest message is past our checkpoint.
	//
	// gws answered this from the space list itself, which carried a
	// lastActiveTime. gog's does not, so activity is one cheap probe per space:
	// the single newest message, newest first, no body needed beyond its time.
	var work []active
	cutoff := time.Now().Add(-gchatFirstRunAge).UTC().Format(time.RFC3339)
	probes := 0
	for _, sp := range spaces {
		if sp.Resource == "" {
			continue
		}
		if probes >= gchatMaxProbes {
			break
		}
		probes++
		newest, err := newestGchatMessage(sp.Resource, account)
		if err != nil {
			if down := latchAuth(err); down {
				return nil
			}
			loopwasm.Log("gchat-watch: %s: %v", sp.Resource, err)
			continue
		}
		hit, record := considerSpace(sp, newest, state[sp.Resource], cutoff)
		if record != "" {
			state[sp.Resource] = record
		}
		if hit != nil {
			work = append(work, *hit)
		}
	}
	if len(work) == 0 {
		saveGchatState(state)
		return nil
	}
	if len(work) > gchatMaxSpaces {
		work = work[:gchatMaxSpaces]
	}

	var list strings.Builder
	for _, w := range work {
		fmt.Fprintf(&list, "- %q | id: %s | new activity since %s\n", label(w.space), w.space.Resource, w.since)
	}

	as := ""
	if account != "" {
		as = " --account " + account
	}
	prompt := "You are the operator's proactive Google Chat assistant, working their account via the gog CLI at " + gog + " (Google Workspace; also available: the gh CLI for GitHub, git, and a full shell).\n\n" +
		"These Google Chat spaces have NEW activity:\n" + list.String() + "\n" +
		"For EACH space:\n" +
		"1. Read the recent messages: `" + gog + " chat messages list <space id> --max 15 --order \"createTime desc\" --json" + as + "` (run `" + gog + " schema chat messages list` if you need the exact contract). Only consider messages newer than the given 'since' time, and ignore messages the operator themself sent (check senders; the operator is the authenticated account — their own asks to others are not for you).\n" +
		"2. If someone asked the operator to do something ROUTINE that you can complete on this machine — e.g. close or merge a specific PR (`gh pr close`/`gh pr merge` in the right repo), share a link/file they already have, a quick factual answer, scheduling via `" + gog + " calendar` — DO IT NOW, then reply briefly in the space via `" + gog + " chat messages send <space id> --text \"...\"" + as + "` in the operator's casual human voice (never reveal you're an AI).\n" +
		"3. If it's a real DECISION, production-destructive (force-push, deleting repos/branches others use), money, or ambiguous — do NOT act; flag it as APPROVE.\n" +
		"4. If it's something ONLY the operator can personally do (attend something, provide a file/credential you don't have): flag it as REMIND.\n" +
		"5. Social chatter with no ask → skip.\n\n" +
		shared.ScanOutputSpec

	out, err := loopwasm.Harness(prompt)
	if err != nil {
		return fmt.Errorf("gchat-watch: harness: %w", err)
	}
	if shared.LooksLikeError(out) {
		return fmt.Errorf("gchat-watch: harness returned error/refusal: %.120s", out)
	}

	// Only advance checkpoints for the spaces we actually processed.
	for _, w := range work {
		state[w.space.Resource] = w.newest
	}
	saveGchatState(state)

	acted, approve, remind, inform := shared.ParseScanOutcomes(out)
	loopwasm.Log("gchat-watch: %d spaces — %d acted, %d need approval, %d reminders, %d fyi", len(work), len(acted), len(approve), len(remind), len(inform))
	if len(acted) > 0 {
		_ = loopwasm.Notify("✅ Handled on Google Chat", "• "+strings.Join(acted, "\n• "))
	}
	shared.ProposeItems("Flagged by the gchat-watch loop from Google Chat activity.", approve)
	shared.RemindItems("Flagged by the gchat-watch loop: only you can do this one.", remind)
	shared.InformItems("📣 Google Chat update", inform)
	return nil
}

// latchAuth reports whether err is Google being disconnected, alerting once.
//
// Once, because the alternative is a WARN every tick for a condition that only
// a person can clear, and an alert that arrives every four hours is one nobody
// reads by the second day.
func latchAuth(err error) bool {
	var authErr *gchatAuthError
	if !errors.As(err, &authErr) {
		return false
	}
	if !gchatAuthDown {
		gchatAuthDown = true
		msg := "⚠️ Google is not connected (Google Chat/Gmail/Calendar via gog). Reconnect Google on the KARMAX host — `gog auth add you@yourdomain.com --services gmail,calendar,chat` — and note that Google Chat needs a Workspace account, not a personal gmail.com one. Until then I can't watch or act on Google Chat. (" + authErr.detail + ")"
		_ = loopwasm.Notify("⚠️ Google is not connected", msg)
		loopwasm.Log("gchat-watch: google auth DOWN — %s", authErr.detail)
	}
	return true
}

func listGchatSpaces(account string) ([]gchatSpace, error) {
	var resp struct {
		Spaces []gchatSpace `json:"spaces"`
	}
	if err := gogJSON([]string{"chat", "spaces", "list", "--max", "100"}, account, &resp); err != nil {
		return nil, err
	}
	return resp.Spaces, nil
}

// newestGchatMessage returns the create time of the latest message in a space,
// or "" when the space has none.
func newestGchatMessage(space, account string) (string, error) {
	var resp struct {
		Messages []gchatMessage `json:"messages"`
	}
	err := gogJSON([]string{"chat", "messages", "list", space,
		"--max", "1", "--order", "createTime desc"}, account, &resp)
	if err != nil {
		// An empty space is exit 3 only with --fail-empty, which we don't pass;
		// but a space the operator can see and not read is a permission error,
		// and that is this space's problem, not Google being down.
		return "", err
	}
	if len(resp.Messages) == 0 {
		return "", nil
	}
	return resp.Messages[0].CreateTime, nil
}

// gogJSON runs one gog command through KARMAX's google tool and decodes it.
func gogJSON(args []string, account string, out any) error {
	input := map[string]any{"args": args}
	if account != "" {
		input["account"] = account
	}
	raw, err := loopwasm.Tool("google", input)
	if err != nil {
		detail := firstLine(err.Error())
		if isGchatAuthError(err.Error()) || isGchatAuthError(raw) {
			return &gchatAuthError{detail: detail}
		}
		return errors.New(detail)
	}
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	return json.Unmarshal([]byte(raw), out)
}

// State lives in short-term memory rather than ~/.karmax/gchat-watch.state.
//
// The sandbox has no filesystem and no $HOME, which is what this loop tripped
// over on its first run. Short-term memory is durable in the same store as
// everything else and the operator can actually see it, so this is the better
// home regardless.
const stateGroup = "gchat-watch-state"

func loadGchatState() map[string]string {
	state := map[string]string{}
	entries, err := loopwasm.ShortAll(stateGroup)
	if err != nil {
		return state
	}
	for _, e := range entries {
		state[e.Key] = e.Value
	}
	return state
}

func saveGchatState(state map[string]string) {
	for k, v := range state {
		// No expiry: a space's last-seen marker is only useful while the space
		// exists, and a stale one costs a single duplicate notification.
		_ = loopwasm.ShortSet(stateGroup, k, v, 0)
	}
}

func main() {}
