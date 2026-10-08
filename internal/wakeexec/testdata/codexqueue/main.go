// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

// A subprocess fixture for the native queue protocol and the real command route.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/agenxy/dibs/internal/paths"
)

func main() {
	path := filepath.Join(os.Getenv("CODEX_HOME"), "pending.json")
	if len(os.Args) == 2 && os.Args[1] == "primary" {
		fmt.Println("thread already has an active writer")
		os.Exit(1)
	}
	if len(os.Args) == 2 && os.Args[1] == "fallback-marker" {
		if err := os.WriteFile(filepath.Join(os.Getenv("CODEX_HOME"), "fallback-ran"), nil, 0o600); err != nil {
			panic(err)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "queue" {
		var thread, message string
		for i := 2; i+1 < len(os.Args); i++ {
			if os.Args[i] == "--thread" {
				thread = os.Args[i+1]
			}
			if os.Args[i] == "--message" {
				message = os.Args[i+1]
			}
		}
		if thread == "" || message == "" {
			os.Exit(2)
		}
		if tag := os.Getenv("DIBS_QUEUE_RACE_HELPER"); tag != "" && thread == "race-thread" {
			if err := os.WriteFile(filepath.Join(os.Getenv("CODEX_HOME"), "command-ready-"+tag), nil, 0o600); err != nil {
				panic(err)
			}
			deadline := time.Now().Add(10 * time.Second)
			for {
				if _, err := os.Stat(filepath.Join(os.Getenv("CODEX_HOME"), "command-release")); err == nil {
					break
				}
				if time.Now().After(deadline) {
					os.Exit(6)
				}
				time.Sleep(5 * time.Millisecond)
			}
		}
		// Model the app's atomic queue transaction across independent writers.
		lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			panic(err)
		}
		if err = paths.LockExclusive(lock, true); err != nil {
			panic(err)
		}
		defer func() { paths.Unlock(lock); _ = lock.Close() }()
		b, _ := os.ReadFile(path)
		var rows []map[string]any
		_ = json.Unmarshal(b, &rows)
		rows = append(rows, map[string]any{"id": fmt.Sprint(len(rows) + 1), "threadId": thread, "input": []map[string]string{{"type": "text", "text": message}}, "clientUserMessageId": "fixture"})
		b, _ = json.Marshal(rows)
		if err := writeQueue(path, b); err != nil {
			panic(err)
		}
		return
	}
	if len(os.Args) != 4 || os.Args[1] != "app-server" || os.Args[2] != "--listen" || os.Args[3] != "stdio://" {
		os.Exit(2)
	}
	if os.Getenv("DIBS_QUEUE_PROBE_FAIL") == "1" {
		os.Exit(5)
	}
	if os.Getenv("DIBS_QUEUE_PROBE_STALL") == "1" {
		time.Sleep(time.Hour)
		return
	}
	s := bufio.NewScanner(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	for s.Scan() {
		var req struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
			Params struct {
				Thread string `json:"threadId"`
			} `json:"params"`
		}
		if json.Unmarshal(s.Bytes(), &req) != nil {
			os.Exit(3)
		}
		f, err := os.OpenFile(filepath.Join(os.Getenv("CODEX_HOME"), "methods"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			panic(err)
		}
		_, err = fmt.Fprintln(f, req.Method)
		if err != nil {
			panic(err)
		}
		if err = f.Close(); err != nil {
			panic(err)
		}
		if req.Method == "initialized" {
			continue
		}
		var result any = map[string]any{}
		if req.Method == "thread/queue/list" {
			b, _ := os.ReadFile(path)
			rows := []map[string]any{}
			_ = json.Unmarshal(b, &rows)
			selected := []map[string]any{}
			for _, row := range rows {
				if row["threadId"] == req.Params.Thread {
					selected = append(selected, row)
				}
			}
			result = map[string]any{"data": selected, "nextCursor": nil}
		} else if req.Method != "initialize" {
			os.Exit(4)
		}
		if err := enc.Encode(map[string]any{"id": req.ID, "result": result}); err != nil {
			panic(err)
		}
	}
}

// The real app commits its queue transaction atomically. An observer must
// never see the fixture's truncate/write interval as an empty or broken queue.
func writeQueue(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".queue-")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err = f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
