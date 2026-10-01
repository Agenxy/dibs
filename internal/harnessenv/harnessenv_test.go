package harnessenv

import "testing"

// The app is named from the process tree, measured on the machine this was
// written on: a ChatGPT-app Codex agent's bridge is a child of the app's own
// Codex runtime inside ChatGPT.app, a Claude desktop session's is a child of
// the claude binary under Application Support, and a terminal Codex's has a
// shell above it. Unknown is the safe answer: nothing is ever opened on a
// guess.
func TestTheAppIsNamedFromTheProcessTree(t *testing.T) {
	cases := map[string]struct {
		ancestors []string
		want      string
	}{
		"ChatGPT app worker": {[]string{
			"/Applications/ChatGPT.app/Contents/Resources/codex-cli/CodexCLI.app/Contents/MacOS/codex",
			"/Applications/ChatGPT.app/Contents/MacOS/ChatGPT",
		}, ChatGPTApp},
		"Claude desktop session": {[]string{
			"/Users/x/Library/Application Support/Claude/claude-code/2.1.284/claude.app/Contents/MacOS/claude",
			"/Applications/Claude.app/Contents/Helpers/disclaimer",
		}, ClaudeDesktop},
		"Codex in a terminal": {[]string{
			"/Users/x/.local/bin/codex", "-zsh", "/System/Applications/Utilities/Terminal.app/Contents/MacOS/Terminal",
		}, ""},
		"nothing readable": {nil, ""},
	}
	for name, c := range cases {
		if got := Classify(c.ancestors); got != c.want {
			t.Errorf("%s: classified as %q, want %q", name, got, c.want)
		}
	}
}

// A Codex agent in a terminal is NOT a ChatGPT-app agent, even though both
// run a binary called codex. Opening its thread in the ChatGPT app would move
// it into an app it never ran in, which is a relocation, not a wake.
func TestATerminalCodexIsNeverMistakenForTheApp(t *testing.T) {
	if got := Classify([]string{"/opt/homebrew/bin/codex", "/bin/zsh"}); got == ChatGPTApp {
		t.Error("a terminal Codex was classified as the ChatGPT app: a wake would then open " +
			"its thread in an app it never ran in")
	}
}
