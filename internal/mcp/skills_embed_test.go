package mcp

import (
	"os"
	"path/filepath"
	"testing"
)

// The embedded playbook must be the one in the repository root.
//
// There are two copies because go:embed cannot reach above its own package, and
// the binary has to answer lanes://skills without the repository: an agent that
// installed a release has no SKILLS.md to open. So the root file is canonical
// and this pins the copy to it.
//
// Without this the failure is silent and one-directional: somebody improves
// SKILLS.md, GitHub shows the improvement, every human reads the new text, and
// every AGENT keeps being served the old one indefinitely. Nothing errors, no
// test fails, and the two versions drift apart in the exact place where the
// audience that cannot see the repository is the one being misinformed.
func TestEmbeddedSkillsMatchesTheCanonicalFile(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", "SKILLS.md"))
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(root)
	if err != nil {
		t.Fatalf("reading the canonical %s: %v", root, err)
	}
	if string(want) != skillsDoc {
		t.Errorf("SKILLS.md and internal/mcp/skills.md have drifted.\n"+
			"  The root file is canonical and agents are served the copy, so right now\n"+
			"  humans and agents are reading different documents. Fix with:\n"+
			"      cp SKILLS.md internal/mcp/skills.md\n"+
			"  (canonical %d bytes, embedded %d bytes)", len(want), len(skillsDoc))
	}
}

// A resource nobody can find is a resource that does not exist.
//
// The point of serving the playbook over MCP is that an agent meets it without
// being told to look, so it has to be in resources/list, not merely readable if
// you already know the URI.
func TestSkillsIsDiscoverableAndAdvertised(t *testing.T) {
	if len(skillsDoc) < 500 {
		t.Fatalf("the embedded playbook is %d bytes; the embed is probably not resolving",
			len(skillsDoc))
	}
	// The connect instructions must point at it, or an agent has to already know
	// to enumerate resources before it learns anything from them.
	if !contains(serverInstructions, "lanes://skills") {
		t.Error("serverInstructions never mentions lanes://skills, so an agent that does not " +
			"enumerate resources will never discover the one document written for it")
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) &&
		(haystack == needle || indexOf(haystack, needle) >= 0)
}

func indexOf(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}
