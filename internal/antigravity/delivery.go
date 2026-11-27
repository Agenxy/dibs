// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package antigravity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/paths"
	"github.com/agenxy/dibs/internal/procexec"
)

const timeout = 3 * time.Second

type descriptor struct {
	PID      int    `json:"pid"`
	HTTPPort int    `json:"httpPort"`
	Token    string `json:"csrfToken"`
}

func hubDescriptor(home string, hub Hub) (descriptor, bool) {
	return selectHubDescriptor(home, func(d descriptor) bool { return d.PID == hub.PID })
}

func selectHubDescriptor(home string, accept func(descriptor) bool) (descriptor, bool) {
	dir := filepath.Join(home, ".gemini", "antigravity", "daemon")
	files, err := os.ReadDir(dir)
	if err != nil {
		return descriptor{}, false
	}
	var match descriptor
	for _, entry := range files {
		portText, ok := strings.CutPrefix(entry.Name(), "hub_")
		portText, jsonFile := strings.CutSuffix(portText, ".json")
		port, err := strconv.Atoi(portText)
		if !ok || !jsonFile || !entry.Type().IsRegular() || err != nil || port <= 0 || port > 65535 {
			continue
		}
		d, ok := readDescriptor(filepath.Join(dir, entry.Name()))
		if ok && d.PID > 1 && d.HTTPPort == port && accept(d) {
			if match.PID != 0 {
				return descriptor{}, false
			}
			match = d
		}
	}
	return match, match.PID != 0
}

func readDescriptor(path string) (descriptor, bool) {
	f, err := os.Open(path) // #nosec G304 -- listed local Antigravity hub descriptor.
	if err != nil {
		return descriptor{}, false
	}
	defer func() { _ = f.Close() }()
	var d descriptor
	data, err := io.ReadAll(io.LimitReader(f, (32<<10)+1))
	ok := err == nil && len(data) <= 32<<10 && json.Unmarshal(data, &d) == nil && d.Token != "" && len(d.Token) <= 1024
	return d, ok
}

// Prepare discovers the current desktop hub and verifies that the exact
// conversation still answers. The returned delivery rechecks that the hub
// and its credentials still match before handing off the notice.
func Prepare(ctx context.Context, conversation, cwd string) (func(context.Context, string) error, error) {
	if !core.LooksLikeThreadID(conversation) {
		return nil, errors.New("invalid Antigravity conversation")
	}
	hub, d, err := currentDesktopDescriptor()
	if err != nil {
		return nil, err
	}
	if err := verifyWorkspace(ctx, binary, d, conversation, cwd); err != nil {
		return nil, err
	}
	return func(ctx context.Context, notice string) error {
		current, err := liveDescriptor(hub)
		if err != nil {
			return err
		}
		if current != d {
			return errors.New("the Antigravity desktop hub changed before delivery")
		}
		return deliver(ctx, binary, d, conversation, notice)
	}, nil
}

func verifyWorkspace(ctx context.Context, exe string, d descriptor, conversation, cwd string) error {
	if cwd != "" {
		info, err := os.Stat(cwd)
		if errors.Is(err, os.ErrNotExist) {
			return errors.New("the recorded working directory no longer exists; " +
				"check in from an existing workspace in this conversation")
		}
		if err != nil || !info.IsDir() {
			return errors.New("the recorded working directory is not an accessible directory; " +
				"check in from an existing workspace in this conversation")
		}
	}
	var reply struct {
		Response struct {
			Conversation struct {
				Metadata *struct {
					WorkspaceURIs []string `json:"workspaceUris"`
				} `json:"metadata"`
			} `json:"conversationMetadata"`
		} `json:"response"`
	}
	if err := call(ctx, exe, d, &reply, "get-conversation-metadata", conversation); err != nil {
		return err
	}
	meta := reply.Response.Conversation.Metadata
	if meta == nil || !workspaceMatches(cwd, meta.WorkspaceURIs) {
		return errors.New("the Antigravity did not confirm the conversation workspace")
	}
	return nil
}

func workspaceMatches(cwd string, uris []string) bool {
	if cwd == "" {
		return true // the caller stated no location to compare
	}
	if !filepath.IsAbs(cwd) {
		return false
	}
	for _, raw := range uris {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "file" || u.Host != "" || u.RawQuery != "" || u.Fragment != "" ||
			!filepath.IsAbs(u.Path) {
			continue
		}
		rel, err := filepath.Rel(paths.Canonical(u.Path), paths.Canonical(cwd))
		if err == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
			return true
		}
		if sameLocalProject(cwd, u.Path) {
			return true
		}
	}
	return false
}

// Both paths are observations from this machine: Prepare only accepts the
// locally verified desktop hub, and the engine excludes remote agent bindings.
// Keep that host beside the Git facts when using core's repository rule. A
// file URI naming another host is rejected before it reaches this comparison.
func sameLocalProject(a, b string) bool {
	host, err := os.Hostname()
	if err != nil || host == "" {
		return false
	}
	identity := func(dir string) *core.AgentInfo {
		common, remote, roots, ok := paths.Identify(dir).Identity()
		if !ok {
			return nil
		}
		return &core.AgentInfo{HostID: host, RepoDir: common, RepoRemote: remote, RepoRoots: roots}
	}
	return core.SameProject(identity(a), identity(b))
}

func currentDesktopDescriptor() (Hub, descriptor, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Hub{}, descriptor{}, err
	}
	var hub Hub
	d, ok := selectHubDescriptor(home, func(d descriptor) bool {
		candidate, live := desktopHub(d.PID)
		if live {
			hub = candidate
		}
		return live
	})
	if !ok {
		return Hub{}, descriptor{}, errors.New("the Antigravity desktop does not have one verified live hub")
	}
	return hub, d, nil
}

func liveDescriptor(hub Hub) (descriptor, error) {
	current, ok := desktopHub(hub.PID)
	if !ok || !current.Start.Equal(hub.Start) {
		return descriptor{}, errors.New("the Antigravity desktop language server changed")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return descriptor{}, err
	}
	d, ok := hubDescriptor(home, hub)
	if !ok {
		return descriptor{}, errors.New("the Antigravity desktop hub descriptor is unavailable")
	}
	return d, nil
}

func deliver(ctx context.Context, exe string, d descriptor, conversation, notice string) error {
	var reply struct {
		Response struct {
			SendMessage struct {
				Recipient string `json:"recipientId"`
				Content   string `json:"content"`
			} `json:"sendMessage"`
		} `json:"response"`
	}
	err := call(ctx, exe, d, &reply, "send-message", "--title=Dibs", conversation, notice)
	if err != nil {
		return err
	}
	if reply.Response.SendMessage.Recipient != conversation || reply.Response.SendMessage.Content != notice {
		return errors.New("the Antigravity did not confirm the exact wake notice")
	}
	return nil
}

func call(ctx context.Context, exe string, d descriptor, reply any, arguments ...string) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// #nosec G204 G702 -- the installed application binary, fixed RPC verbs, validated conversation, factual notice.
	cmd := procexec.Command(ctx, exe, append([]string{"agentapi"}, arguments...)...)
	cmd.Env = []string{
		"ANTIGRAVITY_LS_ADDRESS=127.0.0.1:" + strconv.Itoa(d.HTTPPort),
		"ANTIGRAVITY_CSRF_TOKEN=" + d.Token,
	}
	cmd.WaitDelay = 100 * time.Millisecond
	var output replyBuffer
	cmd.Stdout = &output
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("the Antigravity agentapi failed: %w", err)
	}
	if json.Unmarshal(output.Bytes(), reply) != nil {
		return errors.New("the Antigravity agentapi returned unreadable metadata")
	}
	return nil
}

// The native command is bounded in time and in the number of private bytes
// retained from an app reply. Neither stderr nor reply bodies enter logs.
type replyBuffer struct{ bytes.Buffer }

func (b *replyBuffer) Write(p []byte) (int, error) {
	const maxReply = 1 << 20
	if len(p) > maxReply-b.Len() {
		return 0, errors.New("the Antigravity agentapi reply exceeded its size limit")
	}
	return b.Buffer.Write(p)
}
