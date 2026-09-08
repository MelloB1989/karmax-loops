//go:build wasip1

// Package main is lyzn-tasks: the half of LYZN that runs on your own laptop.
//
// LYZN listens to a conversation, notices the promises in it, and shows them to
// the person who made them. When they approve one, somebody still has to do it,
// and LYZN cannot: it is a phone app and a cloud API, and the work is shell
// commands, files and accounts that live here.
//
// So this loop asks. A laptop behind a home router has no address LYZN could
// reach, and karmax-desktop pins the KARMAX API to 127.0.0.1, so nothing can be
// pushed at it — the schedule in loop.yaml IS the connection. Every two minutes
// it says it is alive, asks what has been approved, claims a task so a second
// paired laptop cannot start the same job, hands it to the coding harness, and
// posts back what happened so LYZN prints the receipt.
//
// It brings no model and no key of its own. The harness runs on whatever the
// operator already configured — their own Claude Code or Codex — and nothing
// here names a model, a base URL or a token for it. That is the whole of the
// Act plan's credential story, and it stays true under Act Pro: which endpoint
// the harness talks to is the harness's configuration, never this loop's.
//
// Config (KARMAX_LOOP_LYZN_TASKS_*):
//
//	TOKEN  the 43-character daemon token from pairing. Absent, this loop says
//	       so once and does nothing else — an unpaired laptop has no business
//	       calling a service it has no account with.
//	API    the LYZN base URL. Defaults to https://api.lyzn.ai; anything else
//	       also needs its host added to capabilities in loop.yaml, which is a
//	       re-sign and a re-approval, as changing where a daemon reports should
//	       be.
package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/MelloB1989/karmax/pkg/loopwasm"
)

const defaultAPI = "https://api.lyzn.ai"

//go:wasmexport run
func run() {
	if err := sweep(); err != nil {
		loopwasm.Log("lyzn-tasks: %v", err)
	}
}

func sweep() error {
	token := strings.TrimSpace(loopwasm.Config("token"))
	if token == "" {
		loopwasm.Log("lyzn-tasks: this laptop is not paired with LYZN — open the app, " +
			"Settings → Pair a laptop, and redeem the code it shows you")
		return nil
	}
	c := client{base: base(), headers: map[string]string{
		"Authorization": "Bearer " + token,
		"Content-Type":  "application/json",
	}}

	// The heartbeat is what keeps the machine's dot lit in the app, and its
	// answer says whether there is anything to fetch — so the common case,
	// which is an idle queue, costs exactly one request.
	var beat struct {
		Tasks int `json:"tasks"`
	}
	if err := c.do("POST", "/daemons/heartbeat", `{"status":"online"}`, &beat); err != nil {
		return err
	}
	if beat.Tasks == 0 {
		return nil
	}

	var page struct {
		Tasks []WorkItem `json:"tasks"`
	}
	if err := c.do("GET", "/daemons/work", "", &page); err != nil {
		return err
	}
	for _, t := range attemptable(page.Tasks) {
		// One task going wrong is not a reason to drop the rest of the queue.
		if err := work(c, t); err != nil {
			loopwasm.Log("lyzn-tasks: %s: %v", t.TaskID, err)
		}
	}
	return nil
}

// work claims one task, does it, and reports what happened.
func work(c client, t WorkItem) error {
	// Claim first, always. The claim is what makes this run THE run: it moves
	// the task to executing, and no other laptop is offered it again.
	if err := c.do("POST", "/daemons/work/"+t.TaskID+"/claim", "", nil); err != nil {
		if status(err) == 409 || status(err) == 404 {
			// Somebody else got there first, or the person dismissed it between
			// the poll and the claim. Both are ordinary.
			loopwasm.Log("lyzn-tasks: %s is not ours to do", t.TaskID)
			return nil
		}
		return err
	}

	started := now()
	// Synchronous on purpose. Handing this to task.create would return at once
	// and leave nothing to wait on — KARMAX publishes no event when a task
	// finishes — so the run holds the harness for as long as it takes, up to
	// the twelve minutes a loop run gets.
	reply, herr := loopwasm.Harness(brief(t))
	o := parseOutcome(reply, herr)

	body, err := json.Marshal(map[string]any{
		"outcome":    o.APIOutcome(),
		"summary":    o.Summary,
		"startedAt":  started,
		"finishedAt": now(),
	})
	if err != nil {
		return err
	}
	// Reported before the operator is told anything: LYZN's receipt is the
	// record, and a push about work whose result never landed is a lie the
	// operator cannot check. The POST is idempotent, so a retry after a lost
	// reply returns the same receipt rather than printing a second one.
	if err := c.do("POST", "/daemons/work/"+t.TaskID+"/result", string(body), nil); err != nil {
		return err
	}

	switch {
	case o.Done():
		loopwasm.Log("lyzn-tasks: done — %s", o.Summary)
		return nil
	case o.NeedsOperator():
		// Blocked work is a decision, not news: it is finished except for the
		// one thing only they have. Propose puts it in the approvals inbox with
		// a button, so unblocking it and asking for it again are one gesture.
		return loopwasm.Propose(
			"LYZN task is waiting on you: "+t.Text,
			o.Summary,
			"Finish the LYZN task \""+t.Text+"\". It stopped because: "+o.Summary)
	default:
		// A failure is information. LYZN has already printed the FAILED receipt
		// on the phone; this is the copy on the machine that tried, where the
		// logs are.
		return loopwasm.Notify("LYZN task did not go through", t.Text+" — "+o.Summary)
	}
}

// client is the LYZN API, as much of it as this loop uses.
type client struct {
	base    string
	headers map[string]string
}

// do makes one request and decodes the answer into out, which may be nil when
// the body is of no interest.
//
// Anything but a 2xx is an error carrying its status, so a 401 after somebody
// unpaired this machine reads as a refusal rather than as an empty queue.
func (c client) do(method, path, body string, out any) error {
	res, err := loopwasm.HTTP(method, c.base+path, c.headers, body)
	if err != nil {
		return err
	}
	if res.Status < 200 || res.Status > 299 {
		if res.Status == 401 {
			// Terminal, and worth saying in words: the token cannot be
			// recovered and no amount of retrying will make it work again.
			loopwasm.Log("lyzn-tasks: LYZN no longer knows this machine — it was unpaired, " +
				"or the token was revoked. Pair it again from the app.")
		}
		return &apiError{status: res.Status, method: method, path: path}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal([]byte(res.Body), out); err != nil {
		return fmt.Errorf("%s %s answered something that is not the documented JSON: %w", method, path, err)
	}
	return nil
}

type apiError struct {
	status       int
	method, path string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("%s %s answered %d", e.method, e.path, e.status)
}

// status digs the HTTP status out of an error, or 0 when it was not one.
func status(err error) int {
	if e, ok := err.(*apiError); ok {
		return e.status
	}
	return 0
}

// base is where LYZN lives. Configurable, because which deployment a daemon
// reports to is not something to compile in — but bounded by the manifest,
// which is what the operator actually approved.
func base() string {
	if v := strings.TrimSpace(loopwasm.Config("api")); v != "" {
		return strings.TrimSuffix(v, "/")
	}
	return defaultAPI
}

func now() string { return time.Now().UTC().Format(time.RFC3339) }

func main() {}
