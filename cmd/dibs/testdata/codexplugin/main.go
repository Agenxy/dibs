// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

// A local Codex plugin-installer fixture. It accepts the production argv,
// copies the actual materialized plugin, and can fail before or after mutation.
package main

import (
	"bufio"
	"encoding/json"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--hold-hook-pipes" {
		hookPipeFixture(true)
		return
	}
	if len(os.Args) == 2 && os.Args[1] == "app-server" {
		if os.Getenv("DIBS_TEST_HOOK_PIPE_CONTROL") != "" && os.Getenv("DIBS_TEST_HOOK_PIPE_PHASE") != "hooks/list" {
			hookPipeFixture(false)
			return
		}
		serveHooks()
		return
	}
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		return
	}
	if len(os.Args) != 9 || os.Args[1] != "-c" || os.Args[2] != `marketplaces.dibs.source_type="local"` ||
		os.Args[3] != "-c" || os.Args[5] != "plugin" || os.Args[6] != "add" || os.Args[7] != "dibs@dibs" || os.Args[8] != "--json" {
		os.Exit(3)
	}
	home := os.Getenv("CODEX_HOME")
	mark := filepath.Join(home, "installer-called")
	if err := os.WriteFile(mark, []byte(strings.Join(os.Args[1:], "\n")), 0o600); err != nil {
		panic(err)
	}
	mode := os.Getenv("DIBS_PLUGIN_FIXTURE")
	if mode == "fail" {
		os.Exit(4)
	}
	source, err := strconv.Unquote(strings.TrimPrefix(os.Args[4], "marketplaces.dibs.source="))
	if err != nil {
		panic(err)
	}
	plugin := filepath.Join(source, "plugin")
	b, err := os.ReadFile(filepath.Join(plugin, ".codex-plugin", "plugin.json"))
	if err != nil {
		panic(err)
	}
	var manifest struct{ Version string }
	if err := json.Unmarshal(b, &manifest); err != nil {
		panic(err)
	}
	base := filepath.Join(home, "plugins", "cache", "dibs", "dibs")
	if err := os.RemoveAll(base); err != nil {
		panic(err)
	}
	root := filepath.Join(base, manifest.Version)
	err = filepath.WalkDir(plugin, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(plugin, path)
		if err != nil {
			return err
		}
		dest := filepath.Join(root, rel)
		if entry.IsDir() {
			return os.MkdirAll(dest, 0o700)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(dest, b, 0o600)
	})
	if err != nil {
		panic(err)
	}
	if mode == "partial" {
		if err := os.WriteFile(filepath.Join(root, ".mcp.json"), []byte("{}"), 0o600); err != nil {
			panic(err)
		}
	}
	if mode == "late-fail" {
		os.Exit(5)
	}
	receiptVersion := manifest.Version
	if mode == "wrong-receipt" {
		receiptVersion = "wrong"
	}
	receiptRoot := root
	if mode == "canonical-receipt" {
		receiptRoot, err = filepath.EvalSymlinks(root)
		if err != nil {
			panic(err)
		}
	}
	if mode == "other-root-receipt" {
		receiptRoot = filepath.Dir(root) // exists, but is not this installed directory
	}
	if err := json.NewEncoder(os.Stdout).Encode(map[string]string{
		"pluginId": "dibs@dibs", "version": receiptVersion, "installedPath": receiptRoot,
	}); err != nil {
		panic(err)
	}
}

func hookPipeFixture(grandchild bool) {
	conn, err := net.DialTimeout("tcp", os.Getenv("DIBS_TEST_HOOK_PIPE_CONTROL"), 5*time.Second)
	if err != nil {
		panic(err)
	}
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		panic(err)
	}
	role := byte('p')
	if grandchild {
		role = 'g'
		st, err := os.Stdout.Stat()
		if err != nil || st.Mode()&os.ModeNamedPipe == 0 {
			panic("grandchild did not inherit the real hook protocol pipe")
		}
		if os.Getenv("DIBS_TEST_HOOK_STDOUT_ONLY") == "1" {
			stderr, err := os.Stderr.Stat()
			if err != nil || stderr.Mode()&os.ModeNamedPipe != 0 {
				panic("stdout-only grandchild retained a stderr pipe")
			}
		}
	}
	if _, err := conn.Write([]byte{role}); err != nil {
		panic(err)
	}
	if !grandchild {
		child := exec.Command(os.Args[0], "--hold-hook-pipes") // #nosec G204 -- private fixture executable
		child.Stdout = os.Stdout
		if os.Getenv("DIBS_TEST_HOOK_STDOUT_ONLY") != "1" {
			child.Stderr = os.Stderr
		}
		if err := child.Start(); err != nil {
			panic(err)
		}
		if err := child.Process.Release(); err != nil {
			panic(err)
		}
	}
	var release [1]byte
	if _, err := io.ReadFull(conn, release[:]); err != nil || (role == 'p' && release[0] != 'e') || (role == 'g' && release[0] != 'x') {
		panic("fixture was not released")
	}
}

func serveHooks() {
	scan := bufio.NewScanner(os.Stdin)
	for scan.Scan() {
		var req struct {
			ID     *int   `json:"id"`
			Method string `json:"method"`
		}
		if err := json.Unmarshal(scan.Bytes(), &req); err != nil {
			panic(err)
		}
		if req.ID == nil {
			continue
		}
		var result any = map[string]any{}
		if req.Method == "hooks/list" {
			if os.Getenv("DIBS_TEST_HOOK_PIPE_CONTROL") != "" && os.Getenv("DIBS_TEST_HOOK_PIPE_PHASE") == "hooks/list" {
				hookPipeFixture(false)
				return
			}
			home := os.Getenv("CODEX_HOME")
			if err := os.WriteFile(filepath.Join(home, "hooks-listed"), []byte("listed"), 0o600); err != nil {
				panic(err)
			}
			status := os.Getenv("DIBS_HOOK_FIXTURE_STATUS")
			if status == "" {
				status = "modified"
			}
			hooks := []map[string]any{}
			if status != "missing" {
				for _, event := range []string{"sessionStart", "stop", "subagentStop"} {
					hooks = append(hooks, map[string]any{
						"key": "dibs@dibs:hooks.json:" + event + ":0:0", "eventName": event,
						"handlerType": "mcpTool", "server": "dibs", "tool": "hook_poll", "pluginId": "dibs@dibs",
						"currentHash": "sha256:fixture", "trustStatus": status, "enabled": true,
					})
				}
			}
			result = map[string]any{"data": []map[string]any{{"hooks": hooks}}}
		}
		if err := json.NewEncoder(os.Stdout).Encode(map[string]any{"jsonrpc": "2.0", "id": *req.ID, "result": result}); err != nil {
			panic(err)
		}
	}
	if err := scan.Err(); err != nil {
		panic(err)
	}
}
