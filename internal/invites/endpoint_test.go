package invites

import (
	"strings"
	"testing"
)

func TestDirectIPRecipeDoesNotOfferAnUnverifiedNativeClientAndWithdraws(t *testing.T) {
	endpoint := NewPublicEndpoint(EndpointInfo{URL: "https://[2600:1700::1]:4778", Mode: "direct-ip", CAPEM: "public-ca", CASPKIPin: strings.Repeat("a", 64), Stability: "operator-asserted"})
	s := &Service{URL: "https://stale.example", Endpoint: endpoint}
	config := endpointRecipe(s.endpointInfo(), "worker", "private-key")
	if config["ca_pem"] != "public-ca" || config["ca_spki_sha256"] != strings.Repeat("a", 64) || !strings.Contains(config["instructions"].(string), "No native guest client is verified") {
		t.Fatalf("incomplete trust recipe: %v", config)
	}
	for _, key := range []string{"claude_command", "codex_toml", "mcp_json"} {
		if _, present := config[key]; present {
			t.Fatalf("offered unverified native-client recipe %s", key)
		}
	}
	if _, _, err := s.mintPolicy(Issuance{Human: true}, true, 0); err != nil {
		t.Fatal(err)
	}
	endpoint.Withdraw("address disappeared")
	if _, _, err := s.mintPolicy(Issuance{Human: true}, true, 0); err == nil {
		t.Fatal("stale fallback URL enabled invitations after withdrawal")
	}
	info := endpoint.Snapshot()
	if info.URL != "" || info.CAPEM != "" || info.CASPKIPin != "" || info.Reason != "address disappeared" {
		t.Fatalf("withdrawal left a stale recipe: %+v", info)
	}
}
