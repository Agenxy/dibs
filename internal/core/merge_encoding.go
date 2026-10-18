// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

import (
	"encoding/json"
)

// Frame one finite record at a time, rather than copying every mail body into
// one giant JSON buffer. Newline framing is unambiguous JSON, including strings.
func newMergeEncoder(w interface{ Write([]byte) (int, error) }) func(any) {
	enc := json.NewEncoder(w)
	return func(v any) {
		if err := enc.Encode(v); err != nil {
			panic("merge projection is not serializable: " + err.Error())
		}
	}
}
