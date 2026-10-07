// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

// registrypublish makes a retry succeed only when the registry's version is
// equivalent to the manifest already bound to the published release. A 409,
// auth error, or timeout is not by itself evidence of successful publication.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"reflect"
	"time"
)

const registryURL = "https://registry.modelcontextprotocol.io"

func main() {
	client := &http.Client{Timeout: 20 * time.Second}
	if err := publish(context.Background(), client, registryURL, "server.json", func() error {
		cmd := exec.Command("./mcp-publisher", "publish")
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		return cmd.Run()
	}); err != nil {
		fmt.Fprintln(os.Stderr, "registrypublish:", err)
		os.Exit(1)
	}
}

func publish(ctx context.Context, client *http.Client, base, path string, send func() error) error {
	// #nosec G304 -- fixed server.json in the release's trusted checkout.
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var want map[string]any
	if err = json.Unmarshal(data, &want); err != nil {
		return err
	}
	name, ok := want["name"].(string)
	if !ok || name != "io.github.Agenxy/dibs" {
		return errors.New("registry manifest is not Dibs")
	}
	version, ok := want["version"].(string)
	if !ok || version == "" {
		return errors.New("registry manifest has no version")
	}
	endpoint := base + "/v0.1/servers/" + url.PathEscape(name) + "/versions/" + url.PathEscape(version)
	matched, err := equivalent(ctx, client, endpoint, want)
	if err != nil {
		return err
	}
	if matched {
		fmt.Println("registry already carries this exact release manifest")
		return nil
	}
	sendErr := send()
	// Check after ALL outcomes: a timed-out response may have applied, and
	// a zero exit without matching public bytes is not a delivered promise.
	matched, err = equivalent(ctx, client, endpoint, want)
	if err != nil {
		return err
	}
	if matched {
		return nil
	}
	if sendErr != nil {
		return sendErr
	}
	return errors.New("publisher exited successfully but registry has no equivalent manifest; " +
		"retry without moving the release tag")
}

func equivalent(ctx context.Context, client *http.Client, endpoint string, want map[string]any) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return false, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("registry lookup returned HTTP %d; not absence or equivalence", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil {
		return false, err
	}
	if len(data) > 1<<20 {
		return false, errors.New("registry response exceeds bound")
	}
	var doc struct {
		Server map[string]any `json:"server"`
	}
	if err = json.Unmarshal(data, &doc); err != nil {
		return false, err
	}
	if !reflect.DeepEqual(doc.Server, want) {
		return false, errors.New("existing registry version differs from the release manifest; preserve it and investigate")
	}
	return true, nil
}
