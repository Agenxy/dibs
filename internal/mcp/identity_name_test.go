package mcp

import "testing"

// The daemon calls itself Dibs everywhere a person can see it.
//
// serverInfo.name is the string every MCP client puts in its server list,
// and it said "agents": what this project was called two names ago. The
// renames to Lanes and then to Dibs swept the prose, the verbs, the docs
// and the manifests, and missed the one field a person actually reads,
// which is the failure mode AGENTS.md warns about for sweeps. Round
// forty-two of the pre-release review was not what found it; a person
// asking why the product was not named properly was.
func TestTheDaemonNamesItselfDibsInTheHandshake(t *testing.T) {
	info := serverBuildInfo()
	if got, _ := info["name"].(string); got != "dibs" {
		t.Errorf("serverInfo.name = %q, want %q: that string is what every harness "+
			"shows a person in its list of servers", got, "dibs")
	}
	if got, _ := info["title"].(string); got != "Dibs" {
		t.Errorf("serverInfo.title = %q, want %q: the human form of the name", got, "Dibs")
	}
	if got, _ := info["websiteUrl"].(string); got == "" {
		t.Error("serverInfo carries no websiteUrl: a client that offers to open the " +
			"server's home page has nowhere to send the person")
	}
	// A name nobody can be told apart from another product's is worse than
	// none, so the old one must not come back through a copy-paste.
	for _, k := range []string{"name", "title"} {
		if got, _ := info[k].(string); got == "agents" || got == "lanes" {
			t.Errorf("serverInfo.%s is %q: a retired name for this project", k, got)
		}
	}
}
