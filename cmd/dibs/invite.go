package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/agenxy/dibs/internal/invites"
)

func inviteLifetime(s string) (time.Duration, error) {
	if strings.HasSuffix(s, "d") {
		d, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		if err != nil || d < 1 || d > 365 {
			return 0, errors.New("invite lifetime must be between 1d and 365d")
		}
		return time.Duration(d) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < time.Second || d > 365*24*time.Hour {
		return 0, errors.New("invite lifetime must be between 1s and 365d (e.g. 30d or 12h)")
	}
	return d, nil
}

func invitePayload(args []string) (map[string]any, error) {
	payload, _, err := inviteOptions(args)
	return payload, err
}

func inviteOptions(args []string) (map[string]any, string, error) {
	fs := flag.NewFlagSet("invite", flag.ContinueOnError)
	ttl := fs.String("ttl", "7d", "invitation lifetime (agent policy defaults to at most 7d)")
	out := fs.String("out", "", "exclusive absolute private guest JSON destination; "+
		"INCOMPLETE, not provisionable until a supporting signed release exists; retain the board key for recovery")
	if helpOnly(args) {
		return nil, "", parseFlags(fs, args)
	}
	if len(args) == 0 {
		return nil, "", errors.New("usage: dibs invite <name> [--ttl 30d] [--out <absolute-private-file>] | " +
			"list | revoke <name>")
	}
	action := args[0]
	if action == "list" && len(args) == 1 {
		return map[string]any{"action": "list"}, "", nil
	}
	if action == "revoke" {
		payload, err := revokePayload(args[1:])
		return payload, "", err
	}
	if !invites.ValidName(action) || action == "list" || action == "revoke" {
		return nil, "", errors.New("invite needs a lowercase ASCII agent name, or list/revoke <name>")
	}
	if err := parseFlags(fs, args[1:]); err != nil {
		return nil, "", err
	}
	if fs.NArg() != 0 {
		return nil, "", errors.New("usage: dibs invite <name> [--ttl 30d] [--out <absolute-private-file>]")
	}
	ttlS, err := inviteMintTTL(fs, *ttl, *out)
	if err != nil {
		return nil, "", err
	}
	payload := map[string]any{"action": "mint", "name": action, "ttl_s": ttlS}
	if *out != "" {
		payload["export"] = true
	}
	return payload, *out, nil
}

// Let the service choose min(7d, configured ceiling) when --ttl is absent.
// Merely choosing --out must not send hard-coded 7d to a board capped at 1h.
func inviteMintTTL(fs *flag.FlagSet, ttl, destination string) (int64, error) {
	ttlSet := false
	outSet := false
	fs.Visit(func(f *flag.Flag) {
		ttlSet = ttlSet || f.Name == "ttl"
		outSet = outSet || f.Name == "out"
	})
	if outSet && destination == "" {
		return 0, errors.New("--out needs an absolute private file; omit it for the existing invitation display")
	}
	if !ttlSet {
		return 0, nil
	}
	d, err := inviteLifetime(ttl)
	return int64(d / time.Second), err
}

func revokePayload(args []string) (map[string]any, error) {
	fs := flag.NewFlagSet("invite revoke", flag.ContinueOnError)
	issuer := fs.String("issued-by", "", "revoke all invitations owned by this issuer")
	if err := parseFlags(fs, args); err != nil {
		return nil, err
	}
	if *issuer != "" && fs.NArg() == 0 {
		return map[string]any{"action": "revoke", "issued_by": *issuer}, nil
	}
	if fs.NFlag() == 0 && fs.NArg() == 1 && invites.ValidName(fs.Arg(0)) {
		return map[string]any{"action": "revoke", "name": fs.Arg(0)}, nil
	}
	return nil, errors.New("usage: dibs invite revoke <name> | --issued-by <issuer>")
}

func inviteCmd(args []string) error {
	payload, destination, err := inviteOptions(args)
	if err != nil {
		return err
	}
	var export *guestRecipeExport
	if destination != "" {
		export, err = openGuestRecipeExport(destination)
		if err != nil {
			return err // refuse before any invitation is minted
		}
		defer func() { _ = export.root.Close() }()
	}
	finish := func(out map[string]any) error {
		if export != nil {
			return export.publish(out, payload["name"].(string))
		}
		return printInviteResult(out, payload["action"] == "mint")
	}
	if err = checkConfigReadable(); err != nil {
		return err
	}
	if token := os.Getenv("DIBS_TOKEN"); token != "" {
		payload["token"] = token
		var out map[string]any
		if err := callHookTool("invite", payload, &out); err != nil {
			return err
		}
		return finish(out)
	}
	return adminOnly("invite", func() error {
		raw, err := adminPost("/api/admin/invites", payload)
		if err != nil {
			return err
		}
		var out map[string]any
		if err = json.Unmarshal(raw, &out); err != nil {
			return err
		}
		return finish(out)
	})
}

func printInviteResult(out map[string]any, minted bool) error {
	if code, _ := out["code"].(string); code != "" {
		return fmt.Errorf("%s: %v; %v", code, out["message"], out["hint"])
	}
	if !minted {
		b, _ := json.Marshal(out)
		fmt.Println(string(b))
		return nil
	}
	name, _ := out["name"].(string)
	key, _ := out["key"].(string)
	origin, _ := out["url"].(string)
	if name == "" || key == "" || origin == "" {
		return errors.New("the daemon did not return an invitation recipe")
	}
	if config, ok := out["config"].(map[string]any); ok {
		if _, guest := config["ca_pem"]; guest {
			return printGuestInvite(os.Stdout, name, key, origin, config)
		}
	}
	return printInviteRecipe(os.Stdout, name, key, origin)
}

func printGuestInvite(w io.Writer, name, key, origin string, config map[string]any) error {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return errors.New("guest invitation needs a literal IPv6 HTTPS origin; ask the issuer to fix its listener")
	}
	ip, err := netip.ParseAddr(u.Hostname())
	if err != nil || !ip.Is6() || ip.Is4In6() || ip.Zone() != "" {
		return errors.New("guest invitation needs a literal IPv6 HTTPS origin; ask the issuer to fix its listener")
	}
	ca, caOK := config["ca_pem"].(string)
	pin, pinOK := config["ca_spki_sha256"].(string)
	if !caOK || ca == "" || !pinOK || len(pin) != 64 {
		return errors.New("the daemon returned incomplete guest CA trust material; " +
			"list/revoke this invitation and retry after fixing the listener")
	}
	_, err = fmt.Fprintf(w, `Invitation for %s. Save this PRIVATE material now: the key cannot be shown again.
No native guest client is verified yet. No runnable native-client setup is offered.
Do not import this CA into system trust, disable TLS verification, or send the
invitation before verifying the intended literal IP. Local TLS is not a WAN proof.

Endpoint: %s/mcp
Invitation key: %s
Guest CA SHA-256 SPKI pin: %s
Address stability: %v
Guest CA public PEM:
%s
Deliver this privately; wait for a verified client adapter before connecting.
Pull-only; revoke with dibs invite revoke %s (effective on the next request).
`, name, strings.TrimSuffix(origin, "/"), key, pin, config["address_stability"], ca, name)
	return err
}

func printInviteRecipe(w io.Writer, name, key, origin string) error {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return errors.New("invitation recipe needs a public HTTPS origin")
	}
	recipe := invites.Recipe(name, key, origin)
	config, err := json.MarshalIndent(recipe["mcp_json"], "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, `Invitation for %s. Save this private recipe now: the key cannot be shown again.
Register as %q with a long random nonce you keep; retain the agent token returned.
This is pull-only cloud access, not the board's secret and not a wake route.

Claude Code:
%s

.mcp.json:
%s

Codex config.toml (merge these entries into your existing configuration):
%s

Cloud network: allow outbound HTTPS to %s in the environment's network allowlist.
If your cloud host cannot configure external MCP servers, call this HTTPS endpoint
directly from the container; do not copy the board's shared secret into it.
Mail: call check_in and inbox with your agent token at each activation.
Revoke: dibs invite revoke %s (effective on the next request).
`, name, name, recipe["claude_command"], config, recipe["codex_toml"], u.Hostname(), name)
	return err
}
