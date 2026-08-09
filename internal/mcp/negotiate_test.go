package mcp

import (
	"encoding/json"
	"testing"
)

// The legacy handshake echoes the client's version when the HANDSHAKE can carry
// it, and counter-offers when it cannot.
//
// Echoing matters for real clients: Codex asks for 2025-06-18, and answering
// 2025-11-25 lets a strict client hang up.
//
// The 2026-07-28 row is the interesting one, and it used to assert the opposite.
// That revision RETIRED the initialize handshake — it is a stateless
// per-request envelope reached through server/discover — so it is not something
// this path can agree to. The reference SDKs encode the split explicitly
// (mcp_types.version: HANDSHAKE_PROTOCOL_VERSIONS stops at 2025-11-25,
// MODERN_PROTOCOL_VERSIONS holds 2026-07-28 alone). Lanes echoing it back meant
// the server claimed a stateless contract over the very handshake that contract
// removed. A client that asks for it here is confused, and the useful answer is
// the newest version the handshake can actually carry.
func TestLegacyHandshakeNeverAgreesToAStatelessVersion(t *testing.T) {
	for _, tc := range []struct{ asked, want, why string }{
		{"2025-06-18", "2025-06-18", "Codex: echo, or a strict client hangs up"},
		{"2025-11-25", "2025-11-25", "Claude Code, opencode"},
		{"2026-07-28", "2025-11-25", "stateless revision: counter-offer, never agree"},
		{"2024-11-05", "2025-11-25", "unsupported ⇒ offer our best handshake version"},
		{"", "2025-11-25", "absent ⇒ same"},
	} {
		params, _ := json.Marshal(map[string]any{"protocolVersion": tc.asked})
		if got := negotiateLegacy(params); got != tc.want {
			t.Errorf("asked %q: got %q, want %q — %s", tc.asked, got, tc.want, tc.why)
		}
	}
	if got := negotiateLegacy([]byte("not json")); got != "2025-11-25" {
		t.Errorf("malformed params: got %q", got)
	}
}

// The two lists must stay disjoint, or the bug above comes back by a different
// route: a version in both would be negotiable on a path that cannot carry it.
func TestHandshakeAndStatelessVersionsDoNotOverlap(t *testing.T) {
	for _, m := range modernVersions {
		for _, h := range handshakeVersions {
			if m == h {
				t.Errorf("%q is listed as both a handshake and a stateless version; "+
					"the initialize path would then agree to a contract that removed it", m)
			}
		}
	}
	if len(supportedVersions) != len(modernVersions)+len(handshakeVersions) {
		t.Errorf("supportedVersions (%d) is not the union of modern (%d) and handshake (%d); "+
			"the unsupported-version error would then advertise a list Lanes does not speak",
			len(supportedVersions), len(modernVersions), len(handshakeVersions))
	}
}
