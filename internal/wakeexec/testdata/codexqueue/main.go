// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

// A subprocess fixture for the native queue protocol and the real command route.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"time"

	"github.com/agenxy/dibs/internal/paths"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--hold-queue-pipes" {
		queuePipeControl(true)
		return
	}
	evidence("entered")
	defer evidence("finished")
	// Model a slow first execution without relying on this host's policy cache.
	// The opt-in marker lives only in the calling test's private directory.
	if marker := os.Getenv("DIBS_TEST_QUEUE_COLD_EXEC"); marker != "" {
		f, err := os.OpenFile(marker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			if err := f.Close(); err != nil {
				panic(err)
			}
			time.Sleep(1200 * time.Millisecond)
		} else if !os.IsExist(err) {
			panic(err)
		}
	}
	// Only this private fixture accepts this preparation mode. It cannot
	// discover, enqueue or open a real app, and writes no queue artifacts.
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		return
	}
	if len(os.Args) == 2 && os.Args[1] == "nonqueue-pipe" {
		queuePipeControl(false)
		fmt.Println("thread already has an active writer")
		return
	}
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
		if marker := os.Getenv("DIBS_TEST_QUEUE_FAIL_ONCE"); marker != "" {
			f, err := os.OpenFile(marker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if err == nil {
				_ = f.Close()
				os.Exit(1)
			}
			if !os.IsExist(err) {
				panic(err)
			}
		}
		if marker := os.Getenv("DIBS_TEST_QUEUE_BLOCK"); marker != "" {
			if err := os.WriteFile(marker, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
				panic(err)
			}
			time.Sleep(time.Hour) // only the test owner's cancellation can finish this command
			return
		}
		if dir := os.Getenv("DIBS_TEST_QUEUE_EVIDENCE"); dir != "" {
			stderr, err := os.OpenFile(filepath.Join(dir, "queue.stderr"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
			if err != nil {
				panic(err)
			}
			os.Stderr = stderr
			defer func() { _ = stderr.Close() }()
			defer func() {
				if problem := recover(); problem != nil {
					_, _ = fmt.Fprintf(stderr, "panic: %v\n%s", problem, debug.Stack())
					panic(problem)
				}
			}()
		}
		evidence("before-command")
		if marker := os.Getenv("DIBS_TEST_QUEUE_COMMAND_DELAY"); marker != "" {
			if err := os.WriteFile(marker, nil, 0o600); err != nil {
				panic(err)
			}
			time.Sleep(1200 * time.Millisecond)
		}
		if record := os.Getenv("DIBS_TEST_QUEUE_RECORD_CWD"); record != "" {
			cwd, err := os.Getwd()
			if err != nil {
				panic(err)
			}
			if err := os.WriteFile(record, []byte(cwd), 0o600); err != nil {
				panic(err)
			}
		}
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
		if failed := os.Getenv("DIBS_TEST_QUEUE_ERROR_THREAD"); failed != "" {
			f, err := os.OpenFile(filepath.Join(os.Getenv("CODEX_HOME"), "queue-attempts"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
			if err != nil {
				panic(err)
			}
			if _, err := fmt.Fprintln(f, thread); err != nil {
				panic(err)
			}
			if err := f.Close(); err != nil {
				panic(err)
			}
			if failed == thread {
				fmt.Fprintln(os.Stderr, os.Getenv("DIBS_TEST_QUEUE_ERROR"))
				os.Exit(1)
			}
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
		evidence("queue-locked")
		defer func() { paths.Unlock(lock); _ = lock.Close() }()
		b, _ := os.ReadFile(path)
		var rows []map[string]any
		_ = json.Unmarshal(b, &rows)
		rows = append(rows, map[string]any{"id": fmt.Sprint(len(rows) + 1), "threadId": thread, "input": []map[string]string{{"type": "text", "text": message}}, "clientUserMessageId": "fixture"})
		b, _ = json.Marshal(rows)
		if err := writeQueue(path, b); err != nil {
			panic(err)
		}
		evidence("queue-committed")
		if mode := os.Getenv("DIBS_TEST_QUEUE_PIPE_CASE"); mode != "" {
			switch mode {
			case "missing", "dispatched":
				if mode == "dispatched" {
					if err := os.WriteFile(filepath.Join(os.Getenv("CODEX_HOME"), "dispatched.json"), b, 0o600); err != nil {
						panic(err)
					}
				}
				rows = nil
			case "wrong-timestamp":
				rows[0]["input"] = []map[string]string{{"type": "text", "text": "Dibs: notify notice issued at 2000-01-01T00:00:00Z. It may already be handled."}}
			case "ambiguous":
				rows = append(rows, map[string]any{"id": "second", "threadId": thread, "input": rows[0]["input"]})
			}
			b, err := json.Marshal(rows)
			if err != nil {
				panic(err)
			}
			if rows == nil {
				b = []byte("[]")
			}
			if err := writeQueue(path, b); err != nil {
				panic(err)
			}
			queuePipeControl(false)
			// A retained diagnostic must never trigger the open-thread fallback
			// after a successful exit with an ambiguous drain outcome.
			fmt.Println("thread already has an active writer")
		}
		return
	}
	if len(os.Args) != 4 || os.Args[1] != "app-server" || os.Args[2] != "--listen" || os.Args[3] != "stdio://" {
		os.Exit(2)
	}
	if os.Getenv("DIBS_TEST_QUEUE_PIPE_CASE") != "" {
		f, err := os.OpenFile(filepath.Join(os.Getenv("CODEX_HOME"), "probe-processes"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			panic(err)
		}
		if _, err := f.WriteString("probe\n"); err != nil {
			panic(err)
		}
		if err := f.Close(); err != nil {
			panic(err)
		}
		if os.Getenv("DIBS_TEST_QUEUE_PIPE_CASE") == "observation-error" {
			if _, err := os.Stat(path); err == nil {
				os.Exit(5)
			}
		}
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
		f, err := os.OpenFile(filepath.Join(os.Getenv("CODEX_HOME"), "methods"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
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
		evidence(req.Method)
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

// Only the retained-pipe test enables this control protocol. Each process
// owns its connection, so EOF proves cleanup, independently of stdout drain.
func queuePipeControl(grandchild bool) {
	conn, err := net.DialTimeout("tcp", os.Getenv("DIBS_TEST_QUEUE_PIPE_CONTROL"), 5*time.Second)
	if err != nil {
		panic(err)
	}
	limit := 10 * time.Second
	// Engine tests keep the real ten-second command grace. Only those tests
	// extend this private control connection so the descendant outlives it.
	if raw := os.Getenv("DIBS_TEST_QUEUE_PIPE_CONTROL_TIMEOUT"); raw != "" {
		limit, err = time.ParseDuration(raw)
		if err != nil || limit <= 0 {
			panic("invalid fixture pipe control timeout")
		}
	}
	if err := conn.SetDeadline(time.Now().Add(limit)); err != nil {
		panic(err)
	}
	role := byte('p')
	if grandchild {
		role = 'g'
		st, err := os.Stdout.Stat()
		if err != nil || st.Mode()&os.ModeNamedPipe == 0 {
			panic("grandchild did not inherit the real command pipe")
		}
	}
	if _, err := conn.Write([]byte{role}); err != nil {
		panic(err)
	}
	if !grandchild {
		child := exec.Command(os.Args[0], "--hold-queue-pipes") // #nosec G204 -- private test fixture
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
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

// Private fixture evidence only. It never runs in a shipped command.
func evidence(phase string) {
	dir := os.Getenv("DIBS_TEST_QUEUE_EVIDENCE")
	if dir == "" {
		return
	}
	file, err := os.OpenFile(filepath.Join(dir, "stages.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		panic(err)
	}
	mode := ""
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}
	err = json.NewEncoder(file).Encode(map[string]any{"at_ns": time.Now().UnixNano(), "mode": mode, "phase": phase})
	closeErr := file.Close()
	if err != nil {
		panic(err)
	}
	if closeErr != nil {
		panic(closeErr)
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
