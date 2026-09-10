package main

import "strings"

// The parts of gchat-watch that are decisions rather than I/O.
//
// Which spaces count as active, and what a gog failure means, are the two
// things that decide whether the operator gets told about a message or never
// hears about it. They are pure given a space list and a checkpoint, so they
// live here where a test can reach them without Google.

// gchatSpace is one row of `gog chat spaces list --json`.
type gchatSpace struct {
	Resource  string `json:"resource"` // "spaces/XXXX"
	Name      string `json:"name"`     // display name; empty for a DM
	SpaceType string `json:"type"`
}

// gchatMessage is one row of `gog chat messages list --json`.
type gchatMessage struct {
	Resource   string `json:"resource"`
	Sender     string `json:"sender"`
	Text       string `json:"text"`
	CreateTime string `json:"createTime"`
}

// active is a space with unseen messages, and the window they fall in.
type active struct {
	space  gchatSpace
	since  string
	newest string
}

// isGchatAuthError classifies a gog failure as "Google is not connected".
//
// gog answers this in its exit status — 4 is auth_required — and KARMAX's tool
// puts that name in the error text, so the first check is the reliable one.
// The rest catch the states that are not strictly an auth failure but have the
// same fix: no account stored at all, and a consumer gmail.com account, which
// the Chat API does not serve.
func isGchatAuthError(s string) bool {
	l := strings.ToLower(s)
	return strings.Contains(l, "auth_required") ||
		strings.Contains(l, "missing --account") ||
		strings.Contains(l, "requires a google workspace account") ||
		strings.Contains(l, "invalid_grant") ||
		strings.Contains(l, "unauthenticated")
}

// considerSpace decides what to do with one space, given the newest message in
// it and what we last saw.
//
// It returns the work to do, and the checkpoint to record when there is none.
// The two are deliberately separate: recording a checkpoint for a space we are
// about to hand to the harness would mean losing the thread if the harness
// fails, and gws-era code got this wrong by advancing first.
func considerSpace(sp gchatSpace, newest, last, cutoff string) (*active, string) {
	switch {
	case sp.Resource == "" || newest == "":
		return nil, "" // no space, or an empty one
	case last == "":
		// First sighting: only look back a bounded window, so installing the
		// loop does not replay a year of history at somebody.
		if newest <= cutoff {
			return nil, newest
		}
		return &active{space: sp, since: cutoff, newest: newest}, ""
	case newest > last:
		return &active{space: sp, since: last, newest: newest}, ""
	}
	return nil, ""
}

// label names a space for the harness prompt.
func label(sp gchatSpace) string {
	if strings.TrimSpace(sp.Name) == "" {
		return "(direct message)"
	}
	return sp.Name
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
