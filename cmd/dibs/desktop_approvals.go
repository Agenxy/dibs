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

const desktopApprovalsHelp = `dibs desktop-approvals [on|off] [--json]

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
		return errors.New(desktopApprovalsHelp)
	}
	var enabled *bool
	if fs.NArg() == 1 {
		value := fs.Arg(0) == "on"
		enabled = &value
	}
	var result map[string]any
	if token := os.Getenv("DIBS_TOKEN"); token != "" {
		call := map[string]any{"token": token}
		if enabled != nil {
			call["setting"], call["value"] = core.DesktopApprovalSettingKey, fmt.Sprint(*enabled)
		}
		if err := agentToolCall("settings", call, &result); err != nil {
			return agentCLIError(err, *asJSON)
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
			return err
		}
	}
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
		return agentCLIError(&core.Error{Code: "E_NO_SETTING", Msg: "this board does not expose desktop approval confirmation",
			Hint: "upgrade the daemon, then retry this command"}, *asJSON)
	}
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
