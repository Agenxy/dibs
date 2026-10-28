// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/agenxy/dibs/internal/core"
)

func processCmd(verb string, args []string) error {
	if verb == "register" {
		return registerCmd(args)
	}
	return sendCmd(args)
}

func registerCmd(args []string) error {
	fs := flag.NewFlagSet("register", flag.ContinueOnError)
	name := fs.String("name", "", "this process's own agent name (must match its invitation)")
	asJSON := fs.Bool("json", false, jsonHelp)
	const synopsis = "usage: dibs register --name <name> [--json]\n" +
		"DIBS_NONCE recovers an existing identity; otherwise a new nonce is generated.\n" +
		"Save the printed token and nonce privately, including the nonce returned on failure."
	if err := parseFlagsUsage(fs, args, synopsis); err != nil {
		return err
	}
	if *name == "" || fs.NArg() != 0 {
		return agentCLIError(errors.New("register needs --name and no positional arguments"), *asJSON)
	}
	nonce := os.Getenv("DIBS_NONCE")
	if nonce == "" {
		var secret [32]byte
		if _, err := rand.Read(secret[:]); err != nil {
			return agentCLIError(err, *asJSON)
		}
		nonce = hex.EncodeToString(secret[:])
	}
	var result struct {
		AgentID string `json:"agent_id"`
		Token   string `json:"token"`
		Nonce   string `json:"nonce"`
	}
	registration := map[string]any{"name": *name, "nonce": nonce, "kind": "persistent"}
	if err := agentToolCall("register", registration, &result); err != nil {
		return registrationCLIError(err, nonce, *asJSON)
	}
	if result.AgentID == "" || result.Token == "" {
		return registrationCLIError(errors.New("registration returned no usable agent identity/token"), nonce, *asJSON)
	}
	result.Nonce = nonce
	if *asJSON {
		return printJSON(result)
	}
	fmt.Printf("agent_id: %s\ntoken: %s\nnonce: %s\n", result.AgentID, result.Token, result.Nonce)
	return nil
}

// A lost reply can follow a successful registration. Return the attempted
// nonce even on failure, so generating one here cannot strand that mailbox.
// Like the successful credential result, this belongs in private stdout.
func registrationCLIError(err error, nonce string, asJSON bool) error {
	if asJSON {
		result := struct {
			core.Error
			Nonce string `json:"nonce"`
		}{agentCLIErrorDetail(err), nonce}
		if err := printJSON(result); err != nil {
			return err
		}
		return reportedError{err}
	}
	fmt.Printf("nonce: %s\n", nonce)
	return agentCLIError(err, false)
}

func sendCmd(args []string) error {
	fs := flag.NewFlagSet("send", flag.ContinueOnError)
	to := fs.String("to", "", "recipient agent name or id")
	kind := fs.String("type", "notify", "notify, question or request")
	asJSON := fs.Bool("json", false, jsonHelp)
	const synopsis = "usage: dibs send --to <agent> [--type notify|question|request] [--json] <body | ->\n" +
		"Use DIBS_TOKEN for your OWN agent; - reads the exact body from stdin. A subject belongs in the body."
	if err := parseFlagsUsage(fs, args, synopsis); err != nil {
		return err
	}
	if *to == "" || fs.NArg() != 1 {
		return agentCLIError(errors.New("send needs --to and exactly one body argument (or - for stdin)"), *asJSON)
	}
	if *kind != "notify" && *kind != "question" && *kind != "request" {
		return agentCLIError(errors.New("send --type must be notify, question or request"), *asJSON)
	}
	token := os.Getenv("DIBS_TOKEN")
	if token == "" {
		return agentCLIError(core.ErrBadToken, *asJSON)
	}
	body := fs.Arg(0)
	if body == "-" {
		// Read at most one byte beyond the server's ordinary body cap, so an
		// unbounded producer cannot exhaust the CLI before the daemon sees it.
		limit := core.DefaultLimits().MaxBodyBytes
		data, err := io.ReadAll(io.LimitReader(os.Stdin, int64(limit)+1))
		if err != nil {
			return agentCLIError(err, *asJSON)
		}
		if len(data) > limit {
			return agentCLIError(errors.New("stdin body exceeds the default body limit; "+
				"shorten it or use a file attachment over MCP"), *asJSON)
		}
		body = string(data)
	}
	var result map[string]json.RawMessage
	mail := map[string]any{"token": token, "to": *to, "type": *kind, "body": body}
	if err := agentToolCall("send", mail, &result); err != nil {
		return agentCLIError(err, *asJSON)
	}
	var serial uint64
	if json.Unmarshal(result["msg_serial"], &serial) != nil || serial == 0 {
		return agentCLIError(errors.New("send returned no message serial; outcome unconfirmed"), *asJSON)
	}
	if *asJSON {
		return printJSON(result)
	}
	fmt.Printf("sent message %d\n", serial)
	return nil
}
