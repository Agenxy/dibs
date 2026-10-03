package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
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
	fs := flag.NewFlagSet("invite", flag.ContinueOnError)
	ttl := fs.String("ttl", "7d", "invitation lifetime (agent policy defaults to at most 7d)")
	if helpOnly(args) {
		return nil, parseFlags(fs, args)
	}
	if len(args) == 0 {
		return nil, errors.New("usage: dibs invite <name> [--ttl 30d] | list | revoke <name>")
	}
	action := args[0]
	if action == "list" && len(args) == 1 {
		return map[string]any{"action": "list"}, nil
	}
	if action == "revoke" {
		return revokePayload(args[1:])
	}
	if !invites.ValidName(action) || action == "list" || action == "revoke" {
		return nil, errors.New("invite needs a lowercase ASCII agent name, or list/revoke <name>")
	}
	if err := parseFlags(fs, args[1:]); err != nil {
		return nil, err
	}
	if fs.NArg() != 0 {
		return nil, errors.New("usage: dibs invite <name> [--ttl 30d]")
	}
	// Let the service choose min(7d, configured ceiling) when --ttl is
	// absent. Sending a hard-coded 7d would break a board capped at 1h.
	var d time.Duration
	if fs.NFlag() > 0 {
		var err error
		d, err = inviteLifetime(*ttl)
		if err != nil {
			return nil, err
		}
	}
	return map[string]any{"action": "mint", "name": action, "ttl_s": int64(d / time.Second)}, nil
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
	payload, err := invitePayload(args)
	if err != nil {
		return err
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
		return printInviteResult(out, payload["action"] == "mint")
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
		return printInviteResult(out, payload["action"] == "mint")
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
	return printInviteRecipe(os.Stdout, name, key, origin)
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
