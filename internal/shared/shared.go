// Package shared holds helpers used by several registry-hosted loops: harness
// output handling (the ACTED / APPROVE / REMIND / SKIP grammar and its
// deterministic routing), the monitored-chats lookup, and the pending-actions
// queue that decouples discovery loops from the act-on-pending executor.
package shared

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/MelloB1989/karmax/pkg/loopkit"
)

// LooksLikeError detects when harness output is actually an error/refusal
// rather than content, so loops don't persist garbage.
func LooksLikeError(s string) bool {
	low := strings.ToLower(strings.TrimSpace(s))
	return strings.HasPrefix(low, "api error") ||
		strings.HasPrefix(low, "error:") ||
		strings.HasPrefix(low, "execution error") ||
		strings.Contains(low, "safeguards flagged") ||
		strings.Contains(low, "i can't help") ||
		strings.Contains(low, "i cannot help") ||
		strings.Contains(low, "session limit") ||
		strings.Contains(low, "usage limit") ||
		strings.Contains(low, "rate limit")
}

// ScanOutputSpec is the output grammar scan loops ask the harness for. Each
// category is handled deterministically: ACTED → informational notification,
// APPROVE → approvals-inbox proposal, REMIND → phone reminder.
const ScanOutputSpec = "Output EXACTLY one line per item, no other text:\n" +
	"ACTED <who/what>: <what you sent/did>\n" +
	"APPROVE <who/what>: <the open item + your suggested reply/action, for the operator to approve>\n" +
	"REMIND <who/what>: <something ONLY the operator can personally do — send a document/file you don't have, reply in a personal chat, an offline task> | due: <ISO-8601 datetime with timezone; omit '| due:' entirely if there is no concrete deadline>\n" +
	"SKIP <who/what>: <why nothing is needed>"

// ParseScanOutcomes splits harness scan output into the standard categories
// (SKIP lines are dropped).
func ParseScanOutcomes(out string) (acted, approve, remind []string) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		up := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(up, "ACTED"):
			acted = append(acted, strings.TrimSpace(line[len("ACTED"):]))
		case strings.HasPrefix(up, "APPROVE"):
			approve = append(approve, strings.TrimSpace(line[len("APPROVE"):]))
		case strings.HasPrefix(up, "REMIND"):
			remind = append(remind, strings.TrimSpace(line[len("REMIND"):]))
		}
	}
	return acted, approve, remind
}

// ProposeItems files one pending APPROVAL per APPROVE item a loop surfaced
// ("<who/what>: <details + suggested action>"), so decisions land in the
// actionable approvals inbox instead of the notification feed. Items whose
// proposal can't be written fall back to a notification so nothing is lost.
func ProposeItems(k loopkit.Kit, source string, items []string) {
	var failed []string
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		title := item
		if i := strings.Index(item, ":"); i > 0 && i <= 80 {
			title = strings.TrimSpace(item[:i])
		}
		if len(title) > 80 {
			title = title[:80] + "…"
		}
		if err := k.Propose("Decision — "+title, source, item); err != nil {
			k.Logf("propose failed for %q: %v (falling back to notification)", title, err)
			failed = append(failed, item)
		}
	}
	if len(failed) > 0 {
		_ = k.Notify("🤔 Needs your decision", "• "+strings.Join(failed, "\n• "))
	}
}

// RemindItems creates one phone reminder per REMIND item a loop surfaced
// ("<who/what>: <what the operator must do> | due: <ISO>"). Reminders are
// additive and need no approval; a failed create falls back to a notification.
func RemindItems(k loopkit.Kit, source string, items []string) {
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		due := ""
		if head, tail, ok := strings.Cut(item, "| due:"); ok {
			item = strings.TrimSpace(head)
			due = strings.TrimSpace(tail)
		}
		title := item
		if len(title) > 100 {
			title = title[:100] + "…"
		}
		if err := k.Remind(title, due, source); err != nil {
			k.Logf("remind failed for %q: %v (falling back to notification)", title, err)
			_ = k.Notify("⏰ You need to do this yourself", item)
		}
	}
}

// MonitoredChats returns the chats KARMAX watches (from the wacli webhook
// scope), excluding the operator's own command chats.
func MonitoredChats(ctx context.Context, k loopkit.Kit) ([]string, error) {
	body, status, err := k.HTTP(ctx, "GET", k.HostTool("wacli-api")+"/webhooks", nil, "")
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, fmt.Errorf("wacli /webhooks: HTTP %d", status)
	}
	var resp struct {
		Webhooks []struct {
			URL      string   `json:"url"`
			ChatJIDs []string `json:"chat_jids"`
			Enabled  bool     `json:"enabled"`
		} `json:"webhooks"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		return nil, err
	}
	operator := OperatorChatSet()
	var out []string
	for _, wh := range resp.Webhooks {
		if !wh.Enabled || !strings.Contains(wh.URL, "/comms/whatsapp") {
			continue
		}
		for _, c := range wh.ChatJIDs {
			if !operator[NormalizeChatID(c)] {
				out = append(out, c)
			}
		}
	}
	return out, nil
}

// OperatorChatSet returns the operator's own chat ids (normalized) from the
// environment, so scans don't treat KARMAX's own command chats as third-party.
func OperatorChatSet() map[string]bool {
	set := make(map[string]bool)
	for _, env := range []string{os.Getenv("WHATSAPP_OPERATOR_CHATS"), os.Getenv("WHATSAPP_TARGET")} {
		for _, p := range strings.Split(env, ",") {
			if n := NormalizeChatID(p); n != "" {
				set[n] = true
			}
		}
	}
	return set
}

// NormalizeChatID reduces a chat id/phone to comparable digits/id, stripping
// any "@domain" and ":device" suffix.
func NormalizeChatID(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if i := strings.IndexAny(s, "@:"); i >= 0 {
		s = s[:i]
	}
	return s
}

// ---- pending-actions queue ---------------------------------------------------
// A tiny append-only file queue of actionable items discovered by scan loops
// (e.g. memory-bootstrap). It decouples DISCOVERY (any loop enqueues) from
// EXECUTION (the act-on-pending loop drains + acts), and survives restarts.

var pendingMu sync.Mutex

func pendingPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".karmax")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	return filepath.Join(dir, "pending-actions.queue"), nil
}

// EnqueuePending appends actionable items (one per line) to the queue.
func EnqueuePending(items []string) error {
	if len(items) == 0 {
		return nil
	}
	pendingMu.Lock()
	defer pendingMu.Unlock()
	path, err := pendingPath()
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	for _, it := range items {
		it = strings.ReplaceAll(strings.TrimSpace(it), "\n", " ")
		if it != "" {
			w.WriteString(it)
			w.WriteByte('\n')
		}
	}
	return w.Flush()
}

// DrainPending atomically reads and clears the queue, returning its items.
func DrainPending() ([]string, error) {
	pendingMu.Lock()
	defer pendingMu.Unlock()
	path, err := pendingPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var items []string
	for _, line := range strings.Split(string(data), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			items = append(items, line)
		}
	}
	// Clear the queue now that we've taken ownership of the items.
	_ = os.Remove(path)
	return items, nil
}

// RequeuePending puts items back (e.g. if execution failed) so they aren't lost.
func RequeuePending(items []string) {
	_ = EnqueuePending(items)
}
