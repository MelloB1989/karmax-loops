//go:build !wasip1

package shared

import "testing"

// The same reply, drafted twice, is one message.
//
// chat-sweep and wa-monitor can reach the same conclusion about the same
// pending question minutes apart. Keying by what is said, to whom, is what
// makes the second one a no-op instead of a second message in somebody's chat.
func TestTheSameReplyToTheSameChatIsOneEntry(t *testing.T) {
	a := SendKey("919999999999@s.whatsapp.net", "See you post 2!")
	for _, same := range []string{
		"see you post 2!",       // a model recasing it
		"  See  you   post 2! ", // and respacing it
		"SEE YOU POST 2!",
	} {
		if SendKey("919999999999@s.whatsapp.net", same) != a {
			t.Errorf("%q was treated as a different message", same)
		}
	}
	// The same chat reached by a different JID form is the same chat, or the
	// guard is defeated by a device suffix.
	if SendKey("919999999999:12@s.whatsapp.net", "See you post 2!") != a {
		t.Error("a device suffix defeated the duplicate guard")
	}
}

// Different messages, and the same message to different people, stay separate.
func TestDifferentMessagesAreNotConfused(t *testing.T) {
	base := SendKey("chat-a", "on my way")
	if SendKey("chat-a", "not on my way") == base {
		t.Error("two different messages collided")
	}
	if SendKey("chat-b", "on my way") == base {
		t.Error("the same message to two people collided")
	}
}

// A scan's SEND lines are "<jid> | <text>", and anything else is reported
// rather than dropped — a reply that was drafted and silently discarded looks
// exactly like one that was never thought of.
func TestUnparseableDraftsAreReportedNotDropped(t *testing.T) {
	_, unparsed := QueueScanSends([]string{
		"no separator here",
		" | text with no chat",
		"chat-with-no-text | ",
	}, "test")
	if len(unparsed) != 3 {
		t.Errorf("reported %d unusable drafts, want 3: %v", len(unparsed), unparsed)
	}
}
