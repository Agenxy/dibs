// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

// External app-server double: only stdout is inherited by the held descendant.
package main

import (
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--prepare" {
		return
	}
	raw, err := os.ReadFile(os.Args[0] + ".json")
	if err != nil {
		panic(err)
	}
	var config struct{ Address, Phase string }
	if err := json.Unmarshal(raw, &config); err != nil {
		panic(err)
	}
	control, err := net.Dial("tcp", config.Address)
	if err != nil {
		panic(err)
	}
	defer func() { _ = control.Close() }()
	if len(os.Args) == 2 && os.Args[1] == "--hold-stdout" {
		_, _ = control.Write([]byte{'g'})
		_, _ = io.Copy(io.Discard, control)
		return
	}
	if len(os.Args) != 2 || os.Args[1] != "app-server" {
		panic("unexpected app-server argv")
	}
	decoder, encoder := json.NewDecoder(os.Stdin), json.NewEncoder(os.Stdout)
	for {
		var request struct {
			ID     int
			Method string
		}
		if err := decoder.Decode(&request); err != nil {
			panic(err)
		}
		if request.Method == config.Phase {
			child := exec.Command(os.Args[0], "--hold-stdout")
			child.Stdout = os.Stdout
			if err := child.Start(); err != nil {
				panic(err)
			}
			_, _ = control.Write([]byte{'p'})
			var command [1]byte
			if _, err := io.ReadFull(control, command[:]); err != nil || command[0] != 'e' {
				panic("parent exit not commanded")
			}
			return
		}
		if request.Method == "initialize" {
			if err := encoder.Encode(map[string]any{"id": request.ID, "result": map[string]any{}}); err != nil {
				panic(err)
			}
		} else if request.Method != "initialized" {
			panic("unexpected request before held phase")
		}
	}
}
