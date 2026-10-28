// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// agentToolCall is the shared transport for process registration, mail and
// file authorization. One stateless 2026 call, no session, automatic retry or
// credential fallback: an uncertain send must not become two messages.
func agentToolCall(tool string, args map[string]any, out any) error {
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": args},
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, mcpEndpoint(), bytes.NewReader(body))
	if err != nil {
		return errors.New("invalid MCP endpoint; check DIBS_ADDR")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("MCP-Protocol-Version", "2026-07-28")
	if invite := os.Getenv("DIBS_INVITE"); invite != "" {
		req.Header.Set("Authorization", "Bearer "+invite)
	} else {
		secret, err := localSecret()
		if err != nil {
			return err
		}
		req.Header.Set("X-Dibs-Local", secret)
	}
	client := daemonClient(30 * time.Second)
	defer client.CloseIdleConnections()
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("MCP connection failed; check address, certificate trust and credentials; outcome unconfirmed")
	}
	defer func() { _ = resp.Body.Close() }()
	return decodeAgentToolResult(resp, out)
}

func decodeAgentToolResult(resp *http.Response, out any) error {
	var envelope struct {
		core.Error
		RPCError *struct {
			Code    int             `json:"code"`
			Message string          `json:"message"`
			Data    json.RawMessage `json:"data"`
		} `json:"error"`
		Result *struct {
			IsError bool
			Content []struct{ Type, Text string }
		}
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&envelope); err != nil {
		return errors.New("invalid MCP response; check board address and authorization; outcome unconfirmed")
	}
	// The HTTP gate, JSON-RPC admission and a tool refusal have different
	// envelopes. Preserve the actual server hint from all three.
	if envelope.Code != "" {
		return &envelope.Error
	}
	if rpc := envelope.RPCError; rpc != nil {
		var domain core.Error
		_ = json.Unmarshal(rpc.Data, &domain)
		if domain.Code == "" {
			domain.Code = "E_MCP_RPC"
		}
		domain.Msg = rpc.Message
		return &domain
	}
	result := envelope.Result
	if resp.StatusCode != http.StatusOK || result == nil || len(result.Content) != 1 || result.Content[0].Type != "text" {
		return errors.New("MCP call refused; check HTTP(S) MCP endpoint and current credentials; outcome unconfirmed")
	}
	text := []byte(result.Content[0].Text)
	if result.IsError {
		var domain core.Error
		if err := json.Unmarshal(text, &domain); err != nil || domain.Code == "" {
			return errors.New("invalid MCP refusal; check board authorization; outcome unconfirmed")
		}
		return &domain
	}
	if err := json.Unmarshal(text, out); err != nil {
		return errors.New("invalid MCP result; outcome unconfirmed")
	}
	return nil
}

func agentCLIErrorDetail(err error) core.Error {
	var domain *core.Error
	if !errors.As(err, &domain) {
		domain = &core.Error{Code: "E_CLI", Msg: err.Error()}
	}
	// Do not edit a shared core sentinel while adding the CLI's fallback hint.
	copy := *domain
	if copy.Hint == "" {
		copy.Hint = "check dibs register --help or dibs send --help, board access and credentials; " +
			"do not blindly retry an unconfirmed send"
	}
	return copy
}

func agentCLIError(err error, asJSON bool) error {
	domain := agentCLIErrorDetail(err)
	if asJSON {
		if err := printJSON(&domain); err != nil {
			return err
		}
		return reportedError{&domain}
	}
	return fmt.Errorf("%w\nhint: %s", &domain, domain.Hint)
}
