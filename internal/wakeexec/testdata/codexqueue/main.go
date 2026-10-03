// A subprocess fixture for the native queue protocol and the real command route.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func main() {
	path := filepath.Join(os.Getenv("CODEX_HOME"), "pending.json")
	if len(os.Args) > 1 && os.Args[1] == "queue" {
		b, _ := os.ReadFile(path)
		var rows []map[string]any
		_ = json.Unmarshal(b, &rows)
		rows = append(rows, map[string]any{"id": fmt.Sprint(len(rows) + 1), "input": []map[string]string{{"type": "text", "text": "Dibs: a new question is waiting."}}, "clientUserMessageId": "fixture"})
		b, _ = json.Marshal(rows)
		if err := os.WriteFile(path, b, 0600); err != nil {
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
			result = map[string]any{"data": rows, "nextCursor": nil}
		} else if req.Method != "initialize" {
			os.Exit(4)
		}
		if err := enc.Encode(map[string]any{"id": req.ID, "result": result}); err != nil {
			panic(err)
		}
	}
}
