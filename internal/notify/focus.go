// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package notify

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
)

// Presentation is evidence about filtering, never evidence that a person saw
// a notification. Shown is "unknown" while Focus is active,
// or absent otherwise. App filters are intentionally not inspected.
type Presentation struct {
	Focus  string `json:"focus,omitempty"`
	Shown  string `json:"shown,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// FocusPresentation runs outside the writer. These private, versioned files
// are observations, not an OS visibility receipt. Unknown shapes stay unknown.
func FocusPresentation() Presentation {
	name, advice := focusAdvice()
	if name == "" {
		return Presentation{}
	}
	return Presentation{
		Focus: name, Shown: "unknown",
		Reason: "Focus " + name + " is on and may hold this; not confirmed seen. " + advice,
	}
}

// focusDoctor describes the route without implying a particular notification
// exists or was seen. The app-list mode informs advice, never visibility.
func focusDoctor() string {
	name, advice := focusAdvice()
	if name == "" {
		return ""
	}
	return "Focus " + name + " is on. " + advice
}

func focusAdvice() (string, string) {
	if goos != "darwin" {
		return "", ""
	}
	id := focusOn()
	if id == "" {
		return "", ""
	}
	name := id
	var configuration struct {
		ApplicationConfigurationType *int `json:"applicationConfigurationType"`
	}
	if home, err := os.UserHomeDir(); err == nil {
		db := filepath.Join(home, "Library", "DoNotDisturb", "DB")
		if mode, ok := focusRecord(filepath.Join(db, "ModeConfigurations.json"), "modeConfigurations", id); ok {
			var identity struct {
				Name string `json:"name"`
			}
			if json.Unmarshal(mode["mode"], &identity) == nil && len(identity.Name) > 0 && len(identity.Name) <= 100 {
				name = identity.Name
			}
			if json.Unmarshal(mode["configuration"], &configuration) != nil {
				configuration.ApplicationConfigurationType = nil
			}
		}
	}
	if configuration.ApplicationConfigurationType != nil {
		switch *configuration.ApplicationConfigurationType {
		case 0:
			return name, "To allow banners, add Dibs to " + name + "'s Allowed Apps in System Settings > Focus."
		case 1:
			return name, "Make sure Dibs isn't in " + name + "'s silenced apps; " +
				"macOS's Intelligent Breakthrough & Silencing can still hold some notifications."
		}
	}
	return name, "Check " + name + "'s notification settings in System Settings > Focus."
}

func focusRecord(path, key, id string) (map[string]json.RawMessage, bool) {
	b, err := readFocusFile(path)
	if err != nil {
		return nil, false
	}
	var doc struct {
		Header struct {
			Version int `json:"version"`
		} `json:"header"`
		Data []map[string]map[string]map[string]json.RawMessage `json:"data"`
	}
	if json.Unmarshal(b, &doc) != nil || doc.Header.Version != 3 || len(doc.Data) != 1 {
		return nil, false
	}
	m := doc.Data[0][key]["com.apple.focus."+id]
	if m == nil {
		m = doc.Data[0][key][id]
	}
	return m, m != nil
}

func readFocusFile(path string) ([]byte, error) {
	// #nosec G304 -- fixed OS observation files under the current user's home.
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, 1<<20+1))
	if len(b) > 1<<20 {
		return nil, io.ErrShortBuffer
	}
	return b, err
}
