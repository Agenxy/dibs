// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package invites

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/agenxy/dibs/internal/boardconfig"
	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/selfupdate"
)

// Service combines access configuration with the engine's authenticated,
// replay-derived issuer evidence. File I/O is always outside the writer loop.
type Service struct {
	Store    Store
	Engine   *engine.Engine
	Policy   boardconfig.InvitesConfig
	URL      string
	Endpoint *PublicEndpoint
	// RecoveryNonce is a fixed-domain derivation owned by the existing at-rest
	// Box, not a raw key or an invitation-store field. Only an explicit export
	// mint discloses it beside the bearer; ordinary mint/list never receive it.
	// Issuance is not ledgered.
	// A later register seals its supplied nonce under the existing ledger rule.
	RecoveryNonce func(string) (string, error)
	// Rebuildable public verification cache. Service's real mint path owns it,
	// so daemon construction cannot forget a setter that tests call by hand.
	releaseEvidence selfupdate.ReleaseEvidenceCache
}

func refused(why, hint string) error {
	return &core.Error{Code: "E_INVITE_POLICY", Msg: why, Hint: hint}
}

func issuanceError(err error) error {
	var policy *PolicyError
	if errors.As(err, &policy) {
		return refused(policy.Error(), "call invite with action: list; revoke an owned child or ask the human")
	}
	return err
}

// Live checks durable issuer generation as well as the credential's own expiry.
// It records revoked configuration lazily, but admission repeats the generation
// check on the writer: lazy persistence never grants a stale credential access.
func (s *Service) Live(ctx context.Context, e Entry) (bool, error) {
	live, err := s.Engine.InvitationIssuerCurrent(ctx, e.IssuedBy, e.IssuerCreated, e.IssuerClosed)
	if err == nil && !live {
		err = s.Store.RevokeGeneration(e.IssuedBy, e.IssuerCreated, e.IssuerClosed)
	}
	return live, err
}

// Handle is the agent-facing invite tool: local agents issue within policy;
// an invited agent can never create another invitation, even with a staff role.
func (s *Service) Handle(ctx context.Context, token, action, name, issuedBy string,
	ttlS int64, export bool,
) (core.Result, error) {
	a, err := s.Engine.InvitationIssuer(ctx, token)
	if err != nil {
		return nil, err
	}
	issuer := Issuance{By: a.ID, Created: a.Created, Closed: a.Closed}
	return s.handle(ctx, issuer, a.Coordinator, action, name, issuedBy, ttlS, export)
}

// Human is called only behind the private god-view proof, never by a tool.
func (s *Service) Human(ctx context.Context, action, name, issuedBy string,
	ttlS int64, export bool,
) (core.Result, error) {
	return s.handle(ctx, Issuance{By: core.HumanActor, Human: true}, true, action, name, issuedBy, ttlS, export)
}

func (s *Service) handle(ctx context.Context, issuer Issuance, coordinator bool,
	action, name, issuedBy string, ttlS int64, export bool,
) (core.Result, error) {
	if export && action != "" && action != "mint" {
		return nil, refused("export is available only while minting an invitation",
			"omit export for list/revoke; use export: true only for a private recipe mint")
	}
	entries, err := s.Store.List()
	if err != nil {
		return nil, err
	}
	if action == "" {
		action = "mint"
	}
	switch action {
	case "list":
		return s.list(ctx, issuer, entries)
	case "revoke":
		if (name == "") == (issuedBy == "") {
			return nil, refused("revoke needs one name OR issued_by",
				"name one invitation or the issuer whose children to revoke")
		}
		if err := s.Store.RevokeOwned(name, issuedBy, issuer); err != nil {
			return nil, issuanceError(err)
		}
		return core.Result{"revoked": name, "issued_by": issuedBy}, nil
	case "mint":
		return s.mint(ctx, issuer, coordinator, entries, name, ttlS, export)
	default:
		return nil, refused("unknown invite action", "use mint (default), list or revoke")
	}
}

func (s *Service) list(ctx context.Context, issuer Issuance, entries []Entry) (core.Result, error) {
	out := []Entry{}
	for _, e := range entries {
		if !issuer.Human && (e.IssuedBy != issuer.By || e.IssuerCreated != issuer.Created) {
			continue
		}
		if !e.Revoked {
			live, err := s.Live(ctx, e)
			if err != nil {
				return nil, err
			}
			if !live {
				e.Revoked = true
			}
		}
		out = append(out, e)
	}
	return core.Result{"invites": out, "url": s.endpointInfo().URL}, nil
}

func (s *Service) mint(ctx context.Context, issuer Issuance, coordinator bool,
	entries []Entry, name string, ttlS int64, export bool,
) (core.Result, error) {
	issuer, ttl, err := s.mintPolicy(issuer, coordinator, ttlS)
	if err != nil {
		return nil, err
	}
	entries, err = s.mintEntries(ctx, entries)
	if err != nil {
		return nil, err
	}
	name, bound, err := mintName(issuer, coordinator, entries, name)
	if err != nil {
		return nil, err
	}
	if !issuer.Human && !coordinator {
		if err := s.Engine.InviteChildPrefixAvailable(ctx, issuer.By, name); err != nil {
			return nil, err
		}
	}
	if err := s.Engine.InviteNameAvailable(ctx, name, bound, issuer.By); err != nil {
		return nil, err
	}
	metadata, provisioning, releaseStatus := s.prepareGuestRelease(ctx, export)
	nonce, err := s.exportRecoveryNonce(name, export)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	key, err := s.Store.MintIssued(name, ttl, now, issuer)
	if err != nil {
		return nil, issuanceError(err)
	}
	info := s.endpointInfo()
	if info.URL == "" {
		return nil, refused("guest listener withdrew during issuance; "+
			"the invitation was stored but its key was not disclosed",
			"call invite(action: list), revoke the unused invitation by name, "+
				"and retry only after the public endpoint is restored")
	}
	config := endpointRecipe(info, name, key)
	if releaseStatus != "" {
		config["bridge_release_status"] = releaseStatus
	}
	if metadata != nil {
		config["bridge_release"] = *metadata
	}
	if provisioning != nil {
		config["bridge_provisioning"] = *provisioning
	}
	result := core.Result{
		"name": name, "key": key, "url": info.URL, "issued_by": issuer.By,
		"config":     config,
		"expires_at": now.Add(ttl),
	}
	if nonce != "" {
		result["recovery_nonce"] = nonce
	}
	return result, nil
}

func (s *Service) exportRecoveryNonce(name string, export bool) (string, error) {
	if !export || s.RecoveryNonce == nil {
		return "", nil
	}
	nonce, err := s.RecoveryNonce(name)
	raw, decodeErr := hex.DecodeString(nonce)
	if err != nil || decodeErr != nil || len(raw) != 32 {
		return "", refused("guest recovery credential is unavailable",
			"restore the board's original at-rest key and restart; do not replace it or mint a new mailbox silently")
	}
	return nonce, nil
}

func (s *Service) mintEntries(ctx context.Context, entries []Entry) ([]Entry, error) {
	if len(entries) >= 768 {
		if err := s.reap(ctx); err != nil {
			return nil, err
		}
		return s.Store.List()
	}
	return entries, nil
}

func (s *Service) mintPolicy(issuer Issuance, coordinator bool, ttlS int64) (Issuance, time.Duration, error) {
	if s.endpointInfo().URL == "" {
		return issuer, 0, refused("no public invitation listener configured",
			"the operator configures public-url behind TLS, public-host with ACME consent, "+
				"or an explicit public-ip with the unverified-client acknowledgement; "+
				"a withdrawn address must be fixed before inviting")
	}
	who, maxLive, maxTTL := s.Policy.IssuancePolicy()
	issuer.MaxLive = maxLive
	if !issuer.Human && (who == "human" || (who == "coordinator" && !coordinator)) {
		return issuer, 0, refused("issuance is not allowed by this board's policy",
			"ask its coordinator or human; [invites] who controls issuance")
	}
	if ttlS == 0 {
		ttlS = min(7*24*3600, maxTTL)
	}
	if ttlS < 1 || ttlS > 365*24*3600 || (!issuer.Human && ttlS > maxTTL) {
		return issuer, 0, refused("invite lifetime exceeds policy",
			"omit ttl_s for the default; agent issuers are capped by [invites] max_ttl_s")
	}
	return issuer, time.Duration(ttlS) * time.Second, nil
}

func mintName(issuer Issuance, coordinator bool, entries []Entry, name string) (string, string, error) {
	if name == "" {
		name = nextChildName(issuer.By, entries)
	}
	if !ValidName(name) || (!issuer.Human && !coordinator && !strings.HasPrefix(name, issuer.By+"-")) {
		return "", "", refused("child name must be under its issuer's own ID prefix",
			"omit name for <issuer>-cloud-N, or use <issuer>-<suffix>; the name and invite host must fit the core name limit")
	}
	bound := ""
	for _, e := range entries {
		if e.Name == name {
			bound = e.AgentID
		}
	}
	return name, bound, nil
}

// Access config stays bounded without a human cleanup loop. Never discard a
// live key or the binding for a still-retained mailbox. Hash CAS makes an
// operator's concurrent reissue safe even while this derived GC is running.
func (s *Service) reap(ctx context.Context) error {
	entries, err := s.Store.entries()
	if err != nil {
		return err
	}
	now := time.Now()
	for _, e := range entries {
		if !e.Revoked && now.Before(e.Expires) {
			continue
		}
		present, err := s.Engine.InvitationHasAgent(ctx, e.Name, e.AgentID)
		if err != nil {
			return err
		}
		if !present {
			if err := s.Store.forget(e, now); err != nil {
				return err
			}
		}
	}
	return nil
}

func nextChildName(issuer string, entries []Entry) string {
	prefix := issuer + "-cloud-"
	n := uint64(1)
	for _, e := range entries {
		if tail, ok := strings.CutPrefix(e.Name, prefix); ok {
			if old, err := strconv.ParseUint(tail, 10, 32); err == nil {
				n = max(n, old+1)
			}
		}
	}
	return fmt.Sprintf("%s%d", prefix, n)
}

// Recipe returns portable host configuration with only this one invitation.
// It is returned to the issuer once and is never persisted or logged.
func Recipe(name, key, origin string) map[string]any {
	endpoint := strings.TrimSuffix(origin, "/") + "/mcp"
	auth := "Bearer " + key
	u, _ := url.Parse(origin) // Service accepts only operator-validated HTTPS origins.
	return map[string]any{
		"network_allowlist": u.Hostname(),
		"claude_command": "claude mcp add --transport http dibs " + strconv.Quote(endpoint) +
			" --header " + strconv.Quote("Authorization: "+auth),
		"mcp_json": map[string]any{"mcpServers": map[string]any{"dibs": map[string]any{
			"type": "http", "url": endpoint, "headers": map[string]string{"Authorization": auth},
		}}},
		"codex_toml": "[features]\nmcp_2026_07_28 = true\n[mcp_servers.dibs]\nurl = " + strconv.Quote(endpoint) +
			"\nhttp_headers = { Authorization = " + strconv.Quote(auth) + " }\n",
		"instructions": "Register as " + name + " with a retained random nonce; keep the returned agent token. " +
			"Allow outbound HTTPS to " + u.Hostname() + " in the cloud network allowlist. " +
			"Pull-only: call check_in and inbox at each activation.",
	}
}
