// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package notify

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// A transport double behind actual public helper discovery. It never posts a
// notification, opens an app, or accepts external commands through a socket.
func asBrokerHelper() {
	if path := os.Getenv("DIBS_TEST_BROKER_PID_CAPTURE"); path != "" {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			os.Exit(3)
		}
		_, _ = file.WriteString(strconv.Itoa(os.Getpid()) + "\n")
		_ = file.Close()
	}
	pendingA := ""
	output := json.NewEncoder(os.Stdout)
	emit := func(e brokerEvent) { e.Version = 1; _ = output.Encode(e) }
	emit(brokerEvent{Event: "ready", Reply: 1})
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), brokerFrameLimit+1)
	for scanner.Scan() {
		var frame brokerPost
		if json.Unmarshal(scanner.Bytes(), &frame) != nil {
			os.Exit(3)
		}
		if frame.Type == "cancel" {
			emit(brokerEvent{ID: frame.ID, Event: "expired"})
			continue
		}
		if frame.Type != "post" {
			os.Exit(3)
		}
		if os.Getenv("DIBS_TEST_DETAIL_DRIVER") == "1" {
			capture := struct {
				Args                   []string
				Stdin, Mode, Until, ID string
			}{os.Args[1:], string(frame.Detail), "broker-jsonl", strconv.FormatFloat(frame.Until, 'f', -1, 64), frame.ID}
			data, _ := json.Marshal(capture)
			if writeDetailCapture(frame.ID, data) != nil {
				os.Exit(3)
			}
		}
		if frame.Title == "banner" && frame.TimeSensitive {
			os.Exit(4)
		}
		if frame.Title == "priority" && !frame.TimeSensitive {
			os.Exit(4)
		}
		emit(brokerEvent{ID: frame.ID, Event: "posted", Receipt: ReceiptData{State: "posted"}})
		if frame.Title == "die" {
			os.Exit(3)
		}
		if frame.Title == "concurrent A" {
			pendingA = frame.ID
			continue
		}
		if frame.Title == "concurrent B" {
			emit(brokerEvent{ID: pendingA, Event: "action", Action: "Approve"})
			continue
		}
		if frame.Title == "unoffered" {
			emit(brokerEvent{ID: frame.ID, Event: "action", Action: "UNAPPROVED"})
		}
		if len(frame.Buttons) == 0 {
			continue
		}
		if strings.Contains(string(frame.Detail), "WITHDRAW_FIXTURE") {
			continue
		}
		if frame.ReplyKind == "question" {
			emit(brokerEvent{ID: frame.ID, Event: "reply", Action: "answer", Choice: frame.Choices[0]})
			continue
		}
		if frame.Title == "ignored" {
			emit(brokerEvent{ID: frame.ID, Event: "expired"})
			continue
		}
		choice := frame.Buttons[0]
		for _, button := range frame.Buttons {
			if button == "Approve" || button == "Yes" {
				choice = button
			}
		}
		emit(brokerEvent{ID: frame.ID, Event: "action", Action: choice})
	}
}

// The directory is inherited once at helper startup. Each frame owns a file,
// and rename prevents readers from observing a truncate or a partial JSON write.
func writeDetailCapture(id string, data []byte) error {
	dir := os.Getenv("DIBS_TEST_DETAIL_CAPTURE_DIR")
	file, err := os.CreateTemp(dir, ".capture-*")
	if err != nil {
		return err
	}
	name := file.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err = file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(dir, id+".json"))
}
