package mcp

import (
	"strings"
	"testing"
)

// serverInstructions is charged on every connection, and on one client forty
// times over.
//
// Codex renders each tool with the first 994 characters of this string
// prepended. At 3412 characters that was 39,760 characters of the same paragraph
// across 40 tools. 58% of everything that client showed the model about Lanes,
// and enough to truncate its capability list. The reviewer who reported it
// measured the total correctly and attributed it to the tool descriptions; the
// descriptions were innocent and this string was not.
//
// The threshold is what makes the budget specific rather than a round number.
// Anything at or above 994 costs the full 994 per tool, so a first cut from 3412
// to 964 (a 72% reduction in the canonical text) saved that client 3%. Below
// the threshold, savings become proportional. 700 leaves room to add a sentence
// without silently falling off the cliff.
func TestServerInstructionsStayUnderTheClientPrefixThreshold(t *testing.T) {
	const (
		codexPrefixCap = 994 // where Codex truncates its per-tool prefix
		budget         = 700
	)
	if n := len(serverInstructions); n > budget {
		over := ""
		if n >= codexPrefixCap {
			over = ", and past the " + itoa(codexPrefixCap) + "-character point where " +
				"Codex stops truncating and charges the full prefix on every one of its " +
				"rendered tools, which is where the cost stops being linear"
		}
		t.Errorf("serverInstructions is %d characters, over the %d budget%s.\n"+
			"  This is charged on EVERY connection. Detail belongs in lanes://skills,\n"+
			"  which is read once; this string is only for what an agent needs before\n"+
			"  its first call.", n, budget, over)
	}
}

// The two mistakes that are silent and expensive must survive every future trim.
//
// Shortening this string is the right instinct and it has an obvious failure
// mode: trimming until it is merely short. An agent that names its lane for the
// work has the wrong address from then on, and one that registers without a
// nonce loses its mailbox to a restart: neither produces an error, and both are
// discovered much later by somebody else.
func TestServerInstructionsKeepTheIrreversibleWarnings(t *testing.T) {
	for _, must := range []struct{ needle, why string }{
		{"AGENT, not a task", "naming a lane for the work makes your address wrong permanently"},
		{"nonce", "registering without one loses the mailbox on restart"},
		{"ack_board", "nothing else says the awareness gate exists before it refuses you"},
		{"lanes://skills", "the detail moved there; without the pointer it is unreachable"},
	} {
		if !strings.Contains(serverInstructions, must.needle) {
			t.Errorf("serverInstructions no longer mentions %q. %s", must.needle, must.why)
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
