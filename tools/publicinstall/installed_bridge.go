// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"time"
)

func (r *runner) installedBoard(ctx context.Context, bin string, env []string) (serviceBoard, error) {
	var board serviceBoard
	out, err := r.commandContext(ctx, "installed-board", env, filepath.Join(bin, "dibs"), "board", "--json")
	if err != nil {
		return board, err
	}
	err = json.Unmarshal([]byte(out), &board)
	if err == nil && board.Node == "" {
		err = errors.New("installed board command omitted board identity")
	}
	return board, err
}

func (r *runner) awaitInstalledBoard(bin string, env []string) error {
	ctx, cancel := context.WithTimeout(r.ctx, time.Minute)
	defer cancel()
	for {
		_, err := r.installedBoard(ctx, bin, env)
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.Join(ctx.Err(), err)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// Only protocol and counts enter command logs. RPC replies carry private
// fixture tokens; logging their bytes would publish those in Actions artifacts.
func (r *runner) installedBridge(bin, home string, env []string, protocol string, index int) (result error) {
	ctx, cancel := context.WithTimeout(r.ctx, time.Minute)
	defer cancel()
	args := []string{filepath.Join(bin, "dibs"), "mcp-stdio"}
	start := time.Now()
	var stderr bytes.Buffer
	var calls int
	defer func() {
		summary := fmt.Sprintf("protocol %s; %d matched replies; private RPC bodies omitted\n", protocol, calls)
		if result != nil {
			summary += "proof failed: " + result.Error() + "\n"
		}
		_, logErr := r.recordCommand("installed-mcp-stdio", start, args, summary, stderr.String(), result)
		result = errors.Join(result, logErr)
	}()
	cmd, err := r.newCommand(ctx, env, args...)
	if err != nil {
		return err
	}
	cmd.Stderr = &stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	bridge := &installedRPC{
		encoder: json.NewEncoder(in), decoder: json.NewDecoder(io.LimitReader(out, 8<<20)),
		protocol: protocol,
	}
	proofErr := bridge.handshake()
	if proofErr == nil {
		proofErr = coordination(bridge.tool, home, index)
	}
	calls = bridge.matched
	closeErr := in.Close()
	return errors.Join(proofErr, closeErr, cmd.Wait())
}

type installedRPC struct {
	encoder  *json.Encoder
	decoder  *json.Decoder
	protocol string
	id       int
	matched  int
}

func (b *installedRPC) request(method string, params map[string]any) (json.RawMessage, error) {
	b.id++
	params["_meta"] = map[string]any{"io.modelcontextprotocol/protocolVersion": b.protocol}
	request := map[string]any{"jsonrpc": "2.0", "id": b.id, "method": method, "params": params}
	if err := b.encoder.Encode(request); err != nil {
		return nil, err
	}
	for range 64 { // bounded unsolicited frames, never an unbounded notification drain
		var reply struct {
			ID     json.RawMessage
			Error  json.RawMessage
			Result json.RawMessage
		}
		if err := b.decoder.Decode(&reply); err != nil {
			return nil, err
		}
		if len(reply.ID) == 0 {
			continue
		}
		var id int
		if json.Unmarshal(reply.ID, &id) != nil || id != b.id {
			return nil, errors.New("installed bridge returned an unmatched RPC id")
		}
		if len(reply.Error) > 0 && string(reply.Error) != "null" {
			return nil, fmt.Errorf("installed bridge refused %s (private reply omitted)", method)
		}
		if len(reply.Result) == 0 || string(reply.Result) == "null" {
			return nil, errors.New("installed bridge omitted the RPC result")
		}
		b.matched++
		return reply.Result, nil
	}
	return nil, errors.New("installed bridge exceeded the unsolicited frame bound")
}

func (b *installedRPC) handshake() error {
	if b.protocol == "2026-07-28" {
		body, err := b.request("server/discover", map[string]any{})
		if err != nil {
			return err
		}
		var result struct{ SupportedVersions []string }
		if err := json.Unmarshal(body, &result); err != nil || !slices.Contains(result.SupportedVersions, b.protocol) {
			return errors.New("installed bridge did not discover the requested modern protocol")
		}
		return nil
	}
	body, err := b.request("initialize", map[string]any{
		"protocolVersion": b.protocol,
		"capabilities":    map[string]any{}, "clientInfo": map[string]any{"name": "dibs-install-acceptance", "version": "1"},
	})
	if err != nil {
		return err
	}
	var result struct{ ProtocolVersion string }
	if err := json.Unmarshal(body, &result); err != nil || result.ProtocolVersion != b.protocol {
		return errors.New("installed bridge did not negotiate the requested legacy protocol")
	}
	return b.encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
}

func (b *installedRPC) tool(name string, args map[string]any) (map[string]any, error) {
	body, err := b.request("tools/call", map[string]any{"name": name, "arguments": args})
	if err != nil {
		return nil, err
	}
	var envelope struct {
		IsError bool
		Content []struct{ Type, Text string }
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, err
	}
	if envelope.IsError || len(envelope.Content) == 0 || envelope.Content[0].Type != "text" {
		return nil, fmt.Errorf("installed bridge tool %s refused (private reply omitted)", name)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(envelope.Content[0].Text), &result); err != nil {
		return nil, err
	}
	if result["code"] != nil {
		return nil, fmt.Errorf("installed bridge tool %s returned a refusal (private details omitted)", name)
	}
	return result, nil
}
