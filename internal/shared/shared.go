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
const ScanOutputSpec = "Output EXACTLY one line per item, no other text. Choose the outcome CAREFULLY — APPROVE is ONLY for a genuine decision the operator must personally make; do NOT use it for updates, FYIs, or things you can handle yourself:\n" +
	"ACTED <who/what>: <what you sent/did on the operator's behalf — prefer this; handle the routine yourself>\n" +
	"APPROVE <who/what>: <ONLY a real decision that is the operator's to make — approving spend/pricing/scope, a commitment, or something risky/irreversible/sensitive where a wrong move is costly — plus your suggested action. If you're capable of handling it, ACT instead. If it just needs the operator to KNOW, use INFORM.>\n" +
	"REMIND <who/what>: <something ONLY the operator can personally do — send a document/file you don't have, reply in a personal chat, an offline task> | due: <ISO-8601 datetime with timezone; omit '| due:' entirely if there is no concrete deadline>\n" +
	"INFORM <who/what>: <an update the operator should simply KNOW that needs NO decision and NO reply — a payment/receipt confirmation, a status update, 'they'll get back to us', a document received. This becomes a notification, NOT an approval.>\n" +
	"SKIP <who/what>: <nothing worth surfacing at all — chatter, noise, already handled>"

// ParseScanOutcomes splits harness scan output into the standard categories
// (SKIP lines are dropped). INFORM carries FYI updates that must NOT clutter the
// approvals inbox — they route to a plain notification instead.
func ParseScanOutcomes(out string) (acted, approve, remind, inform []string) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		up := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(up, "ACTED"):
			if item := strings.TrimSpace(line[len("ACTED"):]); MeaningfulItem(item) {
				acted = append(acted, item)
			}
		case strings.HasPrefix(up, "APPROVE"):
			if item := strings.TrimSpace(line[len("APPROVE"):]); MeaningfulItem(item) {
				approve = append(approve, item)
			}
		case strings.HasPrefix(up, "REMIND"):
			if item := strings.TrimSpace(line[len("REMIND"):]); MeaningfulItem(item) {
				remind = append(remind, item)
			}
		case strings.HasPrefix(up, "INFORM"):
			if item := strings.TrimSpace(line[len("INFORM"):]); MeaningfulItem(item) {
				inform = append(inform, item)
			}
		}
	}
	return acted, approve, remind, inform
}

// MeaningfulItem reports whether a parsed scan item carries real content. The
// harness (especially a weak brain) sometimes emits a placeholder outcome line
// like "APPROVE: none", "APPROVE (none)", or "APPROVE — none" that really means
// "nothing here" — the SKIP case in disguise. Those produced empty
// "Decision — (none)" approvals, so drop any item whose payload, once stripped
// of separator/wrapping punctuation, is empty or a placeholder word.
func MeaningfulItem(item string) bool {
	s := strings.TrimSpace(item)
	// Drop a "| due:" tail before judging (REMIND items carry a deadline).
	if head, _, ok := strings.Cut(s, "| due:"); ok {
		s = strings.TrimSpace(head)
	}
	s = strings.Trim(s, " \t:—–-()[]{}\"'.")
	s = strings.TrimSpace(s)
	switch strings.ToLower(s) {
	case "", "none", "n/a", "na", "nil", "null", "nothing",
		"nothing needed", "no action", "no action needed",
		"none needed", "nothing to do", "no reply needed", "skip":
		return false
	}
	return true
}

// InformItems delivers FYI updates (INFORM outcomes) as a SINGLE notification,
// never as approvals — payment/receipt confirmations, status updates, "they'll
// get back to us". This is what keeps the approvals inbox for genuine decisions
// only. No-op when there's nothing to report.
func InformItems(k loopkit.Kit, title string, items []string) {
	var clean []string
	for _, it := range items {
		if it = strings.TrimSpace(it); it != "" {
			clean = append(clean, it)
		}
	}
	if len(clean) == 0 {
		return
	}
	_ = k.Notify(title, "• "+strings.Join(clean, "\n• "))
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
