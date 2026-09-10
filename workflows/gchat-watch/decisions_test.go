//go:build !wasip1

package main

import "testing"

const cutoff = "2026-09-10T00:00:00Z"

// A space nobody has spoken in since before the look-back window is recorded,
// not worked.
//
// This is the install case. gws gave us a lastActiveTime for free; the gog port
// probes for it, and getting the first-sighting branch wrong would replay every
// old space at the operator the first time the loop ran.
func TestFirstSightingOfAnOldSpaceOnlyCheckpoints(t *testing.T) {
	sp := gchatSpace{Resource: "spaces/AAA", Name: "old"}
	hit, record := considerSpace(sp, "2026-09-01T12:00:00Z", "", cutoff)
	if hit != nil {
		t.Fatalf("worked a space whose last message predates the window: %+v", hit)
	}
	if record != "2026-09-01T12:00:00Z" {
		t.Fatalf("record = %q, want the message time so it is not reconsidered", record)
	}
}

// A space with a message inside the window is worked from the window's edge.
func TestFirstSightingOfARecentSpaceStartsAtTheCutoff(t *testing.T) {
	sp := gchatSpace{Resource: "spaces/BBB", Name: "recent"}
	hit, record := considerSpace(sp, "2026-09-10T09:00:00Z", "", cutoff)
	if hit == nil {
		t.Fatal("skipped a space with a message inside the look-back window")
	}
	if hit.since != cutoff {
		t.Fatalf("since = %q, want the cutoff %q", hit.since, cutoff)
	}
	if record != "" {
		t.Fatalf("checkpointed %q before the harness ran; a failed harness would lose the thread", record)
	}
}

// Nothing new since last time is nothing to do — and nothing to record either,
// because the stored checkpoint is already right.
func TestNoNewMessagesIsNoWork(t *testing.T) {
	sp := gchatSpace{Resource: "spaces/CCC"}
	last := "2026-09-11T08:00:00Z"
	hit, record := considerSpace(sp, last, last, cutoff)
	if hit != nil || record != "" {
		t.Fatalf("hit=%+v record=%q, want neither", hit, record)
	}
}

// A newer message than the checkpoint is work, starting where we left off.
func TestNewerThanCheckpointIsWorkedFromTheCheckpoint(t *testing.T) {
	sp := gchatSpace{Resource: "spaces/DDD"}
	hit, _ := considerSpace(sp, "2026-09-11T09:00:00Z", "2026-09-11T08:00:00Z", cutoff)
	if hit == nil {
		t.Fatal("a message newer than the checkpoint was not work")
	}
	if hit.since != "2026-09-11T08:00:00Z" {
		t.Fatalf("since = %q, want the checkpoint", hit.since)
	}
	if hit.newest != "2026-09-11T09:00:00Z" {
		t.Fatalf("newest = %q, want the message time the checkpoint advances to", hit.newest)
	}
}

// An empty space is skipped without a checkpoint, so the first real message in
// it is still news.
func TestEmptySpaceIsLeftAlone(t *testing.T) {
	hit, record := considerSpace(gchatSpace{Resource: "spaces/EEE"}, "", "", cutoff)
	if hit != nil || record != "" {
		t.Fatalf("hit=%+v record=%q, want an empty space ignored entirely", hit, record)
	}
}

// The states that mean "reconnect Google", and the ones that do not.
//
// gog reports auth in its exit code, which KARMAX names in the error text. The
// gws version of this matched English strings like "401" and "auth error" and
// latched on anything that happened to contain them — including a message body
// somebody had written.
func TestAuthClassification(t *testing.T) {
	down := []string{
		`gog chat spaces list: auth_required: no refresh token for you@example.com`,
		`gog chat spaces list: usage: missing --account (or set GOG_ACCOUNT)`,
		`gog chat spaces list: chat requires a Google Workspace account (non-gmail.com)`,
		`oauth2: "invalid_grant" token expired`,
	}
	for _, s := range down {
		if !isGchatAuthError(s) {
			t.Errorf("did not recognise as disconnected: %s", s)
		}
	}
	up := []string{
		`gog chat messages list: permission_denied: caller has no access to spaces/AAA`,
		`gog chat messages list: rate_limited: quota exceeded`,
		"someone wrote: my 401 build is failing and auth error keeps coming up",
		"",
	}
	for _, s := range up {
		if isGchatAuthError(s) {
			t.Errorf("wrongly reported Google as disconnected: %s", s)
		}
	}
}

// A DM has no display name, and "" in a prompt reads as a bug.
func TestDirectMessagesGetALabel(t *testing.T) {
	if got := label(gchatSpace{Resource: "spaces/FFF"}); got != "(direct message)" {
		t.Fatalf("label = %q", got)
	}
	if got := label(gchatSpace{Resource: "spaces/GGG", Name: "eng"}); got != "eng" {
		t.Fatalf("label = %q", got)
	}
}
