// Package coldscan is the cold-memory pipeline as a marketplace loop: it walks
// OLDER WhatsApp chats (ones the operator is no longer actively texting in) and
// distills a durable per-chat summary — via Kit.Summarize (the agent's cheap
// summary model) — into the chat-summary store the memory retrieval sub-agent
// reads. Hot/active chats are left to hot-sync; large community/promo groups
// the operator barely participates in are skipped.
//
// Config (all optional, via KARMAX_LOOP_COLD_SCAN_*):
//
//	PER_TICK            chats summarized per run (default 3)
//	HOT_DAYS            operator-activity window that keeps a chat "hot" (default 14)
//	MIN_GROUP_OWN       min own messages for a group to matter (default 5)
//	MIN_GROUP_OWN_RATIO min own-message fraction for a group (default 0.2)
package coldscan

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/MelloB1989/karmax/pkg/loopkit"
)

func init() {
	loopkit.Register(loopkit.Loop{
		Name:        "cold-scan",
		Description: "Summarizes older ('cold') WhatsApp chats into per-chat memory for the retrieval sub-agent (cheap summary model).",
		Schedule:    loopkit.Every("45m"),
		Run:         run,
	})
}

const summaryPrompt = `You write durable memory about one of the operator's contacts based on a WhatsApp conversation. Summarize: who the other party is (relationship/role if inferable), the key topics discussed, any commitments / decisions / deadlines / important facts, and anything genuinely useful to remember later. 2–6 factual sentences, no fluff. If the conversation has no substance worth remembering (spam, one-off, pure logistics with no lasting info), reply with exactly: SKIP`

// recheckInterval bounds how often a chat is re-examined (one wacli own-message
// lookup per chat per day), keeping the loop cheap across hundreds of chats.
const recheckInterval = 24 * time.Hour

type chatRec struct {
	JID           string    `json:"jid"`
	Name          string    `json:"name"`
	IsGroup       bool      `json:"is_group"`
	Locked        bool      `json:"locked"`
	LastMessageAt time.Time `json:"last_message_at"`
}

type msgRec struct {
	Content   string    `json:"content"`
	IsFromMe  bool      `json:"is_from_me"`
	Timestamp time.Time `json:"timestamp"`
}

// run examines chats and summarizes the "cold" ones. Hot vs cold is decided by
// the OPERATOR's own last message (not the chat's activity), so a group that
// stays busy with other people but that the operator hasn't texted in for weeks
// correctly becomes cold. Each chat is recorded (summarized | hot | skipped) so
// subsequent runs skip it cheaply until recheckInterval elapses.
func run(ctx context.Context, k loopkit.Kit) error {
	wacli := strings.TrimSpace(k.Config("wacli"))
	if wacli == "" {
		wacli = k.HostTool("wacli")
	}
	perTick := configInt(k, "per_tick", 3)
	hotDays := configInt(k, "hot_days", 14)
	minGroupOwn := configInt(k, "min_group_own", 5)
	minGroupOwnRatio := configFloat(k, "min_group_own_ratio", 0.2)

	chats, err := listChats(ctx, wacli)
	if err != nil {
		return fmt.Errorf("cold-scan: list chats: %w", err)
	}
	cutoff := time.Now().AddDate(0, 0, -hotDays)
	// Oldest chats first so genuinely-cold conversations get summarized promptly.
	sort.Slice(chats, func(i, j int) bool { return chats[i].LastMessageAt.Before(chats[j].LastMessageAt) })

	summarized, examined := 0, 0
	checkBudget := perTick * 8
	if checkBudget < 30 {
		checkBudget = 30
	}

	for _, c := range chats {
		if summarized >= perTick || examined >= checkBudget || ctx.Err() != nil {
			break
		}
		// Note: "locked" chats are NOT skipped — with wacli access control
		// unconfigured every chat defaults to locked yet reads work fine.
		// Relevance is decided by the operator's participation below.
		ex, _ := k.ChatSummary(c.JID)
		if ex != nil && time.Since(ex.SummarizedAt) < recheckInterval {
			continue // examined recently
		}

		ownLast, ownCount := ownLastMessage(ctx, wacli, c.JID)
		examined++

		record := func(status, summary string, msgCount int) {
			if err := k.SaveChatSummary(loopkit.ChatSummaryRecord{
				ChatJID: c.JID, ChatName: c.Name, IsGroup: c.IsGroup,
				Summary: summary, MessageCount: msgCount, OwnMessageCount: ownCount,
				LastMessageAt: ownLast, SummarizedAt: time.Now(), Status: status,
			}); err != nil {
				k.Logf("cold-scan: store state failed for %s: %v", c.Name, err)
			}
		}

		// No / negligible participation -> not useful memory.
		if ownCount == 0 || (c.IsGroup && ownCount < minGroupOwn) {
			record("skipped", "", 0)
			continue
		}
		// Operator still active here -> hot; leave it to hot-sync.
		if ownLast.After(cutoff) {
			record("hot", "", 0)
			continue
		}
		// Cold, but don't re-summarize if nothing changed since last time.
		if ex != nil && ex.Status == "summarized" && !ownLast.After(ex.LastMessageAt) {
			record("summarized", ex.Summary, ex.MessageCount)
			continue
		}
		msgs := fetchMessages(ctx, wacli, c.JID, 150)
		if len(msgs) < 3 {
			record("skipped", "", len(msgs))
			continue
		}
		// Community/broadcast group filter: if the operator's messages are only
		// a small fraction of recent activity, it's a group they don't really
		// converse in (promo/announcement groups) — skip it.
		if c.IsGroup {
			own := 0
			for _, m := range msgs {
				if m.IsFromMe {
					own++
				}
			}
			if float64(own)/float64(len(msgs)) < minGroupOwnRatio {
				record("skipped", "", len(msgs))
				continue
			}
		}
		summary, ok := summarize(ctx, k, c, msgs)
		if !ok {
			record("skipped", "", len(msgs))
			continue
		}
		record("summarized", summary, len(msgs))
		summarized++
	}
	if summarized > 0 || examined > 0 {
		k.Logf("cold-scan: summarized %d chats (examined %d)", summarized, examined)
	}
	return nil
}

func summarize(ctx context.Context, k loopkit.Kit, c chatRec, msgs []msgRec) (string, bool) {
	var b strings.Builder
	for _, m := range msgs {
		txt := strings.TrimSpace(strings.ReplaceAll(m.Content, "\n", " "))
		if txt == "" {
			continue
		}
		who := "them"
		if m.IsFromMe {
			who = "me"
		}
		if len(txt) > 220 {
			txt = txt[:220] + "…"
		}
		b.WriteString(who + ": " + txt + "\n")
	}
	transcript := strings.TrimSpace(b.String())
	if transcript == "" {
		return "", false
	}
	kind := "direct chat"
	if c.IsGroup {
		kind = "group"
	}
	resp, err := k.Summarize(ctx, summaryPrompt+
		fmt.Sprintf("\n\nConversation with %q (%s). Recent messages (\"me\" = the operator):\n\n%s", c.Name, kind, transcript))
	if err != nil {
		k.Logf("cold-scan: summarize failed for %s: %v", c.Name, err)
		return "", false
	}
	resp = strings.TrimSpace(resp)
	if resp == "" || strings.EqualFold(resp, "SKIP") {
		return "", false
	}
	return resp, true
}

func listChats(ctx context.Context, wacli string) ([]chatRec, error) {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, wacli, "chats", "--json", "--limit", "1000").Output()
	if err != nil {
		return nil, err
	}
	var chats []chatRec
	if err := json.Unmarshal(out, &chats); err != nil {
		return nil, err
	}
	return chats, nil
}

// ownLastMessage returns the operator's most recent own-message time in a chat
// and a count of their recent own messages (capped by the lookup limit).
func ownLastMessage(ctx context.Context, wacli, jid string) (time.Time, int) {
	msgs := runMessages(ctx, wacli, jid, 50, true)
	var last time.Time
	for _, m := range msgs {
		if m.Timestamp.After(last) {
			last = m.Timestamp
		}
	}
	return last, len(msgs)
}

func fetchMessages(ctx context.Context, wacli, jid string, limit int) []msgRec {
	return runMessages(ctx, wacli, jid, limit, false)
}

func runMessages(ctx context.Context, wacli, jid string, limit int, fromMeOnly bool) []msgRec {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	args := []string{"messages", "--chat", jid, "--limit", strconv.Itoa(limit)}
	if fromMeOnly {
		args = append(args, "--from-me", "yes")
	}
	out, err := exec.CommandContext(cctx, wacli, args...).Output()
	if err != nil {
		return nil
	}
	return parseMessages(out)
}

// parseMessages handles both {"messages":[...]} and a bare [...] array.
func parseMessages(out []byte) []msgRec {
	var wrap struct {
		Messages []msgRec `json:"messages"`
	}
	if json.Unmarshal(out, &wrap) == nil && len(wrap.Messages) > 0 {
		return wrap.Messages
	}
	var arr []msgRec
	if json.Unmarshal(out, &arr) == nil {
		return arr
	}
	return nil
}

func configInt(k loopkit.Kit, key string, def int) int {
	if v, err := strconv.Atoi(strings.TrimSpace(k.Config(key))); err == nil && v > 0 {
		return v
	}
	return def
}

func configFloat(k loopkit.Kit, key string, def float64) float64 {
	if v, err := strconv.ParseFloat(strings.TrimSpace(k.Config(key)), 64); err == nil && v > 0 {
		return v
	}
	return def
}
