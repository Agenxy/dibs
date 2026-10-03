package invites

import "sync"

// PublicEndpoint is a derived, bounded listener snapshot, never ledger state.
// Loss of the assigned IP withdraws recipes without touching the fleet identity
// or changing core state. Its CA certificate and pin are public; never keys.
type PublicEndpoint struct {
	mu   sync.RWMutex
	info EndpointInfo
}

// EndpointInfo contains public trust material and the current availability
// of the guest listener. It is exposed only behind the private board proof.
type EndpointInfo struct {
	URL             string   `json:"public_origin"`
	Mode            string   `json:"mode"`
	Address         string   `json:"address"`
	Stability       string   `json:"stability"`
	CAPEM           string   `json:"ca_pem,omitempty"`
	CASPKIPin       string   `json:"ca_spki_sha256,omitempty"`
	VerifiedClients []string `json:"verified_clients"`
	Reason          string   `json:"withdrawn_reason,omitempty"`
}

// NewPublicEndpoint copies a listener's initial derived status.
func NewPublicEndpoint(info EndpointInfo) *PublicEndpoint {
	info.VerifiedClients = append([]string{}, info.VerifiedClients...)
	return &PublicEndpoint{info: info}
}

// Snapshot returns a detached status, safe alongside renewal and withdrawal.
func (e *PublicEndpoint) Snapshot() EndpointInfo {
	e.mu.RLock()
	defer e.mu.RUnlock()
	info := e.info
	info.VerifiedClients = append([]string{}, info.VerifiedClients...)
	return info
}

// Withdraw removes the advertised endpoint and trust recipe, not fleet trust.
func (e *PublicEndpoint) Withdraw(reason string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.info.URL, e.info.CAPEM, e.info.CASPKIPin = "", "", ""
	e.info.Reason = reason
}

func (s *Service) endpointInfo() EndpointInfo {
	if s.Endpoint != nil {
		return s.Endpoint.Snapshot()
	}
	return EndpointInfo{URL: s.URL}
}

func endpointRecipe(info EndpointInfo, name, key string) map[string]any {
	if info.Mode != "direct-ip" {
		return Recipe(name, key, info.URL)
	}
	// No trust adapter has passed its exact native-client malicious-leaf matrix
	// yet. Do not offer a runnable client configuration as though it had.
	return map[string]any{
		"endpoint": info.URL + "/mcp", "ca_pem": info.CAPEM,
		"ca_spki_sha256": info.CASPKIPin, "verified_clients": info.VerifiedClients,
		"address_stability": info.Stability,
		"instructions": "No native guest client is verified yet. " +
			"Do not import this CA into system trust or disable TLS verification. " +
			"Deliver the literal-IP endpoint, CA PEM, SPKI pin and invitation privately; " +
			"wait for a verified client adapter before connecting. Invitations remain bounded, revocable and pull-only.",
	}
}
