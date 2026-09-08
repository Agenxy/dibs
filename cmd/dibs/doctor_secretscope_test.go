package main

import "testing"

// A credential belonging to somebody else's MCP server is not Dibs's secret.
//
// A harness config holds every MCP server that harness has, and those servers
// carry their own tokens, keys and hashes. doctor matched a bare 64-hex run
// anywhere in the file, so the SHA-256 in an unrelated server's
// NODE_REPL_TRUSTED_BROWSER_CLIENT_SHA256S was read as Dibs's secret, found not
// to be the current one, and reported as "codex config has a STALE secret: that
// harness sees ZERO Dibs tools and says nothing about it".
//
// The codex install it said that about was correctly configured on the stdio
// bridge and working. Two things make it worse than a wrong line: the advice is
// to re-copy the block from `dibs mcp-config`, which would replace that working
// stdio configuration with an HTTP one; and the branch that prints it already
// carries a comment about this exact class being fixed once before, because a
// diagnostic that cries wolf is one people learn to ignore.
//
// Found in live use, on the machine this is developed on.
func TestOnlyDibsOwnHeaderCarriesDibsSecret(t *testing.T) {
	const other = "9230e2bd8b24b7ac1f2e3d4c5b6a79880f1e2d3c4b5a69780f1e2d3c4b5a6978"
	const mine = "8df91b6d55419c4d5c4c20aab5fc56be2506e6237fcc2c63edf4c55b52040b54"

	for _, tc := range []struct {
		name string
		body string
		want []string
	}{
		{
			// The real config that produced the false alarm: dibs on the stdio
			// bridge, and an unrelated server carrying a hash.
			name: "stdio bridge beside another server's hash",
			body: `[mcp_servers.dibs]
command = "/Users/x/.local/bin/dibs"
args = ["mcp-stdio"]

[mcp_servers.other.env]
NODE_REPL_TRUSTED_BROWSER_CLIENT_SHA256S = "` + other + `"`,
			want: nil,
		},
		{
			name: "toml http config",
			body: `http_headers = { "X-Dibs-Local" = "` + mine + `" }`,
			want: []string{mine},
		},
		{
			name: "json http config",
			body: `"headers": {"X-Dibs-Local": "` + mine + `"}`,
			want: []string{mine},
		},
		{
			name: "bearer form, which the daemon also accepts",
			body: `"Authorization": "Bearer ` + mine + `"`,
			want: []string{mine},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := embeddedSecrets(tc.body)
			if len(got) != len(tc.want) {
				t.Fatalf("found %v, want %v: a config that embeds no Dibs secret "+
					"cannot have a stale one, and saying it does sends the operator "+
					"to replace a working setup", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("found %q, want %q", got[i], tc.want[i])
				}
			}
		})
	}
}
