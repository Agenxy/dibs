// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/agenxy/dibs/internal/core"
)

const desktopApprovalsHelp = `usage: dibs desktop-approvals [on|off] [--json]

Reads or changes the optional Touch ID confirmation for privileged desktop
approvals. Default: off. Ordinary questions and requests are unaffected.

With DIBS_TOKEN, uses settings as that agent: any authenticated agent can
enable the check, and only the human identity can disable it. Without a token,
uses the existing human/admin terminal and board-password authentication.
You can also use Settings on dibs web, which supports Touch ID unlocking.
`

func desktopApprovalsCmd(args []string) error {
	fs := flag.NewFlagSet("desktop-approvals", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, jsonHelp)
	if len(args) > 0 && (args[0] == "on" || args[0] == "off") {
		args = append(append([]string(nil), args[1:]...), args[0])
	}
	if err := parseFlagsUsage(fs, args, desktopApprovalsHelp); err != nil {
		return err
	}
	if fs.NArg() > 1 || fs.NArg() == 1 && fs.Arg(0) != "on" && fs.Arg(0) != "off" {
		return errors.New("expected on or off; see dibs desktop-approvals --help")
	}
	var enabled *bool
	if fs.NArg() == 1 {
		value := fs.Arg(0) == "on"
		enabled = &value
	}
	result, err := callDesktopApproval(enabled)
	if err != nil {
		return desktopApprovalCLIError(err, *asJSON)
	}
	result, err = desktopApprovalResult(result)
	if err != nil {
		return desktopApprovalCLIError(err, *asJSON)
	}
	value := result["value"].(string)
	if *asJSON {
		return printJSON(result)
	}
	state := "off"
	if value == "true" {
		state = "on"
	}
	fmt.Printf("Confirm privileged desktop approvals with Touch ID: %s\n", state)
	return nil
}

func desktopApprovalCLIError(err error, asJSON bool) error {
	detail := agentCLIErrorDetail(err)
	var domain *core.Error
	if !errors.As(err, &domain) || domain.Hint == "" {
		detail.Hint = "check dibs desktop-approvals --help, board access and human authentication; " +
			"use Settings on dibs web"
	}
	return agentCLIError(&detail, asJSON)
}

func callDesktopApproval(enabled *bool) (map[string]any, error) {
	var result map[string]any
	if token := os.Getenv("DIBS_TOKEN"); token != "" {
		call := map[string]any{"token": token}
		if enabled != nil {
			call["setting"], call["value"] = core.DesktopApprovalSettingKey, fmt.Sprint(*enabled)
		}
		if err := agentToolCall("settings", call, &result); err != nil {
			return nil, err
		}
	} else {
		if err := adminOnly("desktop-approvals", func() error {
			payload := map[string]any{}
			if enabled != nil {
				payload["enabled"] = *enabled
			}
			raw, err := adminPost("/api/admin/desktop-approvals", payload)
			if err != nil {
				return err
			}
			return json.Unmarshal(raw, &result)
		}); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func desktopApprovalResult(result map[string]any) (map[string]any, error) {
	if rows, ok := result["settings"].([]any); ok {
		for _, entry := range rows {
			row, rowOK := entry.(map[string]any)
			if rowOK && row["setting"] == core.DesktopApprovalSettingKey {
				result = row
				break
			}
		}
	}
	value, ok := result["value"].(string)
	if !ok || result["setting"] != core.DesktopApprovalSettingKey || value != "true" && value != "false" {
		return nil, &core.Error{
			Code: "E_NO_SETTING", Msg: "this board does not expose desktop approval confirmation",
			Hint: "upgrade the daemon, then retry this command",
		}
	}
	return result, nil
}
